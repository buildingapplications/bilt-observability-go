package obs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestGo(t *testing.T) {
	cases := []struct {
		name      string
		fn        func(context.Context) error
		wantError bool
		wantLog   string
	}{
		{"ok", func(context.Context) error { return nil }, false, ""},
		{"error", func(context.Context) error { return errors.New("upstream down") }, true, "background task failed"},
		{"panic", func(context.Context) error { panic("nil map") }, true, "panic recovered"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetForTest()
			exporter := newTestTracer(t)
			core, logs := observer.New(zap.ErrorLevel)
			ctx := WithLogger(context.Background(), zap.New(core).Sugar())

			Go(ctx, "task."+tc.name, tc.fn)
			span := waitForSpan(t, exporter)

			if span.Name != "task."+tc.name {
				t.Errorf("span name = %q", span.Name)
			}
			if got := span.Status.Code == codes.Error; got != tc.wantError {
				t.Errorf("Error status = %v, want %v", got, tc.wantError)
			}
			if tc.wantLog == "" {
				if logs.Len() != 0 {
					t.Errorf("want no error logs, got %v", logs.All())
				}
				return
			}
			if n := logs.FilterMessage(tc.wantLog).Len(); n != 1 {
				t.Errorf("want 1 %q log, got %d: %v", tc.wantLog, n, logs.All())
			}
		})
	}
}

func TestGo_PanicRecordsStack(t *testing.T) {
	resetForTest()
	exporter := newTestTracer(t)
	Go(context.Background(), "task", func(context.Context) error { panic("boom") })
	if span := waitForSpan(t, exporter); !hasStackedException(span.Events) {
		t.Errorf("want exception event with stacktrace, events=%v", span.Events)
	}
}

func waitForSpan(t *testing.T, exporter *tracetest.InMemoryExporter) tracetest.SpanStub {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if spans := exporter.GetSpans(); len(spans) > 0 {
			return spans[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("span never ended")
	return tracetest.SpanStub{}
}

func hasStackedException(events []sdktrace.Event) bool {
	for _, ev := range events {
		if ev.Name != "exception" {
			continue
		}
		for _, a := range ev.Attributes {
			if a.Key == "exception.stacktrace" && a.Value.AsString() != "" {
				return true
			}
		}
	}
	return false
}

func TestRecover(t *testing.T) {
	resetForTest()
	exporter := newTestTracer(t)
	core, logs := observer.New(zap.ErrorLevel)
	ctx, span := otel.Tracer("test").Start(WithLogger(context.Background(), zap.New(core).Sugar()), "op")

	sentinel := errors.New("nil deref")
	err := func() (err error) {
		defer Recover(ctx, &err)
		panic(sentinel)
	}()
	span.End()

	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want wrapping the panicked error", err)
	}
	panics := logs.FilterMessage("panic recovered").All()
	if len(panics) != 1 {
		t.Fatalf("want 1 panic log, got %d", len(panics))
	}
	if stack, _ := panics[0].ContextMap()["stack"].(string); !strings.Contains(stack, "panic(") {
		t.Errorf("stack lacks the panicking frame:\n%s", stack)
	}
	if got := exporter.GetSpans()[0]; got.Status.Code != codes.Unset || len(got.Events) != 0 {
		t.Errorf("want span untouched for the caller to record, got %v %v", got.Status, got.Events)
	}
}

func TestRecover_NoPanicLeavesErr(t *testing.T) {
	want := errors.New("plain")
	err := func() (err error) {
		defer Recover(context.Background(), &err)
		return want
	}()
	if err != want {
		t.Errorf("err = %v, want %v", err, want)
	}
}
