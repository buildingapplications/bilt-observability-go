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
	defer tp.Shutdown(context.Background())
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
	resp.Body.Close()

	if gotHeader == "" {
		t.Error("traceparent header not injected")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
