package obs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestHandlerError_RoundTrip(t *testing.T) {
	ctx := WithHandlerError(context.Background())
	if got := HandlerError(ctx); got != "" {
		t.Errorf("expected empty initial, got %q", got)
	}
	SetHandlerError(ctx, "boom")
	if got := HandlerError(ctx); got != "boom" {
		t.Errorf("got %q want boom", got)
	}
}

func TestHandlerError_NoSlotIsNoop(t *testing.T) {
	ctx := context.Background()
	SetHandlerError(ctx, "ignored")
	if got := HandlerError(ctx); got != "" {
		t.Errorf("expected empty without slot, got %q", got)
	}
}

func TestHTTPMiddleware_AutoHandlerError(t *testing.T) {
	resetForTest()
	sink := &captureSink{}
	lg := newCapturedLogger(sink)

	r := chi.NewRouter()
	r.Use(HTTPMiddleware(lg, MiddlewareOptions{SkipPaths: []string{}}))
	r.Get("/ok", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	r.Get("/bad", func(w http.ResponseWriter, r *http.Request) {
		SetHandlerError(r.Context(), "validation failed")
		w.WriteHeader(400)
	})
	r.Get("/no-msg", func(w http.ResponseWriter, r *http.Request) {
		// status 500 but handler doesn't call SetHandlerError; no error field
		w.WriteHeader(500)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()
	httpGet(t, srv.URL+"/ok")
	httpGet(t, srv.URL+"/bad")
	httpGet(t, srv.URL+"/no-msg")

	out := strings.Join(sink.lines(), "\n")
	if !strings.Contains(out, `"error":"validation failed"`) {
		t.Errorf("expected error field on /bad: %s", out)
	}
	for _, line := range sink.lines() {
		if strings.Contains(line, "/ok") && strings.Contains(line, `"error":`) {
			t.Errorf("/ok must not have error: %s", line)
		}
		if strings.Contains(line, "/no-msg") && strings.Contains(line, `"error":`) {
			t.Errorf("/no-msg with no SetHandlerError must not emit error field: %s", line)
		}
	}
}

func TestHTTPMiddleware_DisableHandlerError(t *testing.T) {
	resetForTest()
	sink := &captureSink{}
	lg := newCapturedLogger(sink)

	r := chi.NewRouter()
	r.Use(HTTPMiddleware(lg, MiddlewareOptions{
		SkipPaths:           []string{},
		DisableHandlerError: true,
	}))
	r.Get("/x", func(w http.ResponseWriter, r *http.Request) {
		// SetHandlerError is no-op because slot was not allocated
		SetHandlerError(r.Context(), "should not appear")
		w.WriteHeader(500)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()
	httpGet(t, srv.URL+"/x")

	out := strings.Join(sink.lines(), "\n")
	if strings.Contains(out, `"error":`) {
		t.Errorf("expected no error field when disabled: %s", out)
	}
}
