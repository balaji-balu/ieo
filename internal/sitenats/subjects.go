// Package sitenats is the LO's and the EN's side of the site NATS server (SPEC §11.2, ADR 0020):
// the subjects of site messages, and the settings of the connection (SPEC §15.6). What the
// messages hold is in internal/contract; what a tier does with them is in that tier.
package sitenats

import (
	"errors"

	"github.com/balaji-balu/ieo/internal/contract"
)

// ErrDotInID is returned, wrapped, for a site ID or host ID that contains `.`. Such an ID is
// valid everywhere else, but `.` separates the parts of a subject (SPEC §4.2).
var ErrDotInID = errors.New("an ID used in a message subject must not contain `.`")

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
	return Site{}, nil
}

// Host returns the subjects of host h of the site. It fails if h is not a valid host ID
// (contract.ErrInvalidID), or contains `.` (ErrDotInID).
func (s Site) Host(h contract.HostID) (Host, error) {
	return Host{}, nil
}

// ID returns the host's ID.
func (h Host) ID() contract.HostID { return h.id }

// Cmd returns `site.<s>.host.<h>.cmd`, on which the LO sends the host a Command and the EN
// replies with a CommandAck (SPEC §11.2).
func (h Host) Cmd() string { return "" }

// Status returns `site.<s>.host.<h>.status`, on which the EN publishes ComponentStatusEvents.
func (h Host) Status() string { return "" }

// Inventory returns `site.<s>.host.<h>.inventory`, on which the EN publishes its Inventory.
func (h Host) Inventory() string { return "" }

// InventoryRequest returns `site.<s>.inventory.request`, on which the LO asks every EN of the
// site for its Inventory.
func (s Site) InventoryRequest() string { return "" }

// AllStatus returns the subject that matches the Status subject of every host of the site.
func (s Site) AllStatus() string { return "" }

// AllInventory returns the subject that matches the Inventory subject of every host of the site.
func (s Site) AllInventory() string { return "" }

// HostOf returns the host a message belongs to, from the subject it arrived on. ok is false if
// subject is not `site.<s>.host.<h>.<kind>` for this site and a valid host ID; the message is
// then not one of this site's host messages. The LO keys host state by this ID, never by an ID in
// the payload (SPEC §11.2).
func (s Site) HostOf(subject string) (h contract.HostID, ok bool) {
	return "", false
}
