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
	reported := map[string]contract.ComponentStatus{}
	if actual != nil && actual.Digest == d.Digest {
		for _, c := range actual.Components {
			reported[c.Name] = c
		}
	}
	components := make([]contract.ComponentStatus, 0, len(d.Components))
	for _, name := range d.Components {
		c, ok := reported[name]
		if !ok {
			components = append(components, contract.ComponentStatus{Name: name, State: contract.StatePending})
			continue
		}
		var e *contract.StatusError
		if c.Error != nil {
			// SPEC §8.7: a component error's source is the component's name.
			e = &contract.StatusError{Code: c.Error.Code, Source: name, Message: c.Error.Message}
		}
		components = append(components, contract.ComponentStatus{Name: name, State: c.State, Error: e})
	}
	return contract.DeploymentStatus{
		DeploymentID: d.ID, DeviceID: host, AdoptedManifestVersion: d.Adopted,
		Status:     contract.DeploymentState{State: aggregate(components)},
		Components: components,
	}
}

// Gateway returns the status of d failed with a gateway error (SPEC §8.4, §8.7): code and message,
// with the LO's device ID lo as source, on deviceID. `status.error` and every component carry the
// error, and the state is `failed`, also when d has no components.
func Gateway(lo, deviceID contract.DeviceID, d Desired, code, message string) contract.DeploymentStatus {
	errorOf := func() *contract.StatusError {
		return &contract.StatusError{Code: code, Source: lo.String(), Message: message}
	}
	components := make([]contract.ComponentStatus, 0, len(d.Components))
	for _, name := range d.Components {
		components = append(components, contract.ComponentStatus{Name: name, State: contract.StateFailed, Error: errorOf()})
	}
	return contract.DeploymentStatus{
		DeploymentID: d.ID, DeviceID: deviceID, AdoptedManifestVersion: d.Adopted,
		Status:     contract.DeploymentState{State: contract.StateFailed, Error: errorOf()},
		Components: components,
	}
}

// severity orders component states by SPEC §7.2: failed > removing > installing > pending >
// installed > removed.
func severity(s contract.ComponentState) int {
	switch s {
	case contract.StateFailed:
		return 5
	case contract.StateRemoving:
		return 4
	case contract.StateInstalling:
		return 3
	case contract.StatePending:
		return 2
	case contract.StateInstalled:
		return 1
	default: // removed
		return 0
	}
}

// aggregate returns the most severe state of components (SPEC §7.2). A deployment with no
// components is `pending`: nothing of it is installed yet.
func aggregate(components []contract.ComponentStatus) contract.ComponentState {
	if len(components) == 0 {
		return contract.StatePending
	}
	state := components[0].State
	for _, c := range components[1:] {
		if severity(c.State) > severity(state) {
			state = c.State
		}
	}
	return state
}
