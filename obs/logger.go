package obs

import (
	"context"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type contextKey int

const (
	requestIDKey contextKey = iota
	loggerKey
)

// Logger returns the package-level logger for serviceName. After Init has run,
// returns the cached logger. Before Init, builds a fresh one (useful in tests
// or pre-init bootstrap).
func Logger(serviceName string) *zap.SugaredLogger {
	if cachedLogger != nil {
		return cachedLogger
	}
	return buildLogger(serviceName, os.Getenv("LOG_LEVEL"))
}

// LoggerFromContext returns the request-scoped logger from ctx, falling back
// to fallback (or a no-op logger if both are nil). Trace ID, span ID, and
// request ID from ctx are auto-merged onto the returned logger.
func LoggerFromContext(ctx context.Context, fallback *zap.SugaredLogger) *zap.SugaredLogger {
	lg := fallback
	if v, ok := ctx.Value(loggerKey).(*zap.SugaredLogger); ok && v != nil {
		lg = v
	}
	if lg == nil {
		lg = zap.NewNop().Sugar()
	}

	if rid := RequestIDFromContext(ctx); rid != "" {
		lg = lg.With("request_id", rid)
	}
	if sc := trace.SpanFromContext(ctx).SpanContext(); sc.HasTraceID() {
		lg = lg.With("trace_id", sc.TraceID().String())
		if sc.HasSpanID() {
			lg = lg.With("span_id", sc.SpanID().String())
		}
	}
	return lg
}

// WithLogger stores a logger on ctx. Used by HTTPMiddleware; rarely called directly.
func WithLogger(ctx context.Context, lg *zap.SugaredLogger) context.Context {
	return context.WithValue(ctx, loggerKey, lg)
}

// WithRequestID stores a request ID on ctx. Used by HTTPMiddleware.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFromContext returns the request ID, or "" if absent.
func RequestIDFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(requestIDKey).(string); ok {
		return v
	}
	return ""
}

func buildLogger(serviceName, level string) *zap.SugaredLogger {
	cfg := zap.NewProductionConfig()
	cfg.EncoderConfig = zapcore.EncoderConfig{
		TimeKey:       "timestamp",
		LevelKey:      "level",
		NameKey:       "logger",
		CallerKey:     "caller",
		MessageKey:    "msg",
		StacktraceKey: "stack",
		EncodeLevel:   zapcore.LowercaseLevelEncoder,
		EncodeTime: func(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
			enc.AppendString(t.UTC().Format(time.RFC3339Nano))
		},
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	switch strings.ToLower(level) {
	case "debug":
		cfg.Level = zap.NewAtomicLevelAt(zap.DebugLevel)
	case "warn":
		cfg.Level = zap.NewAtomicLevelAt(zap.WarnLevel)
	case "error":
		cfg.Level = zap.NewAtomicLevelAt(zap.ErrorLevel)
	default:
		cfg.Level = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	if serviceName != "" {
		cfg.InitialFields = map[string]any{"service": serviceName}
	}

	l, _ := cfg.Build()
	return l.Sugar()
}
