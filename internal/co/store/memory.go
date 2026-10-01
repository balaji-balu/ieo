// Package store keeps the CO's state (SPEC §12). Memory is the in-memory backend used by Core
// Conformance tests; the Postgres backend follows with the same operations (roadmap B4).
package store

import (
	"bytes"
	"context"
	"maps"
	"sync"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/co/catalog"
	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/contract"
)

// Memory is an in-memory store. It is safe for concurrent use; its state is lost on exit.
type Memory struct {
	mu          sync.Mutex
	apps        map[appKey]catalog.App
	sites       map[contract.SiteID]deploy.Site
	manifests   map[contract.SiteID]deploy.Manifest
	devices     map[contract.DeviceID]contract.DeviceCapabilitiesManifest
	deployments map[uuid.UUID]siteDeployment
	blobs       map[contract.Digest][]byte
}

type appKey struct{ id, version string }

type siteDeployment struct {
	site contract.SiteID
	d    deploy.Deployment
}

// NewMemory returns an empty store.
func NewMemory() *Memory {
	return &Memory{
		apps:        map[appKey]catalog.App{},
		sites:       map[contract.SiteID]deploy.Site{},
		manifests:   map[contract.SiteID]deploy.Manifest{},
		devices:     map[contract.DeviceID]contract.DeviceCapabilitiesManifest{},
		deployments: map[uuid.UUID]siteDeployment{},
		blobs:       map[contract.Digest][]byte{},
	}
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

// PutDevice replaces the latest capabilities reported for a device (SPEC §8.3).
func (m *Memory) PutDevice(_ context.Context, id contract.DeviceID, caps contract.DeviceCapabilitiesManifest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.devices[id] = caps
	return nil
}

// Deployment returns a deployment, and whether it exists.
func (m *Memory) Deployment(_ context.Context, id uuid.UUID) (deploy.Deployment, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sd, ok := m.deployments[id]
	return cloneDeployment(sd.d), ok, nil
}

// DeploymentSite returns the site of a deployment, and whether the deployment exists.
func (m *Memory) DeploymentSite(_ context.Context, id uuid.UUID) (contract.SiteID, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sd, ok := m.deployments[id]
	return sd.site, ok, nil
}

// Manifest returns a site's published manifest, and whether the site has one.
func (m *Memory) Manifest(_ context.Context, site contract.SiteID) (deploy.Manifest, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mf, ok := m.manifests[site]
	return cloneManifest(mf), ok, nil
}

// Blob returns the content stored under digest, and whether there is any.
func (m *Memory) Blob(_ context.Context, digest contract.Digest) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.blobs[digest]
	return bytes.Clone(b), ok, nil
}

// ChangeSite runs change on the state of site and writes what it returns, holding the store's
// lock throughout, so changes are atomic and serialized.
func (m *Memory) ChangeSite(_ context.Context, site contract.SiteID,
	change func(state deploy.SiteState, exists bool) (deploy.SiteChange, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, exists := m.sites[site]
	state := deploy.SiteState{
		Site:        s,
		Hosts:       map[contract.HostID]contract.DeviceCapabilitiesManifest{},
		Deployments: map[uuid.UUID]deploy.Deployment{},
		YAML:        map[uuid.UUID][]byte{},
		Manifest:    cloneManifest(m.manifests[site]),
	}
	for id, caps := range m.devices {
		if id.Site == site && id.Host != "" {
			state.Hosts[id.Host] = caps
		}
	}
	for id, sd := range m.deployments {
		if sd.site != site {
			continue
		}
		state.Deployments[id] = cloneDeployment(sd.d)
		if !sd.d.Deleted {
			state.YAML[id] = bytes.Clone(m.blobs[sd.d.Digest])
		}
	}
	c, err := change(state, exists)
	if err != nil {
		return err
	}
	if c.Site != nil {
		m.sites[site] = *c.Site
	}
	if c.Deployment != nil {
		m.deployments[c.Deployment.ID] = siteDeployment{site, cloneDeployment(*c.Deployment)}
	}
	for d, b := range c.Blobs {
		if _, ok := m.blobs[d]; !ok {
			m.blobs[d] = bytes.Clone(b)
		}
	}
	if c.Manifest != nil {
		m.manifests[site] = cloneManifest(*c.Manifest)
	}
	return nil
}

// The clone functions copy slices and maps, so callers can't change what is stored.

func cloneApp(app catalog.App) catalog.App {
	app.Description = bytes.Clone(app.Description)
	return app
}

func cloneDeployment(d deploy.Deployment) deploy.Deployment {
	d.Parameters = maps.Clone(d.Parameters)
	return d
}

func cloneManifest(mf deploy.Manifest) deploy.Manifest {
	mf.Body = bytes.Clone(mf.Body)
	return mf
}
