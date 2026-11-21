#!/bin/bash
# Run every native fuzz target in the root module for a short budget (gate G3).
# Go runs one fuzz target per invocation, so this loops over them.
#
# Usage (inside the dev container): agent_work/gates/fuzz-short.sh [package-pattern...]
#   FUZZ_SECONDS=30          budget per target
#   FUZZ_ONLY='^FuzzGF256$'  optional regexp on target names
# -fuzzminimizetime is kept at 5s: the default minimize time looks like a hang.
set -u
secs=${FUZZ_SECONDS:-30}
pats=("$@"); [ ${#pats[@]} -gt 0 ] || pats=(./...)
rc=0; n=0
while read -r pkg dir; do
  [ -n "$pkg" ] || continue
  targets=$(grep -hoE '^func (Fuzz[A-Za-z0-9_]*)\(' "$dir"/*_test.go 2>/dev/null | sed -E 's/^func (Fuzz[A-Za-z0-9_]*)\(/\1/' | sort -u)
  for t in $targets; do
    if [ -n "${FUZZ_ONLY:-}" ] && ! [[ "$t" =~ ${FUZZ_ONLY} ]]; then continue; fi
    n=$((n + 1))
    echo "== fuzz $pkg $t for ${secs}s"
    if ! go test "$pkg" -run '^$' -fuzz "^${t}\$" -fuzztime "${secs}s" -fuzzminimizetime 5s; then
      echo "FUZZ FAIL $pkg $t"; rc=1
    fi
  done
done < <(go list -f '{{.ImportPath}} {{.Dir}}' "${pats[@]}")
echo "NOTE: $n fuzz targets, ${secs}s each"
exit $rc
