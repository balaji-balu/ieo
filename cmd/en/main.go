// Command en is the Edge Node agent (SPEC §3). It runs the deployments the LO sends its host over
// the site NATS server with the Compose CLI (SPEC §8.9, ADR 0003), and reports their state as
// status events and Inventories (SPEC §11.2, §16.6). It keeps what it applied in
// <en.data_dir>/state.db and its host ID in <en.data_dir>/host.id (SPEC §9.1, ADR 0016).
//
// It is configured from the environment and flags, which win (SPEC §6.1, §6.3); a .env file in
// the working directory sets variables that are not set. It logs JSON on stderr (SPEC §13.1) and
// never logs the NATS password or a registry credential (SPEC §15.4). It exits 0 after SIGINT or
// SIGTERM, 1 on a failure, and 2 on an invalid configuration (SPEC §6.1).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/en/agent"
	"github.com/balaji-balu/ieo/internal/en/archive"
	"github.com/balaji-balu/ieo/internal/en/compose"
	"github.com/balaji-balu/ieo/internal/en/exec"
	"github.com/balaji-balu/ieo/internal/en/pull"
	"github.com/balaji-balu/ieo/internal/en/registry"
	"github.com/balaji-balu/ieo/internal/en/store"
	"github.com/balaji-balu/ieo/internal/platform"
	"github.com/balaji-balu/ieo/internal/sitenats"
)

// spoolDir is where the EN keeps a layer while it pulls it, in en.data_dir (SPEC §9.1, ADR 0019).
const spoolDir = "pull"

// newRunner returns the Compose runner of the host (ADR 0003, ADR 0017).
type newRunner func(ctx context.Context, cfg compose.CLIConfig) (compose.Runner, error)

func main() {
	_ = godotenv.Load() // no .env file is fine
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stderr, composeCLI)
	stop()
	os.Exit(code)
}

// composeCLI is the Compose CLI of the host. It fails if `<runtime> compose version` does not run.
func composeCLI(ctx context.Context, cfg compose.CLIConfig) (compose.Runner, error) {
	cli, err := compose.NewCLI(ctx, cfg)
	if err != nil {
		return nil, err // not a nil *CLI in a non-nil Runner
	}
	return cli, nil
}

// run runs the EN with the flags args until ctx ends, and returns its exit code: 0 once ctx has
// ended, 1 on a failure, 2 on an invalid configuration (SPEC §6.1).
func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer, runner newRunner) int {
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	cfg, err := loadConfig(args, getenv)
	if err == nil {
		err = natsConfigError(cfg.nats().Check()) // reads en.nats.ca_file
	}
	if err != nil {
		log.Error("invalid configuration", "error", err.Error())
		return 2
	}
	log = log.With(slog.String("site_id", string(cfg.SiteID))) // SPEC §13.1
	site, host, err := hostOf(cfg)
	if err == nil {
		log = log.With(slog.String("host_id", string(host.ID())))
		err = serve(ctx, cfg, site, host, runner, log)
	}
	switch {
	case err == nil:
		log.Info("stopped")
		return 0
	case isConfigError(err):
		log.Error("invalid configuration", "error", err.Error())
		return 2
	}
	log.Error("en failed", "error", err.Error())
	return 1
}

// hostOf returns the subjects of the EN's site and host. The host ID is the one kept in
// <en.data_dir>/host.id, or en.host_id, or a new one (ADR 0016). A file that does not agree with
// en.host_id, or holds no valid ID, is a configError; a data directory or file that cannot be
// created or read is a failure of the host, not of the configuration.
func hostOf(cfg config) (sitenats.Site, sitenats.Host, error) {
	site, err := sitenats.ForSite(cfg.SiteID) // loadConfig has checked it
	if err != nil {
		return sitenats.Site{}, sitenats.Host{}, configError(fmt.Sprintf("%s: %v", keySiteID, err))
	}
	id, err := store.HostID(cfg.DataDir, cfg.HostID)
	var pathErr *fs.PathError
	switch {
	case errors.As(err, &pathErr):
		return sitenats.Site{}, sitenats.Host{}, fmt.Errorf("%s: %w", keyDataDir, err)
	case err != nil:
		return sitenats.Site{}, sitenats.Host{}, configError(fmt.Sprintf("%s: %v", keyHostID, err))
	}
	host, err := site.Host(id) // an ID in host.id that a subject cannot hold
	if err != nil {
		return sitenats.Site{}, sitenats.Host{}, configError(fmt.Sprintf("%s: %v", keyHostID, err))
	}
	return site, host, nil
}

// isConfigError reports whether err is about the configuration, which exits 2 (SPEC §6.1).
func isConfigError(err error) bool {
	var ce configError
	return errors.As(err, &ce) || errors.Is(err, registry.ErrConfig) || errors.Is(err, sitenats.ErrConfig)
}

// serve runs the EN until ctx ends (SPEC §16.7). It opens the EN store, removes what an earlier
// run left unfinished, and takes the host's commands over the site NATS server. A NATS server that
// cannot be reached is not a failure: the connection is made, and made again, in the background
// (SPEC §11.2, ADR 0020). When ctx ends, the EN takes no more commands, then ends the commands in
// flight and waits for them (SPEC §8.9, §16.7).
//
// It fails at startup if the store cannot be opened, and never deletes or overwrites the file
// (SPEC §14.2, ADR 0016); or if the Compose CLI does not run (ADR 0003).
func serve(ctx context.Context, cfg config, site sitenats.Site, host sitenats.Host, newRunner newRunner, log *slog.Logger) error {
	s, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	defer func() {
		if err := s.Close(); err != nil {
			log.Warn("close EN store", "error", err.Error())
		}
	}()
	// SPEC §16.7: before it subscribes, what an earlier run did not finish.
	if err := archive.RemoveStaging(exec.DeploymentsDir(cfg.DataDir)); err != nil {
		return fmt.Errorf("remove unfinished extractions: %w", err)
	}
	spool := filepath.Join(cfg.DataDir, spoolDir)
	if err := pull.EmptySpool(spool); err != nil {
		return fmt.Errorf("empty %s: %w", spool, err)
	}
	runner, err := newRunner(ctx, compose.CLIConfig{Runtime: cfg.Runtime})
	if err != nil {
		return fmt.Errorf("the Compose CLI: %w", err)
	}
	reg, err := registry.New(registry.Config{AuthFile: cfg.RegistryAuthFile, Insecure: cfg.RegistryInsecure, Log: log})
	if err != nil {
		return configError(fmt.Sprintf("%s: %v", keyRegistryAuthFile, err)) // names the file, never its contents
	}
	defer reg.Close()

	natsCfg := cfg.nats()
	natsCfg.Log = log
	conn, err := sitenats.Connect(natsCfg) // returns at once; with a nats:// URL it logs the §15.6 warning
	if err != nil {
		return err
	}
	defer conn.Close() // ADR 0020: after the subscriptions and the commands
	ag := agent.New(conn, site, host, log)
	ex := exec.New(exec.Config{
		DataDir: cfg.DataDir, Archive: cfg.Archive, StartTimeout: cfg.StartTimeout, OTel: cfg.OTel,
	}, pull.New(reg.Open, spool, cfg.Pull), runner, s, ag, platform.SystemClock(), log)
	// The commands are ended by the EN, after it has stopped taking new ones, not by ctx itself.
	commandsCtx, endCommands := context.WithCancel(context.WithoutCancel(ctx))
	commands := exec.NewDispatcher(commandsCtx, ex, log)
	defer func() { // SPEC §16.7, also when Listen fails: no command is taken, then those in flight end
		ag.Stop()
		endCommands()
		commands.Wait()
	}()
	inventory := func(ctx context.Context) (contract.Inventory, error) { return ex.Inventory(ctx, host.ID()) }
	if err := ag.Listen(ctx, commands, inventory, agent.Config{Interval: cfg.InventoryInterval}); err != nil {
		return err
	}
	log.Info("taking commands", "data_dir", cfg.DataDir, "runtime", cfg.Runtime)

	<-ctx.Done()
	return nil
}
