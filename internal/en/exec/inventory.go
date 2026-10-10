package exec

import (
	"context"
	"errors"

	"github.com/balaji-balu/ieo/internal/contract"
)

// Inventory returns the EN's complete actual state as it is recorded: every deployment with an
// applied digest and the last state of each of its components (SPEC §4.1.12, §11.2). Deployments
// are in order of ID and components in order of name, so the same state gives the same message.
// A deployment whose first Apply has recorded no digest yet is not listed.
func (e *Executor) Inventory(ctx context.Context, host contract.HostID) (contract.Inventory, error) {
	return contract.Inventory{}, errors.New("exec: Inventory is not implemented")
}
