#!/usr/bin/env bash
# Prints the Go packages CI checks, one per line.
#
#   scripts/go-packages.sh          packages to build (all but KNOWN_BROKEN)
#   scripts/go-packages.sh test     packages to test and lint with tests
#                                   (also excludes BROKEN_TESTS)
#   scripts/go-packages.sh no-test  BROKEN_TESTS: lint these with --tests=false
#
# Both lists hold packages that are broken on main (see "Known baseline
# issues" in CLAUDE.md). A slice that fixes one deletes it from its list.
# Never add a package here to get CI green.
set -euo pipefail

# Production code doesn't compile: skipped entirely.
KNOWN_BROKEN=(
  internal/era/plugins/wasm # undefined: runtime
  tests/e2e                 # needs a running stack; fails
)

# Production code compiles, test files don't: built and linted without tests.
BROKEN_TESTS=(
  internal/config        # config_test.go: undefined ResolveRootDir
  internal/lo/boltstore  # store_test.go imports missing package rec/store
  internal/lo/reconciler # reconciler_test.go does not parse
)

mode=${1:-build}
module=$(go list -m)

if [[ $mode == no-test ]]; then
  printf './%s\n' "${BROKEN_TESTS[@]}"
  exit 0
fi

skip=("${KNOWN_BROKEN[@]}")
case $mode in
  build) ;;
  test) skip+=("${BROKEN_TESTS[@]}") ;;
  *) echo "usage: $0 [build|test|no-test]" >&2; exit 2 ;;
esac

exclude=()
for p in "${skip[@]}"; do
  exclude+=(-e "^${module}/${p}\$")
done

go list ./... | grep -v "${exclude[@]}" | sed "s#^${module}#.#"
