// Package status builds the Margo DeploymentStatus the LO sends to the CO (SPEC §8.7, §10). It
// holds the status rules and nothing else: one entry per component, the §7.2 precedence, and the
// error sources. It keeps no state and does no I/O; the Reconciler decides when a status is built.
package status

import (
	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
)

// Desired is what the mapping needs of one desired deployment.
type Desired struct {
	// ID is the ID the manifest lists the deployment under, whatever its YAML says (SPEC §8.7).
	ID     uuid.UUID
	Digest contract.Digest
	// Adopted is the deployment's adopted_manifest_version (SPEC §8.2 step 6).
	Adopted contract.ManifestVersion
	// Components are the names of the deployment YAML's components, in its order; nil when the
	// YAML does not decode.
	Components []string
}

// FromHost returns the status of d on host, from what the host reports of it: actual, nil when the
// host reports nothing of d (SPEC §8.7). It has one entry per component of d, in d's order. A
// component the host reports at d's digest has the reported state and error, with the component's
// name as the error's source; any other component is `pending`. The state follows SPEC §7.2.
func FromHost(host contract.DeviceID, d Desired, actual *contract.InventoryDeployment) contract.DeploymentStatus {
	return contract.DeploymentStatus{}
}

// Gateway returns the status of d failed with a gateway error (SPEC §8.4, §8.7): code and message,
// with the LO's device ID lo as source, on deviceID. `status.error` and every component carry the
// error, and the state is `failed`, also when d has no components.
func Gateway(lo, deviceID contract.DeviceID, d Desired, code, message string) contract.DeploymentStatus {
	return contract.DeploymentStatus{}
}
