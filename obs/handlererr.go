package obs

import "context"

type handlerErrKey struct{}

// WithHandlerError attaches a slot to ctx where a handler can record an error
// message that the access-log middleware reads on the way out. Called
// automatically by HTTPMiddleware unless MiddlewareOptions.DisableHandlerError
// is set, so handlers rarely call this directly.
func WithHandlerError(ctx context.Context) context.Context {
	return context.WithValue(ctx, handlerErrKey{}, new(string))
}

// SetHandlerError records msg on ctx. No-op if WithHandlerError was not called.
// Safe to call from any handler / domain-error mapper.
func SetHandlerError(ctx context.Context, msg string) {
	if p, ok := ctx.Value(handlerErrKey{}).(*string); ok {
		*p = msg
	}
}

// HandlerError returns the recorded message, or "" if absent.
func HandlerError(ctx context.Context) string {
	if p, ok := ctx.Value(handlerErrKey{}).(*string); ok {
		return *p
	}
	return ""
}
