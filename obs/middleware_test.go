package obs

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

func newTestTracer(t *testing.T) *tracetest.InMemoryExporter {
	t.Helper()
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })
	return exporter
}

func attrString(attrs []attribute.KeyValue, key string) (string, bool) {
	for _, a := range attrs {
		if string(a.Key) == key {
			return a.Value.AsString(), true
		}
	}
	return "", false
}

func TestHTTPMiddleware_HealthSkipped(t *testing.T) {
	resetForTest()
	exporter := newTestTracer(t)

	r := chi.NewRouter()
	r.Use(HTTPMiddleware(zap.NewNop().Sugar(), MiddlewareOptions{}))
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
}

func TestHTTPMiddleware_RouteAttribute(t *testing.T) {
	resetForTest()
	exporter := newTestTracer(t)

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
	got, _ := attrString(spans[0].Attributes, "http.route")
	if got != "/users/{id}" {
		t.Errorf("http.route: got %q want /users/{id}", got)
	}
}

func TestHTTPMiddleware_RequestIDPropagated(t *testing.T) {
	resetForTest()
	r := chi.NewRouter()
	r.Use(HTTPMiddleware(zap.NewNop().Sugar(), MiddlewareOptions{SkipPaths: []string{}}))
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
	exporter := newTestTracer(t)

	r := chi.NewRouter()
	r.Use(HTTPMiddleware(zap.NewNop().Sugar(), MiddlewareOptions{SkipPaths: []string{}}))
	r.Get("/boom", func(w http.ResponseWriter, r *http.Request) { panic("test panic") })

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
		t.Errorf("expected span http.status_code=500, got %d. Attrs: %v", got, spans[0].Attributes)
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

// Direct span.SetAttributes from handler — the documented way to attach
// per-request span attrs. No lib hook needed.
func TestHTTPMiddleware_HandlerSetsSpanAttrs(t *testing.T) {
	resetForTest()
	exporter := newTestTracer(t)

	r := chi.NewRouter()
	r.Use(HTTPMiddleware(zap.NewNop().Sugar(), MiddlewareOptions{SkipPaths: []string{}}))
	r.Get("/users/{id}", func(w http.ResponseWriter, r *http.Request) {
		trace.SpanFromContext(r.Context()).SetAttributes(
			attribute.String("auth.principal_id", "user-7"),
			attribute.String("tenant.id", "t-42"),
		)
		w.WriteHeader(200)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()
	httpGet(t, srv.URL+"/users/abc")

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	pid, _ := attrString(spans[0].Attributes, "auth.principal_id")
	if pid != "user-7" {
		t.Errorf("auth.principal_id: got %q want user-7", pid)
	}
	tid, _ := attrString(spans[0].Attributes, "tenant.id")
	if tid != "t-42" {
		t.Errorf("tenant.id: got %q want t-42", tid)
	}
}

func httpGet(t *testing.T, url string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
}
