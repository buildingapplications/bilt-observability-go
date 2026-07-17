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
	return otelhttp.NewTransport(base)
}
