#!/usr/bin/env bash
# Runs inside the dev container: start a tier R test, SIGKILL (or SIGINT) the test binary once the
# cluster is up, then report stray objnode processes and leftover temp directories.
set -u
sig=${1:-KILL}
export E2E_TIER_R=1
go test -c -o /tmp/e2e.test ./test/e2e || { echo "SETUPFAIL build"; exit 2; }
before=$(ls -d /tmp/e2e-* 2>/dev/null | wc -l)
(cd test/e2e && exec /tmp/e2e.test -test.run '^TestE2PartitionAndHealR$' -test.v -test.timeout 10m >/tmp/r.log 2>&1) &
for i in $(seq 1 240); do
  n=$(pgrep -c -x objnode 2>/dev/null || true)
  [ "${n:-0}" -ge 9 ] && break
  sleep 1
done
n=$(pgrep -c -x objnode 2>/dev/null || true)
echo "objnode_before_kill=${n:-0}"
[ "${n:-0}" -ge 9 ] || { echo "SETUPFAIL cluster never came up"; tail -5 /tmp/r.log; exit 2; }
sleep 5
tpid=$(pgrep -f '/tmp/e2e.test' | head -1)
kill -$sig "$tpid"
for i in $(seq 1 30); do
  pgrep -f '/tmp/e2e.test' >/dev/null || break
  sleep 1
done
sleep 3
echo "testbin_alive=$(pgrep -c -f '/tmp/e2e.test' || true)"
echo "objnode_after_kill=$(pgrep -c -x objnode || true)"
echo "tempdirs_left=$(( $(ls -d /tmp/e2e-* 2>/dev/null | wc -l) - before ))"
ls -d /tmp/e2e-* 2>/dev/null | sed 's/^/left: /'
ss -ltnup 2>/dev/null | grep -c objnode | sed 's/^/listening_sockets=/'
