package obs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/codes"
	"go.uber.org/zap"
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

func TestHTTPMiddleware_HandlerFault_ReportedAtAnyStatus(t *testing.T) {
	resetForTest()
	exporter := newTestTracer(t)

	r := chi.NewRouter()
	r.Use(HTTPMiddleware(zap.NewNop().Sugar(), MiddlewareOptions{SkipPaths: []string{}}))
	r.Get("/masked", func(w http.ResponseWriter, r *http.Request) {
		SetHandlerFault(r.Context(), errors.New("db down"))
		w.WriteHeader(200)
	})
	r.Get("/broken", func(w http.ResponseWriter, r *http.Request) {
		SetHandlerError(r.Context(), "stale")
		SetHandlerFault(r.Context(), errors.New("db down"))
		w.WriteHeader(500)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()
	httpGet(t, srv.URL+"/masked")
	httpGet(t, srv.URL+"/broken")

	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}
	for _, s := range spans {
		route, _ := attrString(s.Attributes, "http.route")
		if msg, _ := attrString(s.Attributes, "bilt.handler_error"); msg != "db down" {
			t.Errorf("%s: want bilt.handler_error=db down, got %q", route, msg)
		}
		if s.Status.Code != codes.Error {
			t.Errorf("%s: want Error status, got %v", route, s.Status)
		}
		exceptions := 0
		for _, ev := range s.Events {
			if ev.Name == "exception" {
				exceptions++
			}
		}
		if exceptions != 1 {
			t.Errorf("%s: want exactly 1 exception event, got %d", route, exceptions)
		}
	}
}

func TestHTTPMiddleware_HandlerError_PromotedToSpan(t *testing.T) {
	resetForTest()
	exporter := newTestTracer(t)

	r := chi.NewRouter()
	r.Use(HTTPMiddleware(zap.NewNop().Sugar(), MiddlewareOptions{SkipPaths: []string{}}))
	r.Get("/ok", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	r.Get("/bad", func(w http.ResponseWriter, r *http.Request) {
		SetHandlerError(r.Context(), "validation failed")
		w.WriteHeader(400)
	})
	r.Get("/broken", func(w http.ResponseWriter, r *http.Request) {
		SetHandlerError(r.Context(), "db down")
		w.WriteHeader(500)
	})
	r.Get("/no-msg", func(w http.ResponseWriter, r *http.Request) {
		// 500 but handler doesn't call SetHandlerError; no bilt.handler_error attr
		w.WriteHeader(500)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()
	httpGet(t, srv.URL+"/ok")
	httpGet(t, srv.URL+"/bad")
	httpGet(t, srv.URL+"/broken")
	httpGet(t, srv.URL+"/no-msg")

	spans := exporter.GetSpans()
	if len(spans) != 4 {
		t.Fatalf("expected 4 spans, got %d", len(spans))
	}

	for _, s := range spans {
		route, _ := attrString(s.Attributes, "http.route")
		switch route {
		case "/ok":
			if msg, ok := attrString(s.Attributes, "bilt.handler_error"); ok {
				t.Errorf("/ok must not have bilt.handler_error, got %q", msg)
			}
			if s.Status.Code == codes.Error {
				t.Errorf("/ok must not have Error status, got %v", s.Status)
			}
			if len(s.Events) != 0 {
				t.Errorf("/ok must not have events, got %d", len(s.Events))
			}
		case "/bad":
			msg, ok := attrString(s.Attributes, "bilt.handler_error")
			if !ok || msg != "validation failed" {
				t.Errorf("/bad: want bilt.handler_error=validation failed, got %q (ok=%v)", msg, ok)
			}
			if s.Status.Code == codes.Error {
				t.Errorf("/bad: 4xx must not have Error status, got %v", s.Status)
			}
			if len(s.Events) != 0 {
				t.Errorf("/bad: 4xx must not record an exception, events=%v", s.Events)
			}
		case "/broken":
			msg, ok := attrString(s.Attributes, "bilt.handler_error")
			if !ok || msg != "db down" {
				t.Errorf("/broken: want bilt.handler_error=db down, got %q (ok=%v)", msg, ok)
			}
			// Set by otelhttp from the 5xx.
			if s.Status.Code != codes.Error {
				t.Errorf("/broken: want Error status, got %v", s.Status)
			}
			foundEvent := false
			for _, ev := range s.Events {
				if ev.Name == "exception" {
					foundEvent = true
				}
			}
			if !foundEvent {
				t.Errorf("/broken: want exception event from RecordError, events=%v", s.Events)
			}
		case "/no-msg":
			if _, ok := attrString(s.Attributes, "bilt.handler_error"); ok {
				t.Errorf("/no-msg must not have bilt.handler_error (no SetHandlerError called)")
			}
			// otelhttp may auto-set Error on 5xx; we don't assert either way here.
		}
	}
}
