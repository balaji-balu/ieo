package compose

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
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

// maxOutput is how much of a command's output an error carries (ADR 0017).
const maxOutput = 64 << 10

// projectName is the form of contract.ComposeProjectName (SPEC §4.2).
var projectName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// NewCLI returns a CLI for cfg after checking that `<runtime> compose version` runs, so a host
// without the Compose CLI fails at EN startup (ADR 0003).
func NewCLI(ctx context.Context, cfg CLIConfig) (*CLI, error) {
	rt := cfg.Runtime
	switch rt {
	case "":
		rt = "docker"
	case "docker", "podman":
	default:
		return nil, fmt.Errorf("en.runtime %q: want docker or podman", cfg.Runtime)
	}
	path := cfg.Path
	if path == "" {
		p, err := exec.LookPath(rt)
		if err != nil {
			return nil, fmt.Errorf("find the %s CLI: %w", rt, err)
		}
		path = p
	}
	c := &CLI{path: path}
	if _, err := c.run(ctx, "", "compose", "version"); err != nil {
		return nil, fmt.Errorf("check the %s compose CLI: %w", rt, err)
	}
	return c, nil
}

// Services runs `compose config --services` on the project's files.
func (c *CLI) Services(ctx context.Context, p Project) ([]string, error) {
	args, err := projectArgs(p)
	if err != nil {
		return nil, err
	}
	out, err := c.run(ctx, p.Dir, append(args, "config", "--services")...)
	if err != nil {
		return nil, fmt.Errorf("list services of compose project %s: %w", p.Name, err)
	}
	var services []string
	for line := range strings.Lines(string(out)) {
		if s := strings.TrimSpace(line); s != "" {
			services = append(services, s)
		}
	}
	return services, nil
}

// Up runs `compose up -d`, with `--wait` when o.Wait is set (SPEC §8.9 step 4.5). With a timeout,
// the whole command runs under that deadline, and a failure once it has passed is a start
// timeout: Compose's exit status does not tell the two apart (ADR 0017).
func (c *CLI) Up(ctx context.Context, p Project, o UpOptions) error {
	args, err := projectArgs(p)
	if err != nil {
		return err
	}
	args = append(args, "up", "-d")
	runCtx := ctx
	if o.Wait {
		args = append(args, "--wait")
		if o.Timeout > 0 {
			secs := int64((o.Timeout + time.Second - 1) / time.Second)
			args = append(args, "--wait-timeout", strconv.FormatInt(secs, 10))
			var cancel context.CancelFunc
			runCtx, cancel = context.WithTimeout(ctx, o.Timeout)
			defer cancel()
		}
	}
	_, err = c.run(runCtx, p.Dir, args...)
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil: // the caller gave up; that is not the project's timeout
		return fmt.Errorf("bring up compose project %s: %w", p.Name, err)
	case o.Wait && o.Timeout > 0 && runCtx.Err() != nil:
		return fmt.Errorf("bring up compose project %s within %v: %w: %w", p.Name, o.Timeout, ErrStartTimeout, err)
	default:
		return fmt.Errorf("bring up compose project %s: %w", p.Name, err)
	}
}

// Down runs `compose down --remove-orphans` on the named project. Volumes are kept (ADR 0017).
func (c *CLI) Down(ctx context.Context, project string) error {
	if !projectName.MatchString(project) {
		return fmt.Errorf("compose project name %q is not of the form SPEC §4.2 derives", project)
	}
	if _, err := c.run(ctx, "", "compose", "-p", project, "down", "--remove-orphans"); err != nil {
		return fmt.Errorf("bring down compose project %s: %w", project, err)
	}
	return nil
}

// projectArgs returns the arguments that select p's project and files (SPEC §5.4 step 2).
func projectArgs(p Project) ([]string, error) {
	if !projectName.MatchString(p.Name) {
		return nil, fmt.Errorf("compose project name %q is not of the form SPEC §4.2 derives", p.Name)
	}
	if len(p.Files) == 0 || p.Dir == "" {
		return nil, fmt.Errorf("compose project %s: no files or project directory", p.Name)
	}
	args := []string{"compose", "-p", p.Name}
	for _, f := range p.Files {
		args = append(args, "-f", f)
	}
	return append(args, "--project-directory", p.Dir), nil
}

// run runs the CLI with args in dir (the EN's working directory if empty) and the allowlisted
// environment. It returns stdout; a failure's error carries the combined output, capped. A run
// stopped by ctx wraps ctx's error.
func (c *CLI) run(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, c.path, args...)
	cmd.Dir = dir
	cmd.Env = cliEnv(os.Environ())
	cmd.WaitDelay = 5 * time.Second // after a kill, don't wait on pipes a grandchild holds open
	var stdout bytes.Buffer
	combined := &capped{max: maxOutput}
	cmd.Stdout = io.MultiWriter(&stdout, combined)
	cmd.Stderr = combined
	err := cmd.Run()
	if err == nil {
		return stdout.Bytes(), nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = fmt.Errorf("%w: %w", ctxErr, err)
	}
	if out := bytes.TrimSpace(combined.bytes()); len(out) > 0 {
		return nil, fmt.Errorf("%w: %s", err, out)
	}
	return nil, err
}

// capped keeps the first max bytes written to it and drops the rest. Stdout and stderr are copied
// into it by separate goroutines, so it locks.
type capped struct {
	mu  sync.Mutex
	b   []byte
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if room := c.max - len(c.b); room > 0 {
		c.b = append(c.b, p[:min(len(p), room)]...)
	}
	return len(p), nil
}

func (c *capped) bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b
}
