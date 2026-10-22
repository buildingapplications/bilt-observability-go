package obs

import "github.com/go-chi/chi/v5"

// Mux returns a chi router with HTTPMiddleware installed, which includes panic
// recovery. Call it after Init: skip paths and the server name come from Config.
func Mux() *chi.Mux {
	r := chi.NewRouter()
	r.Use(HTTPMiddleware(nil, MiddlewareOptions{}))
	return r
}
