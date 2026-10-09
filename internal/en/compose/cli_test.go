package compose_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/balaji-balu/ieo/internal/en/compose"
)

// These tests run the CLI against a fake `docker`: a copy of this test binary that TestMain turns
// into the fake when it is started under that name (ADR 0009: no shell scripts). The fake reads
// what to do from fake.json beside it and appends each call to calls.jsonl.

// fakeStep is what the fake does for one Compose subcommand.
type fakeStep struct {
	Exit   int           `json:"exit"`
	Stdout string        `json:"stdout"`
	Stderr string        `json:"stderr"`
	Sleep  time.Duration `json:"sleep"`
}

// fakeCall is one recorded run of the fake.
type fakeCall struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
	Dir  string   `json:"dir"`
}

var subcommands = []string{"version", "config", "up", "down"}

func TestMain(m *testing.M) {
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	if name == "docker" || name == "podman" {
		os.Exit(runFake())
	}
	os.Exit(m.Run())
}

func runFake() int {
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake:", err)
		return 99
	}
	dir := filepath.Dir(exe)
	wd, _ := os.Getwd() // recorded only; the tests check it when they care
	call, _ := json.Marshal(fakeCall{Args: os.Args[1:], Env: os.Environ(), Dir: wd})
	f, err := os.OpenFile(filepath.Join(dir, "calls.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake:", err)
		return 99
	}
	_, _ = f.Write(append(call, '\n'))
	_ = f.Close()

	steps := map[string]fakeStep{}
	if b, err := os.ReadFile(filepath.Join(dir, "fake.json")); err == nil {
		if err := json.Unmarshal(b, &steps); err != nil {
			fmt.Fprintln(os.Stderr, "fake:", err)
			return 99
		}
	}
	var step fakeStep
	for _, a := range os.Args[1:] {
		if slices.Contains(subcommands, a) {
			step = steps[a]
			break
		}
	}
	time.Sleep(step.Sleep)
	_, _ = io.WriteString(os.Stdout, step.Stdout)
	_, _ = io.WriteString(os.Stderr, step.Stderr)
	return step.Exit
}

// fake is an installed fake runtime executable.
type fake struct {
	t    *testing.T
	dir  string
	path string
}

// newFake copies this test binary into a new directory under the runtime's name.
func newFake(t *testing.T, runtimeName string, steps map[string]fakeStep) *fake {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	name := runtimeName
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	src, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, src, 0o700); err != nil {
		t.Fatal(err)
	}
	f := &fake{t: t, dir: dir, path: path}
	f.script(steps)
	return f
}

// script replaces what the fake does for each subcommand.
func (f *fake) script(steps map[string]fakeStep) {
	f.t.Helper()
	b, err := json.Marshal(steps)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "fake.json"), b, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// calls returns the recorded runs, oldest first.
func (f *fake) calls() []fakeCall {
	f.t.Helper()
	file, err := os.Open(filepath.Join(f.dir, "calls.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	var calls []fakeCall
	sc := bufio.NewScanner(file)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var c fakeCall
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			f.t.Fatal(err)
		}
		calls = append(calls, c)
	}
	return calls
}

// lastArgs returns the arguments of the latest run.
func (f *fake) lastArgs() []string {
	f.t.Helper()
	calls := f.calls()
	if len(calls) == 0 {
		f.t.Fatal("the fake runtime was not run")
	}
	return calls[len(calls)-1].Args
}

func newCLI(t *testing.T, f *fake) *compose.CLI {
	t.Helper()
	c, err := compose.NewCLI(context.Background(), compose.CLIConfig{Runtime: "docker", Path: f.path})
	if err != nil {
		t.Fatalf("NewCLI: %v", err)
	}
	return c
}

func project(t *testing.T) compose.Project {
	dir := t.TempDir()
	return compose.Project{
		Name:  "0b4f5c1e-6a0e-4d7c-9a51-3f2f8c1d2e01-web",
		Files: []string{filepath.Join(dir, "app", "compose.yaml"), filepath.Join(dir, "compose.ieo.yaml")},
		Dir:   filepath.Join(dir, "app"),
	}
}

// NewCLI checks that the Compose CLI runs, so a host without it fails at startup (ADR 0003).
func TestNewCLIChecksVersion(t *testing.T) {
	f := newFake(t, "docker", nil)
	newCLI(t, f)
	if got, want := f.lastArgs(), []string{"compose", "version"}; !reflect.DeepEqual(got, want) {
		t.Errorf("args = %q, want %q", got, want)
	}

	f.script(map[string]fakeStep{"version": {Exit: 1, Stderr: "docker: 'compose' is not a docker command"}})
	_, err := compose.NewCLI(context.Background(), compose.CLIConfig{Path: f.path})
	if err == nil || !strings.Contains(err.Error(), "is not a docker command") {
		t.Errorf("NewCLI with a failing version = %v, want an error carrying its output", err)
	}
}

func TestNewCLIConfigErrors(t *testing.T) {
	f := newFake(t, "docker", nil)
	cases := []struct {
		name string
		cfg  compose.CLIConfig
	}{
		{"unknown runtime", compose.CLIConfig{Runtime: "containerd", Path: f.path}},
		{"missing executable", compose.CLIConfig{Path: filepath.Join(t.TempDir(), "docker")}},
	}
	for _, tc := range cases {
		if _, err := compose.NewCLI(context.Background(), tc.cfg); err == nil {
			t.Errorf("%s: NewCLI succeeded, want an error", tc.name)
		}
	}
}

// Every Compose command runs with compose.yaml first and compose.ieo.yaml second (SPEC §5.4 step
// 2), in the project directory, under the project name derived from the deployment (SPEC §9.2).
func TestCLIArguments(t *testing.T) {
	f := newFake(t, "docker", map[string]fakeStep{"config": {Stdout: "web\r\ndb\n\n"}})
	c := newCLI(t, f)
	p := project(t)
	ctx := context.Background()
	base := []string{"compose", "-p", p.Name, "-f", p.Files[0], "-f", p.Files[1], "--project-directory", p.Dir}

	services, err := c.Services(ctx, p)
	if err != nil {
		t.Fatalf("Services: %v", err)
	}
	if want := []string{"web", "db"}; !reflect.DeepEqual(services, want) {
		t.Errorf("Services = %q, want %q", services, want)
	}

	cases := []struct {
		name string
		run  func() error
		want []string
	}{
		{"services", func() error { _, err := c.Services(ctx, p); return err }, append(slices.Clone(base), "config", "--services")},
		{"up", func() error { return c.Up(ctx, p, compose.UpOptions{}) }, append(slices.Clone(base), "up", "-d")},
		{"up wait", func() error { return c.Up(ctx, p, compose.UpOptions{Wait: true}) }, append(slices.Clone(base), "up", "-d", "--wait")},
		{"up wait timeout", func() error { return c.Up(ctx, p, compose.UpOptions{Wait: true, Timeout: 90 * time.Second}) },
			append(slices.Clone(base), "up", "-d", "--wait", "--wait-timeout", "90")},
		{"up wait timeout rounds up", func() error { return c.Up(ctx, p, compose.UpOptions{Wait: true, Timeout: 1500 * time.Millisecond}) },
			append(slices.Clone(base), "up", "-d", "--wait", "--wait-timeout", "2")},
		{"up timeout without wait", func() error { return c.Up(ctx, p, compose.UpOptions{Timeout: time.Minute}) }, append(slices.Clone(base), "up", "-d")},
		{"down", func() error { return c.Down(ctx, p.Name) }, []string{"compose", "-p", p.Name, "down", "--remove-orphans"}},
	}
	for _, tc := range cases {
		if err := tc.run(); err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got := f.lastArgs(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: args = %q, want %q", tc.name, got, tc.want)
		}
	}
	for _, call := range f.calls() {
		if slices.Contains(call.Args, "-f") && !sameDir(call.Dir, p.Dir) {
			t.Errorf("%q ran in %s, want the project directory %s", call.Args, call.Dir, p.Dir)
		}
	}
}

func sameDir(a, b string) bool {
	ai, errA := os.Stat(a)
	bi, errB := os.Stat(b)
	return errA == nil && errB == nil && os.SameFile(ai, bi)
}

// A project name not derived as SPEC §4.2 says is refused before anything runs (SPEC §9.2).
func TestCLIRefusesInvalidProjectName(t *testing.T) {
	f := newFake(t, "docker", nil)
	c := newCLI(t, f)
	before := len(f.calls())
	ctx := context.Background()
	for _, name := range []string{"", "Web", "../x", "a b", "-x"} {
		p := project(t)
		p.Name = name
		if _, err := c.Services(ctx, p); err == nil {
			t.Errorf("Services(%q) succeeded, want an error", name)
		}
		if err := c.Up(ctx, p, compose.UpOptions{}); err == nil {
			t.Errorf("Up(%q) succeeded, want an error", name)
		}
		if err := c.Down(ctx, name); err == nil {
			t.Errorf("Down(%q) succeeded, want an error", name)
		}
	}
	if after := len(f.calls()); after != before {
		t.Errorf("the runtime ran %d times for invalid names, want 0", after-before)
	}
}

// A failing command returns an error naming the project and carrying Compose's output, capped.
func TestCLIFailureCarriesOutput(t *testing.T) {
	f := newFake(t, "docker", nil)
	c := newCLI(t, f)
	p := project(t)
	f.script(map[string]fakeStep{"up": {Exit: 1, Stderr: "pull access denied for app"}})
	err := c.Up(context.Background(), p, compose.UpOptions{Wait: true, Timeout: time.Minute})
	if err == nil {
		t.Fatal("Up succeeded, want an error")
	}
	if errors.Is(err, compose.ErrStartTimeout) {
		t.Errorf("Up = %v, want a failure that is not a start timeout", err)
	}
	for _, want := range []string{p.Name, "pull access denied for app"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}

	f.script(map[string]fakeStep{"down": {Exit: 1, Stderr: strings.Repeat("x", 1<<20)}})
	err = c.Down(context.Background(), p.Name)
	if err == nil {
		t.Fatal("Down succeeded, want an error")
	}
	if n := len(err.Error()); n > 70<<10 {
		t.Errorf("error is %d bytes, want the output capped at 64 KiB", n)
	}
}

// With wait, a project whose containers are not running when the timeout elapses fails with
// ErrStartTimeout (SPEC §8.9 step 4.5, ADR 0017).
func TestCLIUpStartTimeout(t *testing.T) {
	f := newFake(t, "docker", nil)
	c := newCLI(t, f)
	p := project(t)

	f.script(map[string]fakeStep{"up": {Sleep: time.Minute}})
	start := time.Now()
	err := c.Up(context.Background(), p, compose.UpOptions{Wait: true, Timeout: 200 * time.Millisecond})
	if !errors.Is(err, compose.ErrStartTimeout) {
		t.Errorf("Up of a project still starting at the timeout = %v, want ErrStartTimeout", err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("Up returned after %v, want it stopped at the timeout", d)
	}

	// Compose's own --wait-timeout ends the command first, with an exit status.
	f.script(map[string]fakeStep{"up": {Sleep: 300 * time.Millisecond, Exit: 1, Stderr: "container app-1 is unhealthy"}})
	err = c.Up(context.Background(), p, compose.UpOptions{Wait: true, Timeout: 200 * time.Millisecond})
	if !errors.Is(err, compose.ErrStartTimeout) {
		t.Errorf("Up failing after the timeout = %v, want ErrStartTimeout", err)
	}

	// Without wait there is no timeout.
	f.script(map[string]fakeStep{"up": {Sleep: 300 * time.Millisecond}})
	if err := c.Up(context.Background(), p, compose.UpOptions{Timeout: 100 * time.Millisecond}); err != nil {
		t.Errorf("Up without wait = %v, want nil", err)
	}
}

// Cancelling the context stops the command.
func TestCLIHonorsContext(t *testing.T) {
	f := newFake(t, "docker", nil)
	c := newCLI(t, f)
	f.script(map[string]fakeStep{"down": {Sleep: time.Minute}})
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.Down(ctx, project(t).Name)
	if err == nil {
		t.Fatal("Down succeeded, want the context's error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Down = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("Down returned after %v, want it stopped with the context", d)
	}
}

// The Compose CLI runs with an environment that holds no tier credential: none of the EN's
// settings, only what the CLI needs to reach the engine (SPEC §15.6, §9.2).
func TestCLIEnvironmentHoldsNoTierCredential(t *testing.T) {
	const secret = "s3cret-nats-password"
	t.Setenv("EN_NATS_PASSWORD", secret)
	t.Setenv("IEO_EN_NATS_PASSWORD", secret)
	t.Setenv("EN_NATS_USERNAME", "site-a")
	t.Setenv("NATS_PASSWORD", secret)
	t.Setenv("LO_SITE_TOKEN", secret)
	t.Setenv("HTTP_PROXY", "http://proxy.invalid:3128")
	t.Setenv("DOCKER_HOST", "tcp://engine.invalid:2375")

	f := newFake(t, "docker", nil)
	c := newCLI(t, f)
	p := project(t)
	ctx := context.Background()
	_, _ = c.Services(ctx, p)
	_ = c.Up(ctx, p, compose.UpOptions{})
	_ = c.Down(ctx, p.Name)

	calls := f.calls()
	if len(calls) != 4 {
		t.Fatalf("the runtime ran %d times, want 4", len(calls))
	}
	for _, call := range calls {
		names := map[string]bool{}
		for _, kv := range call.Env {
			name, value, _ := strings.Cut(kv, "=")
			names[strings.ToUpper(name)] = true
			if strings.Contains(value, secret) {
				t.Errorf("%q: %s carries the secret into the Compose CLI", call.Args, name)
			}
			if strings.HasPrefix(strings.ToUpper(name), "EN_") || strings.HasPrefix(strings.ToUpper(name), "IEO_") {
				t.Errorf("%q: the EN setting %s reached the Compose CLI", call.Args, name)
			}
		}
		if !names["DOCKER_HOST"] || !names["PATH"] {
			t.Errorf("%q: environment %q lacks DOCKER_HOST or PATH, which the CLI needs to reach the engine", call.Args, call.Env)
		}
		if names["HTTP_PROXY"] {
			t.Errorf("%q: HTTP_PROXY reached the Compose CLI, want only the allowlist", call.Args)
		}
	}
}
