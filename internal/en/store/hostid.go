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
// error naming the file, and never rewrites it. So does a configured that is not a valid host ID.
// Of ENs starting at once on one directory, every one that succeeds gets the same ID.
func HostID(dataDir string, configured contract.HostID) (contract.HostID, error) {
	if configured != "" {
		if _, err := contract.ParseHostID(configured.String()); err != nil {
			return "", fmt.Errorf("en.host_id: %w", err)
		}
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return "", fmt.Errorf("create EN data directory: %w", err)
	}
	path := filepath.Join(dataDir, HostIDFile)
	id := configured
	if id == "" {
		id = contract.HostID(uuid.NewString()) // lowercase, and only unreserved characters
	}
	err := createSynced(path, []byte(id.String()+"\n"))
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("write host ID file %s: %w", path, err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read host ID file %s: %w", path, err)
	}
	line := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
	id, err = contract.ParseHostID(line)
	if err != nil {
		return "", fmt.Errorf("host ID file %s: %w; remove it to choose a new identity (SPEC §6.2)", path, err)
	}
	if configured != "" && id != configured {
		return "", fmt.Errorf("host ID file %s holds %q but en.host_id is %q; identity changes only when the operator removes the file (SPEC §6.2)", path, id, configured)
	}
	return id, nil
}

// createSynced creates path with mode 0600 and writes content, synced to disk. It fails with
// fs.ErrExist, and changes nothing, if path exists, so of two ENs starting on one directory only
// one writes its ID. A crash while writing leaves a file HostID refuses, naming it; no ID has been
// used yet, so the operator can remove it.
func createSynced(path string, content []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(content)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// syncDir makes a new entry in dir durable. Windows cannot sync a directory handle; NTFS journals
// the change itself.
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
