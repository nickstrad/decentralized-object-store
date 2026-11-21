#!/bin/bash
# Wait until ANY of the named stages stops. Usage: waitany.sh <deadline-seconds> <run-name>...
# Prints "DONE <name> exit=<rc>" for every stage that has an exit file, or "DIED <name>" for
# one whose process is gone with no exit file, then exits 0. Exit 4 on deadline.
set -u
RUNS=${RUNS:-/root/runs}; deadline=$(( $(date +%s) + $1 )); shift
while :; do
  hit=0
  for n in "$@"; do
    b="$RUNS/objstore-p2-$n"
    if [ -f "$b.exit" ]; then echo "DONE $n exit=$(cat "$b.exit")"; hit=1
    elif ! kill -0 "$(cat "$b.pid" 2>/dev/null)" 2>/dev/null; then
      sleep 5; if [ -f "$b.exit" ]; then echo "DONE $n exit=$(cat "$b.exit")"; else echo "DIED $n"; fi; hit=1
    fi
  done
  [ $hit = 1 ] && exit 0
  [ "$(date +%s)" -ge "$deadline" ] && { echo "DEADLINE"; exit 4; }
  sleep 20
done
