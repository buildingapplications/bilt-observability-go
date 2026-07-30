package obs

import (
	"net/http"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// HTTPClient wraps base with otelhttp.NewTransport so outbound calls inject
// W3C traceparent + record client spans. If base is nil, http.DefaultClient
// is cloned. base.Transport is preserved as the inner round-tripper.
func HTTPClient(base *http.Client) *http.Client {
	if base == nil {
		c := *http.DefaultClient
		base = &c
	}
	out := *base
	out.Transport = HTTPTransport(base.Transport)
	return &out
}

// HTTPTransport is the RoundTripper-level equivalent of HTTPClient, for
// consumers that take a transport rather than a client (httputil.ReverseProxy,
// SDK transport slots). If base is nil, http.DefaultTransport is wrapped.
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return otelhttp.NewTransport(base, otelhttp.WithSpanNameFormatter(clientSpanName))
}

// clientSpanName names client spans by target host, the outbound counterpart of
// the route rename in HTTPMiddleware: otelhttp's default names every one
// "HTTP POST", collapsing every dependency into one bucket. Host without path —
// paths carry ids (models, sessions, tenants) that would explode cardinality.
func clientSpanName(_ string, r *http.Request) string {
	host := r.URL.Hostname()
	if host == "" {
		return r.Method
	}
	return r.Method + " " + host
}
