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
- OTel TracerProvider + MeterProvider over OTLP gRPC
- W3C TraceContext + Baggage propagators
- runtime metrics (`process.runtime.go.*`)
- chi HTTP middleware (RequestID, RealIP, Recoverer, otelhttp, http.route templating, access log) with health-path skip
- otelhttp-wrapped outbound `http.Client`
- Redis traceparent inject/extract + producer/consumer spans

## Usage

```go
package main

import (
    "context"
    "github.com/buildingapplications/bilt-observability-go/obs"
)

func main() {
    ctx := context.Background()

    shutdown, err := obs.Init(ctx, &obs.Config{
        ServiceName:  "bilt-sessions",
        LogLevel:     "info",
        Environment:  "production",
        OTelEndpoint: "http://otel-collector:4317",
        HealthPaths:  obs.DefaultHealthPaths(),
    })
    if err != nil {
        panic(err)
    }
    defer shutdown(ctx)

    lg := obs.Logger("bilt-sessions")
    lg.Infow("started")
}
```

## API

```go
obs.Init(ctx, *Config) (Shutdown, error)
obs.Logger(serviceName) *zap.SugaredLogger               // package logger (no ctx)
obs.Log(ctx) *zap.SugaredLogger                          // request-scoped; falls back to the package logger
obs.LoggerFromContext(ctx, fallback) *zap.SugaredLogger  // Log(ctx) with an explicit fallback
obs.WithRequestID(ctx, id) context.Context
obs.RequestIDFromContext(ctx) string
obs.HTTPMiddleware(lg, MiddlewareOptions) func(http.Handler) http.Handler
// MiddlewareOptions.EnrichLogFields(ctx, status) []any  // optional access-log enrichment
// MiddlewareOptions.DisableHandlerError bool             // opt out of auto handler-error wiring

obs.WithHandlerError(ctx) ctx                // (auto-called by HTTPMiddleware)
obs.SetHandlerError(ctx, msg string)         // call from handler/error mapper
obs.HandlerError(ctx) string                 // (read by HTTPMiddleware on 4xx/5xx)
obs.HTTPClient(*http.Client) *http.Client
obs.DefaultHealthPaths() []string

obs.InjectTraceContext(ctx) string
obs.ExtractTraceContext(ctx, traceparent) context.Context
obs.ConsumerSpan(ctx, ConsumerOpts) (ctx, span)
obs.ProducerSpan(ctx, ProducerOpts) (ctx, span)
obs.WrapRedisClient(*redis.Client) error
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

## Environment override

Set `BILT_OBS_ENVIRONMENT` to override `Config.Environment` for the
`deployment.environment` / `service.namespace` resource attributes. Lets a
launcher (e.g. the dev TUI) retag telemetry per developer without changing the
service's own config.

## Defaults

- BSP: 8192 queue / 1024 batch / 2s flush (matches `@biltme/otel`)
- Sampler: `ParentBased(AlwaysSample)`
- Cardinality: unlimited (Go SDK default)
- Health paths: `/health`, `/healthz`, `/health/live`, `/health/ready`, `/api/health`

## Development

```sh
go test -race -count=1 ./...
go vet ./...
golangci-lint run
```
