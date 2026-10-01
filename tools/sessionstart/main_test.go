package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The hook must install the linter version CI uses, or cloud sessions lint differently from CI.
func TestLintVersionMatchesCI(t *testing.T) {
	ci, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "ci.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if lintModule == "" || lintVersion == "" {
		t.Fatal("lintModule and lintVersion must be set")
	}
	if want := lintModule + "@" + lintVersion; !strings.Contains(string(ci), want) {
		t.Errorf("ci.yaml does not install %s; keep lintVersion and ci.yaml in step", want)
	}
}

func TestRunOutsideCloudDoesNothing(t *testing.T) {
	var calls [][]string
	exec := func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}
	if err := setup(config{remote: false}, exec); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("ran %q outside a cloud session", calls)
	}
}

func TestRunInCloudInstallsToolsAndExportsPath(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env")
	var calls []string
	exec := func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	if err := setup(config{remote: true, envFile: envFile, gobin: "/go/bin"}, exec); err != nil {
		t.Fatalf("setup: %v", err)
	}

	want := []string{"go mod download", "go install " + lintModule + "@" + lintVersion}
	if strings.Join(calls, "\n") != strings.Join(want, "\n") {
		t.Errorf("commands:\n got %q\nwant %q", calls, want)
	}
	got, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	if want := "export PATH=\"/go/bin:$PATH\"\n"; string(got) != want {
		t.Errorf("env file = %q, want %q", got, want)
	}
}
