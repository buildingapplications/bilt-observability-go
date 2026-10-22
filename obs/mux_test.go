package obs

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	return rec
}

func TestMux_SetsRouteAndRecovers(t *testing.T) {
	resetForTest()
	exporter := newTestTracer(t)
	r := Mux()
	r.Get("/users/{id}", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/panic", func(http.ResponseWriter, *http.Request) { panic("boom") })

	if rec := get(t, r, "/users/42"); rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if rec := get(t, r, "/panic"); rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic status %d", rec.Code)
	}

	spans := exporter.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("want 2 spans, got %d", len(spans))
	}
	if got, _ := attrString(spans[0].Attributes, "http.route"); got != "/users/{id}" {
		t.Errorf("http.route: got %q", got)
	}
	if spans[0].Name != "GET /users/{id}" {
		t.Errorf("span name: got %q", spans[0].Name)
	}
}

func TestMux_ServerNameDefaultsToService(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)
	exporter := newTestTracer(t)
	InitLogger("bilt-test", "error")
	r := Mux()
	r.Get("/x", func(w http.ResponseWriter, r *http.Request) {})

	get(t, r, "/missing")
	spans := exporter.GetSpans()
	if len(spans) != 1 || spans[0].Name != "bilt-test" {
		t.Fatalf("unmatched-route span name: %v", spans)
	}
}
