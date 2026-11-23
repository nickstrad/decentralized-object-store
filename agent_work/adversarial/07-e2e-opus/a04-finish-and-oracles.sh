#!/usr/bin/env bash
# Part 2: spot checks of the rest of the finish, one product mutation per scenario oracle,
# and every canary run for its planted reason.
source "$(dirname "$0")/lib.sh"
C=P2

# Finish items.
mutation_check $C-discards a04-health-discards-zero p2a-health-discards-zero.patch '^TestE2PartitionAndHealS$|^TestE8LocalFirstMetadataSyncS$' 'Health.Discards=0'
mutation_check $C-settle a04-get-error-misclassified p2b-get-error-misclassified.patch '^TestE7NemesisHealingCyclesS$|^FuzzClusterNemesis$' 'disallowed error'
GOTAGS="-tags e2efault" GOPKG=./internal/noderuntime mutation_check $C-blocklist a04-blocklist-skips-equal-count p2c-blocklist-skips-equal-size.patch 'Blocklist' 'equal-size rewrite|missed'
ENVS="-e E2E_TIER_R=1" mutation_check $C-startup a04-startup-ignores-exited-node p2i-startup-ignores-exited-node.patch '^TestTierRStartupFailsOnPlantedLaunchFailure$' 'exited|status|planted|not ready' -timeout 15m
# s.Err() versus s.Verify(): read the code.
if awk '/^func FuzzClusterNemesis/,/^}/' "$ROOT/test/e2e/e7_test.go" | grep -q 's.Verify()' && grep -q 's.Verify()' <(awk '/^func checkFuzzCorpus/,/^}/' "$ROOT/test/e2e/e7_test.go"); then
  held $C-verify a04-fuzz-uses-verify "FuzzClusterNemesis and checkFuzzCorpus end with s.Verify() (invariant run counts included), not s.Err() (read, not run)"
else
  broken $C-verify a04-fuzz-uses-verify "FuzzClusterNemesis or checkFuzzCorpus ends with s.Err() instead of s.Verify()"
fi
# E4 object sizes: a multi-stripe object on every tier-S seed (read).
if grep -A14 'func TestE4MigrationUnderTrafficS' "$ROOT/test/e2e/tiers_test.go" | grep -q 'multiStripeSize: simStripe'; then
  held $C-e4size a04-e4-multistripe "TestE4MigrationUnderTrafficS writes a two-stripe object on every seed (multiStripeSize: simStripe+5; read, not run)"
else
  broken $C-e4size a04-e4-multistripe "TestE4MigrationUnderTrafficS no longer writes a multi-stripe object"
fi

# One product mutation per scenario oracle.
mutation_check $C-ledger   a04-oracle-ledger-flipped-byte p2h-get-flips-a-byte-sometimes.patch '^TestE1MultiRegionPutGetS$' 'wrong bytes|no client ever wrote|other than the ones written|hash to the version'
mutation_check $C-spread   a04-oracle-spread-cap-ignored p2d-spread-cap-ignored.patch '^TestE1MultiRegionPutGetS$' 'more than|region|spread'
mutation_check $C-orphan   a04-oracle-orphan-collect-noop p2e-collect-never-deletes.patch '^TestE2PartitionAndHealS$|^TestE6CrashRestartWipeS$' 'stray|orphan|expected by|no live manifest'
ENVS="-e DST_SEEDS=20" mutation_check $C-loss     a04-oracle-loss-declared-early p2f-loss-declared-on-one-absence.patch '^TestE6CrashRestartWipeS$|^TestE3BitRotRepairS$|^TestE7NemesisHealingCyclesS$|^TestFuzzClusterNemesisCorpusMovesShards$' 'declared lost|lost'
mutation_check $C-lineage  a04-oracle-ack-tag-not-written p2g-minority-tag-acked-not-written.patch '^TestE2PartitionAndHealS$|^TestE8LocalFirstMetadataSyncS$' 'discard|current|lineage|acknowledged'
mutation_check $C-deletion a04-oracle-deletion-gc-live p2j-gc-collects-some-live-shards.patch '^TestE2PartitionAndHealS$' 'delet|winner|unreadable|lost'

# Every tier-S canary: passes (= caught its planted fault) and logs the reason it matched.
d=$(mkscratch); log=$EVID/a04-canaries.log
indocker "$d" "$log" "go test ./test/e2e -count=1 -v -run 'TestCanary|TestExpectCanaryFailureRejectsEmptyMatch'"; rc=$?
for t in $(grep -oE '^=== RUN   TestCanary[A-Za-z0-9]+$' "$log" | awk '{print $3}' | sort -u); do
  if grep -qE "^--- SKIP: $t " "$log"; then
    continue  # tier R canary: run with E2E_TIER_R=1 in a05
  elif grep -qE "^--- PASS: $t " "$log"; then
    why=$(awk "/^=== RUN   $t/{f=1} f&&/caught:/{print; exit}" "$log" | sed 's/.*caught: //' | cut -c1-160)
    held $C-canary a04-$t "passes, i.e. the scenario failed with its planted reason (${why:-matched in a subtest})"
  else
    broken $C-canary a04-$t "canary failed: $(grep -A3 -E "^--- FAIL: $t" "$log" | grep -m1 '_test.go' | tr -s ' ' | cut -c1-200)"
  fi
done
[ $rc != 0 ] && ! grep -q -- '--- FAIL: TestCanary' "$log" && setup $C-canary a04-canaries "canary run failed outside a canary (evidence/a04-canaries.log)"
rm -rf "$d"
finish
