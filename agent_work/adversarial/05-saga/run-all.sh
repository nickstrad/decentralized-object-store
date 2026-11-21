#!/usr/bin/env bash
set -u
base=agent_work/adversarial/05-saga
mkdir -p "$base/evidence"
: > "$base/evidence/run-all.txt"
setup=0
start=$SECONDS
for driver in "$base"/a[0-9][0-9]-*.sh; do
 bash "$driver" >> "$base/evidence/run-all.txt"
 rc=$?
 if [ "$rc" -gt 1 ]; then setup=$((setup+1)); fi
done
held=$(grep -c '^HELD ' "$base/evidence/run-all.txt" || true)
broken=$(grep -c '^BROKEN ' "$base/evidence/run-all.txt" || true)
summary="SUMMARY held=$held broken=$broken setup_errors=$setup elapsed_seconds=$((SECONDS-start))"
printf '%s\n' "$summary" >> "$base/evidence/run-all.txt"
cat "$base/evidence/run-all.txt"
if [ "$setup" -gt 0 ]; then exit 2; fi
if [ "$broken" -gt 0 ]; then exit 1; fi
