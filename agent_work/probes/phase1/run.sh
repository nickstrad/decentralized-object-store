#!/bin/bash
# Phase 1 feasibility probes for PLAN.md claims. Usage: agent_work/probes/phase1/run.sh
# 1 sim determinism: same seed -> same digest across separate processes; different seeds differ;
#   the -leak variant (map iteration) is NOT reproducible.
# 2 toolchain: fuzz short run, -race, testing/synctest, GOOS=darwin cross-build.
cd "$(dirname "$0")" || exit 2
fail=0
go version
go build -o /tmp/objstore-simdet ./simdet || exit 1
a=$(/tmp/objstore-simdet -seed 42); b=$(/tmp/objstore-simdet -seed 42); c=$(/tmp/objstore-simdet -seed 43)
echo "seed42 run1: $a"; echo "seed42 run2: $b"; echo "seed43     : $c"
[ "$a" = "$b" ] && echo "PASS same seed, separate processes, identical digest" || { echo "FAIL replay"; fail=1; }
[ "${a#* }" != "${c#* }" ] && echo "PASS different seed, different digest" || { echo "FAIL seeds collide"; fail=1; }
same=0; for i in 1 2 3 4 5 6; do /tmp/objstore-simdet -seed 7 -nodes 100 >> /tmp/objstore-simdet.100; done
[ "$(sort -u /tmp/objstore-simdet.100 | wc -l)" = 1 ] && echo "PASS 100 nodes x 6 runs identical: $(head -1 /tmp/objstore-simdet.100)" || { echo "FAIL 100-node replay"; fail=1; }
rm -f /tmp/objstore-simdet.100
l=$(for i in 1 2 3 4 5 6; do /tmp/objstore-simdet -seed 42 -leak; done | sort -u | wc -l)
[ "$l" -gt 1 ] && echo "PASS -leak (map iteration) gives $l distinct digests in 6 runs: the leak is real and the run-twice check catches it" || echo "NOTE -leak happened to agree in 6 runs"
/usr/bin/time -f "200k events, 50 nodes: %es wall" /tmp/objstore-simdet -seed 1 > /dev/null
go test -race -count=1 ./toolchain && echo "PASS -race + synctest" || fail=1
go test ./toolchain -run '^$' -fuzz FuzzProbeRoundTrip -fuzztime 5s -fuzzminimizetime 2s | tail -2 && echo "PASS fuzz short run" || fail=1
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./simdet && GOOS=darwin GOARCH=arm64 go vet ./... && echo "PASS darwin/arm64 cross-build + vet" || fail=1
rm -f /tmp/objstore-simdet
exit $fail
