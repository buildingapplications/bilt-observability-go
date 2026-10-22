package obs

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHealth_Routes(t *testing.T) {
	ok := func(context.Context) error { return nil }
	failing := func(context.Context) error { return errors.New("db down") }
	h := Health(ok, failing)

	if rec := get(t, h, "/health/live"); rec.Code != http.StatusOK {
		t.Errorf("live: %d", rec.Code)
	}
	rec := get(t, h, "/health/ready")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "db down") {
		t.Errorf("ready: %d %q", rec.Code, rec.Body.String())
	}
	if rec := get(t, Health(ok), "/health/ready"); rec.Code != http.StatusOK {
		t.Errorf("ready with passing check: %d", rec.Code)
	}
	if rec := get(t, h, "/health/other"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown path: %d", rec.Code)
	}
}

func TestHealth_ReadyBudget(t *testing.T) {
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	stuck := func(context.Context) error { <-unblock; return nil }
	start := time.Now()
	rec := get(t, Health(stuck), "/health/ready")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("ready: %d", rec.Code)
	}
	if elapsed := time.Since(start); elapsed > readyBudget+time.Second {
		t.Errorf("ready took %v", elapsed)
	}
}

func TestHealth_RecordsNoSpanError(t *testing.T) {
	resetForTest()
	exporter := newTestTracer(t)
	r := Mux()
	r.Handle("/health/*", Health(func(context.Context) error { return errors.New("down") }))
	if rec := get(t, r, "/health/ready"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready: %d", rec.Code)
	}
	if n := len(exporter.GetSpans()); n != 0 {
		t.Errorf("health probe produced %d spans", n)
	}
}
