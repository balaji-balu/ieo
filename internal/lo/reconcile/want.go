package reconcile

import (
	"bytes"
	"slices"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/status"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
)

// wanted is one desired deployment resolved to a host: what an Apply for it carries, and what its
// status needs (SPEC §8.7).
type wanted struct {
	digest     contract.Digest
	deployment contract.ApplicationDeployment
	adopted    contract.ManifestVersion
}

// desired returns what the status mapping needs of w, the deployment id.
func (w wanted) desired(id uuid.UUID) status.Desired {
	names := make([]string, 0, len(w.deployment.Spec.DeploymentProfile.Components))
	for _, c := range w.deployment.Spec.DeploymentProfile.Components {
		names = append(names, c.Name)
	}
	return status.Desired{ID: id, Digest: w.digest, Adopted: w.adopted, Components: names}
}

// unresolved is a desired deployment that gets no command on any host, why, and how it is
// reported (SPEC §8.4 interim rule, §8.7).
type unresolved struct {
	id     uuid.UUID
	digest contract.Digest
	reason string
	// code is the gateway error it is reported with: 103 for an autonomous deployment until
	// placement is implemented (roadmap slice H), IEO-INVALID-DEPLOYMENT otherwise.
	code string
	// target is its directed target when that is a host of the site, and the zero DeviceID
	// otherwise; the status then goes to the LO's device ID.
	target contract.DeviceID
	// components are the names of its components, as far as its YAML can be read.
	components []string
	adopted    contract.ManifestVersion
}

// desired returns what the status mapping needs of u.
func (u unresolved) desired() status.Desired {
	return status.Desired{ID: u.id, Digest: u.digest, Adopted: u.adopted, Components: u.components}
}

// resolve sorts desired state by the host each deployment is directed to (SPEC §8.4, §8.5 step
// 1). A deployment that is not directed to a host of site is returned in the second result,
// sorted by ID: one whose YAML an EN would refuse or whose `id` is not the ID it is stored under,
// one for another site or for the LO itself, and, until placement is implemented (roadmap slice
// H), an autonomous one.
func resolve(site contract.SiteID, desired map[uuid.UUID]losync.Desired) (map[contract.HostID]map[uuid.UUID]wanted, []unresolved) {
	byHost := map[contract.HostID]map[uuid.UUID]wanted{}
	var skipped []unresolved
	for id, d := range desired {
		dep, err := contract.DecodeApplicationDeployment(id, d.YAML)
		target := dep.Metadata.DeviceID
		var components []string
		for _, c := range dep.Spec.DeploymentProfile.Components {
			components = append(components, c.Name)
		}
		var reason string
		code := contract.CodeInvalidDeployment
		switch {
		case err != nil:
			reason = err.Error()
			target, components = contract.OutlineOfDeployment(d.YAML)
		case target.Site != site:
			reason = "its deviceId " + target.String() + " names another site"
		case target.Autonomous:
			reason = "autonomous placement is not implemented"
			code = contract.CodeAutonomousPlacementNotSupported
		case target.Host == "":
			reason = "its deviceId " + target.String() + " names no host"
		}
		if reason != "" {
			u := unresolved{id: id, digest: d.Digest, reason: reason, code: code, components: components, adopted: d.AdoptedVersion}
			if target.Site == site && target.Host != "" && !target.Autonomous {
				u.target = target
			}
			skipped = append(skipped, u)
			continue
		}
		if byHost[target.Host] == nil {
			byHost[target.Host] = map[uuid.UUID]wanted{}
		}
		byHost[target.Host][id] = wanted{digest: d.Digest, deployment: dep, adopted: d.AdoptedVersion}
	}
	slices.SortFunc(skipped, func(x, y unresolved) int { return bytes.Compare(x.id[:], y.id[:]) })
	return byHost, skipped
}
