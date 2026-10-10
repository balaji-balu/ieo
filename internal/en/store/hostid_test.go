package store_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/en/store"
)

// A first start with no configured ID generates a lowercase UUID, creates the data directory and
// persists the ID; later starts return it (SPEC §4.1.2, ADR 0016).
func TestHostIDGeneratedAndPersisted(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "en", "data")

	id, err := store.HostID(dataDir, "")
	if err != nil {
		t.Fatalf("HostID: %v", err)
	}
	u, err := uuid.Parse(id.String())
	if err != nil || u.String() != id.String() {
		t.Fatalf("generated ID %q is not a lowercase UUID", id)
	}
	if got := readFile(t, filepath.Join(dataDir, store.HostIDFile)); got != id.String()+"\n" {
		t.Errorf("host.id holds %q, want %q", got, id.String()+"\n")
	}
	again, err := store.HostID(dataDir, "")
	if err != nil {
		t.Fatalf("second HostID: %v", err)
	}
	if again != id {
		t.Errorf("second HostID = %q, want the persisted %q", again, id)
	}
	if runtime.GOOS != "windows" { // on Windows the directory's ACL governs (ADR 0016)
		assertMode(t, dataDir, os.ModeDir|0o700)
		assertMode(t, filepath.Join(dataDir, store.HostIDFile), 0o600)
	}
}

// A configured ID is written on first start and accepted while the file matches it.
func TestHostIDConfigured(t *testing.T) {
	dataDir := t.TempDir()
	for i := range 2 {
		id, err := store.HostID(dataDir, "edge-01")
		if err != nil {
			t.Fatalf("HostID call %d: %v", i+1, err)
		}
		if id != "edge-01" {
			t.Fatalf("HostID call %d = %q, want edge-01", i+1, id)
		}
	}
	if got := readFile(t, filepath.Join(dataDir, store.HostIDFile)); got != "edge-01\n" {
		t.Errorf("host.id holds %q, want %q", got, "edge-01\n")
	}
}

// An existing file is read whether or not it ends in a newline.
func TestHostIDReadsExistingFile(t *testing.T) {
	for _, content := range []string{"edge-01\n", "edge-01", "edge-01\r\n"} {
		dataDir := t.TempDir()
		writeFile(t, filepath.Join(dataDir, store.HostIDFile), content)
		id, err := store.HostID(dataDir, "")
		if err != nil {
			t.Fatalf("file %q: HostID: %v", content, err)
		}
		if id != "edge-01" {
			t.Errorf("file %q: HostID = %q, want edge-01", content, id)
		}
	}
}

// A file that conflicts with the configuration, or holds no valid host ID, stops the EN with an
// error naming the file, and is left as it is: identity never changes without the operator
// (SPEC §6.1, §6.2, ADR 0016).
func TestHostIDConfigurationErrors(t *testing.T) {
	cases := []struct {
		name       string
		content    string
		configured contract.HostID
	}{
		{"differs from en.host_id", "edge-01\n", "edge-02"},
		{"empty file", "", ""},
		{"not unreserved characters", "edge 01\n", ""},
		{"two lines", "edge-01\nedge-02\n", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), store.HostIDFile)
			writeFile(t, path, tc.content)
			_, err := store.HostID(filepath.Dir(path), tc.configured)
			if err == nil {
				t.Fatal("HostID succeeded, want a configuration error")
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error %q does not name %s", err, path)
			}
			if got := readFile(t, path); got != tc.content {
				t.Errorf("host.id changed to %q, want it left as %q", got, tc.content)
			}
		})
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := fi.Mode() & (os.ModeDir | os.ModePerm); got != want {
		t.Errorf("%s has mode %v, want %v", path, got, want)
	}
}

// A configured ID that is not a valid host ID is refused before anything is written (SPEC §4.1.2).
func TestHostIDInvalidConfigured(t *testing.T) {
	dataDir := t.TempDir()
	if _, err := store.HostID(dataDir, "edge 01"); err == nil {
		t.Fatal("HostID with an invalid en.host_id succeeded, want an error")
	}
	if _, err := os.Stat(filepath.Join(dataDir, store.HostIDFile)); !os.IsNotExist(err) {
		t.Errorf("host.id was written for an invalid en.host_id: %v", err)
	}
}

// ENs starting at once on one directory never end up with different IDs: each either gets the ID
// in the file or fails.
func TestHostIDConcurrentFirstStart(t *testing.T) {
	dataDir := t.TempDir()
	const n = 8
	ids := make(chan contract.HostID, n)
	var wg sync.WaitGroup
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if id, err := store.HostID(dataDir, ""); err == nil {
				ids <- id
			}
		}()
	}
	wg.Wait()
	close(ids)
	want := strings.TrimSuffix(readFile(t, filepath.Join(dataDir, store.HostIDFile)), "\n")
	for id := range ids {
		if id.String() != want {
			t.Errorf("an EN got ID %q, but host.id holds %q", id, want)
		}
	}
}
