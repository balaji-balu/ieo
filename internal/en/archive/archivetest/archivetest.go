// Package archivetest builds Margo Compose Archives for tests (SPEC §5.2, G-F4): gzip tars held in
// memory, valid or broken in a chosen way.
package archivetest

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"testing"
)

// Entry is one entry of an archive, written as given: Build checks nothing, so a test can build
// an archive that breaks a rule.
type Entry struct {
	Name     string
	Type     byte   // a tar type flag
	Body     string // content of a regular file
	Mode     int64  // Unix permission and special bits
	Linkname string // target of a symbolic or hard link
}

// File is a regular file with mode 0644.
func File(name, body string) Entry {
	return Entry{Name: name, Type: tar.TypeReg, Body: body, Mode: 0o644}
}

// Dir is a directory with mode 0755.
func Dir(name string) Entry {
	return Entry{Name: name, Type: tar.TypeDir, Mode: 0o755}
}

// Symlink is a symbolic link at name that points to target, a path relative to the link's
// directory.
func Symlink(name, target string) Entry {
	return Entry{Name: name, Type: tar.TypeSymlink, Linkname: target, Mode: 0o777}
}

// Hardlink is a hard link at name to target, a path from the root of the archive.
func Hardlink(name, target string) Entry {
	return Entry{Name: name, Type: tar.TypeLink, Linkname: target, Mode: 0o644}
}

// Valid returns the entries of a valid archive: the top-level directory top holding compose.yaml
// with content compose.
func Valid(top, compose string) []Entry {
	return []Entry{Dir(top + "/"), File(top+"/compose.yaml", compose)}
}

// Build returns the gzip tar of entries, in order.
func Build(t testing.TB, entries ...Entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := &tar.Header{Name: e.Name, Typeflag: e.Type, Mode: e.Mode, Linkname: e.Linkname, Size: int64(len(e.Body))}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatalf("archivetest: write header %q: %v", e.Name, err)
		}
		if _, err := tw.Write([]byte(e.Body)); err != nil {
			t.Fatalf("archivetest: write %q: %v", e.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("archivetest: close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("archivetest: close gzip: %v", err)
	}
	return buf.Bytes()
}
