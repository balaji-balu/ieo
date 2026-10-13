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
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/plan"
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
type Reconciler struct {
	site       contract.SiteID
	store      Store
	sender     Sender
	ackTimeout time.Duration
	interval   time.Duration
	clock      platform.Clock
	log        *slog.Logger

	// wake tells Run that a host was marked. It holds at most one signal: Run looks at every host
	// each time it wakes.
	wake chan struct{}

	// mu is the one lock of SPEC §7.6: it guards everything below, and is held while a report is
	// written to the store, so the state held never differs from the state stored. It is not held
	// while a command is sent.
	mu    sync.Mutex
	hosts map[contract.HostID]*host
	// want holds desired state as the last ReconcileAll resolved it, and skipped the deployments
	// that get no command (SPEC §8.4). Both are replaced whole, never changed.
	want    map[contract.HostID]map[uuid.UUID]wanted
	skipped map[uuid.UUID]bool
	// known says the LO knows what should run: it has accepted a State Manifest, or holds the
	// desired state of the one it is accepting. Until then no pass sends a command (SPEC §8.5).
	known bool
	// stale says the last read of desired state failed, so want may be older than the store's.
	stale bool
}

// host is what the Reconciler holds for one host.
type host struct {
	// actual is the host's actual state, equal to the store's; nil until it is known.
	actual *store.HostActual
	// online says the host has published an Inventory since the LO started (SPEC §7.3 interim
	// rule). Only an online host is marked.
	online bool
	// marked says a pass is due, and running that one is in progress.
	marked, running bool
}

// New returns a Reconciler holding the hosts and the desired state of store. Every host starts
// `Unknown` and gets no command until its next Inventory, so actual state from before the LO
// stopped never drives a command (SPEC §7.3 interim rule, ADR 0015). It fails if the store cannot
// be read or cfg has no site, ack timeout or interval.
func New(ctx context.Context, cfg Config, st Store, sender Sender) (*Reconciler, error) {
	switch {
	case cfg.SiteID == "":
		return nil, errors.New("reconcile: no site ID")
	case cfg.AckTimeout <= 0:
		return nil, errors.New("reconcile: the command ack timeout must be positive")
	case cfg.Interval <= 0:
		return nil, errors.New("reconcile: the reconcile interval must be positive")
	}
	r := &Reconciler{
		site: cfg.SiteID, store: st, sender: sender, ackTimeout: cfg.AckTimeout, interval: cfg.Interval,
		clock: cfg.Clock, log: cfg.Log,
		wake:  make(chan struct{}, 1),
		hosts: map[contract.HostID]*host{},
	}
	if r.clock == nil {
		r.clock = platform.SystemClock()
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	r.log = r.log.With(slog.String("site_id", string(cfg.SiteID))) // SPEC §13.1

	stored, err := st.LoadHosts(ctx)
	if err != nil {
		return nil, fmt.Errorf("reconcile: %w", err)
	}
	for id, h := range stored {
		r.hosts[id] = &host{actual: h.Actual}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.loadDesired(ctx); err != nil {
		return nil, fmt.Errorf("reconcile: load desired state: %w", err)
	}
	r.logUnknown(ctx)
	return r, nil
}

// loadDesired reads desired state from the store and resolves it, logging each deployment that
// gets no command (SPEC §8.4 interim rule). On an error nothing changes. r.mu is held.
func (r *Reconciler) loadDesired(ctx context.Context) error {
	st, err := r.store.Load(ctx)
	if err != nil {
		return err
	}
	byHost, unresolved := resolve(r.site, st.Desired)
	skipped := make(map[uuid.UUID]bool, len(unresolved))
	for _, u := range unresolved {
		skipped[u.id] = true
		r.log.LogAttrs(ctx, slog.LevelWarn, "desired deployment gets no command",
			slog.String("deployment_id", u.id.String()), slog.String("digest", string(u.digest)), slog.String("reason", u.reason))
	}
	r.want, r.skipped, r.stale = byHost, skipped, false
	// The sync loop replaces desired state and starts reconciling before it commits the version
	// (SPEC §8.2 steps 6–8), so desired state that holds a deployment is a manifest's too.
	r.known = st.Version > 0 || len(st.Desired) > 0
	return nil
}

// logUnknown logs that no pass sends a command, if the LO does not know yet what should run (SPEC
// §8.5). r.mu is held.
func (r *Reconciler) logUnknown(ctx context.Context) {
	if !r.known {
		r.log.LogAttrs(ctx, slog.LevelInfo, "no State Manifest accepted yet", slog.String("effect", "no command is sent"))
	}
}

// OnInventory replaces the actual state of host with inv and marks the host for a pass; the host
// is `Online` from then on (SPEC §7.3 interim rule, §7.5). hostID is the host of the subject inv
// arrived on: an inv that names another host is logged and dropped (SPEC §11.2). So is one the
// store does not take; the host's next Inventory brings its state again (SPEC §7.6).
func (r *Reconciler) OnInventory(ctx context.Context, hostID contract.HostID, inv contract.Inventory) {
	log := r.log.With(slog.String("host_id", string(hostID)))
	if inv.HostID != hostID {
		log.LogAttrs(ctx, slog.LevelWarn, "inventory dropped", slog.String("payload_host_id", string(inv.HostID)),
			slog.String("reason", "the payload names another host than the subject"))
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	actual, _ := applyInventory(nil, inv, false)
	if err := r.store.PutActual(ctx, hostID, actual); err != nil {
		log.LogAttrs(ctx, slog.LevelError, "inventory dropped", slog.String("reason", err.Error()))
		return
	}
	h := r.hosts[hostID]
	if h == nil {
		h = &host{}
		r.hosts[hostID] = h
	}
	h.actual, h.online = &actual, true
	r.mark(h)
}

// OnStatus applies one status event to the actual state of host, the host of the subject ev
// arrived on (SPEC §7.5, ADR 0015). It starts no pass (SPEC §7.5 interim rule). An event from a
// host with no actual state is logged and dropped (SPEC §11.2), and so is one the store does not
// take.
func (r *Reconciler) OnStatus(ctx context.Context, hostID contract.HostID, ev contract.ComponentStatusEvent) {
	log := r.log.With(slog.String("host_id", string(hostID)),
		slog.String("deployment_id", ev.DeploymentID.String()), slog.String("digest", string(ev.Digest)))

	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.hosts[hostID]
	if h == nil || h.actual == nil {
		log.LogAttrs(ctx, slog.LevelWarn, "status event dropped", slog.String("reason", "no actual state is held for the host"))
		return
	}
	actual, effect := applyStatus(*h.actual, ev)
	if effect != changed {
		return
	}
	if err := r.store.PutActual(ctx, hostID, actual); err != nil {
		log.LogAttrs(ctx, slog.LevelError, "status event dropped", slog.String("reason", err.Error()))
		return
	}
	h.actual = &actual
}

// ReconcileAll reads desired state from the store and marks every `Online` host for a pass (SPEC
// §7.5 `Manifest Accepted` and `Reconcile Safety Timer Fired`). It returns once the hosts are
// marked, not once their commands are acknowledged (SPEC §8.2 step 8); Run sends them. If desired
// state cannot be read it logs and marks no host. The desired state it had may then be older than
// the store's, so the next pass of a host reads it again before it plans.
func (r *Reconciler) ReconcileAll(ctx context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.loadDesired(ctx); err != nil {
		r.stale = true
		r.log.LogAttrs(ctx, slog.LevelError, "reconcile all hosts failed", slog.String("reason", "load desired state: "+err.Error()))
		return
	}
	r.logUnknown(ctx)
	for _, h := range r.hosts {
		if h.online {
			r.mark(h)
		}
	}
	r.logWaiting(ctx)
}

// logWaiting logs each desired deployment whose host has published no Inventory since the LO
// started: it gets no command until the host does (SPEC §7.3 interim rule). Status reporting
// replaces this trace (SPEC §8.4, roadmap slice E). r.mu is held.
func (r *Reconciler) logWaiting(ctx context.Context) {
	for hostID, wants := range r.want {
		if h := r.hosts[hostID]; h != nil && h.online {
			continue
		}
		for id, w := range wants {
			r.log.LogAttrs(ctx, slog.LevelInfo, "desired deployment waits for an inventory of its host",
				slog.String("host_id", string(hostID)), slog.String("deployment_id", id.String()), slog.String("digest", string(w.digest)))
		}
	}
}

// mark notes that a pass of h is due and tells Run. r.mu is held.
func (r *Reconciler) mark(h *host) {
	h.marked = true
	select {
	case r.wake <- struct{}{}:
	default: // Run has a signal waiting, and looks at every host when it takes it
	}
}

// Run runs the passes of marked hosts and calls ReconcileAll every Config.Interval, until ctx
// ends; it then waits for the passes in progress and returns ctx's error. At most one pass runs
// per host at a time, and a host marked during its pass gets one more (SPEC §7.6). Call it once.
func (r *Reconciler) Run(ctx context.Context) error {
	// Every goroutine of the Reconciler is started here and waited for here (G-E5).
	var passes sync.WaitGroup
	defer passes.Wait()
	timer := r.clock.NewTimer(r.interval)
	defer func() { timer.Stop() }() // the timer of the last interval, not the first
	for {
		for _, id := range r.claim() {
			passes.Go(func() { r.runHost(ctx, id) })
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.wake:
		case <-timer.C():
			r.ReconcileAll(ctx)
			timer = r.clock.NewTimer(r.interval)
		}
	}
}

// claim returns the marked hosts that have no pass in progress, in ID order, and notes that each
// now has one: the caller must call runHost for every one of them.
func (r *Reconciler) claim() []contract.HostID {
	r.mu.Lock()
	defer r.mu.Unlock()
	var due []contract.HostID
	for id, h := range r.hosts {
		if h.marked && !h.running {
			h.running = true
			due = append(due, id)
		}
	}
	slices.Sort(due)
	return due
}

// runHost runs passes of a claimed host until it is no longer marked or ctx ends.
func (r *Reconciler) runHost(ctx context.Context, id contract.HostID) {
	for {
		cmds, due := r.plan(ctx, id)
		if !due {
			return
		}
		r.send(ctx, id, cmds)
	}
}

// plan starts one pass of a claimed host: it takes the host's mark and returns the commands that
// converge the host as the Reconciler sees it now (SPEC §8.5 steps 1–5). If the host is not marked
// or ctx has ended there is no pass: plan gives the claim back and returns false.
//
// Desired state is the one the last ReconcileAll read, with two exceptions, in which plan reads it
// again and the pass sends no command if it still cannot plan: no manifest was accepted then, as
// one may have been since (SPEC §8.5), and the last read failed.
func (r *Reconciler) plan(ctx context.Context, id contract.HostID) ([]contract.Command, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := r.hosts[id]
	if !h.marked || ctx.Err() != nil {
		h.running = false
		return nil, false
	}
	h.marked = false
	if !r.known || r.stale {
		if err := r.loadDesired(ctx); err != nil {
			r.log.LogAttrs(ctx, slog.LevelError, "pass sends no command", slog.String("host_id", string(id)),
				slog.String("reason", "load desired state: "+err.Error()))
			return nil, true
		}
		if !r.known {
			// SPEC §8.5: empty desired state is not yet a wish that nothing runs.
			return nil, true
		}
	}
	wants := r.want[id]
	digests := make(map[uuid.UUID]contract.Digest, len(wants))
	for dep, w := range wants {
		digests[dep] = w.digest
	}
	var cmds []contract.Command
	for _, c := range plan.ForHost(digests, have(*h.actual, r.skipped)) {
		cmd := contract.Command{CommandID: uuid.New(), Action: c.Action, DeploymentID: c.DeploymentID, Digest: c.Digest}
		if c.Action == contract.ActionApply {
			deployment := wants[c.DeploymentID].deployment
			cmd.Deployment = &deployment
		}
		cmds = append(cmds, cmd)
	}
	return cmds, true
}

// send sends the commands of one pass, one after the other, each with AckTimeout for its ack. A
// command that is rejected or not acknowledged is logged, and the next pass of the host sends it
// again (SPEC §7.5 interim rule). Nothing here changes actual state: only what the host reports
// does (SPEC §7.6). send stops, without logging a failure, when ctx ends.
//
// The commands were planned when the pass started. If desired or actual state changes while they
// are sent, the rest are still sent as planned: the change marked the host, so the pass that
// follows plans from the new state, and Apply and Remove are idempotent on the EN (SPEC §7.6).
func (r *Reconciler) send(ctx context.Context, id contract.HostID, cmds []contract.Command) {
	for _, cmd := range cmds {
		if ctx.Err() != nil {
			return
		}
		log := r.log.With(slog.String("host_id", string(id)), slog.String("deployment_id", cmd.DeploymentID.String()),
			slog.String("digest", string(cmd.Digest)), slog.String("command_id", cmd.CommandID.String()),
			slog.String("action", string(cmd.Action)))
		sendCtx, cancel := context.WithTimeout(ctx, r.ackTimeout)
		ack, err := r.sender.Send(sendCtx, id, cmd)
		cancel()
		switch {
		case err != nil && ctx.Err() != nil:
			return // the LO is stopping; the command did not fail
		case err != nil:
			log.LogAttrs(ctx, slog.LevelWarn, "command not acknowledged", slog.String("reason", err.Error()))
		case ack.CommandID != cmd.CommandID:
			log.LogAttrs(ctx, slog.LevelWarn, "command not acknowledged",
				slog.String("reason", "the ack is for command "+ack.CommandID.String()))
		case !ack.Accepted:
			var code, reason string
			if ack.Error != nil {
				code, reason = ack.Error.Code, ack.Error.Message
			}
			log.LogAttrs(ctx, slog.LevelWarn, "command rejected", slog.String("code", code), slog.String("reason", reason))
		default:
			log.LogAttrs(ctx, slog.LevelDebug, "command accepted")
		}
	}
}
