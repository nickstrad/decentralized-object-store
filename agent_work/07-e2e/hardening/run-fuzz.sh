#!/bin/bash
# Hardening fuzz runs for Chunk 7 (PLAN 2.3, OQ-9): every fuzz target in the root module,
# 5 minutes each and 15 minutes for the four headline targets, at most 3 at a time,
# -fuzzminimizetime 5s, one fuzz worker per target (-parallel 1) so three targets fit on a
# shared 4-core machine. Run inside the dev container:
#   docker compose run --rm -T dev agent_work/07-e2e/hardening/run-fuzz.sh
# Logs go to agent_work/07-e2e/hardening/fuzz/<Target>.log; the summary to fuzz/summary.tsv.
set -u
out=agent_work/07-e2e/hardening/fuzz
mkdir -p "$out"
headline=" FuzzErasureCoding FuzzMerkleDiff FuzzOCCConditionalWrite FuzzClusterNemesis "
short=${SHORT_SECONDS:-300}
long=${LONG_SECONDS:-900}
jobs=()
for pkg in $(go list ./...); do
  for t in $(go test -list '^Fuzz' "$pkg" | grep '^Fuzz' || true); do
    secs=$short
    [[ "$headline" == *" $t "* ]] && secs=$long
    jobs+=("$secs $pkg $t")
  done
done
printf '%s\n' "${jobs[@]}" | sort -rn > "$out/plan.txt"
run_one() {
  local secs=$1 pkg=$2 t=$3 start rc
  start=$(date +%s)
  go test "$pkg" -run '^$' -fuzz "^${t}\$" -fuzztime "${secs}s" -fuzzminimizetime 5s -parallel 1 > "$out/$t.log" 2>&1
  rc=$?
  local last execs interesting total
  last=$(grep '^fuzz: elapsed' "$out/$t.log" | tail -1)
  execs=$(sed -n 's/.*execs: \([0-9]*\).*/\1/p' <<<"$last")
  interesting=$(sed -n 's/.*new interesting: \([0-9]*\).*/\1/p' <<<"$last")
  total=$(sed -n 's/.*(total: \([0-9]*\)).*/\1/p' <<<"$last")
  local crash=none
  grep -q 'Failing input written to' "$out/$t.log" && crash=$(grep -o 'testdata/fuzz/[^ ]*' "$out/$t.log" | head -1)
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$t" "$pkg" "$secs" "$rc" "${execs:-?}" "${interesting:-?}" "${total:-?}" "$crash" >> "$out/summary.tsv"
  echo "done $t rc=$rc execs=${execs:-?} new=${interesting:-?} crash=$crash wall=$(( $(date +%s) - start ))s"
}
printf 'target\tpackage\tseconds\texit\texecs\tnew_interesting\tcorpus_total\tcrasher\n' > "$out/summary.tsv"
while read -r secs pkg t; do
  while [ "$(jobs -rp | wc -l)" -ge 3 ]; do sleep 5; done
  echo "start $t ${secs}s"
  run_one "$secs" "$pkg" "$t" &
done < "$out/plan.txt"
wait
echo "all done"
