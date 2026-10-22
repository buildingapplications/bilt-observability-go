package obs

import "context"

type handlerErrKey struct{}

type handlerErr struct {
	msg   string
	fault error
}

// WithHandlerError attaches the slot HTTPMiddleware reads. HTTPMiddleware calls it.
func WithHandlerError(ctx context.Context) context.Context {
	return context.WithValue(ctx, handlerErrKey{}, new(handlerErr))
}

// SetHandlerError records msg; HTTPMiddleware reports it only on a status >= 400.
func SetHandlerError(ctx context.Context, msg string) {
	if p := slot(ctx); p != nil {
		p.msg = msg
	}
}

// SetHandlerFault marks the request as failed internally; HTTPMiddleware reports it at any status.
// It wins over SetHandlerError. A nil err is ignored.
func SetHandlerFault(ctx context.Context, err error) {
	if p := slot(ctx); p != nil && err != nil {
		p.msg = err.Error()
		p.fault = err
	}
}

// HandlerError returns the recorded message, or "" if absent.
func HandlerError(ctx context.Context) string {
	if p := slot(ctx); p != nil {
		return p.msg
	}
	return ""
}

func slot(ctx context.Context) *handlerErr {
	p, _ := ctx.Value(handlerErrKey{}).(*handlerErr)
	return p
}
