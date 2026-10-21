// Package status builds the Margo DeploymentStatus the LO sends to the CO (SPEC §7.2, §8.7, §10):
// one component entry per component of the deployment, the aggregate state, and the error
// sources. It holds no state and does no I/O; when a status is built, and where it goes, is the
// caller's.
package status

import (
	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
)

// Desired is what a DeploymentStatus says of one desired deployment.
type Desired struct {
	// ID is the ID the State Manifest lists the deployment under, whatever its YAML's `id` says
	// (SPEC §8.7).
	ID uuid.UUID
	// Digest is the deployment's current digest; only reports at it count.
	Digest contract.Digest
	// AdoptedVersion is the `adopted_manifest_version` recorded for Digest (SPEC §8.2 step 6).
	AdoptedVersion contract.ManifestVersion
	// Components are the component names of the deployment YAML, in its order; none when the YAML
	// cannot be read for them.
	Components []string
}

// FromHost returns the DeploymentStatus of d on host, from actual, what host reports of d; nil
// when it reports nothing (SPEC §8.7). Every component of d has one entry, in d's order: its
// reported state at d.Digest, or `pending` when it has none there. A component error has the
// component's name as its source, whatever the host sent. The state is the most severe of the
// components' (SPEC §7.2). The result shares nothing with actual.
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
			copied := *c.Error
			copied.Source = name // SPEC §8.7
			e = &copied
		}
		components = append(components, contract.ComponentStatus{Name: name, State: c.State, Error: e})
	}
	return contract.DeploymentStatus{
		DeploymentID:           d.ID,
		DeviceID:               host,
		AdoptedManifestVersion: d.AdoptedVersion,
		Status:                 contract.DeploymentState{State: aggregate(components)},
		Components:             components,
	}
}

// Gateway returns the DeploymentStatus of a gateway error of d: no host reported it, and the LO,
// whose device ID is lo, fails it with code and message (SPEC §8.4, §8.7). device is the status's
// `deviceId`: the directed host, when it is one of the LO's site, or else lo. The state is
// `failed` with that error, source lo, and so is every component of d, with the same error.
func Gateway(lo, device contract.DeviceID, d Desired, code, message string) contract.DeploymentStatus {
	e := contract.StatusError{Code: code, Source: lo.String(), Message: message}
	components := make([]contract.ComponentStatus, 0, len(d.Components))
	for _, name := range d.Components {
		ce := e
		components = append(components, contract.ComponentStatus{Name: name, State: contract.StateFailed, Error: &ce})
	}
	return contract.DeploymentStatus{
		DeploymentID:           d.ID,
		DeviceID:               device,
		AdoptedManifestVersion: d.AdoptedVersion,
		Status:                 contract.DeploymentState{State: contract.StateFailed, Error: &e},
		Components:             components,
	}
}

// severity orders the states of SPEC §7.2, most severe first.
var severity = map[contract.ComponentState]int{
	contract.StateFailed:     0,
	contract.StateRemoving:   1,
	contract.StateInstalling: 2,
	contract.StatePending:    3,
	contract.StateInstalled:  4,
	contract.StateRemoved:    5,
}

// aggregate returns the most severe state of components (SPEC §7.2); `pending` when there are
// none, as a deployment no host has reported is. A state §7.2 does not name is not one a host may
// report (SPEC §7.1): it counts as `failed`, so the result is always a Margo state.
func aggregate(components []contract.ComponentStatus) contract.ComponentState {
	worst := contract.StatePending
	for i, c := range components {
		s := c.State
		if _, ok := severity[s]; !ok {
			s = contract.StateFailed
		}
		if i == 0 || severity[s] < severity[worst] {
			worst = s
		}
	}
	return worst
}
