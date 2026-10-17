package pull_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/balaji-balu/ieo/internal/en/pull"
)

// SPEC §16.7, ADR 0019: at startup the EN removes "every file in `<en.data_dir>/pull/`".
func TestEmptySpool(t *testing.T) {
	spool := filepath.Join(t.TempDir(), "pull")
	if err := os.MkdirAll(spool, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"layer-123", "layer-456"} {
		if err := os.WriteFile(filepath.Join(spool, name), []byte("part of a layer"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := pull.EmptySpool(spool); err != nil {
		t.Fatalf("EmptySpool: %v", err)
	}
	entries, err := os.ReadDir(spool)
	if err != nil || len(entries) != 0 {
		t.Errorf("the spool holds %d entries after EmptySpool (%v); want none", len(entries), err)
	}
}

// A data directory where no pull ran yet has an empty spool.
func TestEmptySpoolThatDoesNotExist(t *testing.T) {
	if err := pull.EmptySpool(filepath.Join(t.TempDir(), "pull")); err != nil {
		t.Errorf("EmptySpool of a directory that does not exist: %v", err)
	}
}
