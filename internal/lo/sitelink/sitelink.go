// Package sitelink joins the LO's single authority to the site NATS server (SPEC §11.2, §16.2). It
// is the only LO code that knows subjects and message bytes: it sends each Command to its host and
// returns the ack, gives every Inventory and ComponentStatusEvent to a Handler with the host of
// the subject it arrived on, and asks the hosts for their inventories each time the LO is
// connected (ADR 0020).
package sitelink

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/sitenats"
)

// maxReasonBytes bounds the `reason` of a dropped message in a log line. The reason quotes the
// values that fail the schema, and the sender chooses how long those are.
const maxReasonBytes = 1024

// Handler receives what the hosts report. host is the host of the subject the message arrived
// on, never an ID from its payload (SPEC §11.2). The calls for one kind of message do not overlap
// and come in the order the messages arrived; an Inventory and a status event may overlap.
type Handler interface {
	OnInventory(ctx context.Context, host contract.HostID, inv contract.Inventory)
	OnStatus(ctx context.Context, host contract.HostID, ev contract.ComponentStatusEvent)
}

// Link is the LO's side of the site messages of one site. It is safe for concurrent use.
type Link struct {
	conn *sitenats.Conn
	site sitenats.Site
	log  *slog.Logger

	// mu guards the fields below.
	mu        sync.Mutex
	listening bool
	subs      []*sitenats.Subscription
}

// New returns a Link that uses conn for the subjects of site. log receives a line for each
// message dropped; give it the LO's `site_id` (SPEC §13.1). nil means slog.Default.
func New(conn *sitenats.Conn, site sitenats.Site, log *slog.Logger) *Link {
	if log == nil {
		log = slog.Default()
	}
	return &Link{conn: conn, site: site, log: log}
}

// Send sends cmd to host and returns the host's ack (SPEC §11.2, §16.4). ctx carries the deadline
// for the ack. Any error means the command was not acknowledged: host cannot be put in a subject,
// cmd is not a valid Command, the LO is not connected, no EN is subscribed, no reply came in time,
// or the reply is not a CommandAck.
func (l *Link) Send(ctx context.Context, host contract.HostID, cmd contract.Command) (contract.CommandAck, error) {
	h, err := l.site.Host(host)
	if err != nil {
		return contract.CommandAck{}, err
	}
	data, err := contract.EncodeSiteMessage(cmd)
	if err != nil {
		return contract.CommandAck{}, err
	}
	reply, err := l.conn.Request(ctx, h.Cmd(), data)
	if err != nil {
		return contract.CommandAck{}, err
	}
	ack, err := contract.DecodeSiteMessage[contract.CommandAck](reply)
	if err != nil {
		return contract.CommandAck{}, fmt.Errorf("reply on %s: %w", h.Cmd(), err)
	}
	return ack, nil
}

// Listen gives h every Inventory and ComponentStatusEvent of the site's hosts, with ctx, until
// Stop. A message whose subject names no host of the site, or whose payload fails its schema, is
// logged and dropped (SPEC §11.2). Once it is subscribed, and then each time the LO is connected
// again, the Link publishes `site.<s>.inventory.request` (SPEC §16.2). Call Listen once; it is
// the Link that calls the connection's OnConnect.
func (l *Link) Listen(ctx context.Context, h Handler) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.listening {
		return errors.New("sitelink: Listen was called before")
	}
	inventories, err := l.conn.Subscribe(l.site.AllInventory(), func(m sitenats.Msg) {
		receive(ctx, l, m, "inventory dropped", h.OnInventory)
	})
	if err != nil {
		return err
	}
	statuses, err := l.conn.Subscribe(l.site.AllStatus(), func(m sitenats.Msg) {
		receive(ctx, l, m, "status event dropped", h.OnStatus)
	})
	if err != nil {
		inventories.Stop()
		return err
	}
	l.listening, l.subs = true, []*sitenats.Subscription{inventories, statuses}
	// After the subscriptions, so that no answer to the request is missed (ADR 0020).
	l.conn.OnConnect(func() { l.requestInventories(ctx) })
	return nil
}

// receive decodes one message and gives it to handle with the host of its subject, or logs with
// msg why it is dropped (SPEC §11.2).
func receive[T contract.SiteMessage](ctx context.Context, l *Link, m sitenats.Msg, msg string, handle func(context.Context, contract.HostID, T)) {
	host, ok := l.site.HostOf(m.Subject)
	if !ok {
		l.log.LogAttrs(ctx, slog.LevelWarn, "site message dropped", slog.String("subject", m.Subject),
			slog.String("reason", "the subject names no host of the site"))
		return
	}
	payload, err := contract.DecodeSiteMessage[T](m.Data)
	if err != nil {
		l.log.LogAttrs(ctx, slog.LevelWarn, msg, slog.String("host_id", string(host)), slog.String("reason", truncate(err.Error(), maxReasonBytes)))
		return
	}
	handle(ctx, host, payload)
}

// requestInventories asks every EN of the site for its Inventory (SPEC §11.2). A request that
// cannot be sent is logged and not kept: the next connect sends another (ADR 0020).
func (l *Link) requestInventories(ctx context.Context) {
	data, err := contract.EncodeSiteMessage(contract.InventoryRequest{})
	if err == nil {
		err = l.conn.Publish(l.site.InventoryRequest(), data)
	}
	if err != nil {
		l.log.LogAttrs(ctx, slog.LevelWarn, "inventory request not sent", slog.String("reason", err.Error()))
		return
	}
	l.log.LogAttrs(ctx, slog.LevelInfo, "inventory requested from the hosts")
}

// Stop ends what Listen started and returns once h will not be called again (ADR 0020). It may be
// called more than once, and before Listen, when it does nothing. The inventory request is still
// published on a connect that comes after Stop, until the connection is closed.
func (l *Link) Stop() {
	l.mu.Lock()
	subs := l.subs
	l.subs = nil
	l.mu.Unlock()
	for _, s := range subs {
		s.Stop()
	}
}

// truncate returns s cut to at most n bytes.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
