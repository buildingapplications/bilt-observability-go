package obs

import (
	"context"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/redis/go-redis/extra/redisotel/v9"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ConsumerOpts configures a Redis consumer span.
type ConsumerOpts struct {
	Destination   string
	ConsumerGroup string
	Attrs         []attribute.KeyValue
	// ParentChild opts back into remote parent-child; default false =
	// new-root span linking back to the producer.
	ParentChild bool
}

// ProducerOpts configures a Redis producer span.
type ProducerOpts struct {
	Destination string
	Attrs       []attribute.KeyValue
}

// InjectTraceContext returns the W3C traceparent string for ctx, or "" if no
// active span. Format: 00-{traceID}-{spanID}-{flags}.
func InjectTraceContext(ctx context.Context) string {
	sc := trace.SpanFromContext(ctx).SpanContext()
	if !sc.IsValid() {
		return ""
	}
	return fmt.Sprintf("00-%s-%s-%02x", sc.TraceID(), sc.SpanID(), byte(sc.TraceFlags()))
}

// ExtractTraceContext returns ctx with the remote span context decoded from a
// W3C traceparent. Invalid input returns ctx unchanged.
func ExtractTraceContext(ctx context.Context, traceparent string) context.Context {
	sc := parseTraceparent(traceparent)
	if !sc.IsValid() {
		return ctx
	}
	return trace.ContextWithRemoteSpanContext(ctx, sc)
}

// ConsumerSpan starts a CONSUMER-kind span attributed to a Redis stream.
// Caller defer span.End().
func ConsumerSpan(ctx context.Context, opts ConsumerOpts) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		attribute.String("messaging.system", "redis"),
		attribute.String("messaging.operation", "receive"),
	}
	if opts.Destination != "" {
		attrs = append(attrs, attribute.String("messaging.destination.name", opts.Destination))
	}
	if opts.ConsumerGroup != "" {
		attrs = append(attrs, attribute.String("messaging.consumer.group.name", opts.ConsumerGroup))
	}
	attrs = append(attrs, opts.Attrs...)

	// Span name is kept static — the destination (a per-session stream key
	// with a UUID) goes only to the messaging.destination.name attribute, so
	// it never inflates operation cardinality in spanmetrics.
	startOpts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(attrs...),
	}
	// Default: each consumed message is its own trace that links back to the
	// producer, so a long-lived stream does not nest into one giant trace.
	if !opts.ParentChild {
		if link := trace.SpanContextFromContext(ctx); link.IsValid() {
			startOpts = append(startOpts, trace.WithNewRoot(), trace.WithLinks(trace.Link{SpanContext: link}))
		}
	}
	return otel.Tracer("obs.redis").Start(ctx, "redis.receive", startOpts...)
}

// ProducerSpan starts a PRODUCER-kind span attributed to a Redis stream.
func ProducerSpan(ctx context.Context, opts ProducerOpts) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		attribute.String("messaging.system", "redis"),
		attribute.String("messaging.operation", "publish"),
	}
	if opts.Destination != "" {
		attrs = append(attrs, attribute.String("messaging.destination.name", opts.Destination))
	}
	attrs = append(attrs, opts.Attrs...)

	return otel.Tracer("obs.redis").Start(ctx, "redis.publish",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(attrs...),
	)
}

// WrapRedisClient instruments client with redisotel metrics. Per-command trace
// spans are intentionally not enabled: they are high-volume leaf spans with no
// debugging value. Redis command latency is covered by the metrics instrumentation.
func WrapRedisClient(client *redis.Client) error {
	if err := redisotel.InstrumentMetrics(client); err != nil {
		return fmt.Errorf("redis metrics: %w", err)
	}
	return nil
}

func parseTraceparent(tp string) trace.SpanContext {
	parts := strings.Split(tp, "-")
	if len(parts) != 4 || parts[0] != "00" {
		return trace.SpanContext{}
	}
	traceIDBytes, err := hex.DecodeString(parts[1])
	if err != nil || len(traceIDBytes) != 16 {
		return trace.SpanContext{}
	}
	spanIDBytes, err := hex.DecodeString(parts[2])
	if err != nil || len(spanIDBytes) != 8 {
		return trace.SpanContext{}
	}
	flagsBytes, err := hex.DecodeString(parts[3])
	if err != nil || len(flagsBytes) != 1 {
		return trace.SpanContext{}
	}
	var traceID trace.TraceID
	var spanID trace.SpanID
	copy(traceID[:], traceIDBytes)
	copy(spanID[:], spanIDBytes)
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.TraceFlags(flagsBytes[0]),
		Remote:     true,
	})
}
