// Package harness holds the tests that run SPEC §17.8 against the laptop dev harness
// (deploy/dev/compose.yaml, docs/dev-setup.md), and its test application in hello/. The tests skip
// unless IEO_HARNESS_CO_URL names the harness CO's operator API; they talk to it only through
// edgectl, run in-process.
package harness
