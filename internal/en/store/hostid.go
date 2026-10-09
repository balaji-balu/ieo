// Package store keeps the EN's durable state (SPEC §4.1.12, §9.1, §12): the host ID file and the
// embedded store of applied deployments and their component states, laid out as ADR 0016 says.
package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
)

// HostIDFile is the name of the host ID file in the EN's data directory (SPEC §9.1).
const HostIDFile = "host.id"

// HostID returns the host's ID, kept in <dataDir>/host.id (SPEC §4.1.2, §9.1, ADR 0016). It
// creates dataDir with mode 0700 if it does not exist.
//
// With no file, the ID is configured, or a new lowercase UUID if configured is empty; it is then
// written with mode 0600 and synced to disk before HostID returns, so it is never lost once used.
// With a file, its ID is returned. A file that does not hold a valid host ID, or whose ID differs
// from a non-empty configured, is a configuration error (SPEC §6.1, §6.2): HostID fails with an
// error naming the file, and never rewrites it.
func HostID(dataDir string, configured contract.HostID) (contract.HostID, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", fmt.Errorf("create EN data directory: %w", err)
	}
	path := filepath.Join(dataDir, HostIDFile)
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		id := configured
		if id == "" {
			id = contract.HostID(uuid.NewString()) // lowercase, and only unreserved characters
		}
		if err := writeSynced(path, []byte(id.String()+"\n")); err != nil {
			return "", fmt.Errorf("write host ID file %s: %w", path, err)
		}
		return id, nil
	}
	if err != nil {
		return "", fmt.Errorf("read host ID file %s: %w", path, err)
	}
	line := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	id, err := contract.ParseHostID(line)
	if err != nil {
		return "", fmt.Errorf("host ID file %s: %w; remove it to choose a new identity (SPEC §6.2)", path, err)
	}
	if configured != "" && id != configured {
		return "", fmt.Errorf("host ID file %s holds %q but en.host_id is %q; identity changes only when the operator removes the file (SPEC §6.2)", path, id, configured)
	}
	return id, nil
}

// writeSynced creates path with content, so that after a crash path holds either all of it or
// nothing: it writes a temporary file beside path, syncs it and renames it into place.
func writeSynced(path string, content []byte) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name()) // best effort; the error being returned is the one that matters
		}
	}()
	if _, err := tmp.Write(content); err != nil { // CreateTemp made it with mode 0600
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// syncDir makes a rename in dir durable. Windows cannot sync a directory handle; NTFS journals the
// rename itself.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }() // read-only handle: nothing to lose
	return d.Sync()
}
