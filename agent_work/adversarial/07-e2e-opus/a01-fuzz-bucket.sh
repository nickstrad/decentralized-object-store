#!/usr/bin/env bash
# Part 1.1: does the repaired "fuzzed migrating bucket" bite? Mutations of the fuzz layout,
# of the routed bucket's PUT path and of the migration's copy / verify / purge steps, plus
# instrumentation of the seed corpus and a short fresh fuzz run.
source "$(dirname "$0")/lib.sh"
C=P1.1
FZ='TestFuzzClusterNemesisCorpusMovesShards|^FuzzClusterNemesis$'
ALL="$FZ|TestE4MigrationUnderTrafficS|TestE7NemesisHealingCyclesS"

mutation_check $C a01-unplaceable-9x42  p11a-fuzz-unplaceable-9x42.patch  "$FZ" 'fault-free warm-up put to routed bucket'
mutation_check $C a01-unplaceable-6x21  p11b-fuzz-unplaceable-6x21.patch  "$FZ" 'fault-free warm-up put to routed bucket'
mutation_check $C a01-routed-quota-1    p11d-routed-bucket-quota-1.patch  "$FZ|TestE7NemesisHealingCyclesS" 'disallowed error|warm-up put'
mutation_check $C a01-routed-put-unavailable-after-2 p11e-routed-put-unavailable-after-2.patch "$ALL" 'acknowledged 0 traffic puts|unavailable'
mutation_check $C a01-routed-put-ignores-region p11c-routed-put-ignores-region.patch "$ALL" 'outside|left in r|no shard|pre-existing'
mutation_check $C a01-copy-writes-nothing p11f-copy-writes-nothing.patch "$ALL" 'shard|lost|deleted|outside'
# Purge no-op: tier S first; the background GC (GCGrace 10 s on S, 5 s in E7) may collect the
# dead source versions inside the await bounds, so tier R (GC grace 10 min) is asked too.
mutation_check $C a01-purge-noop-S p11g-retire-purge-noop.patch "$ALL" 'outside|left in r0|no shard'
ENVS="-e E2E_TIER_R=1" mutation_check $C a01-purge-noop-R p11g-retire-purge-noop.patch '^TestE4MigrationUnderTrafficR$' 'outside|left in r0|no shard' -timeout 20m
mutation_check $C a01-replaced-never-dead p11j-retire-never-marks-replaced-dead.patch "$ALL" 'outside|left in r0|no shard|stray|orphan'
mutation_check $C a01-copy-skips-last p11i-copy-skips-last.patch "$ALL" 'not within|completed 0|migration|terminal'
mutation_check $C a01-copy-skips-last-verify-noop p11h-copy-skips-last-and-catchup-verify-noop.patch "$ALL" 'outside|pre-existing|left in r|no shard'

# Instrumentation: per-execution stats over the seed corpus and a 60 s fresh-cache fuzz run.
d=$(mkscratch) && apply_patch "$d" instr-fuzz-stats.patch || { setup $C a01-fuzz-measure "instrumentation patch failed"; finish; }
log=$EVID/a01-fuzz-measure.log
indocker "$d" "$log" "export OPUS_FUZZ_LOG=/src/corpus.stats; go test ./test/e2e -count=1 -run '^FuzzClusterNemesis\$' && export OPUS_FUZZ_LOG=/src/fuzz.stats && go test ./test/e2e -run '^\$' -fuzz '^FuzzClusterNemesis\$' -fuzztime 60s -fuzzminimizetime 0s -parallel 3 -test.fuzzcachedir=/tmp/opusfz"; rc=$?
cp "$d/corpus.stats" "$EVID/a01-corpus.stats" 2>/dev/null; cp "$d/fuzz.stats" "$EVID/a01-fuzz60s.stats" 2>/dev/null
if [ $rc != 0 ] || [ ! -s "$EVID/a01-corpus.stats" ] || [ ! -s "$EVID/a01-fuzz60s.stats" ]; then
  setup $C a01-fuzz-measure "instrumented fuzz run failed rc=$rc (evidence/a01-fuzz-measure.log)"
else
  for f in corpus fuzz60s; do
    s=$(python3 -I "$HERE/tools/fuzzstats.py" "$EVID/a01-$f.stats")
    echo "$s" > "$EVID/a01-$f.summary"
    if echo "$s" | grep -q 'VIOLATION'; then broken $C a01-fuzz-measure-$f "$s"; else held $C a01-fuzz-measure-$f "$s"; fi
  done
fi
rm -rf "$d"
finish
