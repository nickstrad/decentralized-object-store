#!/bin/bash
# Run every native fuzz target in the root module for a short budget (gate G3).
# Go runs one fuzz target per invocation, so this loops over them. Targets are found with
# `go test -list`, the Go tool's own discovery, so a name the tool accepts cannot be missed.
#
# Usage (inside the dev container): agent_work/gates/fuzz-short.sh [package-pattern...]
#   FUZZ_SECONDS=30          budget per target
#   FUZZ_ONLY='^FuzzGF256$'  optional regexp on target names (never set by the gate)
# -fuzzminimizetime is kept at 5s: the default minimize time looks like a hang.
set -u
set -o pipefail
secs=${FUZZ_SECONDS:-30}
pats=("$@"); [ ${#pats[@]} -gt 0 ] || pats=(./...)
rc=0; n=0
pkgs=$(go list "${pats[@]}") || { echo "FUZZ FAIL: go list failed"; exit 1; }
[ -n "$pkgs" ] || { echo "FUZZ FAIL: no packages matched ${pats[*]}"; exit 1; }
for pkg in $pkgs; do
  listed=$(go test -list '^Fuzz' "$pkg") || { echo "FUZZ FAIL $pkg: cannot list tests"; rc=1; continue; }
  targets=$(grep '^Fuzz' <<<"$listed" || true)
  for t in $targets; do
    if [ -n "${FUZZ_ONLY:-}" ] && ! [[ "$t" =~ ${FUZZ_ONLY} ]]; then continue; fi
    n=$((n + 1))
    echo "== fuzz $pkg $t for ${secs}s"
    if ! go test "$pkg" -run '^$' -fuzz "^${t}\$" -fuzztime "${secs}s" -fuzzminimizetime 5s; then
      echo "FUZZ FAIL $pkg $t"; rc=1
    fi
  done
done
echo "NOTE: $n fuzz targets, ${secs}s each"
exit $rc
