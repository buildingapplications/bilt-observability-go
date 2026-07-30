package obs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestHTTPClient_NilBase(t *testing.T) {
	c := HTTPClient(nil)
	if c == nil {
		t.Fatal("expected non-nil client")
	}
	if c.Transport == nil {
		t.Fatal("expected transport set")
	}
}

func TestHTTPClient_PreservesInnerTransport(t *testing.T) {
	called := false
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		called = true
		return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}, Request: r}, nil
	})
	c := HTTPClient(&http.Client{Transport: inner})

	req, _ := http.NewRequestWithContext(context.Background(), "GET", "http://example.invalid/", nil)
	if _, err := c.Do(req); err != nil {
		t.Fatalf("do: %v", err)
	}
	if !called {
		t.Error("inner transport not invoked")
	}
}

func TestHTTPClient_InjectsTraceparent(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	otel.SetTracerProvider(tp)
	defer func() { _ = tp.Shutdown(context.Background()) }()
	otel.SetTextMapPropagator(compositePropagator())

	gotHeader := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("traceparent")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "outbound")
	defer span.End()

	c := HTTPClient(nil)
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	_ = resp.Body.Close()

	if gotHeader == "" {
		t.Error("traceparent header not injected")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPTransport_NilBase(t *testing.T) {
	if HTTPTransport(nil) == nil {
		t.Fatal("expected non-nil transport")
	}
}

func TestHTTPTransport_InjectsTraceparent(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	otel.SetTracerProvider(tp)
	defer func() { _ = tp.Shutdown(context.Background()) }()
	otel.SetTextMapPropagator(compositePropagator())

	gotHeader := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("traceparent")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	tracer := tp.Tracer("test")
	ctx, span := tracer.Start(context.Background(), "outbound")
	defer span.End()

	rt := HTTPTransport(nil)
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
	res, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	_ = res.Body.Close()
	if gotHeader == "" {
		t.Error("traceparent not injected")
	}
}

func TestClientSpanName(t *testing.T) {
	cases := map[string]string{
		"https://bedrock-runtime.eu-north-1.amazonaws.com/model/anthropic.claude/converse-stream": "POST bedrock-runtime.eu-north-1.amazonaws.com",
		"http://tokens:8080/api/v1/usage": "POST tokens", // port belongs on server.port, not the name
		"https://mcp.context7.com/mcp":    "POST mcp.context7.com",
	}
	for rawURL, want := range cases {
		req, err := http.NewRequest(http.MethodPost, rawURL, nil)
		if err != nil {
			t.Fatalf("NewRequest(%q): %v", rawURL, err)
		}
		if got := clientSpanName("", req); got != want {
			t.Errorf("clientSpanName(%q) = %q, want %q", rawURL, got, want)
		}
	}
}

// The recorded span name, not just the formatter, is what dashboards group on.
func TestHTTPTransport_NamesSpanByHost(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exporter)))
	otel.SetTracerProvider(tp)
	defer func() { _ = tp.Shutdown(context.Background()) }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	rt := HTTPTransport(nil)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	res, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("roundtrip: %v", err)
	}
	_ = res.Body.Close()

	spans := exporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	host := req.URL.Hostname()
	if want := "POST " + host; spans[0].Name != want {
		t.Errorf("span name = %q, want %q", spans[0].Name, want)
	}
}
