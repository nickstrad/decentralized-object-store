#!/usr/bin/env bash
set -u
base=agent_work/adversarial/03-merkle
mkdir -p "$base/evidence"
: > "$base/evidence/run-all.txt"
setup=0
start=$SECONDS
for driver in "$base"/a[0-9][0-9]-*.sh; do
  bash "$driver" > "$base/evidence/$(basename "$driver" .sh).txt"
  rc=$?
  cat "$base/evidence/$(basename "$driver" .sh).txt" | tee -a "$base/evidence/run-all.txt"
  if (( rc > 1 )); then setup=$((setup+1)); fi
done
held=$(awk '/^HELD / {n++} END {print n+0}' "$base/evidence/run-all.txt")
broken=$(awk '/^BROKEN / {n++} END {print n+0}' "$base/evidence/run-all.txt")
echo "SUMMARY held=$held broken=$broken setup_errors=$setup elapsed_seconds=$((SECONDS-start))" | tee -a "$base/evidence/run-all.txt"
if (( setup )); then exit 2; fi
if (( broken )); then exit 1; fi
exit 0
