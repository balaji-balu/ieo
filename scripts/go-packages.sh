#!/usr/bin/env bash
# Prints the Go packages CI checks, one per line.
#
#   scripts/go-packages.sh          packages to build
#   scripts/go-packages.sh test     packages to test and lint
#
# Both are every package of the module. The lists of packages that didn't
# compile (KNOWN_BROKEN, BROKEN_TESTS) went with the Git-based code they
# belonged to (roadmap, after slice E). Never skip a package here to get CI
# green.
set -euo pipefail

mode=${1:-build}
case $mode in
  build | test) ;;
  *) echo "usage: $0 [build|test]" >&2; exit 2 ;;
esac

module=$(go list -m)
go list ./... | sed "s#^${module}#.#"
