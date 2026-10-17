package archive_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/balaji-balu/ieo/internal/en/archive"
)

// SPEC §16.7, ADR 0018: at startup the EN removes "every unfinished extraction under
// `<en.data_dir>/deployments/`", and nothing else.
func TestRemoveStaging(t *testing.T) {
	deployments := filepath.Join(t.TempDir(), "deployments")
	digestDir := filepath.Join(deployments, "0a0a0a0a-0000-4000-8000-00000000000a", "sha256-0123")
	finished := filepath.Join(digestDir, "web")
	unfinished := filepath.Join(digestDir, "db"+archive.StagingInfix+"123456")
	for _, dir := range []string{filepath.Join(finished, "app"), filepath.Join(unfinished, "app")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{filepath.Join(finished, "app", "compose.yaml"), filepath.Join(unfinished, "app", "compose.yaml")} {
		if err := os.WriteFile(file, []byte("services: {}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := archive.RemoveStaging(deployments); err != nil {
		t.Fatalf("RemoveStaging: %v", err)
	}
	if _, err := os.Lstat(unfinished); !os.IsNotExist(err) {
		t.Errorf("the unfinished extraction %s is still there (%v)", unfinished, err)
	}
	if _, err := os.Stat(filepath.Join(finished, "app", "compose.yaml")); err != nil {
		t.Errorf("the finished component was touched: %v", err)
	}
}

// A data directory with no deployments yet holds nothing to remove.
func TestRemoveStagingWithoutDeployments(t *testing.T) {
	if err := archive.RemoveStaging(filepath.Join(t.TempDir(), "deployments")); err != nil {
		t.Errorf("RemoveStaging of a directory that does not exist: %v", err)
	}
}
