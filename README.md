# bilt-observability-go

> ## NOT FOR PUBLIC USE
>
> It is **internal infrastructure for bilt** maintained
> for bilt services only.
>
> - **No semver contract.** Breaking changes can land on any version, including patch.
> - **No semantic-convention guarantees.** Span names, attribute keys, and log
>   schemas track bilt's internal needs and can change without notice.
> - **No support, no issue triage, no PRs accepted from outside the bilt team.**
> - **All rights reserved.** No license is granted to use, copy, or redistribute.
>
> If you stumbled here from `pkg.go.dev` or a search engine: do not import this.
> Use the upstream OpenTelemetry Go SDK directly.

## What it does

One `Init` call wires:

- zap logger (`timestamp`, `level`, `msg`, `service`, `request_id`, `trace_id`, `span_id`)
- OTel TracerProvider over OTLP gRPC and a MeterProvider with OTLP and optional pull readers
- W3C TraceContext + Baggage propagators
- runtime metrics (`process.runtime.go.*`)
- chi HTTP middleware (RequestID, RealIP, panic recovery logged + recorded on the span, otelhttp, http.route templating) with health-path skip
- `Mux`, `Health` and `Serve`: the shared router, health endpoints and graceful-shutdown lifecycle
- otelhttp-wrapped outbound `http.Client`
- Redis traceparent inject/extract + producer/consumer spans
- pgx query/connect/acquire spans, named off the sqlc query name

## Usage

```go
package main

import (
    "context"
    "net/http"

    "github.com/buildingapplications/bilt-observability-go/obs"
)

func main() {
    ctx := context.Background()

    if _, err := obs.Init(ctx, &obs.Config{
        ServiceName:  "bilt-sessions",
        LogLevel:     "info",
        Environment:  "production",
        OTelEndpoint: "http://otel-collector:4317",
    }); err != nil {
        panic(err)
    }

    db := openDB()

    r := obs.Mux()
    r.Handle("/health/*", obs.Health(db.Ping))
    r.Get("/api/things", listThings)

    lc := obs.Lifecycle{
        AfterDrain: func(context.Context) error { db.Close(); return nil },
    }
    if err := obs.Serve(ctx, lc, obs.Server{Server: &http.Server{Addr: ":8080", Handler: r}}); err != nil {
        obs.Logger("bilt-sessions").Fatalw("serve", "error", err)
    }
}
```

## Serving

`obs.Serve` owns the process lifecycle so every service shuts down in the same order:

1. Bind every server's listener (`Server.Listener` if set, else `Addr`), then serve them all.
2. Wait for SIGINT, SIGTERM, `ctx` cancellation or the first serve error.
3. Run `Lifecycle.BeforeDrain` while the servers still serve: fail readiness and finish in-flight work, or close
   long-lived streams that `Shutdown` would otherwise wait on.
4. Drain all servers concurrently, each within `Server.DrainTimeout` (default 15s), then close what is left.
5. Run `Lifecycle.AfterDrain`: close DB/Redis, stop workers, final flushes.
6. Call the `Shutdown` returned by `Init`, flushing spans from the requests and both steps.

Serve puts no deadline on either step, so each bounds its own work. Nil steps are skipped. After a listen error,
only `AfterDrain` and the telemetry flush run.

Do not call `Init`'s `Shutdown` yourself when using `Serve`. It is idempotent, so a leftover `defer` is harmless.
`Serve` sets `ReadHeaderTimeout` to 10s when zero and leaves Read/Write/Idle timeouts as given, because
websockets, SSE and proxies need none. A `Server` with `TLSConfig` serves TLS. The returned error joins the
first listen or serve error with any shutdown errors.

`obs.Health(checks...)` serves `/health/live` (always 200) and `/health/ready`, which runs the checks concurrently
within 2s and answers 503 `not ready: <first error>`. It records nothing on spans; both paths are in the default
skip list. Mount it with `r.Handle("/health/*", ...)` or `mux.Handle("/health/", ...)`.

`obs.Mux()` is `chi.NewRouter()` with `HTTPMiddleware` installed (panic recovery included), using the skip paths and
service name from `Init`. Call it after `Init`. Add your own middleware with `r.Use` after it.

## API

```go
obs.Init(ctx, *Config) (Shutdown, error)
obs.Logger(serviceName) *zap.SugaredLogger               // package logger (no ctx)
obs.Log(ctx) *zap.SugaredLogger                          // request-scoped; falls back to the package logger
obs.LoggerFromContext(ctx, fallback) *zap.SugaredLogger  // Log(ctx) with an explicit fallback
obs.WithRequestID(ctx, id) context.Context
obs.RequestIDFromContext(ctx) string
obs.HTTPMiddleware(lg, MiddlewareOptions) func(http.Handler) http.Handler
obs.Mux() *chi.Mux                                       // chi router with HTTPMiddleware installed
obs.Health(ready ...func(ctx) error) http.Handler        // /health/live + /health/ready
obs.Serve(ctx, obs.Lifecycle, servers ...obs.Server) error // listen, signal, BeforeDrain, drain, AfterDrain, telemetry shutdown

obs.WithHandlerError(ctx) ctx                // (auto-called by HTTPMiddleware)
obs.SetHandlerError(ctx, msg string)         // call from handler/error mapper
obs.SetHandlerFault(ctx, err error)          // internal failure; span error at any status
obs.HandlerError(ctx) string                 // (read by HTTPMiddleware on 4xx/5xx)
obs.HandlerFault(ctx) error                  // the error SetHandlerFault recorded, or nil
obs.HTTPClient(*http.Client) *http.Client
obs.HTTPTransport(http.RoundTripper) http.RoundTripper   // for transport slots; client spans are named "<METHOD> <host>"
obs.DefaultHealthPaths() []string

obs.InjectTraceContext(ctx) string
obs.ExtractTraceContext(ctx, traceparent) context.Context
obs.ConsumerSpan(ctx, ConsumerOpts) (ctx, span)
obs.ProducerSpan(ctx, ProducerOpts) (ctx, span)
obs.BackgroundSpan(ctx, name, BackgroundOpts) (ctx, span)
obs.Go(ctx, name, func(ctx) error)                       // goroutine with span; error/panic logged + recorded, panic recovered
obs.Recover(ctx, &err)                                   // defer: panic becomes *err, logged with stack; span left to the caller
obs.EndSpan(span, err)                                   // RecordError + Error status if err != nil, then End
obs.WrapRedisClient(*redis.Client) error
obs.InstrumentPgxPool(*pgxpool.Config)                   // call before pgxpool.NewWithConfig
```

## Context fields

`Log(ctx)` / `LoggerFromContext` always merge `request_id`, `trace_id`, and
`span_id` off ctx. To also merge service-specific fields (e.g. `workflowId`,
`projectId`), set `Config.ContextFields` — it is called per log with ctx and
returns key/value pairs:

```go
obs.Init(ctx, &obs.Config{
    ServiceName:   "bilt-agent",
    ContextFields: common.LogFields, // func(ctx) []any
})
```

This keeps obs generic: each service registers its own context keys without obs
importing their packages.

## Killswitch

Set `BILT_OBS_DISABLE=1` (or `true`) to make `Init` return a no-op shutdown
without touching OTel globals. Logger still works.

## Pull metrics

Pass a Prometheus exporter or another SDK reader in `Config.MetricReaders` to
collect the service's existing instruments on demand. OTLP metric export stays
enabled unless `Config.DisableOTLPMetrics` is true. Traces still use OTLP when
metric push is disabled.

## Local trace storage

Set `Config.TraceClient` to an `otlptrace.Client` that writes to local storage.
`Init` wraps it in an OTLP exporter and synchronous span processor in place of
the default gRPC exporter. For example, simhost passes its durable trace spool
directly as `TraceClient`. `ExtraSpanProcessors` still run alongside it. The
caller owns the storage and its read and acknowledgement API; `Init` shuts down
the exporter with the tracer provider. With `TraceClient`, each `Span.End`
waits for `UploadTraces` to return. Keep that operation bounded and local;
`OTelEndpoint` and `BSPOptions` apply only to the default gRPC exporter.

## Environment override

Set `BILT_OBS_ENVIRONMENT` to override `Config.Environment` for the
`deployment.environment` / `service.namespace` resource attributes. Lets a
launcher (e.g. the dev TUI) retag telemetry per developer without changing the
service's own config.

## Defaults

- BSP: 8192 queue / 1024 batch / 2s flush (matches `@biltme/otel`)
- Sampler: `ParentBased(AlwaysSample)`
- Cardinality: 1000 attribute sets per instrument
- Metric temporality: delta for every instrument kind, so a level must be a
  gauge — an UpDownCounter exports net change, not the level
- Health paths: `/health`, `/healthz`, `/health/live`, `/health/ready`, `/api/health`

## Development

```sh
go test -race -count=1 ./...
go vet ./...
golangci-lint run
```
