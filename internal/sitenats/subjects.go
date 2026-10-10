// Package sitenats is the LO's and the EN's side of the site NATS server (SPEC §11.2, ADR 0020):
// the subjects of site messages, and the settings of the connection (SPEC §15.6). What the
// messages hold is in internal/contract; what a tier does with them is in that tier.
package sitenats

import (
	"errors"
	"fmt"
	"strings"

	"github.com/balaji-balu/ieo/internal/contract"
)

// ErrDotInID is returned, wrapped, for a site ID or host ID that contains `.`. Such an ID is
// valid everywhere else, but `.` separates the parts of a subject (SPEC §4.2).
var ErrDotInID = errors.New("an ID used in a message subject must not contain `.`")

// The fixed parts of a subject (SPEC §4.2, §11.2). No other package formats or splits one.
const (
	separator = "."
	partSite  = "site"
	partHost  = "host"
	anyHost   = "*" // matches exactly one part
	// hostParts is the number of parts of `site.<s>.host.<h>.<kind>`.
	hostParts = 5
)

// Site builds the subjects of one site. Get one from ForSite; the zero Site is not valid.
type Site struct {
	id contract.SiteID
}

// Host builds the subjects of one host of a site. Get one from Site.Host; the zero Host is not
// valid.
type Host struct {
	site Site
	id   contract.HostID
}

// ForSite returns the subjects of site. It fails if site is not a valid site ID
// (contract.ErrInvalidID), or contains `.` (ErrDotInID): the LO and the EN then do not start
// (SPEC §4.2).
func ForSite(site contract.SiteID) (Site, error) {
	// A SiteID converted from a string has not been checked, and `*`, `>` and spaces mean
	// something in a subject.
	if _, err := contract.ParseSiteID(site.String()); err != nil {
		return Site{}, err
	}
	if strings.Contains(site.String(), separator) {
		return Site{}, fmt.Errorf("site ID %q: %w", site, ErrDotInID)
	}
	return Site{id: site}, nil
}

// Host returns the subjects of host h of the site. It fails if h is not a valid host ID
// (contract.ErrInvalidID), or contains `.` (ErrDotInID).
func (s Site) Host(h contract.HostID) (Host, error) {
	if _, err := contract.ParseHostID(h.String()); err != nil {
		return Host{}, err
	}
	if strings.Contains(h.String(), separator) {
		return Host{}, fmt.Errorf("host ID %q: %w", h, ErrDotInID)
	}
	return Host{site: s, id: h}, nil
}

// ID returns the host's ID.
func (h Host) ID() contract.HostID { return h.id }

// Cmd returns `site.<s>.host.<h>.cmd`, on which the LO sends the host a Command and the EN
// replies with a CommandAck (SPEC §11.2).
func (h Host) Cmd() string { return h.site.hostSubject(h.id.String(), "cmd") }

// Status returns `site.<s>.host.<h>.status`, on which the EN publishes ComponentStatusEvents.
func (h Host) Status() string { return h.site.hostSubject(h.id.String(), "status") }

// Inventory returns `site.<s>.host.<h>.inventory`, on which the EN publishes its Inventory.
func (h Host) Inventory() string { return h.site.hostSubject(h.id.String(), "inventory") }

// InventoryRequest returns `site.<s>.inventory.request`, on which the LO asks every EN of the
// site for its Inventory.
func (s Site) InventoryRequest() string {
	return strings.Join([]string{partSite, s.id.String(), "inventory", "request"}, separator)
}

// AllStatus returns the subject that matches the Status subject of every host of the site.
func (s Site) AllStatus() string { return s.hostSubject(anyHost, "status") }

// AllInventory returns the subject that matches the Inventory subject of every host of the site.
func (s Site) AllInventory() string { return s.hostSubject(anyHost, "inventory") }

func (s Site) hostSubject(host, kind string) string {
	return strings.Join([]string{partSite, s.id.String(), partHost, host, kind}, separator)
}

// HostOf returns the host a message belongs to, from the subject it arrived on. ok is false if
// subject is not `site.<s>.host.<h>.<kind>` for this site and a valid host ID; the message is
// then not one of this site's host messages. The LO keys host state by this ID, never by an ID in
// the payload (SPEC §11.2).
func (s Site) HostOf(subject string) (h contract.HostID, ok bool) {
	parts := strings.Split(subject, separator)
	if s.id == "" || len(parts) != hostParts ||
		parts[0] != partSite || parts[1] != s.id.String() || parts[2] != partHost || parts[4] == "" {
		return "", false
	}
	h, err := contract.ParseHostID(parts[3]) // refuses `*` and the empty part
	if err != nil {
		return "", false
	}
	return h, true
}
