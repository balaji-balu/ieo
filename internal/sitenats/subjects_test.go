package sitenats_test

import (
	"errors"
	"testing"

	"github.com/balaji-balu/ieo/internal/contract"
	"github.com/balaji-balu/ieo/internal/sitenats"
)

func site(t *testing.T, id contract.SiteID) sitenats.Site {
	t.Helper()
	s, err := sitenats.ForSite(id)
	if err != nil {
		t.Fatalf("ForSite(%q): %v", id, err)
	}
	return s
}

func host(t *testing.T, s sitenats.Site, id contract.HostID) sitenats.Host {
	t.Helper()
	h, err := s.Host(id)
	if err != nil {
		t.Fatalf("Host(%q): %v", id, err)
	}
	return h
}

// SPEC §17.1: "A site ID or host ID that contains `.` is refused at startup by the LO and the EN,
// and is still accepted wherever it is not used in a message subject (§4.2)." This is the subject
// half: no subject can be built from such an ID. The startup half is in cmd/lo and cmd/en.
func TestSpec_17_1_IDWithDotIsRefusedInSubjects(t *testing.T) {
	for _, id := range []string{"plant.north", ".site", "site.", "a..b"} {
		if _, err := contract.ParseSiteID(id); err != nil {
			t.Errorf("contract.ParseSiteID(%q): %v; want it accepted outside subjects", id, err)
		}
		if _, err := sitenats.ForSite(contract.SiteID(id)); !errors.Is(err, sitenats.ErrDotInID) {
			t.Errorf("ForSite(%q) error = %v, want ErrDotInID", id, err)
		}
	}
	s := site(t, "site-1")
	for _, id := range []string{"press-03.plant2", ".h", "h.", "a.b.c"} {
		if _, err := contract.ParseHostID(id); err != nil {
			t.Errorf("contract.ParseHostID(%q): %v; want it accepted outside subjects", id, err)
		}
		if _, err := s.Host(contract.HostID(id)); !errors.Is(err, sitenats.ErrDotInID) {
			t.Errorf("Host(%q) error = %v, want ErrDotInID", id, err)
		}
	}
}

// An ID that is not valid at all cannot go into a subject either: `*`, `>` and spaces mean
// something to the server.
func TestSubjectsRefuseInvalidIDs(t *testing.T) {
	for _, id := range []string{"", "any", "*", ">", "a b", "a/b", "site\n"} {
		if _, err := sitenats.ForSite(contract.SiteID(id)); !errors.Is(err, contract.ErrInvalidID) {
			t.Errorf("ForSite(%q) error = %v, want contract.ErrInvalidID", id, err)
		}
	}
	s := site(t, "site-1")
	for _, id := range []string{"", "*", ">", "a b", "a/b", "h\t"} {
		if _, err := s.Host(contract.HostID(id)); !errors.Is(err, contract.ErrInvalidID) {
			t.Errorf("Host(%q) error = %v, want contract.ErrInvalidID", id, err)
		}
	}
}

// SPEC §4.2, §11.2: `site.<site_id>.host.<host_id>.<kind>`, and the site's inventory request.
func TestSubjects(t *testing.T) {
	s := site(t, "Site_1~a")
	h := host(t, s, "host-03")
	if got := h.ID(); got != "host-03" {
		t.Errorf("ID = %q, want host-03", got)
	}
	tests := []struct{ name, got, want string }{
		{"Cmd", h.Cmd(), "site.Site_1~a.host.host-03.cmd"},
		{"Status", h.Status(), "site.Site_1~a.host.host-03.status"},
		{"Inventory", h.Inventory(), "site.Site_1~a.host.host-03.inventory"},
		{"InventoryRequest", s.InventoryRequest(), "site.Site_1~a.inventory.request"},
		{"AllStatus", s.AllStatus(), "site.Site_1~a.host.*.status"},
		{"AllInventory", s.AllInventory(), "site.Site_1~a.host.*.inventory"},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// SPEC §11.2: "The LO keys host state by `<h>`", the host ID of the subject a message arrived on.
func TestHostOf(t *testing.T) {
	s := site(t, "site-1")
	h := host(t, s, "host-03")
	for _, subject := range []string{h.Cmd(), h.Status(), h.Inventory(), "site.site-1.host.host-03.heartbeat"} {
		if got, ok := s.HostOf(subject); !ok || got != "host-03" {
			t.Errorf("HostOf(%q) = %q, %v; want host-03, true", subject, got, ok)
		}
	}
	notHostSubjects := []string{
		"",
		"site.site-2.host.host-03.status",        // another site
		"site.site-1.inventory.request",          // the site's own subject
		"site.site-1.host.host-03",               // no kind
		"site.site-1.host.host-03.",              // empty kind
		"site.site-1.host..status",               // empty host
		"site.site-1.host.host-03.status.extra",  // too many parts
		"site.site-1.host.press.03.status",       // a host ID with a dot has too many parts
		"site.site-1.host.*.status",              // a wildcard is not a host
		"site.site-1.host.a b.status",            // not a host ID
		"SITE.site-1.host.host-03.status",        // subjects are case-sensitive
		"site.site-1.hosts.host-03.status",       // not `host`
		"prefix.site.site-1.host.host-03.status", // more in front
	}
	for _, subject := range notHostSubjects {
		if got, ok := s.HostOf(subject); ok || got != "" {
			t.Errorf("HostOf(%q) = %q, %v; want \"\", false", subject, got, ok)
		}
	}
}
