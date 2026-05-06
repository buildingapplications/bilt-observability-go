# bilt-observability-go

Shared observability bootstrap for bilt Go services. Mirrors the surface of
[`@biltme/otel`](https://github.com/buildingapplications/bilt-observability-ts).

> Internal use only. No semver guarantees, no public API contract.
> All rights reserved.

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
