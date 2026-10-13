// Package agent joins the EN's executor to the site NATS server (SPEC §11.2, §16.7). It is the
// only EN code that knows subjects and message bytes: it gives each message on the host's command
// subject to the dispatcher and replies with the ack, sends the host's status events, and
// publishes the host's Inventory each time the EN is connected, each time the LO asks, and every
// `en.inventory.interval` (ADR 0020).
package agent

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/platform"
	"github.com/balaji-balu/ieo/internal/sitenats"
)

// Commands takes the messages of the host's command subject; *exec.Dispatcher is one.
type Commands interface {
	// Handle takes one message and returns the ack to reply with. reply is false when nothing is
	// to be sent (SPEC §11.2). It must not block on the command's work: the next message of the
	// subject waits for it.
	Handle(data []byte) (ack contract.CommandAck, reply bool)
}

// Inventory returns the host's complete actual state as it is now (SPEC §11.2); the executor's
// Inventory, for this host, is one.
type Inventory func(ctx context.Context) (contract.Inventory, error)

// Config is what Listen needs beside the two sides it joins.
type Config struct {
	// Interval is `en.inventory.interval`: the time between two Inventories published without
	// another reason. It must be positive.
	Interval time.Duration
	// Clock times the interval; nil means platform.SystemClock.
	Clock platform.Clock
}

// Agent is the EN's side of the site messages of one host. It is safe for concurrent use.
type Agent struct {
	conn *sitenats.Conn
	site sitenats.Site
	host sitenats.Host
	log  *slog.Logger

	// due holds at most one signal that an Inventory is to be published: every reason to publish
	// one sets it, and one Inventory answers all the reasons that came before it was read.
	due chan struct{}

	// mu guards the fields below.
	mu        sync.Mutex
	listening bool
	subs      []*sitenats.Subscription
	stop      chan struct{}
	publisher sync.WaitGroup
}

// New returns an Agent that uses conn for the subjects of host, a host of site. log receives a
// line for each message that could not be sent; give it the EN's `site_id` and `host_id` (SPEC
// §13.1). nil means slog.Default.
func New(conn *sitenats.Conn, site sitenats.Site, host sitenats.Host, log *slog.Logger) *Agent {
	if log == nil {
		log = slog.Default()
	}
	return &Agent{conn: conn, site: site, host: host, log: log, due: make(chan struct{}, 1)}
}

// Publish sends one status event to the LO (SPEC §11.2). It does not wait for the LO. An error
// means the event was not sent, and it is not kept for later: e is not a valid status event, or
// the EN is not connected (ADR 0020).
func (a *Agent) Publish(ctx context.Context, e contract.ComponentStatusEvent) error {
	return nil
}

// Listen gives cmds every message of the host's command subject and replies with its ack, and
// publishes what inv returns as the host's Inventory, with ctx, until Stop or until ctx ends:
//
//   - each time the EN is connected, the first time and after every loss (SPEC §7.6);
//   - each time the LO publishes `site.<s>.inventory.request`;
//   - every cfg.Interval.
//
// Inventories are published one at a time, so in the order of their `at` (SPEC §7.5), and one
// Inventory answers every reason that came up while the one before it was built. An Inventory inv
// cannot build, or that cannot be sent, is logged and not kept: the next reason publishes the
// state as it is then.
//
// Call Listen once; it is the Agent that calls the connection's OnConnect.
func (a *Agent) Listen(ctx context.Context, cmds Commands, inv Inventory, cfg Config) error {
	return nil
}

// Stop ends what Listen started and returns once cmds and inv will not be called again (SPEC
// §16.7: the EN stops receiving commands first, and then waits for those in flight). It may be
// called more than once, and before Listen, when it does nothing. Publish still works after it.
func (a *Agent) Stop() {
}

// inventoryDue notes a reason to publish an Inventory.
func (a *Agent) inventoryDue() {
}
