package obs

import (
	"context"
	"fmt"
	"net/http"
	"runtime/debug"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

// EndSpan records err (if non-nil) on span as an exception with Error status,
// then ends the span. Typical use with a named return:
//
//	defer func() { obs.EndSpan(span, err) }()
func EndSpan(span trace.Span, err error) {
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	}
	span.End()
}

// Go runs fn in a new goroutine under a BackgroundSpan named name. Errors and
// panics are logged and recorded on the span; panics are recovered so one
// failed task can't take down the process.
func Go(ctx context.Context, name string, fn func(ctx context.Context) error) {
	go func() {
		ctx, span := BackgroundSpan(ctx, name, BackgroundOpts{})
		var err error
		defer func() {
			if rec := recover(); rec != nil {
				_ = recordPanic(Log(ctx).With("task", name), span, rec)
				span.End()
				return
			}
			if err != nil {
				Log(ctx).Errorw("background task failed", "task", name, "error", err)
			}
			EndSpan(span, err)
		}()
		err = fn(ctx)
	}()
}

// Recover turns a panic into *err so the caller's normal failure path runs. It
// logs the panic with its stack and records it on ctx's span. Defer it directly:
//
//	defer obs.Recover(ctx, &err)
func Recover(ctx context.Context, err *error) {
	if rec := recover(); rec != nil {
		*err = recordPanic(Log(ctx), trace.SpanFromContext(ctx), rec)
	}
}

// recoverer replaces chi's Recoverer, which prints to stderr with no trace ID
// and leaves the span without the exception.
func recoverer(lg *zap.SugaredLogger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				ctx := r.Context()
				_ = recordPanic(LoggerFromContext(ctx, lg).With("request_id", chimiddleware.GetReqID(ctx)), trace.SpanFromContext(ctx), rec)
				if r.Header.Get("Connection") != "Upgrade" {
					w.WriteHeader(http.StatusInternalServerError)
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// recordPanic must be called from the deferred recover so the stack still
// contains the panicking frames.
func recordPanic(lg *zap.SugaredLogger, span trace.Span, rec any) error {
	err, ok := rec.(error)
	if ok {
		err = fmt.Errorf("panic: %w", err)
	} else {
		err = fmt.Errorf("panic: %v", rec)
	}
	stack := string(debug.Stack())
	lg.Errorw("panic recovered", "error", err, "stack", stack)
	span.RecordError(err, trace.WithAttributes(attribute.String("exception.stacktrace", stack)))
	span.SetStatus(codes.Error, err.Error())
	return err
}
