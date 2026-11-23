# Executed walkthrough

Commands are literal source commands, with the documented project override and polling guards. Elapsed times include container startup. See FINDINGS.md for prerequisites and safety issues despite successful execution.

| Step | Source | Command | Exit | Seconds | Output versus text |
|---|---|---|---|---|---|
| 1 | README.md:41 | `docker compose build dev` | 0 | 2 | Development image built. See step-1.log. |
| 2 | README.md:42 | `docker compose run --rm dev go test ./...` | 0 | 376 | Whole default test suite passed; Tier R remains opt-in. See step-2.log. |
| 3 | README.md:48 | `agent_work/gates/ci-fast.sh` | 0 | 177 | G0, G1 short/coverage, G6 passed; explicitly not a landing gate. See step-3.log. |
| 4 | README.md:74 | `docker compose run --rm dev go test ./test/e2e -run '^TestE7NemesisHealingCyclesS$/^shard=1$/^seed=1$' -seed=1 -count=1` | 0 | 11 | Pinned E7 seed passed. See step-4.log. |
| 5 | README.md:75 | `docker compose run --rm dev go run ./cmd/objsim -scenario toy-chaos-100 -seed 3` | 0 | 1 | Toy chaos completed and printed events/state SHA-256 digests. See step-5.log. |
| 6 | README.md:83 | `docker compose --profile cluster up -d node-a1 node-b1 node-c1` | 0 | 2 | All three cluster services started. See step-6.log. |
| 7 | README.md:84 | `A=$(docker compose --profile cluster port node-a1 8080)` | 0 | 0 | Resolved node-a1 loopback host/port into A. See step-7.log. |
| 8 | README.md:85 | `C=$(docker compose --profile cluster port node-c1 8080)` | 0 | 0 | Resolved node-c1 loopback host/port into C. See step-8.log. |
| 9 | README.md:86 | `until curl -sf "http://$A/_health" \| grep '"Ready":true,"LiveMembers":3' >/dev/null; do sleep 1; done` | 0 | 1 | Health reached Ready=true and LiveMembers=3. See step-9.log. |
| 10 | README.md:87 | `curl -sf -X PUT "http://$A/demo"` | 0 | 0 | Bucket PUT returned successfully. See step-10.log. |
| 11 | README.md:88 | `echo` | 0 | 0 | Printed newline. See step-11.log. |
| 12 | README.md:89 | `curl -sf -X PUT --data-binary "hello from region a" "http://$A/demo/hello.txt"` | 0 | 0 | Object PUT returned successfully. See step-12.log. |
| 13 | README.md:90 | `echo` | 0 | 0 | Printed newline. See step-13.log. |
| 14 | README.md:91 | `until curl -sf "http://$C/demo/hello.txt"; do sleep 1; done` | 0 | 0 | Cross-region GET returned exactly hello from region a. See step-14.log. |
| 15 | README.md:92 | `echo` | 0 | 0 | Printed newline. See step-15.log. |
| 16 | README.md:93 | `docker compose --profile cluster down` | 0 | 2 | Audit cluster stopped. See step-16.log. |
| 17 | docs/index.md:34 | `docker compose build dev` | 0 | 3 | Development image built. See step-17.log. |
| 18 | docs/index.md:40 | `docker compose run --rm dev go test ./...` | 0 | 3 | Whole default test suite passed (cached results permitted). See step-18.log. |
| 19 | docs/index.md:46 | `docker compose run --rm dev go run ./cmd/objsim -scenario toy-chaos-100 -seed 3` | 0 | 2 | Toy chaos printed the same seed-3 digests as step 5. See step-19.log. |
| 20 | book/chapters/01-deterministic-simulation.md:236 | `docker compose run --rm dev go test ./internal/sim -run '^TestReplaySameProcess$' -seed=3 -count=1` | 0 | 3 | Pinned deterministic replay test passed. See step-20.log. |
| 21 | book/chapters/01-deterministic-simulation.md:237 | `docker compose run --rm dev go run ./cmd/objsim -scenario toy-chaos-100 -seed 3` | 0 | 2 | Toy chaos printed the same seed-3 digests as steps 5 and 19. See step-21.log. |
| 22 | book/chapters/02-swim-gossip.md:266 | `docker compose run --rm dev go test ./internal/gossip -run '^TestSWIMConvergenceDST$/^N=20$/^shard=1$/^seed=33$' -seed=33 -count=1` | 0 | 3 | Pinned 20-node convergence case passed. See step-22.log. |
| 23 | book/chapters/02-swim-gossip.md:267 | `docker compose run --rm dev go test ./internal/gossip -run '^TestSWIMAsymmetricLinkDST$\|^TestCanaryNoIndirectProbe$' -seed=33 -v -count=1` | 0 | 2 | Asymmetric-link and indirect-probe canary tests passed. See step-23.log. |
| 24 | book/chapters/03-reed-solomon-erasure-coding.md:260 | `docker compose run --rm dev go test ./internal/storage -run '^TestStripedPutGetDST$' -seed=987691 -v -count=1` | 0 | 3 | Pinned striped PUT/GET test passed. See step-24.log. |
| 25 | book/chapters/03-reed-solomon-erasure-coding.md:261 | `docker compose run --rm dev go test ./internal/erasure -run '^$' -bench . -benchmem` | 0 | 6 | Erasure benchmarks emitted timings and allocation counts. See step-25.log. |
| 26 | book/chapters/04-merkle-anti-entropy.md:271 | `docker compose run --rm dev go test ./internal/merkle -run '^TestBitRotRepairDST$' -seed=66014753249261 -v -count=1` | 0 | 2 | Pinned bit-rot repair test passed. See step-26.log. |
| 27 | book/chapters/04-merkle-anti-entropy.md:272 | `docker compose run --rm -e MERKLE_MEASURE=1 dev go test ./internal/merkle -run '^TestMeasureDiffCost$' -v -count=1` | 0 | 3 | Merkle measurement test emitted diff-cost data and passed. See step-27.log. |
| 28 | book/chapters/05-optimistic-concurrency-control.md:244 | `docker compose run --rm dev go test ./internal/occ -run '^TestMultiRegionConvergenceDST$' -seed=1 -v -count=1` | 0 | 2 | Pinned multi-region convergence test passed. See step-28.log. |
| 29 | book/chapters/05-optimistic-concurrency-control.md:245 | `docker compose run --rm dev go test ./internal/occ -run '^$' -bench BenchmarkMergeDoc -benchmem -benchtime 20x` | 0 | 6 | MergeDoc benchmark emitted timings and allocation counts. See step-29.log. |
| 30 | book/chapters/06-sagas.md:267 | `docker compose run --rm dev go test ./internal/saga -run '^TestMigrationFailurePointsDST$' -seed=1 -count=1` | 0 | 3 | Pinned migration failure-point test passed. See step-30.log. |
| 31 | book/chapters/06-sagas.md:268 | `docker compose run --rm dev go test ./internal/saga -run '^TestMigrationUnderNemesisDST$' -seed=13 -count=1` | 0 | 3 | Pinned migration nemesis test passed. See step-31.log. |
| 32 | book/chapters/07-object-store-and-sync-engine.md:247 | `docker compose run --rm dev go test ./internal/noderuntime -run '^TestConformance' -v -count=1` | 0 | 3 | Both matching runtime conformance tests passed. See step-32.log. |
| 33 | book/chapters/07-object-store-and-sync-engine.md:248 | `docker compose run --rm dev go test ./internal/node -run '^TestClusterPartitionHealDST$' -seed=1 -v -count=1` | 0 | 4 | Pinned integrated partition/heal test passed. See step-33.log. |
| 34 | book/chapters/08-end-to-end-and-hardening.md:252 | `docker compose run --rm dev go test -short -count=1 ./test/e2e` | 0 | 94 | Short end-to-end suite passed. See step-34.log. |
| 35 | book/chapters/08-end-to-end-and-hardening.md:253 | `docker compose run --rm -e E2E_TIER_R=1 dev go test ./test/e2e -run 'R$\|TierR\|DefaultBuild\|CanaryE9' -count=1 -v -timeout 40m` | 0 | 249 | Opt-in Tier R / default-build / E9-canary selection passed. See step-35.log. |
| 36 | book/chapters/08-end-to-end-and-hardening.md:254 | `docker compose run --rm dev go test ./test/e2e -run '^$' -fuzz '^FuzzClusterNemesis$' -fuzztime 120s -fuzzminimizetime 5s` | 0 | 125 | 120-second cluster nemesis fuzz run passed with no crasher. See step-36.log. |

Total sequential command time: 1093 seconds.
