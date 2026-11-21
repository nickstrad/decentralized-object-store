#!/usr/bin/env bash
set -u
base=agent_work/adversarial/02-erasure
mkdir -p "$base/evidence"
: > "$base/evidence/run-all.txt"
setup=0
failed=0
start=$SECONDS
for driver in "$base"/a[0-9][0-9]-*.sh; do
 "$driver" > "$base/evidence/driver-result.txt"
 rc=$?
 cat "$base/evidence/driver-result.txt" | tee -a "$base/evidence/run-all.txt"
 if ((rc==2)); then setup=$((setup+1)); elif ((rc!=0)); then failed=1; fi
done
rm -f "$base/evidence/driver-result.txt"
held=$(grep -c '^HELD ' "$base/evidence/run-all.txt" || true)
broken=$(grep -c '^BROKEN ' "$base/evidence/run-all.txt" || true)
echo "SUMMARY held=$held broken=$broken setup_errors=$setup elapsed_seconds=$((SECONDS-start))" | tee -a "$base/evidence/run-all.txt"
if ((setup>0)); then exit 2; fi
if ((broken>0 || failed>0)); then exit 1; fi
