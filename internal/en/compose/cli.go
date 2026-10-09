package compose

import (
	"context"
	"errors"
)

// CLIConfig selects the Compose command.
type CLIConfig struct {
	// Runtime is `docker` or `podman` (SPEC §6.3 `en.runtime`); empty means `docker` (ADR 0017).
	Runtime string
	// Path is the runtime's executable; empty means Runtime looked up on PATH.
	Path string
}

// CLI is a Runner that runs `<runtime> compose` as a subprocess (ADR 0003, ADR 0017). The
// subprocess gets only the environment it needs to reach the engine, never the EN's own settings
// or credentials (SPEC §15.6). It is safe for concurrent use.
type CLI struct {
	path string
}

var _ Runner = (*CLI)(nil)

// NewCLI returns a CLI for cfg after checking that `<runtime> compose version` runs, so a host
// without the Compose CLI fails at EN startup (ADR 0003).
func NewCLI(ctx context.Context, cfg CLIConfig) (*CLI, error) {
	return nil, errors.New("not implemented")
}

// Services runs `compose config --services` on the project's files.
func (c *CLI) Services(ctx context.Context, p Project) ([]string, error) {
	return nil, errors.New("not implemented")
}

// Up runs `compose up -d`, with `--wait` when o.Wait is set (SPEC §8.9 step 4.5).
func (c *CLI) Up(ctx context.Context, p Project, o UpOptions) error {
	return errors.New("not implemented")
}

// Down runs `compose down --remove-orphans` on the named project.
func (c *CLI) Down(ctx context.Context, project string) error {
	return errors.New("not implemented")
}
