package obs

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// MiddlewareOptions tunes HTTPMiddleware. All fields optional.
//
// To attach extra per-request span attributes (auth principal, tenant id,
// etc.), call trace.SpanFromContext(r.Context()).SetAttributes(...) directly
// from a handler or downstream middleware. The span is already in ctx; no
// lib hook needed. For error messages on status >= 400, call
// SetHandlerError(ctx, msg) — the lib promotes it to RecordError + Error
// status + bilt.handler_error attribute on the span.
type MiddlewareOptions struct {
	// SkipPaths bypass tracing. Defaults to DefaultHealthPaths().
	SkipPaths []string
	// ServerName overrides the otelhttp server-name attribute. Defaults to ServiceName from Init.
	ServerName string
}

// HTTPMiddleware returns the canonical chi middleware stack:
// RequestID -> RealIP -> otelhttp -> Recoverer -> error+route capture ->
// context enrichment. Paths in SkipPaths bypass otelhttp entirely.
//
// Recoverer runs INSIDE otelhttp so panic-recovered 500 responses are visible
// on the otel span (otelhttp's deferred span end captures the 500 status that
// Recoverer just wrote).
//
// Handler errors recorded via SetHandlerError are promoted onto the active
// span as a RecordError event + Error status + bilt.handler_error attribute
// when the response status is >= 400. No access log is emitted; the span
// (with trace_id) is the single source of truth for per-request observability.
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

	return func(next http.Handler) http.Handler {
		stack := next
		stack = contextEnrich(lg)(stack)
		stack = captureErrorAndRoute()(stack)
		stack = chimiddleware.Recoverer(stack)
		stack = otelWithSkip(serverName, skipSet)(stack)
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

// captureErrorAndRoute wraps the response so status is readable after the
// handler runs, allocates the handler-error slot, and on the way out:
//   - sets http.route from chi RouteContext
//   - promotes SetHandlerError(msg) onto the span (RecordError + Error status
//   - bilt.handler_error attribute) when status >= 400 and msg is non-empty
func captureErrorAndRoute() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := WithHandlerError(r.Context())
			ww := chimiddleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r.WithContext(ctx))

			span := trace.SpanFromContext(ctx)
			if rctx := chi.RouteContext(ctx); rctx != nil {
				if pattern := rctx.RoutePattern(); pattern != "" {
					span.SetAttributes(attribute.String("http.route", pattern))
				}
			}
			if ww.Status() >= 400 {
				if msg := HandlerError(ctx); msg != "" {
					span.RecordError(errors.New(msg))
					span.SetStatus(codes.Error, msg)
					span.SetAttributes(attribute.String("bilt.handler_error", msg))
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
			if lg != nil {
				ctx = WithLogger(ctx, lg)
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
