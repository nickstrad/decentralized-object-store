#!/usr/bin/env bash
# Part 1.2: a migration start whose outcome is unknown. Runner mutations, product mutations,
# and lost-reply schedules with the coordinator paused / crashed / cut off past the heal bound.
source "$(dirname "$0")/lib.sh"
C=P1.2
NEMT='TestNemesisAmbiguousMigrationStartIsTracked|TestCanaryNemesisForgottenAmbiguousMigrationFails|TestFuzzClusterNemesisCorpusMovesShards|^FuzzClusterNemesis$|TestE7NemesisHealingCyclesS'

# Runner mutations.
mutation_check $C a02-no-saga-lookup p12e-no-saga-lookup.patch "$NEMT" 'not recovered|recovered = 0|ambiguousRecovered|bucket cannot migrate|migrated'
# A guard that only prevents false failures: removing it may survive without weakening any check.
d=$(mkscratch); apply_patch "$d" p12d-unblock-coordinator-wipe.patch
log=$EVID/a02-unblock-coordinator-wipe.log
ENVS="-e DST_SEEDS=30" indocker "$d" "$log" "go test ./test/e2e -count=1 -run '$NEMT'"; rc=$?
v=$(classify "$log" $rc 'saga|migration|not within|terminal')
case $v in
  SURVIVED) held $C a02-unblock-coordinator-wipe "removing the coordinator-wipe guard SURVIVED $NEMT at DST_SEEDS=30; judged harmless: the guard only keeps the runner from wiping a live coordinator (stated limit OQ-13), which could cause false failures, never hide a defect";;
  KILLED|OTHERFAIL) held $C a02-unblock-coordinator-wipe "removing the coordinator-wipe guard makes the suite fail ($(firstfail "$log")) - the guard is needed to avoid wiping a live coordinator (stated limit OQ-13)";;
  *) setup $C a02-unblock-coordinator-wipe "setup error (evidence/a02-unblock-coordinator-wipe.log)";;
esac
rm -rf "$d"
# Forgetting a start that failed with ErrUnreachable: the simulator contract says
# unreachable = refused or unknown node, i.e. the request never ran. An equivalent mutant?
d=$(mkscratch); apply_patch "$d" p12a-forget-on-unreachable.patch
log=$EVID/a02-forget-on-unreachable.log
ENVS="-e DST_SEEDS=30" indocker "$d" "$log" "go test ./test/e2e -count=1 -run '$NEMT'"; rc=$?
v=$(classify "$log" $rc 'migration|saga|route')
case $v in
  SURVIVED) held $C a02-forget-on-unreachable "forgetting a start that failed with ErrUnreachable SURVIVED (DST_SEEDS=30); equivalent mutant: in the simulator ErrUnreachable is a refusal by a down node or an unknown node, so StartMigration never ran";;
  KILLED|OTHERFAIL) held $C a02-forget-on-unreachable "forgetting an ErrUnreachable start was killed: $(firstfail "$log")";;
  *) setup $C a02-forget-on-unreachable "setup error (evidence/a02-forget-on-unreachable.log)";;
esac
rm -rf "$d"

# Product mutations: a started saga that never finishes; one that completes leaving source shards;
# the second also with the runner's two post-migration shard checks removed (who else notices?).
mutation_check $C a02-saga-never-finishes p12f-saga-claim-never-completes.patch "$NEMT" 'not within|migration|terminal'
# (A completed migration that leaves source shards = the purge no-op of a01: tier S misses it, tier R kills it.)
mutation_check $C a02-copy-noop-runner-checks-off "p11f-copy-writes-nothing.patch p12g-skip-per-key-and-outside.patch" "$NEMT" 'shard|lost|deleted|unreadable|wrong'

# Lost-reply schedules on a correct product.
d=$(mkscratch); cp "$HERE/tools/opus_sched_test.go.txt" "$d/test/e2e/opus_sched_test.go"
log=$EVID/a02-schedules.log
indocker "$d" "$log" "go test ./test/e2e -count=1 -v -run '^TestOpusLostReplyCoordinatorSchedules\$' -timeout 30m"; rc=$?
if ! grep -q 'OPUS ' "$log" && ! grep -q -- '--- FAIL' "$log"; then
  setup $C a02-schedules "schedule test did not run (evidence/a02-schedules.log)"
else
  for sc in $(grep -oE 'TestOpusLostReplyCoordinatorSchedules/[a-z-]+' "$log" | sort -u | sed 's#.*/##'); do
    fails=$(grep -E -- "--- FAIL: TestOpusLostReplyCoordinatorSchedules/$sc/" "$log" | sed 's/.*seed=\([0-9]*\).*/\1/' | tr '\n' ' ')
    stats=$(grep -E "OPUS $sc " "$log" | sed 's/.*OPUS [a-z-]* //' | tr '\n' ';' | cut -c1-300)
    if [ -n "$fails" ]; then
      first=$(awk -v pat="=== RUN   TestOpusLostReplyCoordinatorSchedules/$sc/" 'index($0,pat)==1{f=1} f&&/opus_sched_test.go:[0-9]+:/&&!/OPUS /{print; exit}' "$log" | tr -s ' ' | cut -c1-200)
      broken $C a02-sched-$sc "lost StartMigration reply then $sc fails on seeds $fails: $first (tools/opus_sched_test.go.txt, go test -run 'TestOpusLostReplyCoordinatorSchedules/$sc')"
    else
      held $C a02-sched-$sc "lost reply then $sc: both cycles end terminal and checked on seeds 702 9001 31337 ($stats)"
    fi
  done
fi
rm -rf "$d"

# The pause schedule again with no lost reply at all, and with OrphanGrace raised to 300 s
# as a control: is the loss a migration whose coordinator stalls past OrphanGrace?
d=$(mkscratch); cp "$HERE/tools/opus_sched_test.go.txt" "$d/test/e2e/opus_sched_test.go"
for v in NOPART CONTROL; do
  e="-e OPUS_ONLY=lost-reply-then-pause-past-heal-bound -e OPUS_SEEDS=31337,3,4"
  [ $v = NOPART ] && e="$e -e OPUS_NOPART=1" || e="$e -e OPUS_ORPHAN_S=300"
  ENVS="$e" indocker "$d" "$EVID/a02-pause-$v.log" "go test ./test/e2e -count=1 -v -run '^TestOpusLostReplyCoordinatorSchedules\$' -timeout 20m"
done
lostN=$(grep -cE -- '--- FAIL: .*seed=' "$EVID/a02-pause-NOPART.log"); lostMsg=$(grep -m1 -E 'declared ~lost' "$EVID/a02-pause-NOPART.log" | tr -s ' ' | cut -c1-160)
ctlLost=$(grep -c 'declared ~lost' "$EVID/a02-pause-CONTROL.log")
if [ "$lostN" -gt 0 ] && grep -q 'declared ~lost' "$EVID/a02-pause-NOPART.log"; then
  broken P1.2-product a02-coordinator-paused-past-orphan-grace-loses-object "a migration (no lost reply) whose coordinator is paused 70 s right after the copy starts completes and loses the acknowledged warm-up object m/warm00 on $lostN of 3 seeds (31337,3,4; fuzz layout, OrphanGrace 60 s): $lostMsg; with OrphanGrace 300 s the loss is gone ($ctlLost declarations) - the destination collects the in-flight copy's shards as orphans (OPUS_NOPART=1 OPUS_ONLY=lost-reply-then-pause-past-heal-bound OPUS_SEEDS=31337 go test ./test/e2e -run TestOpusLostReplyCoordinatorSchedules with tools/opus_sched_test.go.txt)"
elif grep -qE -- '--- PASS: TestOpusLostReplyCoordinatorSchedules' "$EVID/a02-pause-NOPART.log"; then
  held P1.2-product a02-coordinator-paused-past-orphan-grace-loses-object "a migration whose coordinator is paused 70 s keeps every acknowledged object on seeds 31337,3,4"
else
  setup P1.2-product a02-coordinator-paused-past-orphan-grace-loses-object "pause run did not complete (evidence/a02-pause-NOPART.log)"
fi
rm -rf "$d"
finish
