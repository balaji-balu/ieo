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
// DeploymentStatus says of it (SPEC §8.7).
type wanted struct {
	digest     contract.Digest
	deployment contract.ApplicationDeployment
	status     status.Desired
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
	// target is the `deviceId` its YAML names, as far as it can be read.
	target contract.DeviceID
	status status.Desired
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
		code := contract.CodeInvalidDeployment
		var reason string
		switch {
		case err != nil:
			reason = err.Error()
		case target.Site != site:
			reason = "its deviceId " + target.String() + " names another site"
		case target.Autonomous:
			reason = "autonomous placement is not implemented"
			code = contract.CodeAutonomousNotSupported // SPEC §8.4 interim rule
		case target.Host == "":
			reason = "its deviceId " + target.String() + " names no host"
		}
		if reason != "" {
			outline := contract.ReadDeploymentOutline(d.YAML)
			skipped = append(skipped, unresolved{
				id: id, digest: d.Digest, reason: reason, code: code, target: outline.DeviceID,
				status: status.Desired{ID: id, Digest: d.Digest, AdoptedVersion: d.AdoptedVersion, Components: outline.Components},
			})
			continue
		}
		if byHost[target.Host] == nil {
			byHost[target.Host] = map[uuid.UUID]wanted{}
		}
		components := make([]string, 0, len(dep.Spec.DeploymentProfile.Components))
		for _, c := range dep.Spec.DeploymentProfile.Components {
			components = append(components, c.Name)
		}
		byHost[target.Host][id] = wanted{
			digest: d.Digest, deployment: dep,
			status: status.Desired{ID: id, Digest: d.Digest, AdoptedVersion: d.AdoptedVersion, Components: components},
		}
	}
	slices.SortFunc(skipped, func(x, y unresolved) int { return bytes.Compare(x.id[:], y.id[:]) })
	return byHost, skipped
}
