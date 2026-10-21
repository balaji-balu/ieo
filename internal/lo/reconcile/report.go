package reconcile

import (
	"bytes"
	"context"
	"maps"
	"slices"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/status"
	"github.com/balaji-balu/ieo/internal/lo/store"
)

// gatewayKey is what a gateway error was reported at: it is not reported again while both stay
// the same (SPEC §8.7).
type gatewayKey struct {
	digest contract.Digest
	code   string
}

// reportGateway reports each desired deployment that fails target resolution, unless it was
// reported at the same digest and code before, in this LO run (SPEC §8.4, §8.7): the unresolved
// ones, and those directed to a host the LO has no record of, with code 101. A deployment that no
// longer fails is forgotten, so it is reported again if it fails once more. r.mu is held.
func (r *Reconciler) reportGateway(ctx context.Context, byHost map[contract.HostID]map[uuid.UUID]wanted, unresolvedList []unresolved) {
	if r.status == nil {
		return
	}
	lo := contract.DeviceID{Site: r.site}
	type failure struct {
		key     gatewayKey
		device  contract.DeviceID
		desired status.Desired
		message string
	}
	failures := map[uuid.UUID]failure{}
	for _, u := range unresolvedList {
		device := lo
		if t := u.target; t.Site == r.site && t.Host != "" && !t.Autonomous {
			device = t // SPEC §8.7: the directed target, when it is a host of the site
		}
		failures[u.id] = failure{gatewayKey{u.digest, u.code}, device, u.status, u.reason}
	}
	for hostID, wants := range byHost {
		if r.hosts[hostID] != nil {
			continue
		}
		// SPEC §8.4: unknown is no record of the host, not offline.
		for id, w := range wants {
			failures[id] = failure{
				gatewayKey{w.digest, contract.CodeUnknownDevice}, contract.DeviceID{Site: r.site, Host: hostID}, w.status,
				"no host " + string(hostID) + " has reported to the LO of site " + string(r.site),
			}
		}
	}
	reported := make(map[uuid.UUID]gatewayKey, len(failures))
	for _, id := range sortedIDs(failures) {
		f := failures[id]
		reported[id] = f.key
		if r.reported[id] == f.key {
			continue
		}
		r.status.Put(ctx, status.Gateway(lo, f.device, f.desired, f.key.code, f.message), f.key.digest)
	}
	r.reported = reported
}

// reportHost reports each deployment desired on host whose state there differs between before and
// after, the actual state held for the host before and after a report; nil is no state (SPEC
// §8.7). When only is set, the report could change only that deployment, and no other is looked
// at. r.mu is held.
func (r *Reconciler) reportHost(ctx context.Context, hostID contract.HostID, before, after *store.HostActual, only *uuid.UUID) {
	if r.status == nil {
		return
	}
	wants := r.want[hostID]
	ids := sortedIDs(wants)
	if only != nil {
		ids = nil
		if _, ok := wants[*only]; ok {
			ids = []uuid.UUID{*only}
		}
	}
	device := contract.DeviceID{Site: r.site, Host: hostID}
	for _, id := range ids {
		b, a := find(before, id), find(after, id)
		if b == nil && a == nil || b != nil && a != nil && sameDeployment(*b, *a) {
			continue
		}
		w := wants[id]
		r.status.Put(ctx, status.FromHost(device, w.status, a), w.digest)
	}
}

// find returns the entry of deployment id in a, nil if there is none.
func find(a *store.HostActual, id uuid.UUID) *contract.InventoryDeployment {
	if a == nil {
		return nil
	}
	for i := range a.Deployments {
		if a.Deployments[i].DeploymentID == id {
			return &a.Deployments[i]
		}
	}
	return nil
}

// sortedIDs returns the keys of m in byte order, so statuses are put in the same order every run.
func sortedIDs[V any](m map[uuid.UUID]V) []uuid.UUID {
	return slices.SortedFunc(maps.Keys(m), func(x, y uuid.UUID) int { return bytes.Compare(x[:], y[:]) })
}
