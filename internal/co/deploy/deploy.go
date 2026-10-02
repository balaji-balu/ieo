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
	// ErrCheckFailed: a preliminary check of SPEC §8.1.1 failed.
	ErrCheckFailed = errors.New("preliminary check failed")
	// ErrNotAuthorized: a report names a device of another site (SPEC §11.1).
	ErrNotAuthorized = errors.New("not authorized")
	// ErrGatewayNotFound: a host reported before its site's gateway (SPEC §8.3).
	ErrGatewayNotFound = errors.New("gateway not found")
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
	// Gateway reports whether the site's gateway (the LO) has reported its capabilities (SPEC
	// §8.3).
	Gateway bool
	// Reports counts the status reports received for each deployment (SPEC §8.1.2).
	Reports map[uuid.UUID]int
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
	// ChangeSite calls change with the current state of site and whether the site exists, and
	// writes the change it returns, all atomically: no other change to site runs in between. If
	// change returns an error, nothing is written and ChangeSite returns that error.
	ChangeSite(ctx context.Context, site contract.SiteID, change func(state SiteState, exists bool) (SiteChange, error)) error
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
// §4.1.6). Adding a site that exists changes nothing.
func (s *Service) AddSite(ctx context.Context, id contract.SiteID) error {
	err := s.store.ChangeSite(ctx, id, func(_ SiteState, exists bool) (SiteChange, error) {
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
		return fmt.Errorf("add site %s: %w", id, err)
	}
	return nil
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
		return Deployment{}, fmt.Errorf("%w: target %s is a site, not `<site>/<host>` or `<site>/*`", ErrInvalidRequest, req.Target)
	}
	if update {
		site, ok, err := s.store.DeploymentSite(ctx, id)
		if err != nil {
			return Deployment{}, err
		}
		if !ok {
			return Deployment{}, ErrNotFound
		}
		if site != req.Target.Site {
			return Deployment{}, fmt.Errorf("%w: the deployment is at site %s, not %s; delete it and create another",
				ErrInvalidRequest, site, req.Target.Site)
		}
	}
	app, ok, err := s.store.App(ctx, req.AppID, req.Version)
	if err != nil {
		return Deployment{}, err
	}
	if !ok {
		return Deployment{}, fmt.Errorf("%w: application %s version %s", ErrNotFound, req.AppID, req.Version)
	}
	desc, err := contract.ParseApplicationDescription(app.Description)
	if err != nil {
		return Deployment{}, fmt.Errorf("stored application %s %s: %w", req.AppID, req.Version, err)
	}
	profile, err := selectProfile(desc, req.Profile)
	if err != nil {
		return Deployment{}, err
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
		return Deployment{}, fmt.Errorf("%w: %w", ErrCheckFailed, err) // SPEC §8.1.1
	}
	if err != nil {
		return Deployment{}, err
	}
	d.Digest = contract.DigestOf(yamlBytes) // SPEC §8.1 step 3

	err = s.store.ChangeSite(ctx, req.Target.Site, func(state SiteState, exists bool) (SiteChange, error) {
		if err := checkTarget(state, exists, req.Target, deviceConstraints); err != nil {
			return SiteChange{}, err
		}
		if update {
			old, ok := state.Deployments[id]
			if !ok || old.Deleted {
				return SiteChange{}, ErrNotFound
			}
			if old.Digest == d.Digest {
				d.DigestVersion = old.DigestVersion    // SPEC §8.1.2
				return SiteChange{Deployment: &d}, nil // same (ID, digest): no new manifest (SPEC §8.1)
			}
		}
		return republish(state, d, yamlBytes)
	})
	if err != nil {
		return Deployment{}, err
	}
	return d, nil
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
