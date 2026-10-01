// Package margo embeds the pinned Margo specification files under f209a7f/ (SPEC header, ADR
// 0010), so production code validates against the same bytes the tests do. The files themselves
// are never edited; see f209a7f/README.md.
package margo

import _ "embed"

// ApplicationDescriptionSchema is the JSON Schema generated from Margo's LinkML Application
// Description schema (SPEC §5.3), unchanged. Its known defects are listed in f209a7f/README.md;
// internal/contract works around them in memory.
//
//go:embed f209a7f/application-description.schema.json
var ApplicationDescriptionSchema []byte
