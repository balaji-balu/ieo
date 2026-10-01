// Package contract is IEO's wire contract: the Margo Workload Management API bodies (SPEC §11.1),
// the LO ↔ EN site messages and their JSON Schemas (SPEC §11.2), and the stable identifiers
// (SPEC §4.2). Every tier encodes and decodes these through this package, so each wire format is
// decided in one place (G-A2).
//
// Identifiers obtained from a Parse function or by decoding JSON are valid. Site messages are
// decoded with DecodeSiteMessage, which validates against the message's schema first.
//
// The Margo types are hand-written and tested against the vendored OpenAPI file under
// api/margo/<commit>/, not generated from it (ADR 0010).
package contract
