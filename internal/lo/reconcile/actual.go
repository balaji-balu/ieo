package reconcile

import (
	"slices"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/plan"
	"github.com/balaji-balu/ieo/internal/lo/store"
)

// effect says what a report did to the actual state held for its host.
type effect int

const (
	// unchanged: the report says what is already held.
	unchanged effect = iota
	// changed: the state returned differs from the one held.
	changed
	// stale: the report is older than the state held, and is dropped (SPEC §7.5).
	stale
)

// applyStatus returns the actual state of a host after one status event from it, and what the
// event did to it (SPEC §7.5, ADR 0015):
//
//   - `removing` sets every component held for the deployment to `removing`, without its error,
//     and keeps the held digest;
//   - `removed` drops the deployment;
//   - either of them for a deployment not held changes nothing, and the event's digest is not
//     compared with the one held;
//   - any other state sets the component the event names when the event is at the held digest. At
//     another digest, or for a deployment not held, the deployment's entry becomes that one
//     component at the event's digest: no state is carried from one digest to another.
//
// An event that reports what a already holds changes nothing. A changed state is reported at the
// event's `at` and shares nothing with a or ev. a is not modified; it is returned as it is when
// nothing changed.
func applyStatus(a store.HostActual, ev contract.ComponentStatusEvent) (store.HostActual, effect) {
	i := slices.IndexFunc(a.Deployments, func(d contract.InventoryDeployment) bool { return d.DeploymentID == ev.DeploymentID })
	removal := ev.State == contract.StateRemoving || ev.State == contract.StateRemoved
	if removal && i < 0 {
		return a, unchanged
	}
	out := store.HostActual{ReportedAt: ev.At, Deployments: cloneDeployments(a.Deployments)}
	switch ev.State {
	case contract.StateRemoved:
		out.Deployments = slices.Delete(out.Deployments, i, i+1)
	case contract.StateRemoving:
		for j, c := range out.Deployments[i].Components {
			out.Deployments[i].Components[j] = contract.ComponentStatus{Name: c.Name, State: contract.StateRemoving}
		}
	default:
		reported := contract.ComponentStatus{Name: ev.Component, State: ev.State, Error: cloneError(ev.Error)}
		if i < 0 || out.Deployments[i].Digest != ev.Digest {
			entry := contract.InventoryDeployment{
				DeploymentID: ev.DeploymentID, Digest: ev.Digest, Components: []contract.ComponentStatus{reported},
			}
			if i < 0 {
				out.Deployments = append(out.Deployments, entry)
			} else {
				out.Deployments[i] = entry
			}
			break
		}
		d := &out.Deployments[i]
		if j := slices.IndexFunc(d.Components, func(c contract.ComponentStatus) bool { return c.Name == ev.Component }); j >= 0 {
			d.Components[j] = reported
		} else {
			d.Components = append(d.Components, reported)
		}
	}
	if slices.EqualFunc(out.Deployments, a.Deployments, sameDeployment) {
		return a, unchanged
	}
	return out, changed
}

// applyInventory returns the actual state of a host after inv, and whether inv was applied.
func applyInventory(held *store.HostActual, inv contract.Inventory, lastNotApplied bool) (store.HostActual, bool) {
	return store.HostActual{ReportedAt: inv.At, Deployments: cloneDeployments(inv.Deployments)}, true
}

// sameDeployment reports whether x and y are one deployment at one digest with the same components
// in the same order, each in the same state with the same error.
func sameDeployment(x, y contract.InventoryDeployment) bool {
	return x.DeploymentID == y.DeploymentID && x.Digest == y.Digest &&
		slices.EqualFunc(x.Components, y.Components, func(c, d contract.ComponentStatus) bool {
			if c.Name != d.Name || c.State != d.State || (c.Error == nil) != (d.Error == nil) {
				return false
			}
			return c.Error == nil || *c.Error == *d.Error
		})
}

// cloneDeployments returns a deep copy of deployments. The copy and each of its component lists
// are empty, not nil, when they hold nothing, as the store returns them.
func cloneDeployments(deployments []contract.InventoryDeployment) []contract.InventoryDeployment {
	out := make([]contract.InventoryDeployment, 0, len(deployments))
	for _, d := range deployments {
		components := make([]contract.ComponentStatus, 0, len(d.Components))
		for _, c := range d.Components {
			c.Error = cloneError(c.Error)
			components = append(components, c)
		}
		out = append(out, contract.InventoryDeployment{DeploymentID: d.DeploymentID, Digest: d.Digest, Components: components})
	}
	return out
}

func cloneError(e *contract.StatusError) *contract.StatusError {
	if e == nil {
		return nil
	}
	c := *e
	return &c
}

// have returns a host's actual state as the planner reads it (SPEC §8.5 step 2), without the
// deployments in unresolved: those get no command, so the planner must not see them on the host
// and plan their removal (SPEC §8.4 interim rule).
func have(a store.HostActual, unresolved map[uuid.UUID]bool) map[uuid.UUID]plan.Actual {
	out := make(map[uuid.UUID]plan.Actual, len(a.Deployments))
	for _, d := range a.Deployments {
		if unresolved[d.DeploymentID] {
			continue
		}
		components := make(map[string]contract.ComponentState, len(d.Components))
		for _, c := range d.Components {
			components[c.Name] = c.State
		}
		out[d.DeploymentID] = plan.Actual{Digest: d.Digest, Components: components}
	}
	return out
}
