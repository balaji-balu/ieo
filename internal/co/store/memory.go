// Package store keeps the CO's state (SPEC §12). Memory is the in-memory backend used by Core
// Conformance tests; the Postgres backend follows with the same operations (roadmap B4).
package store

import (
	"bytes"
	"context"
	"sync"

	"github.com/balaji-balu/ieo/internal/co/catalog"
)

// Memory is an in-memory store. It is safe for concurrent use; its state is lost on exit.
type Memory struct {
	mu   sync.Mutex
	apps map[appKey]catalog.App
}

type appKey struct{ id, version string }

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{apps: map[appKey]catalog.App{}}
}

// PutApp stores app unless its (ID, Version) is already stored, and returns the app stored for
// that (ID, Version).
func (m *Memory) PutApp(_ context.Context, app catalog.App) (catalog.App, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := appKey{app.ID, app.Version}
	if stored, ok := m.apps[k]; ok {
		return cloneApp(stored), nil
	}
	m.apps[k] = cloneApp(app)
	return cloneApp(app), nil
}

// App returns the stored app with that ID and version, and whether there is one.
func (m *Memory) App(_ context.Context, id, version string) (catalog.App, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	app, ok := m.apps[appKey{id, version}]
	return cloneApp(app), ok, nil
}

// cloneApp copies the description bytes, so callers can't change what is stored.
func cloneApp(app catalog.App) catalog.App {
	app.Description = bytes.Clone(app.Description)
	return app
}
