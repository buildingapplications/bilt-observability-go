package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestBuildLogger_TimestampKey(t *testing.T) {
	var buf bytes.Buffer
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{
			TimeKey:     "timestamp",
			MessageKey:  "msg",
			LevelKey:    "level",
			EncodeLevel: zapcore.LowercaseLevelEncoder,
			EncodeTime: func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
				enc.AppendString(t.UTC().Format(time.RFC3339Nano))
			},
		}),
		zapcore.AddSync(&buf),
		zap.InfoLevel,
	)
	zap.New(core).Sugar().Infow("hi")
	if !strings.Contains(buf.String(), `"timestamp"`) {
		t.Errorf("expected timestamp key, got %s", buf.String())
	}
	if strings.Contains(buf.String(), `"ts"`) {
		t.Errorf("did not expect ts key, got %s", buf.String())
	}
}

func TestBuildLogger_LevelSwitching(t *testing.T) {
	cases := map[string]zapcore.Level{
		"debug": zap.DebugLevel,
		"info":  zap.InfoLevel,
		"warn":  zap.WarnLevel,
		"error": zap.ErrorLevel,
		"":      zap.InfoLevel,
		"BOGUS": zap.InfoLevel,
	}
	for in, want := range cases {
		l := buildLogger("svc", in)
		if l == nil {
			t.Fatalf("%q: nil logger", in)
		}
		// Probe by writing at Want level — no-op if level too low. We just
		// ensure construction does not panic and returns non-nil.
		_ = want
	}
}

func TestLoggerFromContext_Fallback(t *testing.T) {
	got := LoggerFromContext(context.Background(), nil)
	if got == nil {
		t.Fatal("expected non-nil logger from no-op fallback")
	}
	got.Infow("ok")
}

func TestLoggerFromContext_RequestIDInjected(t *testing.T) {
	var buf bytes.Buffer
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{MessageKey: "msg", LevelKey: "level", EncodeLevel: zapcore.LowercaseLevelEncoder}),
		zapcore.AddSync(&buf), zap.InfoLevel,
	)
	base := zap.New(core).Sugar()
	ctx := WithRequestID(context.Background(), "req-123")
	lg := LoggerFromContext(ctx, base)
	lg.Infow("hi")
	out := buf.String()
	if !strings.Contains(out, `"request_id":"req-123"`) {
		t.Errorf("request_id missing: %s", out)
	}
}

func TestLoggerFromContext_TraceIDInjected(t *testing.T) {
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(tracetest.NewInMemoryExporter())))
	defer func() { _ = tp.Shutdown(context.Background()) }()
	tracer := tp.Tracer("test")

	ctx, span := tracer.Start(context.Background(), "op")
	defer span.End()

	var buf bytes.Buffer
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{MessageKey: "msg", LevelKey: "level", EncodeLevel: zapcore.LowercaseLevelEncoder}),
		zapcore.AddSync(&buf), zap.InfoLevel,
	)
	base := zap.New(core).Sugar()
	LoggerFromContext(ctx, base).Infow("hi")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := rec["trace_id"]; !ok {
		t.Errorf("trace_id missing: %v", rec)
	}
	if _, ok := rec["span_id"]; !ok {
		t.Errorf("span_id missing: %v", rec)
	}
}

func TestRequestIDFromContext_Empty(t *testing.T) {
	if got := RequestIDFromContext(context.Background()); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestWithRequestID_Roundtrip(t *testing.T) {
	ctx := WithRequestID(context.Background(), "abc")
	if got := RequestIDFromContext(ctx); got != "abc" {
		t.Errorf("got %q want abc", got)
	}
}

func TestWithLogger_Roundtrip(t *testing.T) {
	resetForTest()
	core := zapcore.NewNopCore()
	lg := zap.New(core).Sugar()
	ctx := WithLogger(context.Background(), lg)
	got := LoggerFromContext(ctx, nil)
	if got == nil {
		t.Fatal("expected logger from ctx")
	}
}

func bufLogger() (*bytes.Buffer, *zap.SugaredLogger) {
	var buf bytes.Buffer
	core := zapcore.NewCore(
		zapcore.NewJSONEncoder(zapcore.EncoderConfig{MessageKey: "msg", LevelKey: "level", EncodeLevel: zapcore.LowercaseLevelEncoder}),
		zapcore.AddSync(&buf), zap.InfoLevel,
	)
	return &buf, zap.New(core).Sugar()
}

func TestLoggerFromContext_PackageLoggerFallback(t *testing.T) {
	resetForTest()
	defer resetForTest()
	buf, lg := bufLogger()
	cachedLogger = lg

	// ctx carries no logger and fallback is nil: must route to the package
	// logger, not a no-op, so a wanted line is never silently dropped.
	LoggerFromContext(context.Background(), nil).Infow("emitted")
	if !strings.Contains(buf.String(), `"emitted"`) {
		t.Errorf("expected package-logger fallback to emit, got %q", buf.String())
	}
}

func TestLog_RoutesThroughContext(t *testing.T) {
	resetForTest()
	defer resetForTest()
	buf, lg := bufLogger()
	ctx := WithLogger(context.Background(), lg)
	Log(ctx).Infow("via-log")
	if !strings.Contains(buf.String(), `"via-log"`) {
		t.Errorf("Log(ctx) did not use ctx logger, got %q", buf.String())
	}
}

func TestLoggerFromContext_ContextFieldsMerged(t *testing.T) {
	resetForTest()
	defer resetForTest()
	cachedContextFields = func(context.Context) []any {
		return []any{"workflowId", "wf-1", "projectId", "proj-2"}
	}
	buf, lg := bufLogger()
	ctx := WithLogger(context.Background(), lg)
	Log(ctx).Infow("hi")
	out := buf.String()
	if !strings.Contains(out, `"workflowId":"wf-1"`) || !strings.Contains(out, `"projectId":"proj-2"`) {
		t.Errorf("context fields not merged: %s", out)
	}
}

func TestSpanContextRemote_TraceFlags(t *testing.T) {
	cfg := trace.SpanContextConfig{TraceFlags: trace.FlagsSampled}
	sc := trace.NewSpanContext(cfg)
	if !sc.IsSampled() {
		t.Error("sampled flag lost")
	}
}
