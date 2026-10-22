// Package obs is bilt's shared observability bootstrap for Go services.
//
// Mirrors @biltme/otel: one Init call wires zap logging, OTel tracing/metrics
// (OTLP gRPC), W3C TraceContext + Baggage propagators, runtime metrics, and
// chi HTTP middleware. Mux, Health and Serve give every service the same
// router, probes and shutdown order. Internal use only.
package obs
