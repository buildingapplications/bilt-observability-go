package obs

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// EnrichLogFieldsFunc returns extra key/value pairs to append to the access
// log line. Called once per request after the handler returns. status is the
// final response status. Return nil for no extras.
type EnrichLogFieldsFunc func(ctx context.Context, status int) []any

// MiddlewareOptions tunes HTTPMiddleware. All fields optional.
type MiddlewareOptions struct {
	// SkipPaths bypass tracing + access logging. Defaults to DefaultHealthPaths().
	SkipPaths []string
	// ServerName overrides the otelhttp server-name attribute. Defaults to ServiceName from Init.
	ServerName string
	// EnrichLogFields lets services append extra fields to the access log
	// line (e.g., principal id, tenant id). Receives the final status code so
	// callers can be status-conditional. Use SetHandlerError for per-request
	// error messages — those are auto-included on 4xx/5xx without a hook.
	EnrichLogFields EnrichLogFieldsFunc
	// DisableHandlerError opts out of the automatic handler-error slot.
	// Default: false (slot allocated; SetHandlerError works; access log emits
	// "error" field on status >= 400 if SetHandlerError was called).
	DisableHandlerError bool
}

// HTTPMiddleware returns the canonical chi middleware stack:
// RequestID -> RealIP -> otelhttp -> Recoverer -> chi route attribute ->
// context enrichment -> access log. Paths in SkipPaths bypass otelhttp + log.
//
// Recoverer runs INSIDE otelhttp so panic-recovered 500 responses are visible
// on the otel span (otelhttp's deferred span end captures the 500 status that
// Recoverer just wrote). If Recoverer were outside otelhttp, the span would
// close before Recoverer wrote 500, losing panic attribution in tracing UIs.
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
	enrich := opts.EnrichLogFields
	autoHandlerErr := !opts.DisableHandlerError

	return func(next http.Handler) http.Handler {
		stack := next
		stack = accessLog(lg, skipSet, enrich, autoHandlerErr)(stack)
		stack = contextEnrich(lg)(stack)
		if autoHandlerErr {
			stack = handlerErrorSlot(stack)
		}
		stack = chiRouteAttr()(stack)
		stack = chimiddleware.Recoverer(stack)
		stack = otelWithSkip(serverName, skipSet)(stack)
		stack = chimiddleware.RealIP(stack)
		stack = chimiddleware.RequestID(stack)
		return stack
	}
}

func handlerErrorSlot(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(WithHandlerError(r.Context())))
	})
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

func accessLog(lg *zap.SugaredLogger, skip map[string]struct{}, enrich EnrichLogFieldsFunc, autoHandlerErr bool) func(http.Handler) http.Handler {
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
			if autoHandlerErr && ww.Status() >= 400 {
				if msg := HandlerError(r.Context()); msg != "" {
					fields = append(fields, "error", msg)
				}
			}
			if enrich != nil {
				if extra := enrich(r.Context(), ww.Status()); len(extra) > 0 {
					fields = append(fields, extra...)
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
