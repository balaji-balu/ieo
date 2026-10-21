// Package outbox sends the LO's deployment statuses to the CO (SPEC §8.8). It holds the latest
// status of each deployment until the CO accepts it.
//
// [IEO, interim] Until roadmap slice J, the outbox lives in memory: an LO restart loses what it
// holds, and a failed send is tried again every retry interval, with no backoff.
package outbox

import (
	"context"
	"log/slog"
	"time"

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
type Outbox struct{}

// New returns an empty Outbox.
func New(cfg Config) *Outbox { return &Outbox{} }

// Put replaces the status held for s's deployment with s, and has Run send it soon (SPEC §8.7,
// §8.8). digest is the deployment's digest, for the log lines. Put does not block on the CO.
func (o *Outbox) Put(ctx context.Context, s contract.DeploymentStatus, digest contract.Digest) {}

// Run sends each status Put gives it, until ctx ends; it then returns ctx's error. A status the CO
// accepts (200 or 201) is dropped, unless a newer one replaced it while it was being sent; a status
// it does not accept stays and is sent again after the retry interval (SPEC §8.8). Call it once.
func (o *Outbox) Run(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
