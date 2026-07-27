package obs

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

func bgTracerProvider(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return exporter
}

func TestBackgroundSpan_NewRootLinksToParent(t *testing.T) {
	exporter := bgTracerProvider(t)
	tracer := otel.Tracer("test")

	ctx, parent := tracer.Start(context.Background(), "request")
	_, span := BackgroundSpan(ctx, "preview.start", BackgroundOpts{})
	span.End()
	parent.End()

	stub := spanStubByName(t, exporter.GetSpans(), "preview.start")
	if stub.SpanContext.TraceID() == parent.SpanContext().TraceID() {
		t.Error("expected a new trace, got the parent's")
	}
	if len(stub.Links) != 1 {
		t.Fatalf("expected 1 link, got %d", len(stub.Links))
	}
	if stub.Links[0].SpanContext.SpanID() != parent.SpanContext().SpanID() {
		t.Error("link does not point at the originating span")
	}
	if stub.SpanKind != trace.SpanKindInternal {
		t.Errorf("kind: got %v", stub.SpanKind)
	}
}

func TestBackgroundSpan_ParentChildStaysInTrace(t *testing.T) {
	exporter := bgTracerProvider(t)
	tracer := otel.Tracer("test")

	ctx, parent := tracer.Start(context.Background(), "request")
	_, span := BackgroundSpan(ctx, "preview.start", BackgroundOpts{ParentChild: true})
	span.End()
	parent.End()

	stub := spanStubByName(t, exporter.GetSpans(), "preview.start")
	if stub.SpanContext.TraceID() != parent.SpanContext().TraceID() {
		t.Error("expected the parent's trace")
	}
	if len(stub.Links) != 0 {
		t.Errorf("expected no links, got %d", len(stub.Links))
	}
}

func TestBackgroundSpan_NoParentSpan(t *testing.T) {
	exporter := bgTracerProvider(t)

	_, span := BackgroundSpan(context.Background(), "preview.start", BackgroundOpts{})
	span.End()

	stub := spanStubByName(t, exporter.GetSpans(), "preview.start")
	if len(stub.Links) != 0 {
		t.Errorf("expected no links, got %d", len(stub.Links))
	}
	if !stub.SpanContext.IsValid() {
		t.Error("expected a valid span context")
	}
}

func TestBackgroundSpan_DetachesFromCancellation(t *testing.T) {
	bgTracerProvider(t)

	ctx, cancel := context.WithCancel(WithRequestID(context.Background(), "req-1"))
	bgCtx, span := BackgroundSpan(ctx, "preview.start", BackgroundOpts{})
	defer span.End()
	cancel()

	if bgCtx.Err() != nil {
		t.Errorf("expected detached context, got %v", bgCtx.Err())
	}
	if got := RequestIDFromContext(bgCtx); got != "req-1" {
		t.Errorf("request ID not retained: got %q", got)
	}
}

func spanStubByName(t *testing.T, stubs tracetest.SpanStubs, name string) tracetest.SpanStub {
	t.Helper()
	for _, s := range stubs {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no %s span recorded", name)
	return tracetest.SpanStub{}
}
