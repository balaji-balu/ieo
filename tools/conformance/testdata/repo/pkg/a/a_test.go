package a

import "testing"

// SPEC §17.1: "Device ID parsing splits on the first"
func TestSpec_17_1_DeviceIDSplitsOnFirstSlash(t *testing.T) {}

// Covers two bullets in one table-driven test.
//
// SPEC §17.1: "Site IDs with characters outside RFC 3986
// unreserved are rejected"
// SPEC §17.2: "First sync stores"
func TestSpec_17_1_SiteIDs(t *testing.T) {}

// SPEC §17.2: "First sync"
func TestSpec_17_2_Ambiguous(t *testing.T) {}

// SPEC §17.2: "No such bullet"
func TestSpec_17_2_Orphan(t *testing.T) {}

func TestSpec_17_2_NoQuote(t *testing.T) {}

// SPEC §17.1: "Device ID parsing"
func TestSpec_17_2_WrongSection(t *testing.T) {}

// SPEC §17.8: "Golden path"
func TestSpec_17_8_GoldenPath(t *testing.T) {}

// Not a spec test, so it is ignored.
func TestHelper(t *testing.T) {}
