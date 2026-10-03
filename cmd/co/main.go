// Command co is the Central Orchestrator (SPEC §3). It serves the Margo Workload Management API to
// LOs (SPEC §11.1) and has the operator commands that run next to it: adding sites and issuing
// their tokens (SPEC §15.6), and the restore procedure (SPEC §12).
//
// It is configured from the environment; a .env file in the working directory sets variables that
// are not set. It logs JSON on stderr (SPEC §13.1) and never logs a token or the database URL
// (SPEC §15.4).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/balaji-balu/ieo/internal/co/store/postgres"
	"github.com/balaji-balu/ieo/internal/contract"
)

const usage = `usage:
  co [serve]                       serve the Margo API on CO_MARGO_ADDR
  co site add <site-id>            add a site and print its token
  co site rotate-token <site-id>   print a new token for a site, replacing its old one
  co sites raise-versions --by N   after a database restore, raise every site's manifestVersion by N
environment:
  DATABASE_URL    Postgres URL of the CO store (required)
  CO_MARGO_ADDR   address of the Margo API (default ` + defaultMargoAddr + `)
  CO_PORT         port of the old API, served only when set (until roadmap slice E)`

const defaultMargoAddr = ":9002"

// config is what co reads from the environment.
type config struct {
	DatabaseURL string // DATABASE_URL
	MargoAddr   string // CO_MARGO_ADDR
	LegacyPort  string // CO_PORT
}

func main() {
	_ = godotenv.Load() // no .env file is fine
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// usageError is a command line or configuration co cannot run.
type usageError string

func (e usageError) Error() string { return string(e) }

// run runs the co command args and returns its exit code: 0 on success, 1 on failure, 2 for a
// usage error. Only a token goes to stdout; logs go to stderr.
func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	log := slog.New(slog.NewJSONHandler(stderr, nil))
	if len(args) == 1 && (args[0] == "help" || args[0] == "-h" || args[0] == "--help") {
		_, _ = fmt.Fprintln(stdout, usage) // nothing to do about a failed write to stdout
		return 0
	}
	cmd, err := parse(args, stdout, log)
	if err == nil {
		cfg := config{DatabaseURL: getenv("DATABASE_URL"), MargoAddr: getenv("CO_MARGO_ADDR"), LegacyPort: getenv("CO_PORT")}
		if cfg.MargoAddr == "" {
			cfg.MargoAddr = defaultMargoAddr
		}
		if cfg.DatabaseURL == "" {
			err = usageError("DATABASE_URL is not set")
		} else {
			err = cmd(ctx, cfg)
		}
	}
	var uerr usageError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &uerr):
		log.Error("invalid command", "error", err.Error(), "usage", usage)
		return 2
	default:
		log.Error("co failed", "error", err.Error())
		return 1
	}
}

type command func(ctx context.Context, cfg config) error

// parse returns the command args name, or a usageError.
func parse(args []string, stdout io.Writer, log *slog.Logger) (command, error) {
	switch {
	case len(args) == 0 || len(args) == 1 && args[0] == "serve":
		return func(ctx context.Context, cfg config) error {
			ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.MargoAddr)
			if err != nil {
				return fmt.Errorf("listen for the Margo API: %w", err)
			}
			return serve(ctx, cfg, ln, log)
		}, nil

	case len(args) >= 2 && args[0] == "site" && (args[1] == "add" || args[1] == "rotate-token"):
		if len(args) != 3 {
			return nil, usageError("co site " + args[1] + " takes one site ID")
		}
		id, err := contract.ParseSiteID(args[2])
		if err != nil {
			return nil, usageError(err.Error())
		}
		op := siteAdd
		if args[1] == "rotate-token" {
			op = siteRotateToken
		}
		return func(ctx context.Context, cfg config) error {
			return withStore(ctx, cfg, func(s *postgres.Store) error { return op(ctx, s, id, stdout, log) })
		}, nil

	case len(args) >= 2 && args[0] == "sites" && args[1] == "raise-versions":
		fs := flag.NewFlagSet("raise-versions", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		by := fs.Uint64("by", 0, "")
		if err := fs.Parse(args[2:]); err != nil {
			return nil, usageError("co sites raise-versions: " + err.Error())
		}
		switch {
		case fs.NArg() > 0:
			return nil, usageError("co sites raise-versions takes no arguments, only --by N")
		case *by == 0:
			return nil, usageError("co sites raise-versions needs --by N, with N at least 1")
		}
		return func(ctx context.Context, cfg config) error {
			return withStore(ctx, cfg, func(s *postgres.Store) error {
				return sitesRaiseVersions(ctx, s, contract.ManifestVersion(*by), log)
			})
		}, nil
	}
	return nil, usageError(fmt.Sprintf("unknown command %q", strings.Join(args, " ")))
}

// withStore opens the CO store, runs f with it, and closes it.
func withStore(ctx context.Context, cfg config, f func(*postgres.Store) error) error {
	s, err := postgres.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	err = f(s)
	if cerr := s.Close(); cerr != nil {
		err = errors.Join(err, fmt.Errorf("close co store: %w", cerr))
	}
	return err
}
