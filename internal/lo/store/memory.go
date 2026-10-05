// Package store keeps the LO's state (SPEC §4.1.11). Memory is the in-memory backend used by Core
// Conformance tests.
package store

import (
	"bytes"
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
)

var _ losync.Store = (*Memory)(nil)

// Memory is an in-memory store. It is safe for concurrent use; its state is lost on exit.
type Memory struct {
	mu    sync.Mutex
	state losync.State
}

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{state: losync.State{Desired: map[uuid.UUID]losync.Desired{}}}
}

// Load returns a copy of the stored state.
func (m *Memory) Load(_ context.Context) (losync.State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.state
	st.Desired = cloneDesired(st.Desired)
	return st, nil
}

// ReplaceDesired replaces desired state with a copy of desired.
func (m *Memory) ReplaceDesired(_ context.Context, desired map[uuid.UUID]losync.Desired) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Desired = cloneDesired(desired)
	return nil
}

// CommitVersion records the accepted manifest's version and ETag.
func (m *Memory) CommitVersion(_ context.Context, v contract.ManifestVersion, etag string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Version, m.state.ETag = v, etag
	return nil
}

func cloneDesired(in map[uuid.UUID]losync.Desired) map[uuid.UUID]losync.Desired {
	out := make(map[uuid.UUID]losync.Desired, len(in))
	for id, d := range in {
		d.YAML = bytes.Clone(d.YAML)
		out[id] = d
	}
	return out
}
