package obs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"slices"
	"sync"
	"syscall"
	"time"
)

const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultDrainTimeout      = 15 * time.Second
	hookTimeout              = 15 * time.Second
	telemetryFlushTimeout    = 5 * time.Second
)

// Server is one HTTP listener run by Serve. A zero ReadHeaderTimeout becomes
// 10s; Read/Write/Idle timeouts stay as set, since streams and proxies need none.
// A non-nil TLSConfig serves TLS from it.
type Server struct {
	*http.Server
	// Listener, if set, is used instead of listening on Addr.
	Listener net.Listener
	// DrainTimeout bounds Shutdown before open connections are closed. Default 15s.
	DrainTimeout time.Duration
}

var (
	hooksMu sync.Mutex
	hooks   []func(context.Context) error
)

// OnShutdown registers fn to run in Serve after the servers drain and before
// telemetry shuts down, so its spans and logs are flushed. Hooks run in
// reverse registration order, like defers.
func OnShutdown(fn func(context.Context) error) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	hooks = append(hooks, fn)
}

// Serve binds every server, then serves them until SIGINT, SIGTERM, ctx
// cancellation or the first serve error. It then drains all servers
// concurrently, runs the OnShutdown hooks, and calls the Shutdown returned by
// Init. Callers should not call that Shutdown themselves. The error joins the
// first listen error with any shutdown errors.
func Serve(ctx context.Context, servers ...Server) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var listenErr, drainErr error
	lns, err := listenAll(servers)
	if err != nil {
		listenErr = err
	} else {
		listenErr = serveUntilDone(ctx, servers, lns)
		stop()
		drainErr = drainAll(context.WithoutCancel(ctx), servers)
	}
	return errors.Join(listenErr, drainErr, finish(context.WithoutCancel(ctx)))
}

func listenAll(servers []Server) ([]net.Listener, error) {
	lns := make([]net.Listener, 0, len(servers))
	for _, s := range servers {
		if s.Server == nil {
			closeAll(lns)
			return nil, errors.New("obs.Serve: Server.Server is nil")
		}
		if s.ReadHeaderTimeout == 0 {
			s.ReadHeaderTimeout = defaultReadHeaderTimeout
		}
		if s.Listener != nil {
			lns = append(lns, s.Listener)
			continue
		}
		addr := s.Addr
		if addr == "" {
			addr = ":http"
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			closeAll(lns)
			return nil, fmt.Errorf("obs.Serve: listen %s: %w", addr, err)
		}
		lns = append(lns, ln)
	}
	return lns, nil
}

func closeAll(lns []net.Listener) {
	for _, ln := range lns {
		_ = ln.Close()
	}
}

func serveUntilDone(ctx context.Context, servers []Server, lns []net.Listener) error {
	errs := make(chan error, len(servers))
	for i, s := range servers {
		ln := lns[i]
		Log(ctx).Infow("http server listening", "addr", ln.Addr().String())
		go func() {
			var err error
			if s.TLSConfig != nil {
				err = s.ServeTLS(ln, "", "")
			} else {
				err = s.Serve(ln)
			}
			if !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("obs.Serve: serve %s: %w", ln.Addr(), err)
			}
		}()
	}
	select {
	case <-ctx.Done():
		Log(ctx).Infow("shutting down", "cause", context.Cause(ctx))
		return nil
	case err := <-errs:
		Log(ctx).Errorw("http server failed, shutting down", "error", err)
		return err
	}
}

func drainAll(ctx context.Context, servers []Server) error {
	errs := make([]error, len(servers))
	var wg sync.WaitGroup
	for i, s := range servers {
		wg.Go(func() {
			timeout := s.DrainTimeout
			if timeout <= 0 {
				timeout = defaultDrainTimeout
			}
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			if err := s.Shutdown(ctx); err != nil {
				_ = s.Close()
				errs[i] = fmt.Errorf("obs.Serve: drain %s: %w", s.Addr, err)
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

func finish(ctx context.Context) error {
	hooksMu.Lock()
	fns := slices.Clone(hooks)
	hooksMu.Unlock()

	var errs []error
	hctx, cancel := context.WithTimeout(ctx, hookTimeout)
	for _, fn := range slices.Backward(fns) {
		if err := fn(hctx); err != nil {
			errs = append(errs, fmt.Errorf("obs.Serve: shutdown hook: %w", err))
		}
	}
	cancel()

	initMu.Lock()
	shut := cachedShut
	initMu.Unlock()
	if shut != nil {
		tctx, cancel := context.WithTimeout(ctx, telemetryFlushTimeout)
		defer cancel()
		if err := shut(tctx); err != nil {
			errs = append(errs, fmt.Errorf("obs.Serve: telemetry: %w", err))
		}
	}
	return errors.Join(errs...)
}
