#!/bin/bash
# Wait for a launched stage to stop. Usage: wait.sh <run-name> <deadline-seconds>
# Exit 0 when the exit file says 0; 1 on a nonzero exit; 3 when the process died with no exit
# file; 4 on deadline. Always prints the last log lines so the caller can diagnose.
set -u
[ $# -ge 1 ] && [ -n "$1" ] || { echo "usage: wait.sh <run-name> [deadline-seconds]" >&2; exit 2; }
RUNS=${RUNS:-/root/runs}
base="$RUNS/objstore-p2-$1"; deadline=$(( $(date +%s) + ${2:-14400} ))
[ -f "$base.pid" ] || [ -f "$base.exit" ] || { echo "WAIT $1: no pid file and no exit file — nothing was launched under this name"; exit 3; }
pid=$(cat "$base.pid" 2>/dev/null || true)
while :; do
  if [ -f "$base.exit" ]; then rc=$(cat "$base.exit"); echo "WAIT $1: exit=$rc"; tail -c 1500 "$base.log"; [ "$rc" = 0 ]; exit $?; fi
  if [ -n "$pid" ] && ! kill -0 "$pid" 2>/dev/null; then sleep 3; [ -f "$base.exit" ] && continue; echo "WAIT $1: died, no exit file"; tail -c 1500 "$base.log"; exit 3; fi
  [ "$(date +%s)" -ge "$deadline" ] && { echo "WAIT $1: deadline"; tail -c 800 "$base.log"; exit 4; }
  sleep 20
done
