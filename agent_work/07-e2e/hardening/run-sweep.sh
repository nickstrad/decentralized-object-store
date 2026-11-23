#!/bin/bash
# Chunk 7 large seed sweep: every package with DST scenarios (it imports simtest in its
# tests), at DST_SEEDS=5000 where that fits in 30 minutes, otherwise the largest count that
# does, estimated from a 40-seed calibration run with another base. Packages run one at a
# time so wall times are comparable. Run inside the dev container:
#   docker compose run --rm -T dev agent_work/07-e2e/hardening/run-sweep.sh
# test/e2e keeps its per-scenario seed shares (E2E_FULL_SEEDS unset), as in the gate.
# Output: agent_work/07-e2e/hardening/sweep/<pkg>.{cal,run}.log and sweep/summary.tsv.
set -u
out=agent_work/07-e2e/hardening/sweep
mkdir -p "$out"
budget=${SWEEP_BUDGET_SECONDS:-1680}
cal_seeds=40
cal_base=${CAL_BASE:-424242}
run_base=${RUN_BASE:-9000001}
only=${SWEEP_ONLY:-}
pkgs=$(go list -f '{{.ImportPath}} {{join .TestImports " "}} {{join .XTestImports " "}}' ./... | grep 'internal/sim/simtest' | cut -d' ' -f1 | grep -v '/sim/simtest$')
printf 'package\tcal_seeds\tcal_seconds\tseeds\tbase\tseconds\texit\tfailing\n' > "$out/summary.tsv"
for pkg in $pkgs; do
  short=${pkg#github.com/nickstrad/object-store/}
  if [ -n "$only" ] && ! [[ "$short" =~ $only ]]; then continue; fi
  name=$(tr '/' '_' <<<"$short")
  s=$(date +%s)
  DST_SEEDS=$cal_seeds DST_SEED_BASE=$cal_base go test "$pkg" -count=1 -timeout 60m > "$out/$name.cal.log" 2>&1
  cal=$(( $(date +%s) - s ))
  n=$(( cal_seeds * budget / (cal > 0 ? cal : 1) ))
  [ $n -gt 5000 ] && n=5000
  [ $n -lt $cal_seeds ] && n=$cal_seeds
  echo "$short: calibration $cal_seeds seeds in ${cal}s -> $n seeds"
  s=$(date +%s)
  DST_SEEDS=$n DST_SEED_BASE=$run_base go test "$pkg" -count=1 -timeout 45m -v > "$out/$name.run.log" 2>&1
  rc=$?
  secs=$(( $(date +%s) - s ))
  failing=$(grep -oE -- '--- FAIL: [^ ]*seed=[0-9]+' "$out/$name.run.log" | sed 's/--- FAIL: //' | sort -u | tr '\n' ' ')
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$short" "$cal_seeds" "$cal" "$n" "$run_base" "$secs" "$rc" "${failing:-none}" >> "$out/summary.tsv"
  echo "$short: $n seeds in ${secs}s rc=$rc failing: ${failing:-none}"
  grep -E '^(ok|FAIL|---)' "$out/$name.run.log" | grep -vE '^    ' > "$out/$name.run.summary" || true
  grep -h 'MEASURE' "$out/$name.run.log" | sed 's/^ *//' >> "$out/$name.run.summary" || true
  gzip -f "$out/$name.run.log"
done
echo "sweep done"
