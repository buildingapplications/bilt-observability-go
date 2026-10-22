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
// lib hook needed.
type MiddlewareOptions struct {
	// SkipPaths bypass tracing. Defaults to DefaultHealthPaths().
	SkipPaths []string
	// ServerName overrides the otelhttp server-name attribute. Defaults to ServiceName from Init.
	ServerName string
}

// HTTPMiddleware returns the canonical chi middleware stack:
// RequestID -> RealIP -> otelhttp -> error+route capture -> recoverer ->
// context enrichment. Paths in SkipPaths bypass otelhttp entirely.
//
// The recoverer runs inside route capture so a panicked request still gets
// its http.route and templated span name.
//
// Handler errors recorded via SetHandlerError are set as the
// bilt.handler_error span attribute when the response status is >= 400, plus
// a RecordError event when it is >= 500. A fault recorded via SetHandlerFault
// is reported at any status: bilt.handler_error, one RecordError, and Error
// span status. No access log is emitted; the span (with trace_id) is the
// single source of truth for per-request observability.
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
		stack = recoverer(lg)(stack)
		stack = captureErrorAndRoute()(stack)
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
//   - promotes SetHandlerFault(err) onto the span at any status
//   - otherwise promotes SetHandlerError(msg) when status >= 400
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
					// Rename the otelhttp server span from the static ServerName
					// to the templated route so spanmetrics yield per-route RED.
					span.SetName(r.Method + " " + pattern)
				}
			}
			if he := slot(ctx); he.fault != nil {
				span.SetAttributes(attribute.String("bilt.handler_error", he.fault.Error()))
				span.RecordError(he.fault)
				span.SetStatus(codes.Error, he.fault.Error())
			} else if ww.Status() >= 400 && he.msg != "" {
				span.SetAttributes(attribute.String("bilt.handler_error", he.msg))
				// A 4xx is the client's fault (OTel server semconv); an exception
				// event on it buries real faults. otelhttp owns the Error status.
				if ww.Status() >= 500 {
					span.RecordError(errors.New(he.msg))
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
