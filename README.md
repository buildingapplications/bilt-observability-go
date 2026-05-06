# bilt-observability-go

Shared observability bootstrap for bilt Go services. Mirrors the surface of
[`@biltme/otel`](https://github.com/buildingapplications/bilt-observability-ts).

> ## NOT FOR PUBLIC USE
>
> This module is published only because Go has no source/artifact decoupling.
> It is not a product. It is **internal infrastructure for bilt** maintained
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
obs.Logger(serviceName) *zap.SugaredLogger
obs.LoggerFromContext(ctx, fallback) *zap.SugaredLogger
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

## Killswitch

Set `BILT_OBS_DISABLE=1` (or `true`) to make `Init` return a no-op shutdown
without touching OTel globals. Logger still works.

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
