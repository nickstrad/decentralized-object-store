#!/usr/bin/env bash
# Part 3: own seeds on tier S, REPLAY really replays, cross-process determinism, edges of
# planFromBytes with time/memory per execution, and tier R hygiene after a killed test.
source "$(dirname "$0")/lib.sh"
C=P3

# 1. Fresh seeds (not the builder's sweep base) on every tier-S scenario but the E5 matrix.
d=$(mkscratch); log=$EVID/a05-own-seeds.log
ENVS="-e DST_SEED_BASE=880000 -e DST_SEEDS=40" indocker "$d" "$log" "go test ./test/e2e -count=1 -v -run '^TestE(1|2|3|4|6|7|8)[A-Za-z]*S\$' -timeout 40m"; rc=$?
if [ $rc = 0 ]; then
  n=$(grep -cE -- '--- PASS: TestE[0-9A-Za-z]+S/shard=[0-9]+/seed=' "$log")
  held $C a05-own-seeds "tier S E1-E4, E6-E8 at DST_SEED_BASE=880000 DST_SEEDS=40: $n seed runs pass, including the E7 sweep floors (>=10 and >=20 seeds)"
else
  for f in $(grep -oE -- '--- FAIL: TestE[0-9A-Za-z]+S/shard=[0-9]+/seed=[0-9-]+' "$log" | sed 's/--- FAIL: //' | sort -u); do
    broken $C a05-own-seeds-$(echo "$f" | tr '/=' '__') "$f fails on a correct product: $(grep -A6 -- "--- FAIL: $f" "$log" | grep -m1 '_test.go' | tr -s ' ' | cut -c1-200) (DST_SEED_BASE=880000 DST_SEEDS=40)"
  done
  grep -qE -- '--- FAIL: TestE[0-9A-Za-z]+S/shard' "$log" || broken $C a05-own-seeds "own-seed run failed outside a seed: $(firstfail "$log")"
fi
rm -rf "$d"

# 2. REPLAY: plant a failure in every E7 seed, then run the printed REPLAY lines.
d=$(mkscratch); apply_patch "$d" p3-plant-e7-failure.patch; log=$EVID/a05-replay-sweep.log
ENVS="-e DST_SEEDS=8" indocker "$d" "$log" "go test ./test/e2e -count=1 -run '^TestE7NemesisHealingCyclesS\$'"
grep -oE "REPLAY: go test .*" "$log" | sort -u > "$EVID/a05-replay-lines.txt"
if [ ! -s "$EVID/a05-replay-lines.txt" ]; then
  setup $C a05-replay "planted E7 failure printed no REPLAY line (evidence/a05-replay-sweep.log)"
else
  while read -r line; do
    cmd=${line#REPLAY: }
    sh=$(echo "$cmd" | grep -oE 'shard=[0-9]+' | head -1); seed=$(echo "$cmd" | grep -oE -- '-seed=[0-9-]+' | cut -d= -f2)
    rl=$EVID/a05-replay-$sh-$seed.log
    indocker "$d" "$rl" "$cmd -v"; rrc=$?
    if [ $rrc != 0 ] && grep -q 'planted opus failure at cycle 1' "$rl"; then
      held $C a05-replay-$sh "the printed REPLAY for seed $seed ($sh) reproduces the planted failure"
    else
      ran=$(grep -cE -- '=== RUN .*seed=' "$rl")
      broken $C a05-replay-$sh "printed '$cmd' exits $rrc and does not reproduce the planted failure (seed subtests run: $ran): -seed puts the one seed in shard=0, so a pattern naming $sh matches nothing and the top-level test passes"
    fi
  done < "$EVID/a05-replay-lines.txt"
fi
rm -rf "$d"

# 3. Determinism across processes: one E7 seed twice, every MEASURE line identical.
d=$(mkscratch)
for i in 1 2; do indocker "$d" "$EVID/a05-det-$i.log" "go test ./test/e2e -count=1 -v -run '^TestE7NemesisHealingCyclesS\$' -seed=880123"; done
a=$(grep -E 'MEASURE' "$EVID/a05-det-1.log" | sed 's/^ *//'); b=$(grep -E 'MEASURE' "$EVID/a05-det-2.log" | sed 's/^ *//')
if [ -z "$a" ]; then setup $C a05-determinism "no MEASURE output (evidence/a05-det-1.log)"
elif [ "$a" = "$b" ]; then held $C a05-determinism "E7 seed 880123 run in two processes gives identical MEASURE lines ($(echo "$a" | wc -l) lines incl. virtual-time convergence)"
else broken $C a05-determinism "E7 seed 880123 differs between two processes (evidence/a05-det-*.log)"; fi
rm -rf "$d"

# 4. planFromBytes edges, same input twice, time and memory per execution.
d=$(mkscratch); cp "$HERE/tools/opus_plan_test.go.txt" "$d/test/e2e/opus_plan_test.go"; log=$EVID/a05-edges.log
indocker "$d" "$log" "go test ./test/e2e -count=1 -v -run '^TestOpus(PlanFromBytesEdges|FuzzEdgeInputs|FuzzSameInputTwice)\$' -timeout 20m"; rc=$?
grep -E 'OPUS-EDGE ' "$log" | sed 's/.*OPUS-EDGE //' > "$EVID/a05-edges.txt"
if ! grep -q 'OPUS-EDGE ' "$log"; then setup $C a05-edges "edge test did not run (evidence/a05-edges.log)"; else
  grep -q 'OPUS-PLAN' "$log" && broken $C a05-plan-shape "planFromBytes produced an out-of-bounds plan: $(grep -m1 OPUS-PLAN "$log" | tr -s ' ')" || held $C a05-plan-shape "planFromBytes keeps 1-3 cycles, <=8 ops per cycle, kinds in range, times in [0, chaos) for $(wc -l < "$EVID/a05-edges.txt") edge inputs"
  if grep -q 'OPUS-EDGE-FAIL' "$log"; then
    while read -r l; do broken $C a05-edge "$(echo "$l" | sed 's/.*OPUS-EDGE-FAIL //' | cut -c1-230) (tools/opus_plan_test.go.txt)"; done < <(grep 'OPUS-EDGE-FAIL' "$log")
  else
    mx=$(awk '{for(i=1;i<=NF;i++){split($i,a,"="); if(a[1]=="wall_ms"&&a[2]>w){w=a[2];wn=$1} if(a[1]=="alloc_mb"&&a[2]>m){m=a[2];mn=$1} if(a[1]=="sys_mb"&&a[2]>s)s=a[2]}} END{print "max wall " w " ms (" wn "), max alloc " m " MiB (" mn "), process sys " s " MiB"}' "$EVID/a05-edges.txt")
    held $C a05-edges "every edge input (empty, all-0xff, 64 KiB zero/0xff/random, every single op kind x24, zero-length cycles, truncated op) passes Verify; $mx"
  fi
  grep -q 'OPUS-NONDET' "$log" && broken $C a05-fuzz-determinism "same fuzz input gave two traces" || { grep -q -- '--- PASS: TestOpusFuzzSameInputTwice' "$log" && held $C a05-fuzz-determinism "the same fuzz input run twice in one process gives the same step count, end time and stats"; }
fi
rm -rf "$d"

# 5. Tier R hygiene: SIGKILL and SIGINT of the test binary with a 9-node cluster up.
for sig in KILL INT; do
  d=$(mkscratch); cp "$HERE/tools/tierr-kill.sh" "$d/tierr-kill.sh"; log=$EVID/a05-tierr-kill-$sig.log
  indocker "$d" "$log" "bash tierr-kill.sh $sig"; rc=$?
  if grep -q SETUPFAIL "$log" || ! grep -q objnode_after_kill "$log"; then setup $C a05-tierr-$sig "tier R kill harness did not run (evidence/a05-tierr-kill-$sig.log)"; else
    procs=$(grep -oE 'objnode_after_kill=[0-9]+' "$log" | cut -d= -f2); dirs=$(grep -oE 'tempdirs_left=[0-9-]+' "$log" | cut -d= -f2)
    if [ "$procs" != 0 ]; then broken $C a05-tierr-$sig "$procs objnode processes survive SIG$sig of the test binary"
    elif [ "$dirs" -gt 0 ]; then held $C a05-tierr-$sig "no objnode survives SIG$sig of the test binary (Pdeathsig); $dirs temp dirs are left in \$TMPDIR (cleanup cannot run after the signal; container-scoped)"
    else held $C a05-tierr-$sig "no objnode process and no temp dir survives SIG$sig of the test binary"; fi
  fi
  rm -rf "$d"
done
# Built-in hygiene tests plus the E9 canary, and the default binary's strings.
d=$(mkscratch); log=$EVID/a05-tierr-hygiene.log
ENVS="-e E2E_TIER_R=1" indocker "$d" "$log" "go test ./test/e2e -count=1 -v -run '^(TestTierRHarnessLeavesNothingBehindWhenATestFails|TestDefaultBuildCarriesNoFaultInjection|TestCanaryE9AKilledNodeFailsTheCleanShutdownChecks)\$' -timeout 20m && go build -o /tmp/objnode-default ./cmd/objnode && echo STRINGS=\$(strings -a /tmp/objnode-default 2>/dev/null | grep -ciE 'e2efault|blocklist' || true)"; rc=$?
for t in TestTierRHarnessLeavesNothingBehindWhenATestFails TestDefaultBuildCarriesNoFaultInjection TestCanaryE9AKilledNodeFailsTheCleanShutdownChecks; do
  if grep -qE "^--- PASS: $t" "$log"; then held $C a05-$t "passes on tier R"; elif grep -qE "^--- SKIP: $t" "$log"; then broken $C a05-$t "skipped with E2E_TIER_R=1"; else broken $C a05-$t "fails: $(firstfail "$log")"; fi
done
s=$(grep -oE 'STRINGS=[0-9]+' "$log" | cut -d= -f2)
if [ -z "$s" ]; then setup $C a05-default-binary "strings check did not run"; elif [ "$s" = 0 ]; then held $C a05-default-binary "the default objnode binary contains no 'e2efault' or 'blocklist' string"; else broken $C a05-default-binary "default objnode carries $s e2efault/blocklist strings"; fi
rm -rf "$d"
finish
