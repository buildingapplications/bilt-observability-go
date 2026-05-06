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
	inner := base.Transport
	if inner == nil {
		inner = http.DefaultTransport
	}
	out := *base
	out.Transport = otelhttp.NewTransport(inner)
	return &out
}
