package obs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/zap"
)

type captureSink struct {
	mu      sync.Mutex
	entries []string
}

func (c *captureSink) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, string(p))
	return len(p), nil
}
func (c *captureSink) Sync() error { return nil }
func (c *captureSink) lines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.entries))
	copy(out, c.entries)
	return out
}

func TestHTTPMiddleware_HealthSkipped(t *testing.T) {
	resetForTest()
	sink := &captureSink{}
	lg := newCapturedLogger(sink)

	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	otel.SetTracerProvider(tp)
	defer func() { _ = tp.Shutdown(t.Context()) }()

	r := chi.NewRouter()
	r.Use(HTTPMiddleware(lg, MiddlewareOptions{}))
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })
	r.Get("/api/things", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })

	srv := httptest.NewServer(r)
	defer srv.Close()

	httpGet(t, srv.URL+"/health")
	httpGet(t, srv.URL+"/api/things")

	gotSpans := exporter.GetSpans()
	if len(gotSpans) != 1 {
		t.Errorf("expected 1 span (health skipped), got %d", len(gotSpans))
	}

	logs := sink.lines()
	hasHealth := false
	hasThings := false
	for _, l := range logs {
		if strings.Contains(l, "/health") {
			hasHealth = true
		}
		if strings.Contains(l, "/api/things") {
			hasThings = true
		}
	}
	if hasHealth {
		t.Error("/health should not be logged")
	}
	if !hasThings {
		t.Error("/api/things should be logged")
	}
}

func TestHTTPMiddleware_RouteAttribute(t *testing.T) {
	resetForTest()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	otel.SetTracerProvider(tp)
	defer func() { _ = tp.Shutdown(t.Context()) }()

	r := chi.NewRouter()
	r.Use(HTTPMiddleware(zap.NewNop().Sugar(), MiddlewareOptions{SkipPaths: []string{}}))
	r.Get("/users/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })

	srv := httptest.NewServer(r)
	defer srv.Close()

	httpGet(t, srv.URL+"/users/42")

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("no spans recorded")
	}
	found := false
	for _, attr := range spans[0].Attributes {
		if string(attr.Key) == "http.route" && attr.Value.AsString() == "/users/{id}" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("http.route attribute missing or wrong: %v", spans[0].Attributes)
	}
}

func TestHTTPMiddleware_LogStatusBuckets(t *testing.T) {
	resetForTest()
	sink := &captureSink{}
	lg := newCapturedLogger(sink)

	r := chi.NewRouter()
	r.Use(HTTPMiddleware(lg, MiddlewareOptions{SkipPaths: []string{}}))
	r.Get("/ok", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	r.Get("/bad", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(400) })
	r.Get("/err", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })

	srv := httptest.NewServer(r)
	defer srv.Close()

	httpGet(t, srv.URL+"/ok")
	httpGet(t, srv.URL+"/bad")
	httpGet(t, srv.URL+"/err")

	out := strings.Join(sink.lines(), "\n")
	if !strings.Contains(out, `"level":"info"`) {
		t.Error("missing info-level log")
	}
	if !strings.Contains(out, `"level":"warn"`) {
		t.Error("missing warn-level log")
	}
	if !strings.Contains(out, `"level":"error"`) {
		t.Error("missing error-level log")
	}
}

func TestHTTPMiddleware_RequestIDPropagated(t *testing.T) {
	resetForTest()
	sink := &captureSink{}
	lg := newCapturedLogger(sink)

	r := chi.NewRouter()
	r.Use(HTTPMiddleware(lg, MiddlewareOptions{SkipPaths: []string{}}))
	r.Get("/x", func(w http.ResponseWriter, r *http.Request) {
		if RequestIDFromContext(r.Context()) == "" {
			t.Error("expected request_id in handler context")
		}
		w.WriteHeader(200)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()
	httpGet(t, srv.URL+"/x")
}

func TestHTTPMiddleware_PanicRecoveredWith500SpanStatus(t *testing.T) {
	resetForTest()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	otel.SetTracerProvider(tp)
	defer func() { _ = tp.Shutdown(t.Context()) }()

	r := chi.NewRouter()
	r.Use(HTTPMiddleware(zap.NewNop().Sugar(), MiddlewareOptions{SkipPaths: []string{}}))
	r.Get("/boom", func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})

	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/boom")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Errorf("expected 500 from Recoverer, got %d", resp.StatusCode)
	}

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("no spans recorded")
	}
	got := -1
	for _, attr := range spans[0].Attributes {
		if string(attr.Key) == "http.response.status_code" {
			got = int(attr.Value.AsInt64())
			break
		}
		if string(attr.Key) == "http.status_code" {
			got = int(attr.Value.AsInt64())
		}
	}
	if got != 500 {
		t.Errorf("expected span http.status_code=500 (Recoverer wrote 500 inside otelhttp), got %d. Attrs: %v", got, spans[0].Attributes)
	}
}

func TestHTTPMiddleware_NilLogger(t *testing.T) {
	resetForTest()
	r := chi.NewRouter()
	r.Use(HTTPMiddleware(nil, MiddlewareOptions{SkipPaths: []string{}}))
	r.Get("/x", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	srv := httptest.NewServer(r)
	defer srv.Close()
	httpGet(t, srv.URL+"/x")
}

func TestHTTPMiddleware_EnrichLogFields(t *testing.T) {
	resetForTest()
	sink := &captureSink{}
	lg := newCapturedLogger(sink)

	type errKey struct{}
	enrich := func(ctx context.Context, status int) []any {
		if status >= 400 {
			if msg, _ := ctx.Value(errKey{}).(string); msg != "" {
				return []any{"error", msg}
			}
		}
		return nil
	}

	r := chi.NewRouter()
	r.Use(HTTPMiddleware(lg, MiddlewareOptions{
		SkipPaths:       []string{},
		EnrichLogFields: enrich,
	}))
	r.Get("/ok", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	r.Get("/bad", func(w http.ResponseWriter, r *http.Request) {
		*r = *r.WithContext(context.WithValue(r.Context(), errKey{}, "boom"))
		w.WriteHeader(500)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()
	httpGet(t, srv.URL+"/ok")
	httpGet(t, srv.URL+"/bad")

	out := strings.Join(sink.lines(), "\n")
	if strings.Contains(out, `"error":"boom"`) == false {
		t.Errorf("expected error field on 500: %s", out)
	}
	// 200 path should not carry error field
	for _, line := range sink.lines() {
		if strings.Contains(line, "/ok") && strings.Contains(line, `"error":`) {
			t.Errorf("/ok line should not have error: %s", line)
		}
	}
}

func TestHTTPMiddleware_EnrichLogFields_NilSafe(t *testing.T) {
	resetForTest()
	r := chi.NewRouter()
	r.Use(HTTPMiddleware(zap.NewNop().Sugar(), MiddlewareOptions{
		SkipPaths:       []string{},
		EnrichLogFields: nil,
	}))
	r.Get("/x", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	srv := httptest.NewServer(r)
	defer srv.Close()
	httpGet(t, srv.URL+"/x")
}

func httpGet(t *testing.T, url string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
}
