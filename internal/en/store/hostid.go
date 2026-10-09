// Package store keeps the EN's durable state (SPEC §4.1.12, §9.1, §12): the host ID file and the
// embedded store of applied deployments and their component states, laid out as ADR 0016 says.
package store

import (
	"errors"

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
	return "", errors.New("not implemented")
}
