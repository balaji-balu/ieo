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
func (e *Executor) Inventory(ctx context.Context, host contract.HostID) (contract.Inventory, error) {
	stored, err := e.store.Load(ctx)
	if err != nil {
		return contract.Inventory{}, fmt.Errorf("build inventory: %w", err)
	}
	inv := contract.Inventory{HostID: host, At: e.clock.Now()}
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
