package archive_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/balaji-balu/ieo/internal/en/archive"
	"github.com/balaji-balu/ieo/internal/en/archive/archivetest"
)

const composeYAML = "services:\n  web:\n    image: nginx\n"

var roomy = archive.Limits{MaxEntries: 100, MaxExtractedBytes: 1 << 20}

// componentDir returns a component's directory as SPEC §9.1 lays it out, in a new data directory.
// Nothing of it exists yet.
func componentDir(t *testing.T) (dataDir, dir string) {
	t.Helper()
	dataDir = t.TempDir()
	return dataDir, filepath.Join(dataDir, "deployments", "6f1d7d3e-9d0c-4a39-8f5d-1b8f0c9f2a11", "sha256-abc", "web")
}

func extract(t *testing.T, dir string, l archive.Limits, entries ...archivetest.Entry) (string, error) {
	t.Helper()
	return archive.Extract(context.Background(), bytes.NewReader(archivetest.Build(t, entries...)), dir, l)
}

// files returns every non-directory under root, as slash paths relative to it.
func files(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return out
}

// assertNothingExtracted checks what SPEC §5.2 promises for an invalid archive: the component's
// directory does not exist, and no file was left anywhere in the data directory.
func assertNothingExtracted(t *testing.T, dataDir, dir string) {
	t.Helper()
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("component directory %s still exists (Lstat error: %v)", dir, err)
	}
	if left := files(t, dataDir); len(left) != 0 {
		t.Errorf("files left behind: %v", left)
	}
	siblings, err := os.ReadDir(filepath.Dir(dir))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("read %s: %v", filepath.Dir(dir), err)
	}
	if len(siblings) != 0 {
		t.Errorf("%d entries left beside the component directory, first %q", len(siblings), siblings[0].Name())
	}
}

func assertInvalid(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, archive.ErrInvalid) {
		t.Errorf("error = %v, want one wrapping archive.ErrInvalid", err)
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

// canSymlink reports whether this process may create symbolic links. Windows allows it only with
// Developer Mode or a privilege (ADR 0018).
func canSymlink(t *testing.T) bool {
	t.Helper()
	return os.Symlink("target", filepath.Join(t.TempDir(), "link")) == nil
}

// A valid archive is extracted under the component's directory and Extract names its top-level
// directory (SPEC §5.2, §9.1).
func TestExtractValidArchive(t *testing.T) {
	_, dir := componentDir(t)
	entries := append(archivetest.Valid("app", composeYAML),
		archivetest.File("app/conf/nginx.conf", "worker_processes 1;\n"), // no entry for app/conf
		archivetest.Dir("app/empty/"))

	top, err := extract(t, dir, roomy, entries...)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if top != "app" {
		t.Errorf("top-level directory = %q, want %q", top, "app")
	}
	if got := readFile(t, filepath.Join(dir, top, archive.ComposeFile)); got != composeYAML {
		t.Errorf("compose.yaml = %q, want %q", got, composeYAML)
	}
	if got := readFile(t, filepath.Join(dir, "app", "conf", "nginx.conf")); got != "worker_processes 1;\n" {
		t.Errorf("conf/nginx.conf = %q", got)
	}
	if fi, err := os.Stat(filepath.Join(dir, "app", "empty")); err != nil || !fi.IsDir() {
		t.Errorf("app/empty is not a directory (err %v)", err)
	}
	siblings, err := os.ReadDir(filepath.Dir(dir))
	if err != nil {
		t.Fatalf("read parent: %v", err)
	}
	if len(siblings) != 1 {
		t.Errorf("%d entries beside the component directory, want only it", len(siblings))
	}
}

// Archives written with a leading "./", as `tar -C dir -czf a.tgz ./app` does, are valid.
func TestExtractAcceptsDotSlashNames(t *testing.T) {
	_, dir := componentDir(t)
	top, err := extract(t, dir, roomy,
		archivetest.Dir("./"), archivetest.Dir("./app/"), archivetest.File("./app/compose.yaml", composeYAML))
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if top != "app" {
		t.Errorf("top-level directory = %q, want %q", top, "app")
	}
	if got := readFile(t, filepath.Join(dir, "app", "compose.yaml")); got != composeYAML {
		t.Errorf("compose.yaml = %q", got)
	}
}

// SPEC §17.6: "Archives are rejected for: two top-level directories; missing `compose.yaml`;
// `docker-compose.yml` instead of `compose.yaml`; absolute path; `..` segment; symlink escaping
// the directory; hard link escaping the directory."
func TestSpec_17_6_ArchiveRejected(t *testing.T) {
	valid := func(more ...archivetest.Entry) []archivetest.Entry {
		return append(archivetest.Valid("app", composeYAML), more...)
	}
	cases := []struct {
		name    string
		entries []archivetest.Entry
	}{
		{"two top-level directories", valid(archivetest.Dir("other/"), archivetest.File("other/notes.txt", "x"))},
		{"missing compose.yaml", []archivetest.Entry{archivetest.Dir("app/"), archivetest.File("app/readme.md", "x")}},
		{"docker-compose.yml instead of compose.yaml", []archivetest.Entry{
			archivetest.Dir("app/"), archivetest.File("app/docker-compose.yml", composeYAML)}},
		{"absolute path", valid(archivetest.File("/app/evil", "x"))},
		{".. segment", valid(archivetest.File("app/../../evil", "x"))},
		{".. segment that stays inside", valid(archivetest.File("app/conf/../evil", "x"))},
		{"symlink escaping the directory", valid(archivetest.Symlink("app/out", "../outside"))},
		{"symlink with an absolute target", valid(archivetest.Symlink("app/out", "/etc/passwd"))},
		{"hard link escaping the directory", valid(archivetest.Hardlink("app/out", "../outside"))},
		{"hard link with an absolute target", valid(archivetest.Hardlink("app/out", "/etc/passwd"))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dataDir, dir := componentDir(t)
			// A hard link that escaped would reach this file.
			outside := filepath.Join(filepath.Dir(dir), "outside")
			_, err := extract(t, dir, roomy, c.entries...)
			assertInvalid(t, err)
			assertNothingExtracted(t, dataDir, dir)
			if _, err := os.Lstat(outside); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("%s exists after extraction", outside)
			}
		})
	}
}

// SPEC §17.6: "Archives are rejected for: an entry that is not a file, directory or link; a path
// held by an earlier entry; a path through a symbolic link; a `compose.yaml` that is a link; a
// symbolic link that leaves the directory only through another link; a hard link to a path no
// earlier entry holds."
func TestSpec_17_6_ArchiveRejectedEntries(t *testing.T) {
	valid := func(more ...archivetest.Entry) []archivetest.Entry {
		return append(archivetest.Valid("app", composeYAML), more...)
	}
	cases := []struct {
		name    string
		entries []archivetest.Entry
	}{
		{"named pipe", valid(archivetest.Entry{Name: "app/pipe", Type: tar.TypeFifo, Mode: 0o644})},
		{"character device", valid(archivetest.Entry{Name: "app/null", Type: tar.TypeChar, Mode: 0o666})},
		{"file at a path a file holds", valid(archivetest.File("app/a", "1"), archivetest.File("app/a", "2"))},
		{"file at a path a directory holds", valid(archivetest.Dir("app/a/"), archivetest.File("app/a", "2"))},
		{"directory at a path a file holds", valid(archivetest.File("app/a", "1"), archivetest.Dir("app/a/"))},
		{"file at a path a symlink holds", valid(archivetest.Symlink("app/a", "compose.yaml"), archivetest.File("app/a", "2"))},
		{"symlink at a path a file holds", valid(archivetest.File("app/a", "1"), archivetest.Symlink("app/a", "compose.yaml"))},
		{"path through a symlink", valid(
			archivetest.Dir("app/sub/"), archivetest.Symlink("app/l", "sub"), archivetest.File("app/l/x", "x"))},
		{"compose.yaml that is a symlink", []archivetest.Entry{
			archivetest.Dir("app/"), archivetest.File("app/real.yaml", composeYAML),
			archivetest.Symlink("app/compose.yaml", "real.yaml")}},
		{"compose.yaml that is a directory", []archivetest.Entry{
			archivetest.Dir("app/"), archivetest.Dir("app/compose.yaml/")}},
		// app/l reads as app/b lexically, but app/a is app itself, so it is the directory above.
		{"symlink leaving through another link", valid(
			archivetest.Symlink("app/a", "."), archivetest.Symlink("app/l", "a/../b"))},
		{"symlink leaving through a later link", valid(
			archivetest.Symlink("app/l", "a/a/../../../b"), archivetest.Symlink("app/a", "."))},
		{"symlinks that never end", valid(archivetest.Symlink("app/a", "b"), archivetest.Symlink("app/b", "a"))},
		{"hard link to a path no entry holds", valid(archivetest.Hardlink("app/h", "app/nope"))},
		{"hard link to a later entry", valid(archivetest.Hardlink("app/h", "app/later"), archivetest.File("app/later", "x"))},
		{"hard link to a directory", valid(archivetest.Dir("app/sub/"), archivetest.Hardlink("app/h", "app/sub"))},
		{"hard link to a symlink", valid(
			archivetest.Symlink("app/l", "compose.yaml"), archivetest.Hardlink("app/h", "app/l"))},
		{"hard link through a symlink", valid(
			archivetest.Dir("app/sub/"), archivetest.File("app/sub/x", "x"),
			archivetest.Symlink("app/l", "sub"), archivetest.Hardlink("app/h", "app/l/x"))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dataDir, dir := componentDir(t)
			_, err := extract(t, dir, roomy, c.entries...)
			assertInvalid(t, err)
			assertNothingExtracted(t, dataDir, dir)
		})
	}
}

// Other ways to break "exactly one top-level directory" (SPEC §5.2), and input that is no archive.
func TestExtractRejects(t *testing.T) {
	valid := archivetest.Build(t, archivetest.Valid("app", composeYAML)...)
	cases := []struct {
		name string
		data []byte
	}{
		{"no entries", archivetest.Build(t)},
		{"file at the top level", archivetest.Build(t, archivetest.File("compose.yaml", composeYAML))},
		{"file beside the top-level directory", archivetest.Build(t,
			append(archivetest.Valid("app", composeYAML), archivetest.File("README", "x"))...)},
		{"top-level entry that is a symlink", archivetest.Build(t, archivetest.Symlink("app", "."))},
		{"entry with no name", archivetest.Build(t,
			append(archivetest.Valid("app", composeYAML), archivetest.File("", "x"))...)},
		{"not gzip", []byte("services: {}\n")},
		{"gzip cut short", valid[:len(valid)/2]},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dataDir, dir := componentDir(t)
			_, err := archive.Extract(context.Background(), bytes.NewReader(c.data), dir, roomy)
			assertInvalid(t, err)
			assertNothingExtracted(t, dataDir, dir)
		})
	}
}

// SPEC §17.6: "setuid, setgid and sticky bits are cleared after extraction."
func TestSpec_17_6_SpecialBitsCleared(t *testing.T) {
	_, dir := componentDir(t)
	entries := []archivetest.Entry{
		{Name: "app/", Type: tar.TypeDir, Mode: 0o1777},        // sticky
		{Name: "app/shared/", Type: tar.TypeDir, Mode: 0o2775}, // setgid
		archivetest.File("app/compose.yaml", composeYAML),
		{Name: "app/suid", Type: tar.TypeReg, Body: "#!/bin/sh\n", Mode: 0o4755},
		{Name: "app/sgid", Type: tar.TypeReg, Body: "#!/bin/sh\n", Mode: 0o2750},
		{Name: "app/all", Type: tar.TypeReg, Body: "x", Mode: 0o7644},
	}
	if _, err := extract(t, dir, roomy, entries...); err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("Windows files have no setuid, setgid or sticky bits (ADR 0018)")
	}
	want := map[string]fs.FileMode{
		"app":              fs.ModeDir | 0o777,
		"app/shared":       fs.ModeDir | 0o775,
		"app/compose.yaml": 0o644,
		"app/suid":         0o755,
		"app/sgid":         0o750,
		"app/all":          0o644,
	}
	for name, mode := range want {
		fi, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatalf("lstat %s: %v", name, err)
		}
		if fi.Mode() != mode {
			t.Errorf("%s has mode %v, want %v", name, fi.Mode(), mode)
		}
	}
}

// SPEC §17.6: "An archive with more than `en.archive.max_entries` entries, or whose files
// decompress to more than `en.archive.max_extracted_bytes`, fails with `IEO-ARCHIVE-INVALID`; no
// more than the limit is written, and nothing extracted is left behind." The error code is the
// EN's mapping of ErrInvalid (roadmap D4).
func TestSpec_17_6_ArchiveLimits(t *testing.T) {
	body := strings.Repeat("x", 500)
	entries := append(archivetest.Valid("app", composeYAML), // 2 entries
		archivetest.File("app/a", body), archivetest.File("app/b", body), archivetest.File("app/c", body))
	size := int64(len(composeYAML) + 3*len(body))

	t.Run("at both limits", func(t *testing.T) {
		_, dir := componentDir(t)
		if _, err := extract(t, dir, archive.Limits{MaxEntries: 5, MaxExtractedBytes: size}, entries...); err != nil {
			t.Fatalf("Extract: %v", err)
		}
	})
	t.Run("one entry over", func(t *testing.T) {
		dataDir, dir := componentDir(t)
		_, err := extract(t, dir, archive.Limits{MaxEntries: 4, MaxExtractedBytes: size}, entries...)
		assertInvalid(t, err)
		assertNothingExtracted(t, dataDir, dir)
	})
	t.Run("one byte over", func(t *testing.T) {
		dataDir, dir := componentDir(t)
		_, err := extract(t, dir, archive.Limits{MaxEntries: 5, MaxExtractedBytes: size - 1}, entries...)
		assertInvalid(t, err)
		assertNothingExtracted(t, dataDir, dir)
	})
	// The file over the limit is refused from its header: its content, which gzip cannot shrink,
	// is never read, so it cannot have been written.
	t.Run("stops reading at the entry over the limit", func(t *testing.T) {
		big := make([]byte, 1<<20)
		if _, err := rand.New(rand.NewSource(1)).Read(big); err != nil {
			t.Fatal(err)
		}
		data := archivetest.Build(t, append(archivetest.Valid("app", composeYAML), archivetest.File("app/big", string(big)))...)
		in := &countingReader{r: bytes.NewReader(data)}
		dataDir, dir := componentDir(t)

		_, err := archive.Extract(context.Background(), in, dir, archive.Limits{MaxEntries: 10, MaxExtractedBytes: 1024})
		assertInvalid(t, err)
		assertNothingExtracted(t, dataDir, dir)
		if in.n > 128<<10 {
			t.Errorf("read %d of %d archive bytes; want it to stop at the header of the file over the limit", in.n, len(data))
		}
	})
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// Links that stay inside the top-level directory are extracted as links (SPEC §5.2).
func TestExtractLinksInsideDirectory(t *testing.T) {
	t.Run("hard link", func(t *testing.T) {
		_, dir := componentDir(t)
		_, err := extract(t, dir, roomy, append(archivetest.Valid("app", composeYAML),
			archivetest.File("app/conf/a.conf", "shared\n"), archivetest.Hardlink("app/b.conf", "app/conf/a.conf"))...)
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		a, err := os.Stat(filepath.Join(dir, "app", "conf", "a.conf"))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.Stat(filepath.Join(dir, "app", "b.conf"))
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(a, b) {
			t.Error("app/b.conf is not a hard link to app/conf/a.conf")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		if !canSymlink(t) {
			t.Skip("this process may not create symbolic links (ADR 0018)")
		}
		_, dir := componentDir(t)
		_, err := extract(t, dir, roomy, append(archivetest.Valid("app", composeYAML),
			archivetest.Symlink("app/conf/current", "../versions/v2"), // made before its target
			archivetest.File("app/versions/v2/app.conf", "v2\n"),
			archivetest.Symlink("app/up", "conf/current/.."), // app/versions, through a link
			archivetest.Symlink("app/missing", "nothing-here"))...)
		if err != nil {
			t.Fatalf("Extract: %v", err)
		}
		if got := readFile(t, filepath.Join(dir, "app", "conf", "current", "app.conf")); got != "v2\n" {
			t.Errorf("read through app/conf/current = %q, want %q", got, "v2\n")
		}
		target, err := os.Readlink(filepath.Join(dir, "app", "conf", "current"))
		if err != nil {
			t.Fatal(err)
		}
		if filepath.ToSlash(target) != "../versions/v2" {
			t.Errorf("app/conf/current points to %q, want %q", target, "../versions/v2")
		}
		if _, err := os.Lstat(filepath.Join(dir, "app", "missing")); err != nil {
			t.Errorf("dangling link app/missing was not extracted: %v", err)
		}
	})
}

// Extract replaces what the directory held; a failed Extract leaves no directory, not the old one.
func TestExtractReplacesDirectory(t *testing.T) {
	dataDir, dir := componentDir(t)
	if _, err := extract(t, dir, roomy, append(archivetest.Valid("old", composeYAML), archivetest.File("old/only-old", "x"))...); err != nil {
		t.Fatalf("first Extract: %v", err)
	}
	top, err := extract(t, dir, roomy, archivetest.Valid("new", "services: {}\n")...)
	if err != nil {
		t.Fatalf("second Extract: %v", err)
	}
	if top != "new" {
		t.Errorf("top-level directory = %q, want %q", top, "new")
	}
	if got, want := files(t, dataDir), []string{"deployments/6f1d7d3e-9d0c-4a39-8f5d-1b8f0c9f2a11/sha256-abc/web/new/compose.yaml"}; !equal(got, want) {
		t.Errorf("files after the second Extract = %v, want %v", got, want)
	}

	_, err = extract(t, dir, roomy, archivetest.File("app/readme.md", "x"))
	assertInvalid(t, err)
	assertNothingExtracted(t, dataDir, dir)
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A cancelled context stops extraction and is not reported as an invalid archive.
func TestExtractContextCancelled(t *testing.T) {
	dataDir, dir := componentDir(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := archive.Extract(ctx, bytes.NewReader(archivetest.Build(t, archivetest.Valid("app", composeYAML)...)), dir, roomy)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
	if errors.Is(err, archive.ErrInvalid) {
		t.Errorf("error %v wraps ErrInvalid; the archive was not at fault", err)
	}
	assertNothingExtracted(t, dataDir, dir)
}

// A failure to read the archive is returned as it is, not as an invalid archive: the EN could not
// tell what the archive holds.
func TestExtractReadError(t *testing.T) {
	dataDir, dir := componentDir(t)
	data := archivetest.Build(t, append(archivetest.Valid("app", composeYAML), archivetest.File("app/a", strings.Repeat("y", 4096)))...)
	errDisk := errors.New("disk read failed")
	in := io.MultiReader(bytes.NewReader(data[:len(data)/2]), errReader{errDisk})

	_, err := archive.Extract(context.Background(), in, dir, roomy)
	if !errors.Is(err, errDisk) {
		t.Errorf("error = %v, want the reader's error", err)
	}
	if errors.Is(err, archive.ErrInvalid) {
		t.Errorf("error %v wraps ErrInvalid; the archive was not at fault", err)
	}
	assertNothingExtracted(t, dataDir, dir)
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
