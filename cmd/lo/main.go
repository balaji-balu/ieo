// Command lo is the Local Orchestrator (SPEC §3). It keeps its site's desired state in step with
// the CO over the Margo API (SPEC §8.2) and stores it in <lo.data_dir>/lo.db (SPEC §12, ADR 0014).
// Over the site NATS server it takes what the hosts report and sends them the commands that
// converge them on desired state (SPEC §8.5, §11.2, §16.2).
//
// It is configured from the environment and flags, which win (SPEC §6.1, §6.3); a .env file in
// the working directory sets variables that are not set. It logs JSON on stderr (SPEC §13.1) and
// never logs the site token or the NATS password (SPEC §15.4). It exits 0 after SIGINT or SIGTERM,
// 1 on a failure, and 2 on an invalid configuration.
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

	"github.com/balaji-balu/ieo/internal/lo/coapi"
	"github.com/balaji-balu/ieo/internal/lo/outbox"
	"github.com/balaji-balu/ieo/internal/lo/reconcile"
	"github.com/balaji-balu/ieo/internal/lo/sitelink"
	"github.com/balaji-balu/ieo/internal/lo/store"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
	"github.com/balaji-balu/ieo/internal/sitenats"
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
	if err == nil {
		err = natsConfigError(cfg.nats().Check()) // reads lo.nats.ca_file
	}
	if err != nil {
		log.Error("invalid configuration", "error", err.Error())
		return 2
	}
	defer transport.CloseIdleConnections()
	if err := serve(ctx, cfg, transport, log); err != nil {
		log.Error("lo failed", "site_id", string(cfg.SiteID), "error", err.Error())
		return 1
	}
	return 0
}

// serve opens the LO store, connects to the site NATS server, and runs the sync loop, the
// reconciler and the status outbox until ctx ends (SPEC §16.2); when one stops, all do. It fails at startup if the store cannot be opened, and never deletes or
// overwrites the file (SPEC §14.2). A NATS server that cannot be reached is not a failure: the
// connection is made, and made again, in the background (SPEC §11.2, ADR 0020).
func serve(ctx context.Context, cfg config, transport http.RoundTripper, base *slog.Logger) error {
	log := base.With(slog.String("site_id", string(cfg.SiteID)))  // SPEC §13.1
	if strings.HasPrefix(strings.ToLower(cfg.COURL), "http://") { // the scheme is case-insensitive
		// SPEC §15.6: the URL, never the token.
		log.Warn("lo.co_insecure is set: the site token is sent to the CO without TLS", "co_url", cfg.COURL)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", keyDataDir, err)
	}
	storePath := filepath.Join(cfg.DataDir, storeFile)
	s, err := store.OpenBolt(storePath)
	if err != nil {
		return err
	}
	if v := s.MigratedFrom(); v != 0 { // ADR 0015: one way; an LO of the earlier layout refuses the file
		log.Info("LO store migrated to a new layout", "store", storePath, "from_layout", v)
	}
	defer func() {
		if err := s.Close(); err != nil {
			log.Warn("close LO store", "error", err.Error())
		}
	}()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	site, err := sitenats.ForSite(cfg.SiteID)
	if err != nil {
		return err
	}
	natsCfg := cfg.nats()
	natsCfg.Log = log
	conn, err := sitenats.Connect(natsCfg) // returns at once; with a nats:// URL it logs the §15.6 warning
	if err != nil {
		return err
	}
	defer conn.Close() // ADR 0020: last, after the subscriptions and the passes
	link := sitelink.New(conn, site, log)
	// SPEC §8.8: until roadmap slice J, a status the CO refuses is sent again every poll interval.
	box := outbox.New(outbox.Config{
		SiteID: cfg.SiteID, Client: coapi.NewClient(cfg.COURL, cfg.SiteToken, transport),
		RetryInterval: cfg.PollInterval, Log: base, // the Outbox adds site_id
	})
	reconciler, err := reconcile.New(ctx, reconcile.Config{
		SiteID: cfg.SiteID, AckTimeout: cfg.AckTimeout, Interval: cfg.ReconcileInterval, Log: base, // the Reconciler adds site_id
		Status: box, // SPEC §8.7
	}, s, link)
	if err != nil {
		return err
	}
	// SPEC §16.2: subscribe, then ask the hosts for their inventories, now and on every reconnect.
	if err := link.Listen(ctx, reconciler); err != nil {
		return err
	}
	defer link.Stop()
	syncer := losync.New(losync.Config{
		SiteID: cfg.SiteID, COURL: cfg.COURL, Token: cfg.SiteToken, PollInterval: cfg.PollInterval,
		Transport: transport, Log: base, // the Syncer adds site_id
		Reconcile: reconciler.ReconcileAll, // SPEC §8.2 step 7
	}, s)

	errs := make(chan error, 3)
	running := 3
	go func() { errs <- syncer.Run(ctx) }()     // SPEC §16.2: the first tick at delay 0
	go func() { errs <- reconciler.Run(ctx) }() // the passes, and the reconcile safety timer
	go func() { errs <- box.Run(ctx) }()        // SPEC §8.8: the statuses to the CO
	log.Info("syncing with the CO", "co_url", cfg.COURL, "poll_interval", cfg.PollInterval.String())
	var first error
	for range running {
		// The sync loop, the reconciler and the outbox end only with ctx; that is a stop, not a
		// failure.
		if err := <-errs; err != nil && !errors.Is(err, context.Canceled) && first == nil {
			first = err
		}
		cancel() // one stopped: stop the others
		// ADR 0020: the subscriptions end first, so nothing reaches the reconciler while it waits
		// for its passes; then the connection is closed.
		link.Stop()
	}
	log.Info("stopped")
	return first
}
