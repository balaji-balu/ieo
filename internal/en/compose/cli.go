package compose

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/balaji-balu/ieo/internal/contract"
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
	if err := c.run(ctx, "", nil, nil, "compose", "version"); err != nil {
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
	var out bytes.Buffer
	if err := c.run(ctx, p.Dir, nil, &out, append(args, "config", "--services")...); err != nil {
		return nil, fmt.Errorf("list services of compose project %s: %w", p.Name, err)
	}
	var services []string
	for line := range strings.Lines(out.String()) {
		if s := strings.TrimSpace(line); s != "" {
			services = append(services, s)
		}
	}
	return services, nil
}

// Up runs `compose up -d`, with `--wait --wait-timeout` when o.Wait is set (SPEC §8.9 step 4.5).
// A failure once o.Timeout has elapsed is a start timeout: Compose's exit status does not tell
// the two apart. Compose's own --wait-timeout normally ends the command; killGrace later, Up stops
// it (ADR 0017).
func (c *CLI) Up(ctx context.Context, p Project, o UpOptions) error {
	args, err := projectArgs(p)
	if err != nil {
		return err
	}
	args = append(args, "up", "-d")
	timed := o.Wait && o.Timeout > 0
	runCtx := ctx
	if o.Wait {
		args = append(args, "--wait")
	}
	if timed {
		secs := int64((o.Timeout + time.Second - 1) / time.Second)
		args = append(args, "--wait-timeout", strconv.FormatInt(secs, 10))
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, o.Timeout+killGrace)
		defer cancel()
	}
	start := time.Now()
	err = c.run(runCtx, p.Dir, nil, nil, args...)
	switch {
	case err == nil:
		return nil
	case ctx.Err() != nil: // the caller gave up; that is not the project's timeout
		return fmt.Errorf("bring up compose project %s: %w", p.Name, err)
	case timed && time.Since(start) >= o.Timeout:
		return fmt.Errorf("bring up compose project %s within %v: %w: %w", p.Name, o.Timeout, ErrStartTimeout, err)
	default:
		return fmt.Errorf("bring up compose project %s: %w", p.Name, err)
	}
}

// emptyModel is the Compose file Down reads from stdin. Down finds a project's containers and
// networks by its name; giving it an explicit file keeps Compose from loading a compose.yaml or
// .env from the working directory or its parents (ADR 0017).
const emptyModel = "services: {}\n"

// Down runs `compose down --remove-orphans` on the named project. Volumes are kept (ADR 0017).
func (c *CLI) Down(ctx context.Context, project string) error {
	if err := checkName(project); err != nil {
		return err
	}
	err := c.run(ctx, "", strings.NewReader(emptyModel), nil, "compose", "-p", project, "-f", "-", "down", "--remove-orphans")
	if err != nil {
		return fmt.Errorf("bring down compose project %s: %w", project, err)
	}
	return nil
}

// checkName refuses a project name not derived as SPEC §4.2 says (SPEC §9.2).
func checkName(project string) error {
	if !contract.IsComposeProjectName(project) {
		return fmt.Errorf("compose project name %q is not of the form SPEC §4.2 derives", project)
	}
	return nil
}

// projectArgs returns the arguments that select p's project and files (SPEC §5.4 step 2).
func projectArgs(p Project) ([]string, error) {
	if err := checkName(p.Name); err != nil {
		return nil, err
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

// killGrace is how long after a start timeout Up waits for Compose to end before stopping it.
// A variable so tests can shorten it.
var killGrace = 30 * time.Second

// stopDelay is how long a stopped command has to exit after an interrupt before it is killed.
const stopDelay = 5 * time.Second

// run runs the CLI with args in dir (the EN's working directory if empty), the allowlisted
// environment, and stdin if not nil; stdout, if not nil, receives the command's stdout. A
// failure's error carries the combined output, capped.
//
// When ctx is done, the command is interrupted rather than killed, so the `docker` front end can
// pass the signal on to the Compose plugin it runs, and only killed stopDelay later. A run stopped
// by ctx wraps ctx's error.
func (c *CLI) run(ctx context.Context, dir string, stdin io.Reader, stdout io.Writer, args ...string) error {
	cmd := exec.CommandContext(ctx, c.path, args...)
	cmd.Dir = dir
	cmd.Env = cliEnv(os.Environ())
	cmd.Stdin = stdin
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(os.Interrupt); err != nil { // not supported on Windows
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = stopDelay
	combined := &capped{max: maxOutput}
	cmd.Stdout = combined
	if stdout != nil {
		cmd.Stdout = io.MultiWriter(stdout, combined)
	}
	cmd.Stderr = combined
	err := cmd.Run()
	if err == nil {
		return nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		err = fmt.Errorf("%w: %w", ctxErr, err)
	}
	if out := bytes.TrimSpace(combined.bytes()); len(out) > 0 {
		return fmt.Errorf("%w: %s", err, out)
	}
	return err
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
