// Package operatorapi holds the paths and JSON bodies of the CO's operator API (SPEC §11.3), so
// that the CO, which encodes them, and edgectl, which decodes them, name each field in one place.
package operatorapi

import (
	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
)

// Paths of the operations specified so far (SPEC §11.3).
const (
	AppsPath        = "/api/v1/apps"
	DevicesPath     = "/api/v1/devices"
	DeploymentsPath = "/api/v1/deployments"
)

// AppPath is the path of the application appID's imported versions.
func AppPath(appID string) string { return AppsPath + "/" + appID }

// DeploymentPath is the path of deployment id.
func DeploymentPath(id uuid.UUID) string { return DeploymentsPath + "/" + id.String() }

// StatusPath is the path of deployment id's status.
func StatusPath(id uuid.UUID) string { return DeploymentPath(id) + "/status" }

// ImportRequest is the body of POST /api/v1/apps. Both fields are required.
type ImportRequest struct {
	Repository string `json:"repository"`
	Version    string `json:"version"`
}

// App is one imported application version. Digest is the SHA-256 of its Application
// Description's bytes.
type App struct {
	ID         string          `json:"id"`
	Version    string          `json:"version"`
	Repository string          `json:"repository"`
	Digest     contract.Digest `json:"digest"`
}

// Device is a device with its last capability report as the LO sent it. The gateway's DeviceID
// is `<site_id>`.
type Device struct {
	DeviceID     contract.DeviceID                   `json:"deviceId"`
	Capabilities contract.DeviceCapabilitiesManifest `json:"capabilities"`
}

// CreateRequest is the body of POST /api/v1/deployments. AppID, Version and Target are required;
// Target is `<site>/<host>` or `<site>/*`. Parameters hold JSON values by parameter name.
type CreateRequest struct {
	AppID      string         `json:"appId"`
	Version    string         `json:"version"`
	Profile    string         `json:"profile,omitempty"`
	Target     string         `json:"target"`
	Name       string         `json:"name,omitempty"`
	Namespace  string         `json:"namespace,omitempty"`
	Parameters map[string]any `json:"parameters,omitempty"`
}

// Deployment is a deployment as the CO keeps it. Profile is the selected profile's `id`, Target
// its current target, Parameters the request's values by name, and Digest its current YAML's.
type Deployment struct {
	ID         uuid.UUID         `json:"id"`
	AppID      string            `json:"appId"`
	Version    string            `json:"version"`
	Profile    string            `json:"profile"`
	Target     contract.DeviceID `json:"target"`
	Name       string            `json:"name"`
	Namespace  string            `json:"namespace"`
	Parameters map[string]any    `json:"parameters"`
	Digest     contract.Digest   `json:"digest"`
	Deleted    bool              `json:"deleted"`
}

// Status is a deployment's current DeploymentStatus, nil before its first report, and every
// DeploymentStatus received for it, oldest first.
type Status struct {
	Current *contract.DeploymentStatus  `json:"current"`
	History []contract.DeploymentStatus `json:"history"`
}
