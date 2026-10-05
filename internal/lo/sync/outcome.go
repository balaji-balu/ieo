package sync

// Outcome is how one sync attempt ended (SPEC §7.4). Every attempt ends in exactly one outcome,
// and its log line carries it in the `outcome` field (SPEC §13.1).
type Outcome string

// The outcomes the sync tick produces. Until roadmap slice K adds Throttled, Retired and
// SkippedWindow, a failure that would be one of those is Unreachable.
const (
	// NotModified: the CO answered `304`; nothing changed.
	NotModified Outcome = "NotModified"
	// Accepted: a new manifest was verified and committed.
	Accepted Outcome = "Accepted"
	// RejectedRollback: the manifest's version is not greater than the accepted one. Security
	// event.
	RejectedRollback Outcome = "RejectedRollback"
	// AbortedDigestMismatch: a fetched artifact failed digest verification, or the bundle does not
	// match the manifest. Security event.
	AbortedDigestMismatch Outcome = "AbortedDigestMismatch"
	// Unreachable: a transport error, an unexpected response, a manifest that fails validation,
	// or a failed content fetch.
	Unreachable Outcome = "Unreachable"
)
