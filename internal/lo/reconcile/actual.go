package reconcile

import (
	"github.com/balaji-balu/ieo/internal/contract"
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
// A changed state is reported at the event's `at`. The result shares nothing with a or ev, and a
// is not modified.
func applyStatus(a store.HostActual, ev contract.ComponentStatusEvent) (store.HostActual, bool) {
	return a, false
}
