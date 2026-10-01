// Command conformance lists the SPEC §17 bullets that have no TestSpec_ test (G-F2), and the
// TestSpec_ tests that cover no bullet. A test covers a bullet when its doc comment quotes the
// bullet's opening words (ADR 0011):
//
//	// SPEC §17.4: "Reconciling twice with no change"
//	func TestSpec_17_4_ReconcileIsIdempotent(t *testing.T) {
//
// Usage, from the repository root:
//
//	go run ./tools/conformance [-root dir] [-spec file] [-strict]
//
// It exits 0 after printing the report, 1 with -strict when a core bullet has no test or a test
// covers nothing, and 2 on a usage or read error.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("conformance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	root := fs.String("root", ".", "repository root to search for _test.go files")
	spec := fs.String("spec", "", "path of SPEC.md (default <root>/SPEC.md)")
	strict := fs.Bool("strict", false, "exit 1 if a core bullet has no test or a test covers nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *spec == "" {
		*spec = filepath.Join(*root, "SPEC.md")
	}

	r, err := load(*root, *spec)
	if err != nil {
		return fail(stderr, err)
	}
	if err := r.write(stdout); err != nil {
		return fail(stderr, err)
	}
	if *strict && r.failsStrict() {
		return 1
	}
	return 0
}

// fail reports a usage or read error and returns its exit code.
func fail(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintf(stderr, "conformance: %v\n", err) // nowhere left to report a failed write
	return 2
}
