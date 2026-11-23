#!/usr/bin/env bash
# Part 1.3: partition-time reads and writes in E2, on tier S and tier R.
source "$(dirname "$0")/lib.sh"
C=P1.3
S='^TestE2PartitionAndHealS$'
R='^TestE2PartitionAndHealR$'
WANT='must succeed|want unavailable|wrong bytes|no client ever wrote|other than the ones written|reads returned wrong'

mutation_check $C a03-S-get-put-unavailable-split p13a-get-and-put-unavailable-while-split.patch "$S" "$WANT"
mutation_check $C a03-S-get-unavailable-split    p13b-get-unavailable-while-split.patch "$S" "$WANT"
mutation_check $C a03-S-put-unavailable-split    p13c-put-unavailable-while-split.patch "$S" "$WANT"
mutation_check $C a03-S-minority-unavailable     p13d-minority-side-unavailable.patch "$S" "$WANT"
mutation_check $C a03-S-majority-unavailable     p13e-majority-side-unavailable.patch "$S" "$WANT"
mutation_check $C a03-S-pre-cut-keys-unavailable p13f-get-unavailable-for-pre-cut-keys.patch "$S" "$WANT" -parallel 1
mutation_check $C a03-S-wrong-bytes-split        p13i-wrong-bytes-while-split.patch "$S" "$WANT"
mutation_check $C a03-S-stale-read-split p13h-stale-read-while-split.patch "$S" "$WANT|stale" -parallel 1
# Tier R (real objnode processes; one heavy run at a time).
ENVS="-e E2E_TIER_R=1" mutation_check $C a03-R-majority-unavailable p13e-majority-side-unavailable.patch "$R" "$WANT" -timeout 20m
ENVS="-e E2E_TIER_R=1" mutation_check $C a03-R-tier-r-only-unavailable p13g-tier-r-only-unavailable-while-split.patch "$R" "$WANT" -timeout 20m
ENVS="-e E2E_TIER_R=1" mutation_check $C a03-R-wrong-bytes-split p13i-wrong-bytes-while-split.patch "$R" "$WANT" -timeout 20m
# Minority side only, tier R: 3 nodes < k+m = 6 and at most m = 2 shards per stripe there, so E2
# already requires ErrUnavailable from every minority call; survival here is the specified
# behaviour, not a hole (tier S, where the minority can write, kills it above).
d=$(mkscratch); apply_patch "$d" p13d-minority-side-unavailable.patch; log=$EVID/a03-R-minority-unavailable.log
ENVS="-e E2E_TIER_R=1" indocker "$d" "$log" "go test ./test/e2e -count=1 -v -run '$R' -timeout 20m"; rc=$?
v=$(classify "$log" $rc "$WANT")
case $v in
  SURVIVED) held $C a03-R-minority-unavailable "minority-only unavailability survives tier R as specified: there E2 requires unavailable from the 3-node minority anyway (post_puts_required=$(grep -oE 'e2r/post_puts_required [0-9]+' "$log" | awk '{print $2}'), all on the majority); tier S kills it";;
  KILLED|OTHERFAIL) held $C a03-R-minority-unavailable "minority-only unavailability killed on tier R: $(firstfail "$log")";;
  *) setup $C a03-R-minority-unavailable "setup error (evidence/a03-R-minority-unavailable.log)";;
esac
rm -rf "$d"

# Vacuity: on each tier, which sides carry a must-succeed read and write (measured, unmutated).
d=$(mkscratch)
log=$EVID/a03-vacuity.log
ENVS="-e E2E_TIER_R=1 -e DST_SEEDS=12" indocker "$d" "$log" "go test ./test/e2e -count=1 -v -run '$S|$R' -timeout 20m"; rc=$?
if [ $rc != 0 ]; then
  broken $C a03-vacuity "unmutated E2 failed on S or R: $(firstfail "$log")"
else
  mS=$(grep -E 'MEASURE e2/(post_puts_required|post_gets_required|early_gets_required) ' "$log" | sed 's/.*MEASURE e2\///' | tr -s ' ' | tr '\n' ';')
  mR=$(grep -E 'MEASURE e2r/(post_puts_required|post_gets_required|early_gets_required|puts_unavailable) ' "$log" | sed 's/.*MEASURE e2r\///' | tr '\n' ';')
  [ -n "$mS" ] && held $C a03-vacuity-S "E2 tier S (DST_SEEDS=12) must-succeed counts never zero: $mS minority (7 nodes) has required writes, never required reads (it holds <= m=2 shards per stripe)" || setup $C a03-vacuity-S "no tier S measures"
  [ -n "$mR" ] && held $C a03-vacuity-R "E2 tier R must-succeed counts: $mR all required successes are on the 6-node majority; the 3-node minority can neither read nor write by layout" || setup $C a03-vacuity-R "no tier R measures"
fi
rm -rf "$d"
finish
