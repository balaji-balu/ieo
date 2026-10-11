// Package reconcile is the LO's single authority (SPEC §7.6): it holds every host's actual state,
// changes it only on what the hosts report, and sends the Apply and Remove commands that converge
// each host on desired state (SPEC §8.5, §16.4).
//
// It knows nothing of the site message transport. Its caller decodes each message, reads the host
// from the subject it arrived on, and gives a Sender that delivers a command (SPEC §11.2).
package reconcile

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/store"
	losync "github.com/balaji-balu/ieo/internal/lo/sync"
	"github.com/balaji-balu/ieo/internal/platform"
)

// Sender delivers one command to a host and returns the host's ack (SPEC §11.2).
type Sender interface {
	// Send returns the ack of cmd from host. ctx carries the deadline for the ack
	// (`lo.command.ack_timeout`). Any error means the command was not acknowledged: no subscriber,
	// no reply in time and no connection are one outcome for the Reconciler (SPEC §11.2).
	Send(ctx context.Context, host contract.HostID, cmd contract.Command) (contract.CommandAck, error)
}

// Store is the part of the LO store the Reconciler reads and writes (ADR 0015).
type Store interface {
	// Load returns the stored sync state; the Reconciler reads desired state from it.
	Load(ctx context.Context) (losync.State, error)
	// LoadHosts returns every host the store holds.
	LoadHosts(ctx context.Context) (map[contract.HostID]store.HostState, error)
	// PutActual replaces the actual state of host in one transaction.
	PutActual(ctx context.Context, host contract.HostID, a store.HostActual) error
}

// Config configures a Reconciler.
type Config struct {
	// SiteID is the LO's site. It is logged on every line (SPEC §13.1), and a desired deployment
	// for another site gets no command (SPEC §8.4).
	SiteID contract.SiteID
	// AckTimeout is how long a host has to acknowledge a command (`lo.command.ack_timeout`).
	AckTimeout time.Duration
	// Interval is the time between two passes over all hosts by the safety timer
	// (`lo.reconcile.interval`).
	Interval time.Duration
	// Clock times the safety timer; nil means platform.SystemClock.
	Clock platform.Clock
	// Log receives the Reconciler's lines; nil means slog.Default.
	Log *slog.Logger
}

// Reconciler is the LO's single authority for one site. It is safe for concurrent use.
type Reconciler struct{}

// New returns a Reconciler holding the hosts and the desired state of store. Every host starts
// `Unknown` and gets no command until its next Inventory, so actual state from before the LO
// stopped never drives a command (SPEC §7.3 interim rule, ADR 0015). It fails if the store cannot
// be read or cfg has no site, ack timeout or interval.
func New(ctx context.Context, cfg Config, st Store, sender Sender) (*Reconciler, error) {
	return nil, errors.New("not implemented")
}

// OnInventory replaces the actual state of host with inv and marks the host for a pass; the host
// is `Online` from then on (SPEC §7.3 interim rule, §7.5). host is the host of the subject inv
// arrived on: an inv that names another host is logged and dropped (SPEC §11.2). So is one the
// store does not take; the host's next Inventory brings its state again (SPEC §7.6).
func (r *Reconciler) OnInventory(ctx context.Context, host contract.HostID, inv contract.Inventory) {
}

// OnStatus applies one status event to the actual state of host, the host of the subject ev
// arrived on (SPEC §7.5, ADR 0015). It starts no pass (SPEC §7.5 interim rule). An event from a
// host with no actual state is logged and dropped (SPEC §11.2), and so is one the store does not
// take.
func (r *Reconciler) OnStatus(ctx context.Context, host contract.HostID, ev contract.ComponentStatusEvent) {
}

// ReconcileAll reads desired state from the store and marks every `Online` host for a pass (SPEC
// §7.5 `Manifest Accepted` and `Reconcile Safety Timer Fired`). It returns once the hosts are
// marked, not once their commands are acknowledged (SPEC §8.2 step 8); Run sends them. If desired
// state cannot be read it logs, marks no host and keeps the desired state it had.
func (r *Reconciler) ReconcileAll(ctx context.Context) {}

// Run runs the passes of marked hosts and calls ReconcileAll every Config.Interval, until ctx
// ends; it then waits for the passes in progress and returns ctx's error. At most one pass runs
// per host at a time, and a host marked during its pass gets one more (SPEC §7.6). Call it once.
func (r *Reconciler) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

// runPending runs, one host after the other, the pass of every marked host that has none in
// progress, and returns when they are done.
func (r *Reconciler) runPending(ctx context.Context) {}

// passRunning reports whether a pass of host is in progress.
func (r *Reconciler) passRunning(host contract.HostID) bool { return false }
