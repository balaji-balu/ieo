// Package store keeps the LO's state (SPEC §4.1.11). Memory is the in-memory backend used by Core
// Conformance tests.
package store

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
)

var _ losync.Store = (*Memory)(nil)

// Memory is an in-memory store. It is safe for concurrent use; its state is lost on exit. The
// zero Memory is an empty store.
type Memory struct {
	mu    sync.Mutex
	state losync.State
	// hosts and actual hold the records Bolt would, by host ID, so both backends accept and
	// return exactly the same state.
	hosts, actual map[string][]byte
}

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{
		state: losync.State{Desired: map[uuid.UUID]losync.Desired{}},
		hosts: map[string][]byte{}, actual: map[string][]byte{},
	}
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

// LoadHosts returns a copy of every host the store holds, as Bolt.LoadHosts does.
func (m *Memory) LoadHosts(_ context.Context) (map[contract.HostID]HostState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	each := func(held map[string][]byte) records {
		return func(fn func(k, v []byte) error) error {
			for k, v := range held {
				if err := fn([]byte(k), v); err != nil {
					return err
				}
			}
			return nil
		}
	}
	out, err := decodeHosts(each(m.hosts), each(m.actual))
	if err != nil {
		return nil, fmt.Errorf("load hosts from the LO store: %w", err) // as Bolt words it
	}
	return out, nil
}

// PutActual replaces the actual state of host with a copy of a, as Bolt.PutActual does.
func (m *Memory) PutActual(_ context.Context, host contract.HostID, a HostActual) error {
	key, value, err := encodeActual(host, a)
	if err != nil {
		return fmt.Errorf("put actual state of host %q: %w", host, err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hosts == nil { // the zero Memory is an empty store
		m.hosts, m.actual = map[string][]byte{}, map[string][]byte{}
	}
	if _, ok := m.hosts[string(key)]; !ok {
		m.hosts[string(key)] = newHostRecord()
	}
	m.actual[string(key)] = value
	return nil
}
