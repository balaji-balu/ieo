package contract_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/balaji-balu/ieo/internal/contract"
)

// SPEC §17.1: "Site IDs and host IDs with characters outside RFC 3986 unreserved are rejected;
// `any` is rejected as a site ID."
func TestSpec_17_1_RejectsNonUnreservedSiteAndHostIDs(t *testing.T) {
	const unreserved = "ABCXYZabcxyz0189._~-"
	invalid := []string{"", "a b", "a/b", "*", "a%20", "a:b", "a@b", "a+b", "site-é", "a\x00"}

	for _, id := range invalid {
		if _, err := contract.ParseSiteID(id); !errors.Is(err, contract.ErrInvalidID) {
			t.Errorf("ParseSiteID(%q): err = %v, want ErrInvalidID", id, err)
		}
		if _, err := contract.ParseHostID(id); !errors.Is(err, contract.ErrInvalidID) {
			t.Errorf("ParseHostID(%q): err = %v, want ErrInvalidID", id, err)
		}
	}

	if _, err := contract.ParseSiteID("any"); !errors.Is(err, contract.ErrInvalidID) {
		t.Errorf("ParseSiteID(%q): err = %v, want ErrInvalidID (reserved)", "any", err)
	}
	if h, err := contract.ParseHostID("any"); err != nil || h.String() != "any" {
		t.Errorf("ParseHostID(%q) = %q, %v; want accepted: `any` is reserved for site IDs only", "any", h, err)
	}

	for _, id := range []string{unreserved, "site-1", "Any", "h"} {
		if s, err := contract.ParseSiteID(id); err != nil || s.String() != id {
			t.Errorf("ParseSiteID(%q) = %q, %v; want accepted", id, s, err)
		}
		if h, err := contract.ParseHostID(id); err != nil || h.String() != id {
			t.Errorf("ParseHostID(%q) = %q, %v; want accepted", id, h, err)
		}
	}
}

// SPEC §17.1: "Device ID parsing splits on the first `/` only."
func TestSpec_17_1_DeviceIDSplitsOnFirstSlash(t *testing.T) {
	tests := []struct {
		in         string
		site, host string
		autonomous bool
	}{
		{in: "site-1", site: "site-1"},
		{in: "site-1/host-03", site: "site-1", host: "host-03"},
		{in: "site-1/*", site: "site-1", autonomous: true},
	}
	for _, tt := range tests {
		d, err := contract.ParseDeviceID(tt.in)
		if err != nil {
			t.Errorf("ParseDeviceID(%q): %v", tt.in, err)
			continue
		}
		if d.Site.String() != tt.site || d.Host.String() != tt.host || d.Autonomous != tt.autonomous {
			t.Errorf("ParseDeviceID(%q) = {%q %q %v}, want {%q %q %v}",
				tt.in, d.Site, d.Host, d.Autonomous, tt.site, tt.host, tt.autonomous)
		}
		if d.String() != tt.in {
			t.Errorf("ParseDeviceID(%q).String() = %q, want the input back", tt.in, d.String())
		}
	}

	// Only the first `/` splits: the host part of "s/h/x" is "h/x", which is not a valid host ID.
	_, err := contract.ParseDeviceID("s/h/x")
	if !errors.Is(err, contract.ErrInvalidID) || !strings.Contains(err.Error(), `"h/x"`) {
		t.Errorf(`ParseDeviceID("s/h/x"): err = %v, want ErrInvalidID naming host "h/x"`, err)
	}
	for _, in := range []string{"", "/h", "any/h", "s/", "s/h*", "s/**"} {
		if _, err := contract.ParseDeviceID(in); !errors.Is(err, contract.ErrInvalidID) {
			t.Errorf("ParseDeviceID(%q): err = %v, want ErrInvalidID", in, err)
		}
	}
}

// SPEC §17.1: "Compose project names follow §4.2 for component names with mixed case and symbols."
func TestSpec_17_1_ComposeProjectNameNormalization(t *testing.T) {
	id := uuid.MustParse("3F2504E0-4F89-11D3-9A0C-0305E82C3301")
	tests := []struct {
		component string
		want      string
	}{
		{"web", "3f2504e0-4f89-11d3-9a0c-0305e82c3301-web"},
		{"Web_API", "3f2504e0-4f89-11d3-9a0c-0305e82c3301-web_api"},
		{"My App.v2", "3f2504e0-4f89-11d3-9a0c-0305e82c3301-my-app-v2"},
		{"db@primary:5432", "3f2504e0-4f89-11d3-9a0c-0305e82c3301-db-primary-5432"},
		{"Café", "3f2504e0-4f89-11d3-9a0c-0305e82c3301-caf-"},
	}
	for _, tt := range tests {
		if got := contract.ComposeProjectName(id, tt.component); got != tt.want {
			t.Errorf("ComposeProjectName(%s, %q) = %q, want %q", id, tt.component, got, tt.want)
		}
		if !contract.IsComposeProjectName(tt.want) {
			t.Errorf("IsComposeProjectName(%q) = false, want true", tt.want)
		}
	}
}

// IsComposeProjectName accepts only names of the form ComposeProjectName produces (SPEC §4.2, §9.2).
func TestIsComposeProjectNameRejects(t *testing.T) {
	for _, s := range []string{
		"",
		"web",
		"3f2504e0-4f89-11d3-9a0c-0305e82c3301",  // no component
		"3f2504e0-4f89-11d3-9a0c-0305e82c3301-", // empty component
		"3F2504E0-4F89-11D3-9A0C-0305E82C3301-web",            // upper case
		"3f2504e0-4f89-11d3-9a0c-0305e82c3301-We b",           // characters outside [a-z0-9_-]
		"3f2504e0-4f89-11d3-9a0c-0305e82c3301-../x",           // path
		"3f2504e04f8911d39a0c0305e82c3301-web-and-more-chars", // not a canonical UUID
	} {
		if contract.IsComposeProjectName(s) {
			t.Errorf("IsComposeProjectName(%q) = true, want false", s)
		}
	}
}

// SPEC §17.1: "Tag-to-SemVer conversion turns `_` into `+`; tag and revision compare as exact
// strings."
func TestSpec_17_1_TagToSemVerConvertsUnderscore(t *testing.T) {
	conversions := []struct{ revision, semver string }{
		{"1.2.3", "1.2.3"},
		{"1.2.3_build.5", "1.2.3+build.5"},
		{"1.0.0-rc.1_sha.abc-def", "1.0.0-rc.1+sha.abc-def"},
	}
	for _, tt := range conversions {
		if got := contract.RevisionSemVer(tt.revision); got != tt.semver {
			t.Errorf("RevisionSemVer(%q) = %q, want %q", tt.revision, got, tt.semver)
		}
	}

	matches := []struct {
		tag, revision string
		want          bool
	}{
		{"1.2.3", "1.2.3", true},
		{"1.2.3_build.5", "1.2.3_build.5", true},
		{"1.2.3_build.5", "1.2.3_build.6", false},
		{"1.2.3_build.5", "1.2.3", false},
		{"v1.2.3", "1.2.3", false},
	}
	for _, tt := range matches {
		if got := contract.TagMatchesRevision(tt.tag, tt.revision); got != tt.want {
			t.Errorf("TagMatchesRevision(%q, %q) = %v, want %v", tt.tag, tt.revision, got, tt.want)
		}
	}
}

// A component's slug is the part of its project name after the deployment ID (SPEC §4.2, §9.1).
func TestComponentSlug(t *testing.T) {
	id := uuid.MustParse("6F1D7D3E-9D0C-4A39-8F5D-1B8F0C9F2A11")
	for name, want := range map[string]string{
		"web":       "web",
		"Web_API-2": "web_api-2",
		"a/b":       "a-b",
		"../etc":    "---etc",
		"café x":    "caf--x",
		"":          "",
	} {
		if got := contract.ComponentSlug(name); got != want {
			t.Errorf("ComponentSlug(%q) = %q, want %q", name, got, want)
		}
		if got, want := contract.ComposeProjectName(id, name), "6f1d7d3e-9d0c-4a39-8f5d-1b8f0c9f2a11-"+want; got != want {
			t.Errorf("ComposeProjectName(%q) = %q, want %q", name, got, want)
		}
	}
}
