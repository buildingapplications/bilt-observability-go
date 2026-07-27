package obs

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type BackgroundOpts struct {
	Attrs []attribute.KeyValue
	// ParentChild opts back into parent-child; default false = new-root span
	// linking back to the originating request.
	ParentChild bool
}

func BackgroundSpan(ctx context.Context, name string, opts BackgroundOpts) (context.Context, trace.Span) {
	startOpts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(opts.Attrs...),
	}
	if !opts.ParentChild {
		if link := trace.SpanContextFromContext(ctx); link.IsValid() {
			startOpts = append(startOpts, trace.WithNewRoot(), trace.WithLinks(trace.Link{SpanContext: link}))
		}
	}
	return otel.Tracer("obs.background").Start(context.WithoutCancel(ctx), name, startOpts...)
}
