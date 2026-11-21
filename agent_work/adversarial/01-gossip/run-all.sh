#!/usr/bin/env bash
set -u
base=agent_work/adversarial/01-gossip
mkdir -p "$base/evidence"
: > "$base/evidence/run-all.txt"
setup=0
start=$SECONDS
for driver in "$base"/a[0-9][0-9]-*.sh; do
 bash "$driver" >> "$base/evidence/run-all.txt"
 rc=$?
 if ((rc>1)); then setup=$((setup+1)); fi
done
cat "$base/evidence/run-all.txt"
held=$(awk '/^HELD /{n++}END{print n+0}' "$base/evidence/run-all.txt")
broken=$(awk '/^BROKEN /{n++}END{print n+0}' "$base/evidence/run-all.txt")
printf 'SUMMARY held=%d broken=%d setup_errors=%d elapsed_seconds=%d\n' "$held" "$broken" "$setup" "$((SECONDS-start))" | tee -a "$base/evidence/run-all.txt"
if ((setup)); then exit 2; fi
if ((broken)); then exit 1; fi
