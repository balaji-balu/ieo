// Command gopackages prints the Go packages CI checks, one per line.
//
//	go run ./tools/gopackages          packages to build (all but knownBroken)
//	go run ./tools/gopackages test     packages to test and lint with tests
//	                                   (also excludes brokenTests)
//	go run ./tools/gopackages no-test  brokenTests: lint these with --tests=false
//
// Both lists hold packages that are broken on main (see "Known baseline
// issues" in CLAUDE.md). A slice that fixes one deletes it from its list.
// Never add a package here to get CI green.
//
// It is a Go program rather than a shell script so the same command works in
// PowerShell and bash (ADR 0009). Run it from the module root.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// knownBroken packages' production code doesn't compile: skipped entirely.
var knownBroken = []string{
	"internal/en/plugins/wasm", // undefined: runtime
	"tests/e2e",                // needs a running stack; fails
}

// brokenTests packages compile, but their test files don't: built and linted
// without tests.
var brokenTests = []string{
	"internal/config",        // config_test.go: undefined ResolveRootDir
	"internal/lo/boltstore",  // store_test.go imports missing package rec/store
	"internal/lo/reconciler", // reconciler_test.go does not parse
}

func main() {
	mode := "build"
	if len(os.Args) > 2 {
		usage()
	}
	if len(os.Args) == 2 {
		mode = os.Args[1]
	}

	var module string
	var all []string
	if mode != "no-test" {
		module = strings.TrimSpace(goList("-m"))
		all = strings.Fields(goList("./..."))
	}

	pkgs, err := selectPackages(mode, module, all)
	if err != nil {
		usage()
	}
	for _, p := range pkgs {
		fmt.Println(p)
	}
}

// selectPackages returns the packages for mode as ./-relative paths, given the
// module path and the import paths of every package in it.
func selectPackages(mode, module string, all []string) ([]string, error) {
	skip := map[string]bool{}
	switch mode {
	case "no-test":
		return relative(brokenTests), nil
	case "build":
	case "test":
		for _, p := range brokenTests {
			skip[p] = true
		}
	default:
		return nil, fmt.Errorf("unknown mode %q", mode)
	}
	for _, p := range knownBroken {
		skip[p] = true
	}

	var out []string
	for _, imp := range all {
		rel := strings.TrimPrefix(strings.TrimPrefix(imp, module), "/")
		if imp == module {
			rel = ""
		}
		if skip[rel] {
			continue
		}
		out = append(out, "./"+rel)
	}
	return out, nil
}

func relative(pkgs []string) []string {
	out := make([]string, len(pkgs))
	for i, p := range pkgs {
		out[i] = "./" + p
	}
	return out
}

func goList(args ...string) string {
	cmd := exec.CommandContext(context.Background(), "go", append([]string{"list"}, args...)...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "gopackages: go list %s: %v\n", strings.Join(args, " "), err)
		os.Exit(1)
	}
	return string(out)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: go run ./tools/gopackages [build|test|no-test]")
	os.Exit(2)
}
