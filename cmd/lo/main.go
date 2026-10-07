// Command lo is the Local Orchestrator (SPEC §3). It keeps its site's desired state in step with
// the CO over the Margo API (SPEC §8.2) and stores it in <lo.data_dir>/lo.db (SPEC §12, ADR 0014).
// Until roadmap slices C3b–E replace it, it also runs the old, Git-based LO when LO_PORT is set
// (legacy.go).
//
// It is configured from the environment and flags, which win (SPEC §6.1, §6.3); a .env file in
// the working directory sets variables that are not set. It logs JSON on stderr (SPEC §13.1) and
// never logs the site token (SPEC §15.4). It exits 0 after SIGINT or SIGTERM, 1 on a failure, and
// 2 on an invalid configuration.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/balaji-balu/ieo/internal/lo/store"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
)

// storeFile is the LO store's file name in lo.data_dir (ADR 0014).
const storeFile = "lo.db"

func main() {
	_ = godotenv.Load() // no .env file is fine
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stderr)
	stop()
	os.Exit(code)
}

// run runs the LO with the flags args until ctx ends, and returns its exit code: 0 once ctx has
// ended, 1 on a failure, 2 on an invalid configuration (SPEC §6.1).
func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) int {
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	cfg, err := loadConfig(args, getenv)
	var transport *http.Transport
	if err == nil {
		if transport, err = newTransport(cfg.CAFile); err != nil {
			err = configError(fmt.Sprintf("%s: %v", keyCAFile, err))
		}
	}
	if err != nil {
		log.Error("invalid configuration", "error", err.Error())
		return 2
	}
	defer transport.CloseIdleConnections()
	if err := serve(ctx, cfg, transport, getenv, log); err != nil {
		log.Error("lo failed", "site_id", string(cfg.SiteID), "error", err.Error())
		return 1
	}
	return 0
}

// serve opens the LO store and runs the sync loop until ctx ends (SPEC §16.2), with the old LO
// beside it when cfg.LegacyPort is set; when either stops, both do. It fails at startup if the
// store cannot be opened, and never deletes or overwrites the file (SPEC §14.2).
func serve(ctx context.Context, cfg config, transport http.RoundTripper, getenv func(string) string, base *slog.Logger) error {
	log := base.With(slog.String("site_id", string(cfg.SiteID))) // SPEC §13.1
	if strings.HasPrefix(cfg.COURL, "http://") {
		// SPEC §15.6: the URL, never the token.
		log.Warn("lo.co_insecure is set: the site token is sent to the CO without TLS", "co_url", cfg.COURL)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", keyDataDir, err)
	}
	s, err := store.OpenBolt(filepath.Join(cfg.DataDir, storeFile))
	if err != nil {
		return err
	}
	defer func() {
		if err := s.Close(); err != nil {
			log.Warn("close LO store", "error", err.Error())
		}
	}()
	syncer := losync.New(losync.Config{
		SiteID: cfg.SiteID, COURL: cfg.COURL, Token: cfg.SiteToken, PollInterval: cfg.PollInterval,
		Transport: transport, Log: base, // the Syncer adds site_id
	}, s)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	running := 1
	go func() { errs <- syncer.Run(ctx) }() // SPEC §16.2: the first tick at delay 0
	if cfg.LegacyPort != "" {
		running++
		go func() { errs <- legacy(ctx, cfg, getenv) }()
	}
	log.Info("syncing with the CO", "co_url", cfg.COURL, "poll_interval", cfg.PollInterval.String())
	var first error
	for range running {
		// The sync loop ends only with ctx; that is a stop, not a failure.
		if err := <-errs; err != nil && !errors.Is(err, context.Canceled) && first == nil {
			first = err
		}
		cancel() // one stopped: stop the other
	}
	log.Info("stopped")
	return first
}
