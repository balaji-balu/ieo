package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// ErrInvalidID is returned, wrapped with the offending value, when an identifier does not follow
// SPEC §4.2.
var ErrInvalidID = errors.New("invalid identifier")

// reservedSiteID may not be used as a site ID (SPEC §4.1.1).
const reservedSiteID = "any"

// SiteID identifies a site; it is also the LO's Margo device ID (SPEC §4.1.1). A SiteID obtained
// from ParseSiteID or by decoding JSON is valid.
type SiteID string

// ParseSiteID returns s as a SiteID: one or more RFC 3986 unreserved characters, not `any`.
func ParseSiteID(s string) (SiteID, error) {
	if err := checkUnreserved("site ID", s); err != nil {
		return "", err
	}
	if s == reservedSiteID {
		return "", fmt.Errorf("%w: site ID %q is reserved", ErrInvalidID, s)
	}
	return SiteID(s), nil
}

func (id SiteID) String() string { return string(id) }

// UnmarshalText decodes and validates a site ID, so JSON never yields an invalid one.
func (id *SiteID) UnmarshalText(b []byte) error {
	v, err := ParseSiteID(string(b))
	*id = v
	return err
}

// HostID identifies a host within its site (SPEC §4.1.2). A HostID obtained from ParseHostID or
// by decoding JSON is valid.
type HostID string

// ParseHostID returns s as a HostID: one or more RFC 3986 unreserved characters.
func ParseHostID(s string) (HostID, error) {
	if err := checkUnreserved("host ID", s); err != nil {
		return "", err
	}
	return HostID(s), nil
}

func (id HostID) String() string { return string(id) }

// UnmarshalText decodes and validates a host ID.
func (id *HostID) UnmarshalText(b []byte) error {
	v, err := ParseHostID(string(b))
	*id = v
	return err
}

// DeviceID is a Margo device ID (SPEC §4.2) in one of three forms:
//
//   - `<site>`: the site's LO (Host empty, Autonomous false);
//   - `<site>/<host>`: a host;
//   - `<site>/*`: any host of the site, the target of an autonomous deployment (Autonomous true).
//
// It encodes to and decodes from its string form.
type DeviceID struct {
	Site       SiteID
	Host       HostID
	Autonomous bool
}

// ParseDeviceID splits s on its first `/` only (SPEC §4.2) and validates both parts.
func ParseDeviceID(s string) (DeviceID, error) {
	sitePart, rest, hasHost := strings.Cut(s, "/")
	site, err := ParseSiteID(sitePart)
	if err != nil {
		return DeviceID{}, fmt.Errorf("device ID %q: %w", s, err)
	}
	if !hasHost {
		return DeviceID{Site: site}, nil
	}
	if rest == "*" {
		return DeviceID{Site: site, Autonomous: true}, nil
	}
	host, err := ParseHostID(rest)
	if err != nil {
		return DeviceID{}, fmt.Errorf("device ID %q: %w", s, err)
	}
	return DeviceID{Site: site, Host: host}, nil
}

// String returns the Margo form of d.
func (d DeviceID) String() string {
	switch {
	case d.Autonomous:
		return string(d.Site) + "/*"
	case d.Host != "":
		return string(d.Site) + "/" + string(d.Host)
	default:
		return string(d.Site)
	}
}

// MarshalText encodes d in its Margo form.
func (d DeviceID) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// UnmarshalText decodes and validates a device ID.
func (d *DeviceID) UnmarshalText(b []byte) error {
	v, err := ParseDeviceID(string(b))
	*d = v
	return err
}

// checkUnreserved reports whether s is a non-empty string of RFC 3986 unreserved characters:
// A–Z a–z 0–9 . _ ~ -
func checkUnreserved(what, s string) error {
	if s == "" {
		return fmt.Errorf("%w: empty %s", ErrInvalidID, what)
	}
	for i := 0; i < len(s); i++ {
		if !isUnreserved(s[i]) {
			return fmt.Errorf("%w: %s %q has a character outside RFC 3986 unreserved", ErrInvalidID, what, s)
		}
	}
	return nil
}

func isUnreserved(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' ||
		c == '.' || c == '_' || c == '~' || c == '-'
}

// Digest is a content digest, always `sha256:<64 lowercase hex>` (SPEC §4.2). Digests compare as
// exact strings.
type Digest string

// DigestOf returns the digest of b's exact bytes.
func DigestOf(b []byte) Digest {
	sum := sha256.Sum256(b)
	return Digest("sha256:" + hex.EncodeToString(sum[:]))
}

// ParseDigest returns s as a Digest if it has the form `sha256:<64 lowercase hex>`.
func ParseDigest(s string) (Digest, error) {
	hexPart, ok := strings.CutPrefix(s, "sha256:")
	if !ok || len(hexPart) != sha256.Size*2 || strings.Trim(hexPart, "0123456789abcdef") != "" {
		return "", fmt.Errorf("%w: digest %q is not sha256:<64 lowercase hex>", ErrInvalidID, s)
	}
	return Digest(s), nil
}

func (d Digest) String() string { return string(d) }

// UnmarshalText decodes and validates a digest.
func (d *Digest) UnmarshalText(b []byte) error {
	v, err := ParseDigest(string(b))
	*d = v
	return err
}

// ComposeProjectName returns the Compose project name of one component of a deployment (SPEC
// §4.2): `<deployment_id>-<component_name>`, lowercased, with every character outside
// [a-z0-9_-] replaced by `-`.
func ComposeProjectName(deploymentID uuid.UUID, component string) string {
	return strings.Map(func(r rune) rune {
		if projectNameRune(r) {
			return r
		}
		return '-'
	}, strings.ToLower(deploymentID.String()+"-"+component))
}

// IsComposeProjectName reports whether s has the form ComposeProjectName produces for a non-empty
// component name (SPEC §4.2): a lowercase deployment ID, `-`, then one or more of [a-z0-9_-].
// The EN refuses any other project name, so none can come from archive content (SPEC §9.2).
func IsComposeProjectName(s string) bool {
	const idLen = 36 // canonical UUID
	if len(s) <= idLen+1 || s[idLen] != '-' {
		return false
	}
	if id, err := uuid.Parse(s[:idLen]); err != nil || id.String() != s[:idLen] {
		return false
	}
	for _, r := range s[idLen+1:] {
		if !projectNameRune(r) {
			return false
		}
	}
	return true
}

// projectNameRune reports whether r may appear in a Compose project name: [a-z0-9_-] (SPEC §4.2).
func projectNameRune(r rune) bool {
	return 'a' <= r && r <= 'z' || '0' <= r && r <= '9' || r == '_' || r == '-'
}

// RevisionSemVer returns a component revision as SemVer 2.0: Margo writes build metadata after
// `_` because OCI tags cannot contain `+` (SPEC §4.2).
func RevisionSemVer(revision string) string {
	return strings.ReplaceAll(revision, "_", "+")
}

// TagMatchesRevision reports whether an OCI tag is a component's revision. The revision is the
// tag, so they compare as exact strings (SPEC §4.2).
func TagMatchesRevision(tag, revision string) bool {
	return tag == revision
}
