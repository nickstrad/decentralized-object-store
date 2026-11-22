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
rc=0; n=0; retried=
pkgs=$(go list "${pats[@]}") || { echo "FUZZ FAIL: go list failed"; exit 1; }
[ -n "$pkgs" ] || { echo "FUZZ FAIL: no packages matched ${pats[*]}"; exit 1; }
for pkg in $pkgs; do
  listed=$(go test -list '^Fuzz' "$pkg") || { echo "FUZZ FAIL $pkg: cannot list tests"; rc=1; continue; }
  targets=$(grep '^Fuzz' <<<"$listed" || true)
  for t in $targets; do
    if [ -n "${FUZZ_ONLY:-}" ] && ! [[ "$t" =~ ${FUZZ_ONLY} ]]; then continue; fi
    n=$((n + 1))
    echo "== fuzz $pkg $t for ${secs}s"
    log=$(mktemp)
    if ! go test "$pkg" -run '^$' -fuzz "^${t}\$" -fuzztime "${secs}s" -fuzzminimizetime 5s 2>&1 | tee "$log"; then
      # The Go fuzzer can end a clean run with "context deadline exceeded" when the time
      # budget expires while a worker is mid-call on a loaded machine: no failing input,
      # no crasher file. That one signature (fuzzing had started, and the failure is the
      # fuzzer's own bare deadline line) is retried ONCE; a target that really stalls
      # shows it again and fails, and any failure that names an input fails at once.
      if grep -q '^fuzz: elapsed:' "$log" && grep -qE '^[[:space:]]+context deadline exceeded$' "$log" && ! grep -qE 'Failing input written to|panic:|fuzzing process hung or terminated' "$log" \
         && [ "$(grep -c -- '--- FAIL' "$log")" = 1 ]; then
        echo "NOTE-RETRY $pkg $t: deadline at the end of the budget with no failing input; running it once more"
        retried="$retried $t"
        if ! go test "$pkg" -run '^$' -fuzz "^${t}\$" -fuzztime "${secs}s" -fuzzminimizetime 5s; then
          echo "FUZZ FAIL $pkg $t (twice)"; rc=1
        fi
      else
        echo "FUZZ FAIL $pkg $t"; rc=1
      fi
    fi
    rm -f "$log"
  done
done
echo "NOTE: $n fuzz targets, ${secs}s each${retried:+; retried once after a deadline at the end of the budget:$retried}"
exit $rc
