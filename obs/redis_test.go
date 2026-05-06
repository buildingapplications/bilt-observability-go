package obs

import (
	"context"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func compositePropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{})
}

func TestInjectTraceContext_Empty(t *testing.T) {
	if got := InjectTraceContext(context.Background()); got != "" {
		t.Errorf("expected empty traceparent, got %q", got)
	}
}

func TestInjectExtract_Roundtrip(t *testing.T) {
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(tracetest.NewInMemoryExporter())))
	otel.SetTracerProvider(tp)
	defer tp.Shutdown(context.Background())

	ctx, span := tp.Tracer("t").Start(context.Background(), "outer")
	defer span.End()

	tp1 := InjectTraceContext(ctx)
	if !strings.HasPrefix(tp1, "00-") {
		t.Fatalf("bad traceparent: %s", tp1)
	}

	ctx2 := ExtractTraceContext(context.Background(), tp1)
	sc := trace.SpanContextFromContext(ctx2)
	if sc.TraceID() != span.SpanContext().TraceID() {
		t.Errorf("trace id lost: got %s want %s", sc.TraceID(), span.SpanContext().TraceID())
	}
	if sc.SpanID() != span.SpanContext().SpanID() {
		t.Errorf("span id lost: got %s want %s", sc.SpanID(), span.SpanContext().SpanID())
	}
}

func TestExtractTraceContext_Malformed(t *testing.T) {
	cases := []string{
		"",
		"01-aa-bb-00",
		"00-zz-bb-00",
		"00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bb-00",
		"not-a-traceparent",
	}
	for _, c := range cases {
		ctx := ExtractTraceContext(context.Background(), c)
		if trace.SpanContextFromContext(ctx).IsValid() {
			t.Errorf("expected invalid for %q", c)
		}
	}
}

func TestConsumerSpan_Kind(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	otel.SetTracerProvider(tp)
	defer tp.Shutdown(context.Background())

	_, span := ConsumerSpan(context.Background(), ConsumerOpts{Destination: "stream-x", ConsumerGroup: "g1"})
	span.End()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span got %d", len(spans))
	}
	if spans[0].SpanKind != trace.SpanKindConsumer {
		t.Errorf("expected consumer kind, got %v", spans[0].SpanKind)
	}
	if spans[0].Name != "redis.receive stream-x" {
		t.Errorf("name: got %s", spans[0].Name)
	}
	hasDest := false
	hasGroup := false
	for _, a := range spans[0].Attributes {
		if string(a.Key) == "messaging.destination.name" && a.Value.AsString() == "stream-x" {
			hasDest = true
		}
		if string(a.Key) == "messaging.consumer.group.name" && a.Value.AsString() == "g1" {
			hasGroup = true
		}
	}
	if !hasDest {
		t.Error("missing destination attribute")
	}
	if !hasGroup {
		t.Error("missing consumer group attribute")
	}
}

func TestProducerSpan_Kind(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	otel.SetTracerProvider(tp)
	defer tp.Shutdown(context.Background())

	_, span := ProducerSpan(context.Background(), ProducerOpts{Destination: "stream-y"})
	span.End()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span got %d", len(spans))
	}
	if spans[0].SpanKind != trace.SpanKindProducer {
		t.Errorf("expected producer kind, got %v", spans[0].SpanKind)
	}
}

func TestParseTraceparent_Basic(t *testing.T) {
	tp := "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	sc := parseTraceparent(tp)
	if !sc.IsValid() {
		t.Fatal("expected valid")
	}
	if !sc.IsSampled() {
		t.Error("expected sampled flag")
	}
	if !sc.IsRemote() {
		t.Error("expected remote")
	}
}
