// Package store keeps the CO's state (SPEC §12). Memory is the in-memory backend used by Core
// Conformance tests; package postgres is the Postgres backend, with the same operations and rules.
package store

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/co/auth"
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
	tokens      map[auth.TokenHash]contract.SiteID
	siteTokens  map[contract.SiteID]auth.TokenHash
	// published records every digest a deployment had in a manifest, and every bundle of each
	// site's manifests, so the API serves a site only its own content (SPEC §11.1).
	published map[publication]bool
	// history holds every status reported for a deployment, oldest first; current its current
	// status (SPEC §8.1.2).
	history map[uuid.UUID][]contract.DeploymentStatus
	current map[uuid.UUID]contract.DeploymentStatus
}

type appKey struct{ id, version string }

// publication is a digest a site published in a manifest: a deployment's YAML, or a bundle.
type publication struct {
	site       contract.SiteID
	bundle     bool
	deployment uuid.UUID // zero for a bundle
	digest     contract.Digest
}

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
		tokens:      map[auth.TokenHash]contract.SiteID{},
		published:   map[publication]bool{},
		history:     map[uuid.UUID][]contract.DeploymentStatus{},
		current:     map[uuid.UUID]contract.DeploymentStatus{},
		siteTokens:  map[contract.SiteID]auth.TokenHash{},
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

// PutDevice replaces the latest capabilities reported for a device of an existing site (SPEC
// §8.3).
func (m *Memory) PutDevice(_ context.Context, id contract.DeviceID, caps contract.DeviceCapabilitiesManifest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sites[id.Site]; !ok {
		return fmt.Errorf("put device %s: no site %s", id, id.Site)
	}
	m.devices[id] = cloneCapabilities(caps)
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
			state.Hosts[id.Host] = cloneCapabilities(caps)
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
	return m.write(site, c)
}

// ChangeDeployment is ChangeSite for a change that reads only deployment id of site.
func (m *Memory) ChangeDeployment(_ context.Context, site contract.SiteID, id uuid.UUID,
	change func(state deploy.DeploymentState, exists bool) (deploy.SiteChange, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, exists := m.sites[site]
	state := deploy.DeploymentState{Site: s, ManifestVersion: m.manifests[site].Version}
	if sd, ok := m.deployments[id]; ok && sd.site == site {
		state.Deployment, state.Found = cloneDeployment(sd.d), true
		state.Reported = len(m.history[id]) > 0
	}
	c, err := change(state, exists)
	if err != nil {
		return err
	}
	return m.write(site, c)
}

// ChangeDevice is ChangeSite for a change that reads only device id of site.
func (m *Memory) ChangeDevice(_ context.Context, site contract.SiteID, id contract.DeviceID,
	change func(state deploy.DeviceState, exists bool) (deploy.SiteChange, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, exists := m.sites[site]
	state := deploy.DeviceState{Site: s}
	_, state.Gateway = m.devices[contract.DeviceID{Site: site}]
	_, state.Device = m.devices[id]
	state.Device = state.Device && id.Site == site
	c, err := change(state, exists)
	if err != nil {
		return err
	}
	return m.write(site, c)
}

// write writes c to site. Before writing anything, it refuses a manifest version below the
// site's current one (SPEC §12) and a deployment of another site.
func (m *Memory) write(site contract.SiteID, c deploy.SiteChange) error {
	if c.Manifest != nil && c.Manifest.Version < m.manifests[site].Version {
		return fmt.Errorf("site %s: manifestVersion %d is below the current %d",
			site, c.Manifest.Version, m.manifests[site].Version)
	}
	if c.Deployment != nil {
		if sd, ok := m.deployments[c.Deployment.ID]; ok && sd.site != site {
			return fmt.Errorf("site %s: deployment %s is at site %s", site, c.Deployment.ID, sd.site)
		}
	}
	if c.Site != nil {
		m.sites[site] = *c.Site
	}
	if c.Deployment != nil {
		m.deployments[c.Deployment.ID] = siteDeployment{site, cloneDeployment(*c.Deployment)}
		if c.Manifest != nil && !c.Deployment.Deleted { // the new manifest holds its digest
			m.published[publication{site, false, c.Deployment.ID, c.Deployment.Digest}] = true
		}
	}
	for d, b := range c.Blobs {
		if _, ok := m.blobs[d]; !ok {
			m.blobs[d] = bytes.Clone(b)
		}
	}
	if c.Device != nil {
		if c.Device.Capabilities == nil {
			delete(m.devices, c.Device.ID)
		} else {
			m.devices[c.Device.ID] = cloneCapabilities(*c.Device.Capabilities)
		}
	}
	if c.Status != nil {
		id := c.Status.Status.DeploymentID
		m.history[id] = append(m.history[id], cloneStatus(c.Status.Status))
		if c.Status.Current {
			m.current[id] = cloneStatus(c.Status.Status)
		}
	}
	if c.Manifest != nil {
		m.manifests[site] = cloneManifest(*c.Manifest)
		if c.Manifest.Bundle != "" {
			m.published[publication{site, true, uuid.Nil, c.Manifest.Bundle}] = true
		}
	}
	return nil
}

// The clone functions copy slices and maps, so callers can't change what is stored.

func cloneApp(app catalog.App) catalog.App {
	app.Description = bytes.Clone(app.Description)
	return app
}

func cloneDeployment(d deploy.Deployment) deploy.Deployment {
	if d.Parameters != nil {
		d.Parameters = cloneJSON(d.Parameters).(map[string]any)
	}
	return d
}

// cloneJSON deep-copies a JSON value: maps and slices are copied at every level.
func cloneJSON(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = cloneJSON(e)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = cloneJSON(e)
		}
		return out
	default:
		return v
	}
}

func cloneManifest(mf deploy.Manifest) deploy.Manifest {
	mf.Body = bytes.Clone(mf.Body)
	return mf
}

// Site returns a site, and whether it exists.
func (m *Memory) Site(_ context.Context, id contract.SiteID) (deploy.Site, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	site, ok := m.sites[id]
	return site, ok, nil
}

// DeploymentYAML returns the YAML of deployment id under digest, and whether site published it:
// the deployment is at site and had digest in one of its manifests.
func (m *Memory) DeploymentYAML(_ context.Context, site contract.SiteID, id uuid.UUID, digest contract.Digest) ([]byte, bool, error) {
	return m.publishedBlob(publication{site, false, id, digest})
}

// Bundle returns the bundle under digest, and whether one of site's manifests named it.
func (m *Memory) Bundle(_ context.Context, site contract.SiteID, digest contract.Digest) ([]byte, bool, error) {
	return m.publishedBlob(publication{site, true, uuid.Nil, digest})
}

func (m *Memory) publishedBlob(p publication) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p.digest == "" || !m.published[p] {
		return nil, false, nil
	}
	b, ok := m.blobs[p.digest]
	return bytes.Clone(b), ok, nil
}

// PutSiteToken replaces the token hash of site, and reports false if there is no such site.
func (m *Memory) PutSiteToken(_ context.Context, site contract.SiteID, hash auth.TokenHash) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sites[site]; !ok {
		return false, nil
	}
	if old, ok := m.siteTokens[site]; ok {
		delete(m.tokens, old)
	}
	m.siteTokens[site] = hash
	m.tokens[hash] = site
	return true, nil
}

// SiteByToken returns the site whose token has hash, and whether there is one.
func (m *Memory) SiteByToken(_ context.Context, hash auth.TokenHash) (contract.SiteID, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	site, ok := m.tokens[hash]
	return site, ok, nil
}

// CurrentStatus returns the current status of a deployment, and whether it has one.
func (m *Memory) CurrentStatus(_ context.Context, id uuid.UUID) (contract.DeploymentStatus, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.current[id]
	return cloneStatus(s), ok, nil
}

// StatusHistory returns every status reported for a deployment, oldest first.
func (m *Memory) StatusHistory(_ context.Context, id uuid.UUID) ([]contract.DeploymentStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]contract.DeploymentStatus, len(m.history[id]))
	for i, s := range m.history[id] {
		out[i] = cloneStatus(s)
	}
	return out, nil
}

func cloneStatus(s contract.DeploymentStatus) contract.DeploymentStatus {
	s.Components = slices.Clone(s.Components)
	return s
}

func cloneCapabilities(c contract.DeviceCapabilitiesManifest) contract.DeviceCapabilitiesManifest {
	c.Labels = maps.Clone(c.Labels)
	p := &c.Properties
	p.CPUs = slices.Clone(p.CPUs)
	p.Peripherals = slices.Clone(p.Peripherals)
	p.Interfaces = slices.Clone(p.Interfaces)
	p.SupportedRuntimes = slices.Clone(p.SupportedRuntimes)
	p.SupportedDeploymentTypes = slices.Clone(p.SupportedDeploymentTypes)
	return c
}
