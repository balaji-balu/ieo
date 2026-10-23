// Package deploy creates, updates and deletes the CO's deployments and publishes each site's State
// Manifest (SPEC §8.1, §16.1). Every change to a site's deployments and its manifest is one
// atomic store operation (SPEC §12); the storage itself is passed in.
package deploy

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/co/catalog"
	"github.com/balaji-balu/ieo/internal/constraints"
	"github.com/balaji-balu/ieo/internal/contract"
)

// Errors returned, wrapped with the reason, by Service. Nothing is changed when one is returned.
var (
	// ErrNotFound: the application version, deployment or site does not exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalidRequest: the request can't name a deployment (no such profile, a target that is
	// not a host or `<site>/*`, an update moving a deployment to another site).
	ErrInvalidRequest = errors.New("invalid deployment request")
	// ErrInvalidTarget, which also wraps ErrInvalidRequest: the target names a site, not a host or
	// `<site>/*` (SPEC §11.3: a 400, not a 422).
	ErrInvalidTarget = fmt.Errorf("%w: invalid target", ErrInvalidRequest)
	// ErrCheckFailed: a preliminary check of SPEC §8.1.1 failed.
	ErrCheckFailed = errors.New("preliminary check failed")
	// ErrNotAuthorized: a report names a device of another site (SPEC §11.1).
	ErrNotAuthorized = errors.New("not authorized")
	// ErrGatewayNotFound: a host reported before its site's gateway (SPEC §8.3).
	ErrGatewayNotFound = errors.New("gateway not found")
	// ErrUnknownSite: the reporting site does not exist (SPEC §11.1, §15.6).
	ErrUnknownSite = errors.New("unknown site")
	// ErrSiteRetired: the reporting site is retired (SPEC §11.1).
	ErrSiteRetired = errors.New("site retired")
)

// InvalidField is an error, wrapping ErrInvalidRequest, about one field of an LO's report: Field
// is the path segment `deviceId` or `deploymentId`, or a dotted body field (SPEC §11.1).
type InvalidField struct {
	Field   string
	Message string
}

func (e *InvalidField) Error() string {
	return fmt.Sprintf("%v: %s: %s", ErrInvalidRequest, e.Field, e.Message)
}

func (e *InvalidField) Unwrap() error { return ErrInvalidRequest }

// Site is a site as the CO keeps it (SPEC §4.1.1).
type Site struct {
	ID      contract.SiteID
	Retired bool
}

// Device is a site's gateway or one of its hosts, with its latest reported capabilities (SPEC
// §8.3).
type Device struct {
	ID           contract.DeviceID
	Capabilities contract.DeviceCapabilitiesManifest
}

// Deployment is one deployment as the CO keeps it (SPEC §4.1.5, §12). Digest names its current
// YAML. A deleted deployment is kept, out of every manifest, for status history.
type Deployment struct {
	ID         uuid.UUID
	Target     contract.DeviceID
	AppID      string
	AppVersion string
	Profile    string
	Name       string
	Namespace  string
	// Parameters hold JSON values: string, float64, bool, nil, []any and map[string]any.
	Parameters map[string]any
	Digest     contract.Digest
	// DigestVersion is the manifest version that first carried Digest (SPEC §8.1.2).
	DigestVersion contract.ManifestVersion
	Deleted       bool
	// DeletedVersion is the manifest version that deleted the deployment (SPEC §8.1.2).
	DeletedVersion contract.ManifestVersion
	// Removed: the deployment is deleted and its device reported it removed (SPEC §8.1.2).
	Removed bool
}

// Manifest is a site's published State Manifest (SPEC §4.1.6): Body is the exact response body,
// ETag its entity tag, and Bundle the digest of its bundle, empty when it has none.
type Manifest struct {
	Version contract.ManifestVersion
	Body    []byte
	ETag    string
	Bundle  contract.Digest
}

// SiteState is one site as a change reads it.
type SiteState struct {
	Site Site
	// Hosts are the site's hosts with their latest reported capabilities.
	Hosts map[contract.HostID]contract.DeviceCapabilitiesManifest
	// Deployments are every deployment of the site, deleted ones included.
	Deployments map[uuid.UUID]Deployment
	// YAML holds the current YAML of each deployment that is not deleted.
	YAML     map[uuid.UUID][]byte
	Manifest Manifest
}

// DeploymentState is what a status report reads: one deployment of a site (SPEC §8.1.2).
type DeploymentState struct {
	Site            Site
	ManifestVersion contract.ManifestVersion
	// Deployment is the deployment, if Found: it exists at this site.
	Deployment Deployment
	Found      bool
	// Reported: the deployment has at least one status report.
	Reported bool
}

// DeviceState is what a capability report reads: one device of a site (SPEC §8.3).
type DeviceState struct {
	Site Site
	// Gateway: the site's gateway has reported its capabilities.
	Gateway bool
	// Device: the device has reported its capabilities.
	Device bool
}

// SiteChange is what a change writes to one site. Nil fields are left as they are.
type SiteChange struct {
	Site       *Site
	Deployment *Deployment
	// Blobs are immutable content by digest: deployment YAML and bundles. Content already stored
	// under a digest is kept.
	Blobs    map[contract.Digest][]byte
	Manifest *Manifest
	Device   *DeviceChange
	Status   *StatusReport
}

// DeviceChange replaces the latest capabilities of a device of the site, or removes the device
// when Capabilities is nil (SPEC §8.3).
type DeviceChange struct {
	ID           contract.DeviceID
	Capabilities *contract.DeviceCapabilitiesManifest
}

// StatusReport appends Status to its deployment's status history, and makes it the current status
// when Current is set (SPEC §8.1.2).
type StatusReport struct {
	Status  contract.DeploymentStatus
	Current bool
}

// Store keeps sites, deployments and manifests.
type Store interface {
	// App returns the imported application version, and whether there is one.
	App(ctx context.Context, id, version string) (catalog.App, bool, error)
	// DeploymentSite returns the site of a deployment, and whether the deployment exists.
	DeploymentSite(ctx context.Context, id uuid.UUID) (contract.SiteID, bool, error)
	// Deployment returns a deployment, and whether it exists.
	Deployment(ctx context.Context, id uuid.UUID) (Deployment, bool, error)
	// ChangeSite calls change with the current state of site and whether the site exists, and
	// writes the change it returns, all atomically: no other change to site runs in between. If
	// change returns an error, nothing is written and ChangeSite returns that error.
	ChangeSite(ctx context.Context, site contract.SiteID, change func(state SiteState, exists bool) (SiteChange, error)) error
	// ChangeDeployment is ChangeSite for a change that reads only deployment id of site.
	ChangeDeployment(ctx context.Context, site contract.SiteID, id uuid.UUID,
		change func(state DeploymentState, exists bool) (SiteChange, error)) error
	// ChangeDevice is ChangeSite for a change that reads only device id of site.
	ChangeDevice(ctx context.Context, site contract.SiteID, id contract.DeviceID,
		change func(state DeviceState, exists bool) (SiteChange, error)) error
	// Sites returns every site in ID order, retired ones included.
	Sites(ctx context.Context) ([]Site, error)
}

// Request asks for a deployment of one application version (SPEC §11.3).
type Request struct {
	AppID   string
	Version string
	// Profile is the deployment profile `id`; empty selects the only compose profile.
	Profile string
	// Target is `<site>/<host>` (directed) or `<site>/*` (autonomous).
	Target contract.DeviceID
	// Name and Namespace default to the application ID.
	Name      string
	Namespace string
	// Parameters are values by parameter name; a parameter left out takes the description's
	// value.
	Parameters map[string]any
}

// Service changes deployments and publishes manifests.
type Service struct {
	store Store
}

// New returns a Service that keeps its state in store.
func New(store Store) *Service {
	return &Service{store: store}
}

// AddSite adds an active site and publishes its first manifest: version 1, no deployments (SPEC
// §4.1.6), and reports whether it added the site. Adding a site that exists, retired or not,
// changes nothing.
func (s *Service) AddSite(ctx context.Context, id contract.SiteID) (bool, error) {
	created := false
	err := s.store.ChangeSite(ctx, id, func(_ SiteState, exists bool) (SiteChange, error) {
		created = !exists
		if exists {
			return SiteChange{}, nil
		}
		m, blobs, err := publish(1, nil)
		if err != nil {
			return SiteChange{}, err
		}
		return SiteChange{Site: &Site{ID: id}, Blobs: blobs, Manifest: &m}, nil
	})
	if err != nil {
		return false, fmt.Errorf("add site %s: %w", id, err)
	}
	return created, nil
}

// Raised is one site whose manifest version RaiseManifestVersions raised.
type Raised struct {
	Site     contract.SiteID
	From, To contract.ManifestVersion
}

// maxManifestVersion is the largest manifestVersion the CO stores (a Postgres bigint).
const maxManifestVersion = contract.ManifestVersion(math.MaxInt64)

// RaiseManifestVersions republishes every site's manifest, retired sites included, at its version
// plus by: the restore procedure of SPEC §12. Deployments, digests and bundles are left as they
// are; only the manifest body and its ETag change. Each site changes in a transaction of its own,
// in ID order, so after an error the sites already raised stay raised and rerunning is safe:
// versions only grow. by 0 is an InvalidField.
func (s *Service) RaiseManifestVersions(ctx context.Context, by contract.ManifestVersion) ([]Raised, error) {
	if by == 0 {
		return nil, fmt.Errorf("raise manifest versions: %w", &InvalidField{"by", "must be at least 1"})
	}
	sites, err := s.store.Sites(ctx)
	if err != nil {
		return nil, fmt.Errorf("raise manifest versions: %w", err)
	}
	raised := make([]Raised, 0, len(sites))
	for _, site := range sites {
		var r Raised
		err := s.store.ChangeSite(ctx, site.ID, func(state SiteState, exists bool) (SiteChange, error) {
			if !exists {
				return SiteChange{}, ErrNotFound // sites are never removed
			}
			from := state.Manifest.Version
			if by > maxManifestVersion || from > maxManifestVersion-by {
				return SiteChange{}, &InvalidField{"by", fmt.Sprintf("raises manifestVersion %d above %d", from, maxManifestVersion)}
			}
			m, blobs, err := publish(from+by, state.YAML)
			if err != nil {
				return SiteChange{}, err
			}
			r = Raised{Site: site.ID, From: from, To: m.Version}
			return SiteChange{Blobs: blobs, Manifest: &m}, nil
		})
		if err != nil {
			return raised, fmt.Errorf("raise manifest version of site %s: %w", site.ID, err)
		}
		raised = append(raised, r)
	}
	return raised, nil
}

// RetireSite marks a site retired; its deployments and manifest are kept. Retiring a retired
// site changes nothing; an unknown site wraps ErrNotFound.
func (s *Service) RetireSite(ctx context.Context, id contract.SiteID) error {
	err := s.store.ChangeSite(ctx, id, func(state SiteState, exists bool) (SiteChange, error) {
		if !exists {
			return SiteChange{}, ErrNotFound
		}
		site := state.Site
		site.Retired = true
		return SiteChange{Site: &site}, nil
	})
	if err != nil {
		return fmt.Errorf("retire site %s: %w", id, err)
	}
	return nil
}

// Create creates a deployment and publishes its site's new manifest (SPEC §8.1).
func (s *Service) Create(ctx context.Context, req Request) (Deployment, error) {
	d, err := s.put(ctx, uuid.New(), req, false)
	if err != nil {
		return Deployment{}, fmt.Errorf("create deployment of %s %s to %s: %w", req.AppID, req.Version, req.Target, err)
	}
	return d, nil
}

// Update rebuilds deployment id from req, keeping its ID and site (SPEC §8.1). The manifest
// changes only if the YAML does.
func (s *Service) Update(ctx context.Context, id uuid.UUID, req Request) (Deployment, error) {
	d, err := s.put(ctx, id, req, true)
	if err != nil {
		return Deployment{}, fmt.Errorf("update deployment %s: %w", id, err)
	}
	return d, nil
}

// Patch is an update request that names only what changes (SPEC §11.3). A nil field keeps the
// deployment's value. Parameters merge by name: a value replaces that parameter's, and a nil value
// removes it.
type Patch struct {
	Version, Profile, Name, Namespace *string
	Target                            *contract.DeviceID
	Parameters                        map[string]any
}

// Patch merges p onto deployment id and applies the result as Update does (SPEC §8.1, §11.3):
// a field p leaves out keeps its value, the profile keeps its `id` when the version changes, and
// a kept parameter the new version does not define is dropped. The merge reads the deployment in
// the transaction that writes it, so a concurrent change is not undone. A deleted deployment wraps
// ErrInvalidRequest, whatever p holds.
func (s *Service) Patch(ctx context.Context, id uuid.UUID, p Patch) (Deployment, error) {
	d, err := s.patch(ctx, id, p)
	if err != nil {
		return Deployment{}, fmt.Errorf("update deployment %s: %w", id, err)
	}
	return d, nil
}

// errVersionChanged ends a Patch transaction that found another version than the one whose
// application Patch read before it: Patch then starts again.
var errVersionChanged = errors.New("the deployment's version changed")

func (s *Service) patch(ctx context.Context, id uuid.UUID, p Patch) (Deployment, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Deployment{}, err
		}
		current, ok, err := s.store.Deployment(ctx, id)
		if err != nil {
			return Deployment{}, err
		}
		if !ok {
			return Deployment{}, ErrNotFound
		}
		if current.Deleted {
			return Deployment{}, deletedError(id)
		}
		version := current.AppVersion
		if p.Version != nil {
			version = *p.Version
		}
		// The application is read outside the transaction: an imported version never changes.
		app, desc, err := s.app(ctx, current.AppID, version)
		if err != nil {
			return Deployment{}, err
		}
		var d Deployment
		err = s.store.ChangeSite(ctx, current.Target.Site, func(state SiteState, exists bool) (SiteChange, error) {
			stored, ok := state.Deployments[id]
			switch {
			case !ok:
				return SiteChange{}, ErrNotFound
			case stored.Deleted:
				return SiteChange{}, deletedError(id)
			case p.Version == nil && stored.AppVersion != version:
				return SiteChange{}, errVersionChanged
			}
			req, err := merge(stored, p, version, desc)
			if err != nil {
				return SiteChange{}, err
			}
			var yamlBytes []byte
			var c contract.DeviceConstraints
			if d, yamlBytes, c, err = build(id, app, desc, req); err != nil {
				return SiteChange{}, err
			}
			return change(state, exists, &d, yamlBytes, c, &stored)
		})
		if errors.Is(err, errVersionChanged) {
			continue
		}
		if err != nil {
			return Deployment{}, err
		}
		return d, nil
	}
}

func deletedError(id uuid.UUID) error {
	return fmt.Errorf("%w: deployment %s is deleted", ErrInvalidRequest, id)
}

// merge returns the request that applies p to stored at version, whose description is desc (SPEC
// §11.3). Parameters keep the stored values desc defines, then each p names is replaced, or
// removed when its value is nil.
func merge(stored Deployment, p Patch, version string, desc contract.ApplicationDescription) (Request, error) {
	req := Request{
		AppID: stored.AppID, Version: version, Profile: stored.Profile, Target: stored.Target,
		Name: stored.Name, Namespace: stored.Namespace, Parameters: map[string]any{},
	}
	if p.Profile != nil {
		req.Profile = *p.Profile
	}
	if p.Target != nil {
		req.Target = *p.Target
	}
	if p.Name != nil {
		req.Name = *p.Name
	}
	if p.Namespace != nil {
		req.Namespace = *p.Namespace
	}
	if err := checkUpdateTarget(stored.Target.Site, req.Target); err != nil {
		return Request{}, err
	}
	for name, v := range stored.Parameters {
		if _, ok := desc.Parameters[name]; ok {
			req.Parameters[name] = v
		}
	}
	for name, v := range p.Parameters {
		if v == nil {
			delete(req.Parameters, name)
		} else {
			req.Parameters[name] = v
		}
	}
	return req, nil
}

// checkUpdateTarget checks the target of an update: a host or `<site>/*` (SPEC §11.3), at the
// deployment's site (SPEC §8.1).
func checkUpdateTarget(site contract.SiteID, target contract.DeviceID) error {
	if target.Host == "" && !target.Autonomous {
		return fmt.Errorf("%w: %s names a site; want `<site>/<host>` or `<site>/*`", ErrInvalidTarget, target)
	}
	if target.Site != site {
		return fmt.Errorf("%w: the deployment is at site %s, not %s; delete it and create another",
			ErrInvalidRequest, site, target.Site)
	}
	return nil
}

// Delete removes deployment id from its site's manifest (SPEC §8.1). Deleting a deleted
// deployment changes nothing; an unknown one wraps ErrNotFound.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	site, ok, err := s.store.DeploymentSite(ctx, id)
	if err != nil {
		return fmt.Errorf("delete deployment %s: %w", id, err)
	}
	if !ok {
		return fmt.Errorf("delete deployment %s: %w", id, ErrNotFound)
	}
	err = s.store.ChangeSite(ctx, site, func(state SiteState, _ bool) (SiteChange, error) {
		d := state.Deployments[id]
		if d.Deleted {
			return SiteChange{}, nil // G-B1
		}
		d.Deleted = true
		return republish(state, d, nil)
	})
	if err != nil {
		return fmt.Errorf("delete deployment %s: %w", id, err)
	}
	return nil
}

// put builds deployment id from req and stores it with its site's new manifest (SPEC §16.1). On
// update the deployment must exist and stay at its site.
func (s *Service) put(ctx context.Context, id uuid.UUID, req Request, update bool) (Deployment, error) {
	if req.Target.Host == "" && !req.Target.Autonomous {
		return Deployment{}, fmt.Errorf("%w: %s names a site; want `<site>/<host>` or `<site>/*`", ErrInvalidTarget, req.Target)
	}
	if update {
		site, ok, err := s.store.DeploymentSite(ctx, id)
		if err != nil {
			return Deployment{}, err
		}
		if !ok {
			return Deployment{}, ErrNotFound
		}
		if err := checkUpdateTarget(site, req.Target); err != nil {
			return Deployment{}, err
		}
	}
	app, desc, err := s.app(ctx, req.AppID, req.Version)
	if err != nil {
		return Deployment{}, err
	}
	d, yamlBytes, deviceConstraints, err := build(id, app, desc, req)
	if err != nil {
		return Deployment{}, err
	}
	err = s.store.ChangeSite(ctx, req.Target.Site, func(state SiteState, exists bool) (SiteChange, error) {
		var old *Deployment
		if update {
			stored, ok := state.Deployments[id]
			if !ok || stored.Deleted {
				return SiteChange{}, ErrNotFound
			}
			old = &stored
		}
		return change(state, exists, &d, yamlBytes, deviceConstraints, old)
	})
	if err != nil {
		return Deployment{}, err
	}
	return d, nil
}

// app returns an imported application version and its parsed description.
func (s *Service) app(ctx context.Context, id, version string) (catalog.App, contract.ApplicationDescription, error) {
	app, ok, err := s.store.App(ctx, id, version)
	if err != nil {
		return catalog.App{}, contract.ApplicationDescription{}, err
	}
	if !ok {
		return catalog.App{}, contract.ApplicationDescription{}, fmt.Errorf("%w: application %s version %s", ErrNotFound, id, version)
	}
	desc, err := contract.ParseApplicationDescription(app.Description)
	if err != nil {
		return catalog.App{}, contract.ApplicationDescription{}, fmt.Errorf("stored application %s %s: %w", id, version, err)
	}
	return app, desc, nil
}

// build builds deployment id of app from req (SPEC §8.1 steps 1 and 3) and returns it with its
// YAML and the device constraints of its profile. Only the parameter checks of SPEC §8.1.1 run
// here; the target's run in the transaction that writes it.
func build(id uuid.UUID, app catalog.App, desc contract.ApplicationDescription, req Request) (Deployment, []byte, contract.DeviceConstraints, error) {
	profile, err := selectProfile(desc, req.Profile)
	if err != nil {
		return Deployment{}, nil, contract.DeviceConstraints{}, err
	}
	var deviceConstraints contract.DeviceConstraints
	if profile.DeviceConstraints != nil {
		deviceConstraints = *profile.DeviceConstraints
	}
	d := Deployment{
		ID:         id,
		Target:     req.Target,
		AppID:      req.AppID,
		AppVersion: req.Version,
		Profile:    profile.ID,
		Name:       cmp.Or(req.Name, req.AppID),
		Namespace:  cmp.Or(req.Namespace, req.AppID),
		Parameters: req.Parameters,
	}
	yamlBytes, err := contract.BuildApplicationDeployment(app.Description, contract.DeploymentChoices{
		ID: id, Name: d.Name, Namespace: d.Namespace, DeviceID: d.Target, Profile: profile.ID, Parameters: d.Parameters,
	})
	if errors.Is(err, contract.ErrDeploymentParameter) {
		return Deployment{}, nil, contract.DeviceConstraints{}, fmt.Errorf("%w: %w", ErrCheckFailed, err) // SPEC §8.1.1
	}
	if err != nil {
		return Deployment{}, nil, contract.DeviceConstraints{}, err
	}
	d.Digest = contract.DigestOf(yamlBytes) // SPEC §8.1 step 3
	return d, yamlBytes, deviceConstraints, nil
}

// change is the write of SPEC §8.1 for d, built with yamlBytes and constraints c: the target
// checks, then the site's new manifest. On update, old is the stored deployment; when d has its
// digest, d keeps old's DigestVersion and no manifest is published.
func change(state SiteState, exists bool, d *Deployment, yamlBytes []byte, c contract.DeviceConstraints, old *Deployment) (SiteChange, error) {
	if err := checkTarget(state, exists, d.Target, c); err != nil {
		return SiteChange{}, err
	}
	if old != nil && old.Digest == d.Digest {
		d.DigestVersion = old.DigestVersion   // SPEC §8.1.2
		return SiteChange{Deployment: d}, nil // same (ID, digest): no new manifest (SPEC §8.1)
	}
	return republish(state, *d, yamlBytes)
}

// selectProfile returns the requested compose profile, or the only one when none is requested
// (SPEC §11.3).
func selectProfile(desc contract.ApplicationDescription, requested string) (contract.ApplicationDeploymentProfile, error) {
	var compose []contract.ApplicationDeploymentProfile
	var ids []string
	for _, p := range desc.DeploymentProfiles {
		if p.Type != contract.ProfileTypeCompose {
			continue
		}
		if requested != "" && p.ID == requested {
			return p, nil
		}
		compose = append(compose, p)
		ids = append(ids, p.ID)
	}
	switch {
	case requested != "":
		return contract.ApplicationDeploymentProfile{}, fmt.Errorf("%w: no %s deployment profile %q",
			ErrInvalidRequest, contract.ProfileTypeCompose, requested)
	case len(compose) == 1:
		return compose[0], nil
	default:
		return contract.ApplicationDeploymentProfile{}, fmt.Errorf("%w: %d %s deployment profiles, name one of %v",
			ErrInvalidRequest, len(compose), contract.ProfileTypeCompose, ids)
	}
}

// checkTarget runs the site and host checks of SPEC §8.1.1: a directed host must satisfy c, and an
// autonomous target needs at least one host at the site that does (SPEC §5.5).
func checkTarget(state SiteState, exists bool, target contract.DeviceID, c contract.DeviceConstraints) error {
	switch {
	case !exists:
		return fmt.Errorf("%w: unknown site %s", ErrCheckFailed, target.Site)
	case state.Site.Retired:
		return fmt.Errorf("%w: site %s is retired", ErrCheckFailed, target.Site)
	case !target.Autonomous:
		caps, ok := state.Hosts[target.Host]
		if !ok {
			return fmt.Errorf("%w: unknown host %s", ErrCheckFailed, target)
		}
		if err := constraints.Check(c, caps); err != nil {
			return fmt.Errorf("%w: host %s: %w", ErrCheckFailed, target, err)
		}
		return nil
	}
	var reasons []string
	for _, host := range slices.Sorted(maps.Keys(state.Hosts)) {
		err := constraints.Check(c, state.Hosts[host])
		if err == nil {
			return nil
		}
		reasons = append(reasons, fmt.Sprintf("%s: %v", host, err))
	}
	if len(reasons) == 0 {
		return fmt.Errorf("%w: site %s has no hosts", ErrCheckFailed, target.Site)
	}
	return fmt.Errorf("%w: no eligible host at site %s (%s)", ErrCheckFailed, target.Site, strings.Join(reasons, "; "))
}

// republish returns the change that puts d, whose YAML is yamlBytes (nil when d is deleted), into
// state and publishes the site's next manifest (SPEC §8.1 steps 4–6).
func republish(state SiteState, d Deployment, yamlBytes []byte) (SiteChange, error) {
	current := maps.Clone(state.YAML)
	delete(current, d.ID)
	if !d.Deleted {
		current[d.ID] = yamlBytes
	}
	m, blobs, err := publish(state.Manifest.Version+1, current)
	if err != nil {
		return SiteChange{}, err
	}
	if !d.Deleted {
		blobs[d.Digest] = yamlBytes
		d.DigestVersion = m.Version // the first manifest to carry this digest (SPEC §8.1.2)
	} else {
		d.DeletedVersion = m.Version
	}
	return SiteChange{Deployment: &d, Blobs: blobs, Manifest: &m}, nil
}

// publish builds manifest version of a site whose deployments have the YAML in deployments, and
// returns it with its bundle by digest; a site without deployments has no bundle (SPEC §4.1.6).
func publish(version contract.ManifestVersion, deployments map[uuid.UUID][]byte) (Manifest, map[contract.Digest][]byte, error) {
	body := contract.StateManifest{ManifestVersion: version}
	blobs := map[contract.Digest][]byte{}
	var files []contract.BundleFile
	for id, b := range deployments {
		digest := contract.DigestOf(b)
		body.Deployments = append(body.Deployments, contract.DeploymentRef{
			DeploymentID: id, Digest: digest, SizeBytes: uint64(len(b)), URL: contract.DeploymentURL(id, digest),
		})
		files = append(files, contract.BundleFile{DeploymentID: id, YAML: b})
	}
	var bundleDigest contract.Digest
	if len(files) > 0 {
		bundle, err := contract.EncodeBundle(files)
		if err != nil {
			return Manifest{}, nil, err
		}
		bundleDigest = contract.DigestOf(bundle)
		blobs[bundleDigest] = bundle
		body.Bundle = &contract.BundleRef{
			MediaType: contract.BundleMediaType, Digest: bundleDigest, SizeBytes: uint64(len(bundle)),
			URL: contract.BundleURL(bundleDigest),
		}
	}
	b, err := contract.EncodeStateManifest(body)
	if err != nil {
		return Manifest{}, nil, err
	}
	return Manifest{Version: version, Body: b, ETag: contract.ETag(b), Bundle: bundleDigest}, blobs, nil
}
