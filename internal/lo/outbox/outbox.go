// Package outbox is the LO's status outbox (SPEC §8.8).
package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/lo/coapi"
	"github.com/balaji-balu/ieo/internal/platform"
)

// Config configures an Outbox.
type Config struct {
	Client        *coapi.Client
	RetryInterval time.Duration
	Clock         platform.Clock
	Log           *slog.Logger
}

// Outbox is not implemented yet.
type Outbox struct{}

// New is not implemented yet.
func New(cfg Config) *Outbox { return &Outbox{} }

// Put is not implemented yet.
func (o *Outbox) Put(ctx context.Context, s contract.DeploymentStatus, digest contract.Digest) {}

// Run is not implemented yet.
func (o *Outbox) Run(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
