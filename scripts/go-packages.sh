#!/usr/bin/env bash
# Prints the Go packages CI builds, tests and lints, one per line.
#
# KNOWN_BROKEN lists packages that don't compile or whose tests don't compile
# on main (see "Known baseline issues" in CLAUDE.md). They are skipped until a
# slice fixes them; that slice deletes the package from this list. Never add a
# package here to get CI green.
set -euo pipefail

KNOWN_BROKEN=(
  internal/config           # config_test.go: undefined ResolveRootDir
  internal/era/plugins/wasm # undefined: runtime
  internal/lo/boltstore     # store_test.go imports missing package rec/store
  internal/lo/reconciler    # reconciler_test.go does not parse
  tests/e2e                 # needs a running stack; fails
)

module=$(go list -m)
exclude=()
for p in "${KNOWN_BROKEN[@]}"; do
  exclude+=(-e "^${module}/${p}\$")
done

go list ./... | grep -v "${exclude[@]}" | sed "s#^${module}#.#"
