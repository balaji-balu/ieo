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
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"

	"github.com/balaji-balu/ieo/internal/en/compose"
)

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
	return 1
}
