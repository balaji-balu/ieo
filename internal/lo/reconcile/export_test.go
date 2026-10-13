package reconcile

import (
	"context"

	"github.com/balaji-balu/ieo/internal/contract"
)

// RunPending runs, in the caller's goroutine and one host after the other, the pass of every marked
// host that has none in progress. A test that does not call Run sees every command of a trigger
// once RunPending returns.
func (r *Reconciler) RunPending(ctx context.Context) {
	for _, id := range r.claim() {
		r.runHost(ctx, id)
	}
}

// Holds reports whether the Reconciler holds anything for host.
func (r *Reconciler) Holds(host contract.HostID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.hosts[host] != nil
}

// PassRunning reports whether a pass of host is in progress.
func (r *Reconciler) PassRunning(host contract.HostID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.hosts[host]
	return h != nil && h.running
}
