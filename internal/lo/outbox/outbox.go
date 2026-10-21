// Package outbox sends the LO's deployment statuses to the CO (SPEC §8.8). It holds the latest
// status of each deployment until the CO accepts it.
//
// [IEO, interim] Until roadmap slice J, the outbox lives in memory: an LO restart loses what it
// holds, and a failed send is tried again every retry interval, with no backoff.
package outbox

import (
	"bytes"
	"context"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/coapi"
	"github.com/balaji-balu/ieo/internal/platform"
)

// Client sends one status to the CO; *coapi.Client is one. A nil error means the CO answered 200
// or 201.
type Client interface {
	PostStatus(ctx context.Context, s contract.DeploymentStatus) *coapi.RequestError
}

// Config configures an Outbox.
type Config struct {
	// SiteID is the LO's site, logged on every line (SPEC §13.1).
	SiteID contract.SiteID
	// Client sends the statuses.
	Client Client
	// RetryInterval is how long a status the CO did not accept waits before it is sent again
	// (`lo.poll.interval` until roadmap slice J).
	RetryInterval time.Duration
	// Clock times the retries; nil means platform.SystemClock.
	Clock platform.Clock
	// Log receives the Outbox's lines; nil means slog.Default.
	Log *slog.Logger
}

// Outbox holds the latest status of each deployment and sends it to the CO. It is safe for
// concurrent use.
type Outbox struct {
	client Client
	retry  time.Duration
	clock  platform.Clock
	log    *slog.Logger

	// wake tells Run that Put added a status. It holds at most one signal: Run sends every status
	// held each time it wakes.
	wake chan struct{}

	mu sync.Mutex
	// entries holds the latest status of each deployment not yet accepted by the CO (SPEC
	// §4.1.10); seq numbers every Put, so a send knows whether its entry was replaced meanwhile.
	entries map[uuid.UUID]entry
	seq     uint64
}

// entry is one status waiting for the CO.
type entry struct {
	status contract.DeploymentStatus
	digest contract.Digest
	seq    uint64
}

// New returns an empty Outbox.
func New(cfg Config) *Outbox {
	o := &Outbox{
		client: cfg.Client, retry: cfg.RetryInterval, clock: cfg.Clock, log: cfg.Log,
		wake:    make(chan struct{}, 1),
		entries: map[uuid.UUID]entry{},
	}
	if o.clock == nil {
		o.clock = platform.SystemClock()
	}
	if o.log == nil {
		o.log = slog.Default()
	}
	o.log = o.log.With(slog.String("site_id", string(cfg.SiteID))) // SPEC §13.1
	return o
}

// Put replaces the status held for s's deployment with s, and has Run send it soon (SPEC §8.7,
// §8.8). digest is the deployment's digest, for the log lines. Put does not block on the CO.
func (o *Outbox) Put(_ context.Context, s contract.DeploymentStatus, digest contract.Digest) {
	o.mu.Lock()
	o.seq++
	o.entries[s.DeploymentID] = entry{status: s, digest: digest, seq: o.seq}
	o.mu.Unlock()
	select {
	case o.wake <- struct{}{}:
	default: // a wake is already pending
	}
}

// Run sends each status Put gives it, until ctx ends; it then returns ctx's error. A status the CO
// accepts (200 or 201) is dropped, unless a newer one replaced it while it was being sent; a status
// it does not accept stays and is sent again after the retry interval (SPEC §8.8). Call it once.
func (o *Outbox) Run(ctx context.Context) error {
	timer := o.clock.NewTimer(o.retry)
	defer func() { timer.Stop() }() // the timer of the last interval, not the first
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-o.wake:
		case <-timer.C():
			// The next interval's timer exists before anything is sent again.
			timer = o.clock.NewTimer(o.retry)
		}
		o.sendAll(ctx)
	}
}

// sendAll sends every status held, one at a time in deployment ID order, until ctx ends.
func (o *Outbox) sendAll(ctx context.Context) {
	o.mu.Lock()
	pending := slices.SortedFunc(maps.Values(o.entries), func(x, y entry) int {
		return bytes.Compare(x.status.DeploymentID[:], y.status.DeploymentID[:])
	})
	o.mu.Unlock()
	for _, e := range pending {
		if ctx.Err() != nil {
			return
		}
		o.send(ctx, e)
	}
}

// send sends e, and drops it if the CO accepts it and no Put replaced it meanwhile.
func (o *Outbox) send(ctx context.Context, e entry) {
	attrs := []slog.Attr{
		slog.String("deployment_id", e.status.DeploymentID.String()), slog.String("digest", string(e.digest)), // SPEC §13.1
		slog.String("device_id", e.status.DeviceID.String()), slog.String("state", string(e.status.Status.State)),
	}
	if err := o.client.PostStatus(ctx, e.status); err != nil {
		if ctx.Err() != nil {
			return // stopped by shutdown, not refused by the CO
		}
		o.log.LogAttrs(ctx, slog.LevelWarn, "status not accepted by the CO", append(attrs, err.Attrs()...)...)
		return
	}
	o.mu.Lock()
	if cur, ok := o.entries[e.status.DeploymentID]; ok && cur.seq == e.seq {
		delete(o.entries, e.status.DeploymentID)
	}
	o.mu.Unlock()
	o.log.LogAttrs(ctx, slog.LevelDebug, "status sent", attrs...)
}
