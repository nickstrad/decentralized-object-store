#!/usr/bin/env bash
# Isolate Compose: compose.yaml otherwise makes different clones share one project.
export COMPOSE_PROJECT_NAME=objstore-walk
set -u
root=$(cd "$(dirname "$0")/../../.." && pwd)
out="$root/agent_work/adversarial/08-book"
clone="$out/clean-clone"
if [ -e "$clone" ]; then echo "Refusing to overwrite $clone" >&2; exit 2; fi
if [ -n "$(docker ps -aq --filter label=com.docker.compose.project=$COMPOSE_PROJECT_NAME)" ]; then
 echo "Refusing to reuse an occupied Compose project" >&2; exit 2
fi
git clone --no-hardlinks "$root" "$clone" >"$out/clone.log" 2>&1 || exit $?
cd "$clone" || exit 2
cleanup() {
 if [ -f "$clone/agent_work/ci/gates/result.json" ]; then cp "$clone/agent_work/ci/gates/result.json" "$out/ci-result.json"; fi
 docker compose --profile cluster down >"$out/cleanup.log" 2>&1
 cd "$root" || return
 rm -rf -- "$clone"
}
trap cleanup EXIT
printf 'step\tsource\tline\texit\tseconds\tcommand\n' >"$out/steps.tsv"
failures=0
step() {
 local n="$1" source="$2" line="$3" cmd="$4" begin=$SECONDS rc
 # Only reader polling loops get a 180-second guard; commands are otherwise literal.
 if [[ "$cmd" == until* ]]; then
  export A C
  timeout 180 bash -c "$cmd" >"$out/step-$n.log" 2>&1
  rc=$?
 else
  eval "$cmd" >"$out/step-$n.log" 2>&1
  rc=$?
 fi
 printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$n" "$source" "$line" "$rc" "$((SECONDS-begin))" "$cmd" >>"$out/steps.tsv"
 if [ "$rc" -eq 0 ]; then printf 'PASS'; else printf 'FAIL'; failures=$((failures+1)); fi
 printf ' %s %s:%s (%ss, exit %s) %s\n' "$n" "$source" "$line" "$((SECONDS-begin))" "$rc" "$cmd"
}
step 1 README.md 41 'docker compose build dev'
step 2 README.md 42 'docker compose run --rm dev go test ./...'
step 3 README.md 48 agent_work/gates/ci-fast.sh
step 4 README.md 74 'docker compose run --rm dev go test ./test/e2e -run '"'"'^TestE7NemesisHealingCyclesS$/^shard=1$/^seed=1$'"'"' -seed=1 -count=1'
step 5 README.md 75 'docker compose run --rm dev go run ./cmd/objsim -scenario toy-chaos-100 -seed 3'
step 6 README.md 83 'docker compose --profile cluster up -d node-a1 node-b1 node-c1'
step 7 README.md 84 'A=$(docker compose --profile cluster port node-a1 8080)'
step 8 README.md 85 'C=$(docker compose --profile cluster port node-c1 8080)'
step 9 README.md 86 'until curl -sf "http://$A/_health" | grep '"'"'"Ready":true,"LiveMembers":3'"'"' >/dev/null; do sleep 1; done'
step 10 README.md 87 'curl -sf -X PUT "http://$A/demo"'
step 11 README.md 88 echo
step 12 README.md 89 'curl -sf -X PUT --data-binary "hello from region a" "http://$A/demo/hello.txt"'
step 13 README.md 90 echo
step 14 README.md 91 'until curl -sf "http://$C/demo/hello.txt"; do sleep 1; done'
step 15 README.md 92 echo
step 16 README.md 93 'docker compose --profile cluster down'
step 17 docs/index.md 34 'docker compose build dev'
step 18 docs/index.md 40 'docker compose run --rm dev go test ./...'
step 19 docs/index.md 46 'docker compose run --rm dev go run ./cmd/objsim -scenario toy-chaos-100 -seed 3'
step 20 book/chapters/01-deterministic-simulation.md 236 'docker compose run --rm dev go test ./internal/sim -run '"'"'^TestReplaySameProcess$'"'"' -seed=3 -count=1'
step 21 book/chapters/01-deterministic-simulation.md 237 'docker compose run --rm dev go run ./cmd/objsim -scenario toy-chaos-100 -seed 3'
step 22 book/chapters/02-swim-gossip.md 266 'docker compose run --rm dev go test ./internal/gossip -run '"'"'^TestSWIMConvergenceDST$/^N=20$/^shard=1$/^seed=33$'"'"' -seed=33 -count=1'
step 23 book/chapters/02-swim-gossip.md 267 'docker compose run --rm dev go test ./internal/gossip -run '"'"'^TestSWIMAsymmetricLinkDST$|^TestCanaryNoIndirectProbe$'"'"' -seed=33 -v -count=1'
step 24 book/chapters/03-reed-solomon-erasure-coding.md 260 'docker compose run --rm dev go test ./internal/storage -run '"'"'^TestStripedPutGetDST$'"'"' -seed=987691 -v -count=1'
step 25 book/chapters/03-reed-solomon-erasure-coding.md 261 'docker compose run --rm dev go test ./internal/erasure -run '"'"'^$'"'"' -bench . -benchmem'
step 26 book/chapters/04-merkle-anti-entropy.md 271 'docker compose run --rm dev go test ./internal/merkle -run '"'"'^TestBitRotRepairDST$'"'"' -seed=66014753249261 -v -count=1'
step 27 book/chapters/04-merkle-anti-entropy.md 272 'docker compose run --rm -e MERKLE_MEASURE=1 dev go test ./internal/merkle -run '"'"'^TestMeasureDiffCost$'"'"' -v -count=1'
step 28 book/chapters/05-optimistic-concurrency-control.md 244 'docker compose run --rm dev go test ./internal/occ -run '"'"'^TestMultiRegionConvergenceDST$'"'"' -seed=1 -v -count=1'
step 29 book/chapters/05-optimistic-concurrency-control.md 245 'docker compose run --rm dev go test ./internal/occ -run '"'"'^$'"'"' -bench BenchmarkMergeDoc -benchmem -benchtime 20x'
step 30 book/chapters/06-sagas.md 267 'docker compose run --rm dev go test ./internal/saga -run '"'"'^TestMigrationFailurePointsDST$'"'"' -seed=1 -count=1'
step 31 book/chapters/06-sagas.md 268 'docker compose run --rm dev go test ./internal/saga -run '"'"'^TestMigrationUnderNemesisDST$'"'"' -seed=13 -count=1'
step 32 book/chapters/07-object-store-and-sync-engine.md 247 'docker compose run --rm dev go test ./internal/noderuntime -run '"'"'^TestConformance'"'"' -v -count=1'
step 33 book/chapters/07-object-store-and-sync-engine.md 248 'docker compose run --rm dev go test ./internal/node -run '"'"'^TestClusterPartitionHealDST$'"'"' -seed=1 -v -count=1'
step 34 book/chapters/08-end-to-end-and-hardening.md 252 'docker compose run --rm dev go test -short -count=1 ./test/e2e'
step 35 book/chapters/08-end-to-end-and-hardening.md 253 'docker compose run --rm -e E2E_TIER_R=1 dev go test ./test/e2e -run '"'"'R$|TierR|DefaultBuild|CanaryE9'"'"' -count=1 -v -timeout 40m'
step 36 book/chapters/08-end-to-end-and-hardening.md 254 'docker compose run --rm dev go test ./test/e2e -run '"'"'^$'"'"' -fuzz '"'"'^FuzzClusterNemesis$'"'"' -fuzztime 120s -fuzzminimizetime 5s'
echo "Walk finished; PASS means exit zero; semantic findings are in FINDINGS.md."

[ "$failures" -eq 0 ]
