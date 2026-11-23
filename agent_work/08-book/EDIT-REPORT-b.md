# Consistency Edit Report — Chapters 5 to 8

Checks run after all edits:
- `docker compose run --rm -T dev python3 agent_work/gates/site_lint.py /src`: **ok (0 findings)**.
- `DRAW_VISUAL_MAX_WIDTH=80 python3 /root/.claude/skills/draw-visual/scripts/render.py --check` on chapters 5–8: **all 8 embedded diagrams checked and clean** (no `.mmd` sources changed).
- Automated exact-match check of all 14 code excerpts in `## Key pieces in code` across chapters 5–8: **all 14 exact verbatim matches, 9–20 lines each**.
- Automated relative-link check across chapters 5–8: **0 broken links**.

---

## Chapter 5 — `book/chapters/05-optimistic-concurrency-control.md`

- **Words (`wc -w`):** 2,866 before -> **2,580** after (target: 1,800–2,600).
- **Headings renamed:**
  - `### One version per key, LWW order, and HLC (`hlc.go`, `version.go`, `record.go`, `doc.go`)` -> `### One version per key, last-writer-wins order, and hybrid logical clocks` (file names moved into text).
  - `### Version vectors as classifier and actor epoch retirement (`vv.go`, `version.go`, `store.go`)` -> `### Version vectors as classifier and actor epoch retirement` (file names moved into text).
  - `### Transactions, crash-safe outbox, and encoding (`store.go`, `doc.go`, `encoding.go`)` -> `### Transactions, crash-safe outbox, and encoding` (file names moved into text).
  - `### Typed metadata, quotas, and replicator (`meta.go`, `quota.go`, `wire.go`, `node.go`, `errors.go`)` -> `### Typed metadata, quotas, and the replicator` (file names moved into text).
- **Excerpts corrected:** None drifted. All 4 excerpts verified exact against `internal/occ/record.go` (`Precondition.Holds`, 9 lines; `Record.Merge`, 11 lines) and `internal/occ/store.go` (`Store.replaced`, 18 lines; `Store.end`, 14 lines).
- **Paths or names fixed or removed:**
  - Removed unpublished operational path with chunk number: `agent_work/adversarial/04-occ/` (`ATTACKS.md`, `RESULTS.md`, `run-all.sh`) from `## Go deeper in the repo` (kept all adversarial test measurements in prose).
  - Added [`internal/occ/node_merge.go`](../../internal/occ/node_merge.go) and [`internal/occ/node_merge_test.go`](../../internal/occ/node_merge_test.go) to the `internal/occ/` listings so the package inventory matches the current tree.
  - Replaced "arrive in coming chapters" / "wait for coming chapters" with relative links to existing chapters (`02-swim-gossip.md`, `03-reed-solomon-erasure-coding.md`, `04-merkle-anti-entropy.md`, `06-sagas.md`, `07-object-store-and-sync-engine.md`).
  - Spelled out "deterministic simulation testing" once and aligned simulator vocabulary.
- **Cross-chapter disagreements:** None. (Note: Chapter 5 cites `occ.DefaultPeriod` = 500 ms ±50% from `internal/occ/node.go:48`, while Chapter 7 cites `node.DefaultOCCPeriod` = 3 s from `internal/node/node.go:32`; both match their respective packages.)
- **Could not verify:** Historical benchmark/sweep timings (e.g., 241.8 s sweep, `BenchmarkMergeDoc` ms/MB, coverage percentages) were not re-executed; all measurements were preserved verbatim as required.

---

## Chapter 6 — `book/chapters/06-sagas.md`

- **Words (`wc -w`):** 2,907 before -> **2,594** after (target: 1,800–2,600).
- **Headings renamed / added:**
  - `### Testing on the deterministic simulator` -> `### Testing on the simulator`.
  - Added required `### Narrowed claims` (with `None.`) inside `## What the tests show, and what they do not`.
- **Excerpts corrected:** None drifted. All 4 excerpts verified exact against `internal/saga/log.go` (`AppendRecord`, 16 lines), `internal/saga/resume.go` (`Snapshot.plan`, 20 lines), `internal/saga/engine.go` (`Engine.startDo`, 12 lines), and `internal/saga/sagasim/matrix.go` (`Cell.Want`, 11 lines).
- **Paths or names fixed or removed:**
  - Removed unpublished operational path with chunk number: `agent_work/adversarial/05-saga/` (`ATTACKS.md`, `RESULTS.md`, `run-all.sh`) from `## Go deeper in the repo` (kept all 125-group adversarial results in prose).
  - Fixed wrong forward reference: "importing nothing from erasure coding, Merkle trees, optimistic concurrency control, or gossip (each arriving in coming chapters)" -> linked back to chapters 2–5 (`02-swim-gossip.md`..`05-optimistic-concurrency-control.md`) and forward to `07-object-store-and-sync-engine.md`.
  - Added [`internal/saga/sagasim/review2_test.go`](../../internal/saga/sagasim/review2_test.go) to `## Go deeper in the repo`.
  - Spelled out "last-writer-wins (LWW)" in the opening before the first "LWW" usage (including `diagrams/06-migration-flow.mmd`) and spelled out "deterministic simulation testing" once.
- **Cross-chapter disagreements:** None.
- **Could not verify:** Historical sweep/fuzz/race/coverage numbers were not re-executed; all measurements were preserved verbatim.

---

## Chapter 7 — `book/chapters/07-object-store-and-sync-engine.md`

- **Words (`wc -w`):** 3,275 before -> **2,591** after (target: 1,800–2,600).
- **Headings renamed / split:**
  - `### Wiring, metadata, and last-writer-wins (`node.go`, `mux.go`, `objects.go`, `meta.go`, `codes.go`)` -> `### Wiring, metadata, and last-writer-wins`.
  - `### Shard collection and loss declaration (`inventory.go`)` -> `### Shard collection and loss declaration`.
  - `### Bucket migration on real components (`migrate.go`)` -> `### Bucket migration on real components`.
  - `### Real runtime, HTTP API, and Docker cluster (`internal/noderuntime`, `internal/storage/httpapi`, `cmd/objnode`)` -> `### Real runtime, the object API, and Docker cluster`.
  - `### Design choices and testing on simulator and real runtime` -> split into `### Design choices and what each costs` and `### Testing on the simulator`.
  - `### Narrowed claims and what the tests do not prove` -> split into `### Narrowed claims` and `### What the tests do not prove`.
- **Excerpts corrected:** None drifted. Verified exact against `internal/node/mux.go` (`chanNet` + `withChannel` + `Send` + `Request`, 19 lines), `internal/node/inventory.go` (`unrecoverableStripe`, 18 lines), and `internal/noderuntime/realenv.go` (`realTimer.arm` + `fire`, 19 lines). Removed the fourth excerpt (`AllCodeEntries`, already summarized in prose) to keep the chapter within the 2,600-word limit while staying within the style sheet's 3–5 excerpt range.
- **Paths or names fixed or removed:**
  - Updated channel-5 node RPC list from three ops (`metadata fetch, pointer view, shard purge`) to four (`metadata fetch, pointer view, shard purge, shard verify`) to match `internal/node/meta.go:95-100` (`opVerifyShards = 4`, linking `08-end-to-end-and-hardening.md`).
  - Removed unpublished operational paths with chunk numbers: `agent_work/06-integration/cluster-smoke.sh` and `agent_work/adversarial/06-integration/` (`ATTACKS.md`, `RESULTS.md`, `run-all.sh`) from `## What the tests show` and `## Go deeper in the repo` (kept all smoke and adversarial measurements).
  - Replaced "wait for a coming hardening chapter" with a relative link to `08-end-to-end-and-hardening.md`, noting that multi-process fault injection is covered there while a full 78 x 500 real-component sweep remains unrun.
  - Replaced `k=2, m=1` with `2+1`, spelled out "deterministic simulation testing" and "last-writer-wins (LWW)", and used `objnode` and "the object API" consistently.
- **Cross-chapter disagreements:** None remaining after syncing channel-5 RPCs with `internal/node/meta.go` and Chapter 8.
- **Could not verify:** Historical 500-seed sweep, loopback `MEASURE_RUNTIME=1`, Docker smoke, fuzz, and coverage figures were not re-executed; all measurements were preserved verbatim.

---

## Chapter 8 — `book/chapters/08-end-to-end-and-hardening.md`

- **Words (`wc -w`):** 2,996 before -> **2,591** after (target: 1,800–2,600).
- **Headings renamed / added / reordered:**
  - `### Two tiers behind one `Cluster` interface` -> `### Two tiers behind one interface`.
  - `### Two product defects found — and why a suite needs an outside attacker` -> `### Two product defects found by adversarial testing`.
  - `### Coverage holes caught in the suite, replay, and canaries` -> `### Coverage holes, replay, and canaries`.
  - `### Seed shares, measurement names, and how long runs execute` -> `### Design choices and what each costs`.
  - `### Continuous oracles, nemesis cycles, and whole-cluster fuzzing` -> `### Testing on the simulator` (moved after `### Design choices and what each costs` to match the chapter shape).
  - Added required `### Narrowed claims` (with `None.`) and renamed `### Stated limits` -> `### What the tests do not prove`.
- **Excerpts corrected:**
  - `test/e2e/e5sim_test.go` (`e5CellSeeds`): the previous excerpt concatenated `const e5TrimStride = 8` and `func e5CellSeeds(...)`, omitting the 3-line doc comment between lines 223 and 228. Corrected to an exact contiguous copy of `func e5CellSeeds(...)` (lines 228–238, 11 lines) with `e5TrimStride = 8` stated in the caption.
  - Verified exact: `availabilityReadClean` (`test/e2e/nemesis_test.go:168-180`, 13 lines) and `CodeEntries` (`internal/storage/errors.go:52-60`, 9 lines). Removed `Ledger.checkRead` (19 lines with long error strings) to keep the chapter under 2,600 words while retaining 3 exact excerpts (within the 3–5 excerpt range).
- **Paths or names fixed or removed:**
  - Fixed misleading file range `[scenario_e1_test.go](...)–[scenario_e6_test.go](...)` (there is no `scenario_e5_test.go`; E5 is in `e5sim_test.go` and `tierr_test.go`, and E8 is in `scenario_e2_test.go`) to `[scenario_e1_test.go](...)–[scenario_e4_test.go](...), [scenario_e6_test.go](...)`.
  - Removed unpublished operational paths with chunk numbers: `agent_work/gates/ci-fast.sh`, `gate.sh`, `agent_work/07-e2e/hardening/run-fuzz.sh`, `agent_work/07-e2e/hardening/run-sweep.sh`, and `agent_work/adversarial/07-e2e/` (replaced push CI pointer with `.github/workflows/ci.yml` and preserved every measurement and behavior).
  - Replaced `k=4, m=2` with `4+2` and `k=2, m=1` with `2+1`; spelled out "deterministic simulation testing" and "optimistic concurrency control".
- **Cross-chapter disagreements:** None. (Note: Chapter 6 and Chapter 7 report 577 runs / max 18.9 s for `internal/node`'s `TestClusterMigrationFailurePointsDST`, whereas Chapter 8 reports 202 runs / 280 with seed 1 pinned / max 16.9 s for `test/e2e`'s `TestE5MigrationEveryFailurePointS`; these are two distinct test suites.)
- **Could not verify:** Historical Tier S / Tier R / 15-target fuzz / sweep measurements were not re-executed; all measurements were preserved verbatim.
