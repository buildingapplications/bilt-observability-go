package obs

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// MiddlewareOptions tunes HTTPMiddleware. All fields optional.
type MiddlewareOptions struct {
	// SkipPaths bypass tracing + access logging. Defaults to DefaultHealthPaths().
	SkipPaths []string
	// ServerName overrides the otelhttp server-name attribute. Defaults to ServiceName from Init.
	ServerName string
}

// HTTPMiddleware returns the canonical chi middleware stack:
// RequestID -> RealIP -> Recoverer -> otelhttp -> chi route attribute ->
// context enrichment -> access log. Paths in SkipPaths bypass otelhttp + log.
func HTTPMiddleware(lg *zap.SugaredLogger, opts MiddlewareOptions) func(http.Handler) http.Handler {
	skip := opts.SkipPaths
	if len(skip) == 0 {
		skip = cachedHealth
	}
	if len(skip) == 0 {
		skip = DefaultHealthPaths()
	}
	skipSet := make(map[string]struct{}, len(skip))
	for _, p := range skip {
		skipSet[p] = struct{}{}
	}

	serverName := opts.ServerName
	if serverName == "" && cachedLogger != nil {
		// best-effort: cached logger has service field; we don't extract here.
	}

	return func(next http.Handler) http.Handler {
		stack := next
		stack = accessLog(lg, skipSet)(stack)
		stack = contextEnrich(lg)(stack)
		stack = chiRouteAttr()(stack)
		stack = otelWithSkip(serverName, skipSet)(stack)
		stack = chimiddleware.Recoverer(stack)
		stack = chimiddleware.RealIP(stack)
		stack = chimiddleware.RequestID(stack)
		return stack
	}
}

func otelWithSkip(serverName string, skip map[string]struct{}) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		wrapped := otelhttp.NewMiddleware(serverName)(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := skip[r.URL.Path]; ok {
				next.ServeHTTP(w, r)
				return
			}
			wrapped.ServeHTTP(w, r)
		})
	}
}

func chiRouteAttr() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			if rctx := chi.RouteContext(r.Context()); rctx != nil {
				if pattern := rctx.RoutePattern(); pattern != "" {
					trace.SpanFromContext(r.Context()).SetAttributes(
						attribute.String("http.route", pattern),
					)
				}
			}
		})
	}
}

func contextEnrich(lg *zap.SugaredLogger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rid := chimiddleware.GetReqID(r.Context())
			ctx := WithRequestID(r.Context(), rid)
			ctx = WithLogger(ctx, lg)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func accessLog(lg *zap.SugaredLogger, skip map[string]struct{}) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := skip[r.URL.Path]; ok {
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)

			if lg == nil {
				return
			}
			dur := time.Since(start)
			fields := []any{
				"request_id", chimiddleware.GetReqID(r.Context()),
				"method", r.Method,
				"url", r.URL.String(),
				"status", ww.Status(),
				"duration_ms", dur.Milliseconds(),
				"remote_addr", r.RemoteAddr,
				"user_agent", r.UserAgent(),
				"bytes_written", ww.BytesWritten(),
			}
			if sc := trace.SpanFromContext(r.Context()).SpanContext(); sc.HasTraceID() {
				fields = append(fields, "trace_id", sc.TraceID().String())
				if sc.HasSpanID() {
					fields = append(fields, "span_id", sc.SpanID().String())
				}
			}
			switch {
			case ww.Status() >= 500:
				lg.Errorw("HTTP request", fields...)
			case ww.Status() >= 400:
				lg.Warnw("HTTP request", fields...)
			default:
				lg.Infow("HTTP request", fields...)
			}
		})
	}
}
