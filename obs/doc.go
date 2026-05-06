// Package obs is bilt's shared observability bootstrap for Go services.
//
// Mirrors @biltme/otel: one Init call wires zap logging, OTel tracing/metrics
// (OTLP gRPC), W3C TraceContext + Baggage propagators, runtime metrics, and
// chi HTTP middleware. Internal use only.
package obs
