// Package outbox is the LO's status outbox (SPEC §8.8): it holds the latest DeploymentStatus of
// each deployment until the CO has taken it. Until roadmap slice J it holds them in memory only,
// tries a failed send again after a fixed interval, and has no backoff.
package outbox

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/coapi"
	"github.com/balaji-balu/ieo/internal/platform"
)

// Config configures an Outbox.
type Config struct {
	// Client sends the statuses to the CO with the site token (SPEC §15.6).
	Client *coapi.Client
	// RetryInterval is how long a status that was not sent waits before it is sent again, and how
	// long one send may take (`lo.poll.interval`, as the sync loop's next attempt waits).
	RetryInterval time.Duration
	// Clock times the retries; nil means platform.SystemClock.
	Clock platform.Clock
	// Log receives a line per failed send, and one at debug per status sent; nil means
	// slog.Default.
	Log *slog.Logger
}

// Outbox holds at most one DeploymentStatus per deployment, the latest one it was given, and
// sends each to the CO (SPEC §8.8). It is safe for concurrent use.
type Outbox struct {
	client *coapi.Client
	retry  time.Duration
	clock  platform.Clock
	log    *slog.Logger

	// wake tells Run that an entry is due. It holds at most one signal: Run sends every due entry
	// each time it wakes.
	wake chan struct{}

	mu      sync.Mutex
	entries map[uuid.UUID]*entry
	seq     uint64 // the sequence number of the last Put
}

// entry is one deployment's status waiting to be sent.
type entry struct {
	status contract.DeploymentStatus
	digest contract.Digest
	// seq is the Put that made the entry; a 2xx removes it only if no Put replaced it since.
	seq uint64
	// due says the entry is sent the next time Run wakes; a failed one waits for the retry timer.
	due bool
}

// New returns an empty Outbox; Run sends what it is given.
func New(cfg Config) *Outbox {
	o := &Outbox{
		client: cfg.Client, retry: cfg.RetryInterval, clock: cfg.Clock, log: cfg.Log,
		wake:    make(chan struct{}, 1),
		entries: map[uuid.UUID]*entry{},
	}
	if o.clock == nil {
		o.clock = platform.SystemClock()
	}
	if o.log == nil {
		o.log = slog.Default()
	}
	return o
}

// Put makes s the status of its deployment to send, replacing any older one, and returns at
// once; digest is the deployment's, for the log lines (SPEC §13.1).
func (o *Outbox) Put(_ context.Context, s contract.DeploymentStatus, digest contract.Digest) {
	o.mu.Lock()
	o.seq++
	o.entries[s.DeploymentID] = &entry{status: s, digest: digest, seq: o.seq, due: true}
	o.mu.Unlock()
	select {
	case o.wake <- struct{}{}:
	default: // Run has a signal waiting, and sends every due entry when it takes it
	}
}

// Run sends each status when it is put, and those that failed again every RetryInterval, until
// ctx ends; it then returns ctx's error. Statuses are sent one at a time, in deployment ID order.
// Call it once.
func (o *Outbox) Run(ctx context.Context) error {
	timer := o.clock.NewTimer(o.retry)
	defer func() { timer.Stop() }() // the timer of the last interval, not the first
	for {
		o.sendDue(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-o.wake:
		case <-timer.C():
			o.mu.Lock()
			for _, e := range o.entries {
				e.due = true
			}
			o.mu.Unlock()
			timer = o.clock.NewTimer(o.retry)
		}
	}
}

// sendDue sends every due entry, those put while it sends included, until none is due or ctx
// ends.
func (o *Outbox) sendDue(ctx context.Context) {
	for ctx.Err() == nil {
		e, ok := o.next()
		if !ok {
			return
		}
		o.send(ctx, e)
	}
}

// next returns a copy of the due entry with the lowest deployment ID, and notes that it is not due
// any more.
func (o *Outbox) next() (entry, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	var first *entry
	for _, e := range o.entries {
		if e.due && (first == nil || bytes.Compare(e.status.DeploymentID[:], first.status.DeploymentID[:]) < 0) {
			first = e
		}
	}
	if first == nil {
		return entry{}, false
	}
	first.due = false
	return *first, true
}

// send POSTs e to the CO (SPEC §8.8) and removes its entry on 2xx, unless a Put replaced it while
// it was sent. Otherwise the entry stays, and waits for the retry timer.
func (o *Outbox) send(ctx context.Context, e entry) {
	id := e.status.DeploymentID
	log := o.log.With(slog.String("deployment_id", id.String()), slog.String("digest", string(e.digest)),
		slog.String("state", string(e.status.Status.State)))
	if h := e.status.DeviceID.Host; h != "" {
		log = log.With(slog.String("host_id", string(h))) // SPEC §13.1: a host's status
	}
	p := "/api/v1/deployments/" + id.String() + "/status" // SPEC §11.1
	sendCtx, cancel := context.WithTimeout(ctx, o.retry)
	resp, rerr := o.client.PostJSON(sendCtx, p, e.status)
	cancel()
	if rerr == nil && (resp.Status < 200 || resp.Status > 299) {
		rerr = coapi.Failed(http.MethodPost, p, resp)
	}
	if rerr != nil {
		if ctx.Err() != nil {
			return // the LO is stopping; the send did not fail
		}
		log.LogAttrs(ctx, slog.LevelWarn, "status not sent", rerr.Attrs()...)
		return
	}
	o.mu.Lock()
	if cur := o.entries[id]; cur != nil && cur.seq == e.seq {
		delete(o.entries, id)
	}
	o.mu.Unlock()
	log.LogAttrs(ctx, slog.LevelDebug, "status sent", slog.Int("status", resp.Status))
}
