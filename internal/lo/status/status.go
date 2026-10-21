// Package status builds the Margo DeploymentStatus the LO sends to the CO (SPEC §7.2, §8.7, §10).
package status

import (
	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
)

// Desired is what a DeploymentStatus says of one desired deployment.
type Desired struct {
	ID             uuid.UUID
	Digest         contract.Digest
	AdoptedVersion contract.ManifestVersion
	Components     []string
}

// FromHost is not implemented yet.
func FromHost(host contract.DeviceID, d Desired, actual *contract.InventoryDeployment) contract.DeploymentStatus {
	return contract.DeploymentStatus{}
}

// Gateway is not implemented yet.
func Gateway(lo, device contract.DeviceID, d Desired, code, message string) contract.DeploymentStatus {
	return contract.DeploymentStatus{}
}
