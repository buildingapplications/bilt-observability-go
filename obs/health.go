package obs

import (
	"context"
	"net/http"
	"time"
)

const readyBudget = 2 * time.Second

// Health serves /health/live (always 200) and /health/ready, which runs the
// checks concurrently within 2s and answers 503 with the first failure. It
// records nothing on spans. It matches the full path, so mount it as
// r.Handle("/health/*", obs.Health(db.Ping)) or mux.Handle("/health/", ...).
func Health(ready ...func(context.Context) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health/live":
			writeHealth(w, http.StatusOK, "ok")
		case "/health/ready":
			if err := runChecks(r.Context(), ready); err != nil {
				writeHealth(w, http.StatusServiceUnavailable, "not ready: "+err.Error())
				return
			}
			writeHealth(w, http.StatusOK, "ok")
		default:
			http.NotFound(w, r)
		}
	})
}

func runChecks(ctx context.Context, checks []func(context.Context) error) error {
	if len(checks) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, readyBudget)
	defer cancel()
	errs := make(chan error, len(checks))
	for _, check := range checks {
		go func() { errs <- check(ctx) }()
	}
	for range checks {
		select {
		case err := <-errs:
			if err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func writeHealth(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body + "\n"))
}
