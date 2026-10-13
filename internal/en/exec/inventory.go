package exec

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/balaji-balu/ieo/internal/contract"
)

// Inventory returns the EN's complete actual state as it is recorded: every deployment with an
// applied digest and the last state of each of its components (SPEC §4.1.12, §11.2). Deployments
// are in order of ID and components in order of name, so the same state gives the same message.
// A deployment whose first Apply has recorded no digest yet is not listed.
//
// Its `at` is taken before the record is read, and a status event's after the change is recorded.
// So an event for a change this Inventory does not show has a later `at`, and the LO, which drops
// a report older than the one it holds, keeps that event (SPEC §7.5, §11.2). `removing` records
// nothing: until `removed`, an Inventory lists that deployment as it was before the Remove.
func (e *Executor) Inventory(ctx context.Context, host contract.HostID) (contract.Inventory, error) {
	at := e.clock.Now()
	stored, err := e.store.Load(ctx)
	if err != nil {
		return contract.Inventory{}, fmt.Errorf("build inventory: %w", err)
	}
	inv := contract.Inventory{HostID: host, At: at}
	for id, d := range stored {
		components := make([]contract.ComponentStatus, 0, len(d.Components))
		for _, status := range d.Components {
			components = append(components, status)
		}
		slices.SortFunc(components, func(a, b contract.ComponentStatus) int { return cmp.Compare(a.Name, b.Name) })
		inv.Deployments = append(inv.Deployments, contract.InventoryDeployment{
			DeploymentID: id, Digest: d.Applied.Digest, Components: components,
		})
	}
	slices.SortFunc(inv.Deployments, func(a, b contract.InventoryDeployment) int {
		return bytes.Compare(a.DeploymentID[:], b.DeploymentID[:])
	})
	return inv, nil
}
