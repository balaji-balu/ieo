package reconcile

import (
	"slices"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/plan"
	"github.com/balaji-balu/ieo/internal/lo/store"
)

// applyStatus returns the actual state of a host after one status event from it, and whether the
// event changed anything (SPEC §7.5, ADR 0015):
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
// A changed state is reported at the event's `at` and shares nothing with a or ev. a is not
// modified; it is returned as it is when nothing changed.
func applyStatus(a store.HostActual, ev contract.ComponentStatusEvent) (store.HostActual, bool) {
	i := slices.IndexFunc(a.Deployments, func(d contract.InventoryDeployment) bool { return d.DeploymentID == ev.DeploymentID })
	removal := ev.State == contract.StateRemoving || ev.State == contract.StateRemoved
	if removal && i < 0 {
		return a, false
	}
	out := store.HostActual{ReportedAt: ev.At, Deployments: cloneDeployments(a.Deployments)}
	switch {
	case ev.State == contract.StateRemoved:
		out.Deployments = slices.Delete(out.Deployments, i, i+1)
	case ev.State == contract.StateRemoving:
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
	return out, true
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
