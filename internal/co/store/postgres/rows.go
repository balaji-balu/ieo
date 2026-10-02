package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/co/deploy"
	"github.com/balaji-balu/ieo/internal/co/store/postgres/ent"
	"github.com/balaji-balu/ieo/internal/contract"
)

// siteRow returns the row of site id, and whether there is one. A zero row is returned for none.
func siteRow(ctx context.Context, sites *ent.SiteClient, id contract.SiteID) (*ent.Site, bool, error) {
	row, err := sites.Get(ctx, string(id))
	if ent.IsNotFound(err) {
		return &ent.Site{}, false, nil
	}
	if err != nil {
		return &ent.Site{}, false, fmt.Errorf("get site %s: %w", id, err)
	}
	return row, true, nil
}

func siteOf(row *ent.Site) deploy.Site {
	return deploy.Site{ID: contract.SiteID(row.ID), Retired: row.Retired}
}

// manifestOf returns the site's manifest; version 0 when the site has none.
func manifestOf(row *ent.Site) deploy.Manifest {
	return deploy.Manifest{
		Version: contract.ManifestVersion(row.ManifestVersion), Body: row.ManifestBody,
		ETag: row.ManifestEtag, Bundle: contract.Digest(row.ManifestBundle),
	}
}

func deploymentOf(r *ent.Deployment) (deploy.Deployment, error) {
	target, err := contract.ParseDeviceID(r.Target)
	if err != nil {
		return deploy.Deployment{}, fmt.Errorf("stored deployment %s: %w", r.ID, err)
	}
	var params map[string]any
	if err := json.Unmarshal(r.Parameters, &params); err != nil {
		return deploy.Deployment{}, fmt.Errorf("stored deployment %s: parameters: %w", r.ID, err)
	}
	return deploy.Deployment{
		ID: r.ID, Target: target, AppID: r.AppID, AppVersion: r.AppVersion, Profile: r.Profile,
		Name: r.Name, Namespace: r.Namespace, Parameters: params, Digest: contract.Digest(r.Digest),
		DigestVersion: contract.ManifestVersion(r.DigestVersion), Deleted: r.Deleted,
		DeletedVersion: contract.ManifestVersion(r.DeletedVersion), Removed: r.Removed,
	}, nil
}

// statusRecord is how a status is stored: contract.DeploymentStatus with its device ID as a plain
// string, because Margo makes deviceId optional and an empty contract.DeviceID does not decode.
type statusRecord struct {
	DeploymentID           uuid.UUID                  `json:"deploymentId"`
	DeviceID               string                     `json:"deviceId"`
	AdoptedManifestVersion contract.ManifestVersion   `json:"adoptedManifestVersion"`
	Status                 contract.DeploymentState   `json:"status"`
	Components             []contract.ComponentStatus `json:"components"`
}

func encodeStatus(s contract.DeploymentStatus) ([]byte, error) {
	var device string
	if s.DeviceID != (contract.DeviceID{}) {
		device = s.DeviceID.String()
	}
	b, err := json.Marshal(statusRecord{s.DeploymentID, device, s.AdoptedManifestVersion, s.Status, s.Components})
	if err != nil {
		return nil, fmt.Errorf("encode status of %s: %w", s.DeploymentID, err)
	}
	return b, nil
}

func decodeStatus(b []byte) (contract.DeploymentStatus, error) {
	var r statusRecord
	if err := json.Unmarshal(b, &r); err != nil {
		return contract.DeploymentStatus{}, fmt.Errorf("decode stored status: %w", err)
	}
	s := contract.DeploymentStatus{
		DeploymentID: r.DeploymentID, AdoptedManifestVersion: r.AdoptedManifestVersion,
		Status: r.Status, Components: r.Components,
	}
	if r.DeviceID != "" {
		id, err := contract.ParseDeviceID(r.DeviceID)
		if err != nil {
			return contract.DeploymentStatus{}, fmt.Errorf("decode stored status of %s: %w", r.DeploymentID, err)
		}
		s.DeviceID = id
	}
	return s, nil
}
