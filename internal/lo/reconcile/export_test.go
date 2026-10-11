package reconcile

import (
	"context"

	"github.com/balaji-balu/ieo/internal/contract"
)

// RunPending runs the passes of the marked hosts in the caller's goroutine, so a test that does not
// call Run sees every command of a trigger once RunPending returns.
func (r *Reconciler) RunPending(ctx context.Context) { r.runPending(ctx) }

// PassRunning reports whether a pass of host is in progress.
func (r *Reconciler) PassRunning(host contract.HostID) bool { return r.passRunning(host) }
