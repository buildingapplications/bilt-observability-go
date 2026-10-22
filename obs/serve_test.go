package obs

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"
)

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) add(e string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, e)
}

func (l *eventLog) get() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.events)
}

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func TestServe_DrainsBeforeTelemetryShutdown(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)
	var log eventLog
	initMu.Lock()
	initialized = true
	cachedShut = once(func(context.Context) error { log.add("telemetry"); return nil })
	initMu.Unlock()
	OnShutdown(func(context.Context) error { log.add("hook-first-registered"); return nil })
	OnShutdown(func(context.Context) error { log.add("hook-last-registered"); return nil })

	entered := make(chan struct{})
	release := make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		log.add("request")
	})}
	ln := listen(t)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, Server{Server: srv, Listener: ln}) }()

	reqDone := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		if err == nil {
			_ = resp.Body.Close()
		}
		reqDone <- err
	}()
	<-entered
	cancel()
	time.Sleep(50 * time.Millisecond)
	if got := log.get(); len(got) != 0 {
		t.Fatalf("shutdown ran before the request drained: %v", got)
	}
	close(release)

	if err := <-reqDone; err != nil {
		t.Fatalf("in-flight request: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
	want := []string{"request", "hook-last-registered", "hook-first-registered", "telemetry"}
	if got := log.get(); !slices.Equal(got, want) {
		t.Fatalf("order: got %v want %v", got, want)
	}
	if srv.ReadHeaderTimeout != defaultReadHeaderTimeout || srv.WriteTimeout != 0 {
		t.Errorf("timeouts: ReadHeader %v Write %v", srv.ReadHeaderTimeout, srv.WriteTimeout)
	}
}

func TestServe_ReturnsListenErrorAndStillShutsDown(t *testing.T) {
	resetForTest()
	t.Cleanup(resetForTest)
	var shut bool
	initMu.Lock()
	initialized = true
	cachedShut = func(context.Context) error { shut = true; return nil }
	initMu.Unlock()

	taken := listen(t)
	defer func() { _ = taken.Close() }()
	free := listen(t)

	err := Serve(t.Context(),
		Server{Server: &http.Server{}, Listener: free},
		Server{Server: &http.Server{Addr: taken.Addr().String()}},
	)
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("want listen error, got %v", err)
	}
	if !shut {
		t.Error("telemetry not shut down after listen error")
	}
	if _, err := net.Dial("tcp", free.Addr().String()); err == nil {
		t.Error("pre-bound listener left open")
	}
}

func TestShutdown_IsIdempotent(t *testing.T) {
	calls := 0
	shut := once(func(context.Context) error { calls++; return errors.New("boom") })
	first, second := shut(t.Context()), shut(t.Context())
	if calls != 1 || first == nil || second != first {
		t.Fatalf("calls %d, errors %v / %v", calls, first, second)
	}
}
