package obs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const (
	defaultReadHeaderTimeout = 10 * time.Second
	defaultDrainTimeout      = 15 * time.Second
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

// Lifecycle holds a service's shutdown steps. Serve imposes no deadline on
// either step, so each bounds its own work; a nil step is skipped.
type Lifecycle struct {
	// BeforeDrain runs after the stop signal while the servers still serve,
	// e.g. failing readiness and finishing in-flight work, or closing streams
	// that Shutdown would otherwise wait on.
	BeforeDrain func(context.Context) error
	// AfterDrain runs once the servers have drained and before telemetry shuts
	// down, so its spans are flushed: close DB/Redis, stop workers.
	AfterDrain func(context.Context) error
}

// Serve binds every server, then serves them until SIGINT, SIGTERM, ctx
// cancellation or the first serve error. It then runs lc.BeforeDrain, drains
// all servers concurrently, runs lc.AfterDrain, and finally calls the
// Shutdown returned by Init, which callers should not call themselves. The
// error joins the first listen or serve error with any shutdown errors.
func Serve(ctx context.Context, lc Lifecycle, servers ...Server) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	bg := context.WithoutCancel(ctx)

	var serveErr, beforeErr, drainErr error
	if lns, err := listenAll(servers); err != nil {
		serveErr = err
	} else {
		serveErr = serveUntilDone(ctx, servers, lns)
		stop()
		beforeErr = runStep(bg, "before drain", lc.BeforeDrain)
		drainErr = drainAll(bg, servers)
	}
	afterErr := runStep(bg, "after drain", lc.AfterDrain)
	return errors.Join(serveErr, beforeErr, drainErr, afterErr, flushTelemetry(bg))
}

func runStep(ctx context.Context, name string, fn func(context.Context) error) error {
	if fn == nil {
		return nil
	}
	if err := fn(ctx); err != nil {
		return fmt.Errorf("obs.Serve: %s: %w", name, err)
	}
	return nil
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

func flushTelemetry(ctx context.Context) error {
	initMu.Lock()
	shut := cachedShut
	initMu.Unlock()
	if shut == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, telemetryFlushTimeout)
	defer cancel()
	if err := shut(ctx); err != nil {
		return fmt.Errorf("obs.Serve: telemetry: %w", err)
	}
	return nil
}
