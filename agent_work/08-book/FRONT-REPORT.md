# Front matter, README, and site front page report

## Files created and edited

- `book/front/preface.md` (new, 382 words; target 300–500)
- `book/front/topic-map.md` (new, 201 words)
- `README.md` (edited, 878 words; target 600–900, down from 898)
- `docs/index.md` (edited, 285 words; target 250–400)
- `book/chapters/01-deterministic-simulation.md` (one-sentence `detlint` scope update, 2,571 words; within 2,600)
- `agent_work/08-book/FRONT-REPORT.md` (this report)

## Determinism lint (`book/chapters/01-deterministic-simulation.md` addendum)

Ran `detlint` inside `dev` using the exact package-discovery logic from `agent_work/gates/gate.sh` (`detlint_dirs` + `g0_detlint`), with `COMPOSE_PROJECT_NAME=objstore-front`:

- Lint output line: `detlint: checked 72 files in 11 packages`
- Packages checked: `internal/env`, `internal/erasure`, `internal/gossip`, `internal/merkle`, `internal/node`, `internal/occ`, `internal/saga`, `internal/saga/sagasim`, `internal/sim`, `internal/sim/simtoy`, `internal/storage`
- Updated `book/chapters/01-deterministic-simulation.md` line 217 to `72 files in 11 packages` with no "future" or "today". Word count after edit: 2,571 words (limit 2,600).

## Site lint

Command (`COMPOSE_PROJECT_NAME=objstore-front`):

```sh
docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src
```

Result: exit `0` — `build_site: 21 pages into /tmp/site-lint-en13lsr3/site (front page, book index + 8 chapters + 0 front matter, c4 index + 9 diagrams + model.json, plan; 2 assets)`, `site-lint: ok (0 findings)`.

## README commands verified

All commands ran at the repository root with `COMPOSE_PROJECT_NAME=objstore-front` exported in the shell (never mentioned in `README.md`). Every ` ```sh ` block in `README.md` was also extracted and executed top to bottom in one `bash -euo pipefail` shell (exit `0`, 182 s total with Go test cache warm from the preceding uncached run).

| Block / Command | Fence | Result | Time |
| --- | --- | --- | --- |
| `docker compose build dev` | `sh` | pass (`objstore-dev:local` built) | 2 s (warm layers) |
| `docker compose run --rm dev go test ./...` | `sh` | pass (all packages ok; `test/e2e` 391.5 s uncached) | 423 s uncached / 3 s cached (with build) |
| `agent_work/gates/ci-fast.sh` | `sh` | pass (`overall=pass`: G0 all 9 checks pass, G1 pass 147 s, G6 pass; see note below) | 165 s |
| `GATE_STAGE=final agent_work/gates/gate.sh demo` | `text` | not executed (takes ~2.5 h on a 4-core machine; shown in `text` fence) | — |
| `docker compose run --rm dev go test ./test/e2e -run '^TestE7NemesisHealingCyclesS$/^shard=1$/^seed=1$' -seed=1 -count=1` | `sh` | pass (`ok test/e2e 5.886s`) | 11 s cold / 8 s warm |
| `docker compose run --rm dev go run ./cmd/objsim -scenario toy-chaos-100 -seed 3` | `sh` | pass (`steps=15717`, `events=e822492b...`, `state=ec1f7be5...`) | 2 s |
| Local cluster block (`docker compose --profile cluster up -d node-a1 node-b1 node-c1`, wait `/_health` `"Ready":true,"LiveMembers":3`, `PUT /demo`, `PUT /demo/hello.txt`, `GET /demo/hello.txt` on `node-c1` returning `hello from region a`, `docker compose --profile cluster down`) | `sh` | pass (exit `0`; zero leftover containers or networks) | 7 s standalone / 4 s in top-to-bottom run |

## Topic map test names verified

Every test and fuzz-target name in `book/front/topic-map.md` was verified with `grep -rn "func <Name>(" internal cmd test`:

| Row | Test / fuzz target | File and line |
| --- | --- | --- |
| 1 | `TestReplaySameProcess` | `internal/sim/meta_test.go:33` |
| 1 | `TestReplayAcrossProcesses` | `internal/sim/meta_test.go:50` |
| 1 | `TestPropNemesisRespectsBudget` | `internal/sim/prop_test.go:347` |
| 1 | `FuzzSimSchedule` | `internal/sim/fuzz_test.go:34` |
| 2 | `TestSWIMConvergenceDST` | `internal/gossip/dst_test.go:443` |
| 2 | `TestSWIMAsymmetricLinkDST` | `internal/gossip/dst_test.go:495` |
| 2 | `TestSWIMLongPartitionHealDST` | `internal/gossip/dst_test.go:1002` |
| 2 | `FuzzGossipWire` | `internal/gossip/fuzz_test.go:17` |
| 3 | `TestStripedPutGetDST` | `internal/storage/dst_test.go:249` |
| 3 | `TestPropChunkStoreModel` | `internal/storage/chunkstore_test.go:323` |
| 3 | `FuzzErasureCoding` | `internal/erasure/codec_test.go:533` |
| 3 | `FuzzShardDecode` | `internal/storage/shard_test.go:189` |
| 4 | `TestBitRotRepairDST` | `internal/merkle/dst_test.go:367` |
| 4 | `TestLyingPeerDST` | `internal/merkle/dst_test.go:605` |
| 4 | `FuzzMerkleDiff` | `internal/merkle/prop_test.go:188` |
| 4 | `FuzzMerkleWire` | `internal/merkle/wire_test.go:169` |
| 5 | `TestMultiRegionConvergenceDST` | `internal/occ/dst_test.go:476` |
| 5 | `TestOfflineRegionDST` | `internal/occ/dst_test.go:563` |
| 5 | `TestQuotaOvershootUnderPartitionDST` | `internal/occ/dst_test.go:841` |
| 5 | `FuzzOCCConditionalWrite` | `internal/occ/prop_test.go:252` |
| 6 | `TestMigrationFailurePointsDST` | `internal/saga/dst_test.go:41` |
| 6 | `TestMigrationUnderNemesisDST` | `internal/saga/dst_test.go:140` |
| 6 | `TestDuelingMigrationsDST` | `internal/saga/dst_test.go:165` |
| 6 | `FuzzSagaLogRecover` | `internal/saga/prop_test.go:531` |
| 7 | `TestClusterPartitionHealDST` | `internal/node/dst_partition_test.go:273` |
| 7 | `TestConformanceOnLoopback` | `internal/noderuntime/conformance_test.go:296` |
| 7 | `TestFileDiskCrashLeavesOldOrNewBytes` | `internal/noderuntime/crashfs_test.go:348` |
| 7 | `FuzzObjectAPI` | `internal/storage/httpapi/http_test.go:833` |
| 8 | `TestE2PartitionAndHealS` | `test/e2e/tiers_test.go:44` |
| 8 | `TestE7NemesisHealingCyclesS` | `test/e2e/e7_test.go:70` |
| 8 | `TestE9ShutdownUnderLoadR` | `test/e2e/tierr_test.go:529` |
| 8 | `FuzzClusterNemesis` | `test/e2e/e7_test.go:492` |

## What could not be verified and notes

- **Full gate (`GATE_STAGE=final agent_work/gates/gate.sh demo`):** Not executed (~2.5 h runtime; fenced as `text` in `README.md`). Its timing statement (~2.5 hours, ~8,400 s) and step list (G0, G5R, G1–G7) were verified directly against `agent_work/gates/gate.sh` and `agent_work/07-e2e/build-notes.md` section 16.
- **Rendered Pages links after `book/manifest.json` update:** `book/manifest.json` was not edited (orchestrator lists `book/front/preface.md` and `book/front/topic-map.md` there). Currently `site_lint.py` passes with 0 findings because `build_site.py` serves unlisted `book/front/*.md` files as assets; once listed under `front_matter`, `build_site.py` will rewrite the relative links in `docs/index.md` to `book/preface.html` and `book/topic-map.html`.
- **Intermittent `-short` flake observed during testing (no `.go` files edited):** On the first run of `agent_work/gates/ci-fast.sh` (178 s), G1 failed once in `internal/storage` (`TestCanaryChecksumSkipBreaksCorruptionProperty`: `TestPropCorruptionBecomesErasure passes with checksum verification skipped`) because `-short` reduces `rapid`'s default check count and the random seed drew no payload-corrupting case. It passed 5/5 times in `go test -short -count=5 ./internal/storage -run TestCanaryChecksumSkipBreaksCorruptionProperty`, passed in uncached `go test ./...`, and passed in the final top-to-bottom `ci-fast.sh` run (165 s).
