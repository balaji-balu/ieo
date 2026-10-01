// Command sessionstart is the Claude Code SessionStart hook (.claude/settings.json). In a cloud
// session it downloads modules and installs the linter CI uses, so the agent can run the checks in
// CLAUDE.md before pushing. Elsewhere it does nothing: developers install their own tools.
//
// It is a Go program, not a shell script, so the same hook works on Windows (ADR 0009).
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// The linter CI installs; TestLintVersionMatchesCI keeps the two in step.
const (
	lintModule  = "github.com/golangci/golangci-lint/v2/cmd/golangci-lint"
	lintVersion = "v2.5.0"
)

type config struct {
	remote  bool   // CLAUDE_CODE_REMOTE=true: a Claude Code cloud session
	envFile string // CLAUDE_ENV_FILE: lines appended here are exported to the session's shell
	gobin   string // where go install puts binaries
}

type runner func(name string, args ...string) error

func main() {
	ctx := context.Background()
	run := func(name string, args ...string) error { return runCmd(ctx, name, args...) }
	if err := setup(loadConfig(ctx), run); err != nil {
		fmt.Fprintf(os.Stderr, "sessionstart: %v\n", err)
		os.Exit(1)
	}
}

func setup(c config, run runner) error {
	if !c.remote {
		return nil
	}
	if err := run("go", "mod", "download"); err != nil {
		return err
	}
	if err := run("go", "install", lintModule+"@"+lintVersion); err != nil {
		return err
	}
	if c.envFile == "" || c.gobin == "" {
		return nil
	}
	f, err := os.OpenFile(c.envFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	// Cloud sessions are Linux, so the env file is POSIX shell.
	_, err = fmt.Fprintf(f, "export PATH=%q\n", c.gobin+":$PATH")
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func loadConfig(ctx context.Context) config {
	c := config{
		remote:  os.Getenv("CLAUDE_CODE_REMOTE") == "true",
		envFile: os.Getenv("CLAUDE_ENV_FILE"),
		gobin:   os.Getenv("GOBIN"),
	}
	if c.gobin == "" {
		out, err := exec.CommandContext(ctx, "go", "env", "GOPATH").Output()
		if err == nil {
			// GOPATH may list several directories; go install uses the first.
			first := strings.Split(strings.TrimSpace(string(out)), string(os.PathListSeparator))[0]
			c.gobin = filepath.Join(first, "bin")
		}
	}
	return c
}

func runCmd(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr // keep stdout quiet: hook stdout reaches the session context
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}
