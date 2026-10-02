package deploy

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
)

// ReportCapabilities stores the latest capabilities of device id, reported by site's LO, and
// reports whether the device is new (SPEC §8.3, §11.1). A host needs its site's gateway report
// first (ErrGatewayNotFound); a device of another site wraps ErrNotAuthorized.
func (s *Service) ReportCapabilities(ctx context.Context, site contract.SiteID, id contract.DeviceID,
	caps contract.DeviceCapabilitiesManifest) (bool, error) {
	if err := checkDevice(site, id); err != nil {
		return false, fmt.Errorf("report capabilities of %s: %w", id, err)
	}
	if caps.Properties.ID != id {
		return false, fmt.Errorf("report capabilities of %s: %w", id,
			&InvalidField{"properties.id", "differs from the path deviceId " + id.String()})
	}
	created := false
	err := s.store.ChangeSite(ctx, site, func(state SiteState, exists bool) (SiteChange, error) {
		switch {
		case !exists:
			return SiteChange{}, ErrNotFound
		case id.Host == "":
			created = !state.Gateway
		case !state.Gateway:
			return SiteChange{}, ErrGatewayNotFound // SPEC §8.3
		default:
			_, known := state.Hosts[id.Host]
			created = !known
		}
		return SiteChange{Device: &DeviceChange{ID: id, Capabilities: &caps}}, nil
	})
	if err != nil {
		return false, fmt.Errorf("report capabilities of %s: %w", id, err)
	}
	return created, nil
}

// RemoveDevice removes host id, unregistered by site's LO (SPEC §8.3). The gateway itself can't
// be removed (ErrInvalidRequest); retire the site instead.
func (s *Service) RemoveDevice(ctx context.Context, site contract.SiteID, id contract.DeviceID) error {
	if err := checkDevice(site, id); err != nil {
		return fmt.Errorf("remove device %s: %w", id, err)
	}
	if id.Host == "" {
		return fmt.Errorf("remove device %s: %w", id, &InvalidField{"deviceId", "the gateway can't be removed; retire the site"})
	}
	err := s.store.ChangeSite(ctx, site, func(state SiteState, _ bool) (SiteChange, error) {
		if _, ok := state.Hosts[id.Host]; !ok {
			return SiteChange{}, ErrNotFound
		}
		return SiteChange{Device: &DeviceChange{ID: id}}, nil
	})
	if err != nil {
		return fmt.Errorf("remove device %s: %w", id, err)
	}
	return nil
}

// checkDevice checks that id names site's gateway or one of its hosts.
func checkDevice(site contract.SiteID, id contract.DeviceID) error {
	switch {
	case id.Site != site:
		return fmt.Errorf("%w: device of site %s", ErrNotAuthorized, id.Site)
	case id.Autonomous:
		return &InvalidField{"deviceId", "names an autonomous target, not a device"}
	}
	return nil
}

// ReportStatus records the status of deployment id reported by site's LO, and reports whether it
// is the deployment's first report (SPEC §8.1.2). A deployment that is not site's is an
// InvalidField: the Margo file lists no 404 for status.
func (s *Service) ReportStatus(ctx context.Context, site contract.SiteID, id uuid.UUID,
	status contract.DeploymentStatus) (bool, error) {
	switch {
	case status.DeploymentID != id:
		return false, fmt.Errorf("report status of %s: %w", id, &InvalidField{"deploymentId", "differs from the path deploymentId"})
	case status.DeviceID != (contract.DeviceID{}) && status.DeviceID.Site != site:
		return false, fmt.Errorf("report status of %s: %w", id, &InvalidField{"deviceId", "is at another site"})
	case status.AdoptedManifestVersion == 0:
		return false, fmt.Errorf("report status of %s: %w", id, &InvalidField{"adoptedManifestVersion", "must be at least 1"})
	}
	created := false
	err := s.store.ChangeSite(ctx, site, func(state SiteState, _ bool) (SiteChange, error) {
		d, ok := state.Deployments[id]
		if !ok {
			return SiteChange{}, &InvalidField{"deploymentId", "no such deployment"} // the Margo file lists no 404
		}
		if status.AdoptedManifestVersion > state.Manifest.Version {
			return SiteChange{}, &InvalidField{"adoptedManifestVersion",
				fmt.Sprintf("is above the site's manifestVersion %d", state.Manifest.Version)}
		}
		created = state.Reports[id] == 0
		// A report about an older digest is history only (SPEC §8.1.2).
		current := status.AdoptedManifestVersion >= d.DigestVersion
		c := SiteChange{Status: &StatusReport{Status: status, Current: current}}
		if d.Deleted && !d.Removed && status.Status.State == contract.StateRemoved &&
			status.AdoptedManifestVersion >= d.DeletedVersion {
			d.Removed = true
			c.Deployment = &d
		}
		return c, nil
	})
	if err != nil {
		return false, fmt.Errorf("report status of %s: %w", id, err)
	}
	return created, nil
}
