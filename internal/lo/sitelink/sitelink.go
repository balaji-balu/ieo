// Package sitelink joins the LO's single authority to the site NATS server (SPEC §11.2, §16.2). It
// is the only LO code that knows subjects and message bytes: it sends each Command to its host and
// returns the ack, gives every Inventory and ComponentStatusEvent to a Handler with the host of
// the subject it arrived on, and asks the hosts for their inventories each time the LO is
// connected (ADR 0020).
package sitelink

import (
	"context"
	"errors"
	"log/slog"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/sitenats"
)

// Handler receives what the hosts report. host is the host of the subject the message arrived
// on, never an ID from its payload (SPEC §11.2). The calls for one kind of message do not overlap
// and come in the order the messages arrived; an Inventory and a status event may overlap.
type Handler interface {
	OnInventory(ctx context.Context, host contract.HostID, inv contract.Inventory)
	OnStatus(ctx context.Context, host contract.HostID, ev contract.ComponentStatusEvent)
}

// Link is the LO's side of the site messages of one site. It is safe for concurrent use.
type Link struct{}

// New returns a Link that uses conn for the subjects of site.
func New(conn *sitenats.Conn, site sitenats.Site, log *slog.Logger) *Link {
	return &Link{}
}

// Send sends cmd to host and returns the host's ack (SPEC §11.2, §16.4).
func (l *Link) Send(ctx context.Context, host contract.HostID, cmd contract.Command) (contract.CommandAck, error) {
	return contract.CommandAck{}, errors.New("sitelink: not implemented")
}

// Listen gives h every Inventory and ComponentStatusEvent of the site's hosts, until Stop.
func (l *Link) Listen(ctx context.Context, h Handler) error {
	return errors.New("sitelink: not implemented")
}

// Stop ends what Listen started.
func (l *Link) Stop() {}
