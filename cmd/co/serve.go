package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/balaji-balu/ieo/internal/co/api"
	"github.com/balaji-balu/ieo/internal/co/auth"
	"github.com/balaji-balu/ieo/internal/co/catalog"
	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/co/operator"
	"github.com/balaji-balu/ieo/internal/co/store/postgres"
)

const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 30 * time.Second // a whole request; Margo request bodies are small
	idleTimeout       = 2 * time.Minute  // a keep-alive connection between polls
	shutdownTimeout   = 10 * time.Second
	healthTimeout     = 2 * time.Second
)

// newServer returns an HTTP server of h with the CO's timeouts. Its WriteTimeout is left unset:
// an import's response waits on a registry (SPEC §11.3).
func newServer(h http.Handler, log *slog.Logger) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
}

// serve serves the Margo API on margo and the operator API on operator until ctx ends, then shuts
// them down gracefully (SPEC §11.1, §11.3). When cfg.LegacyPort is set it also serves the old API
// there; when one server stops, all do. cfg.OperatorToken must be set.
func serve(ctx context.Context, cfg config, margo, operatorLn net.Listener, log *slog.Logger) error {
	// SPEC §5.3, §6.3: built first, so that co.registry.insecure is warned of at startup.
	registry := catalog.NewRegistry(cfg.RegistryInsecure, log)
	defer registry.Close()
	s, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		_ = margo.Close() // the open error is the one to report
		_ = operatorLn.Close()
		return err
	}
	defer func() {
		if err := s.Close(); err != nil {
			log.Warn("close co store", "error", err.Error())
		}
	}()
	svc := deploy.New(s)
	margoSrv := newServer(handler(s, api.New(auth.New(s), s, svc, log), log), log)
	// No /healthz here: every operator API request needs the token (SPEC §11.3).
	operatorSrv := newServer(operator.New(operator.Config{
		Token: *cfg.OperatorToken, Catalog: catalog.New(registry.Open, s), Deploy: svc, Store: s, Log: log,
	}), log)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 3)
	servers := 2
	go func() { errs <- serveHTTP(ctx, margoSrv, margo) }()
	go func() { errs <- serveHTTP(ctx, operatorSrv, operatorLn) }()
	if cfg.LegacyPort != "" {
		servers++
		go func() { errs <- legacy(ctx, cfg.LegacyPort, cfg.DatabaseURL) }()
	}
	log.Info("serving the Margo API", "addr", margo.Addr().String())
	log.Info("serving the operator API", "addr", operatorLn.Addr().String())
	var first error
	for range servers {
		if err := <-errs; err != nil && first == nil {
			first = err
		}
		cancel() // one server stopped: stop the other
	}
	log.Info("stopped")
	return first
}

// serveHTTP serves srv on ln until ctx ends, then shuts it down, waiting up to shutdownTimeout for
// requests in flight.
func serveHTTP(ctx context.Context, srv *http.Server, ln net.Listener) error {
	errs := make(chan error, 1)
	go func() { errs <- srv.Serve(ln) }()
	select {
	case err := <-errs: // Serve stopped by itself
		return fmt.Errorf("serve %s: %w", ln.Addr(), err)
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		return fmt.Errorf("shut down %s: %w", ln.Addr(), err)
	}
	if err := <-errs; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve %s: %w", ln.Addr(), err)
	}
	return nil
}

// database is the CO store as the health check sees it.
type database interface {
	Ping(ctx context.Context) error
}

// handler serves GET /healthz without authentication, and every other path with api (SPEC §13,
// §11.1). /healthz is 200 while the database answers and 503 when it does not. An outage is
// logged when it starts and when it ends, not on every probe.
func handler(db database, api http.Handler, log *slog.Logger) http.Handler {
	var down atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), healthTimeout)
		defer cancel()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if err := db.Ping(ctx); err != nil {
			if !down.Swap(true) {
				log.Warn("health check: database unavailable", "error", err.Error())
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "database unavailable\n") // the client is gone
			return
		}
		if down.Swap(false) {
			log.Info("health check: database available")
		}
		_, _ = io.WriteString(w, "ok\n") // the client is gone
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	mux.Handle("/", api)
	return mux
}
