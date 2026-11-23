# Brief — Chunk 7 BUILD: end-to-end suite, nemesis cycles, fuzz hardening

Seat: Opus builder (fresh). Worktree: /root/Software/decentralized-object-store-work/07-e2e
(branch `chunk/07-e2e`). Work notes: `agent_work/07-e2e/`.

FIRST read `agent_work/07-e2e/objstore-p2-builder-rules.md` (binding), then PLAN.md
sections 1, 2 (all; 2.3 fuzz budgets, 2.5 concurrency, 2.6 errors and 2.7 END-TO-END TESTS
govern this chunk), 2.9, 4.2 "Chunk 7", and the rulings in 9.1. The plan item "Chunk 7 —
E2E suite, nemesis cycles, fuzz hardening" and the scenario table in 2.7 (E1–E9, with
their pass criteria word for word) are your specification. This brief adds detail; where
it differs from PLAN.md wording, this brief wins (it carries later rulings).

## What this chunk is
Chunk 6 landed one node that wires gossip, erasure-coded storage, OCC metadata with
last-writer-wins, Merkle anti-entropy and sagas, on the simulator and on a real
UDP/TCP/file runtime, with `cmd/objnode` and an HTTP object API. This chunk asks the
question none of the earlier ones could: does it all work as ONE system? You build
`test/e2e` — the nine scenarios on two tiers — the long nemesis-and-heal runner, the
whole-cluster fuzz target, and you run the hardening fuzz budgets and a large seed sweep
and record what they show. Product defects you find are fixed here, test first.

## What you build on — READ THESE BEFORE DESIGNING
- `docs/knowledge/chunk-06-integration.md` (written for you), the chapter
  `book/chapters/07-object-store-and-sync-engine.md`, and
  `agent_work/06-integration/build-notes.md` — especially the limits, section 14
  ("Arbitration fix": when a version is declared lost and the stated residuals), the
  measurements and the "Requests to the orchestrator".
- `internal/node` test helpers (`helpers_test.go`, `oracle_test.go`, the `dst_*_test.go`
  scenarios): the harness, the oracles and the fault vocabulary that already exist. Tier
  S reuses the IDEAS; anything you need from a `_test.go` file must be moved into a
  clearly named test-support package (for example `internal/node/nodetest`) by the
  smallest change, with the existing tests still passing unchanged in meaning.
- `agent_work/adversarial/06-integration/` (the independent harness, 662 checks) and
  `agent_work/06-integration/cluster-smoke.sh` with gate G7.
- Every earlier chunk's `docs/knowledge/chunk-NN-*.md` for the claims its chapter
  publishes: E-scenarios retest them on the whole system.

Facts that bind your design:
1. **Last-writer-wins, no siblings.** One version per key; concurrent writes across a
   partition are both acknowledged and one is discarded — counted, logged, on record. E2
   and E8 check EVERY lost write against the nodes' discard logs with an oracle that is
   independent of the product's own classifier.
2. **Readable or declared lost.** "No acknowledged object write is lost" (E2, E6, E7)
   means, exactly as Chunk 6 states it: once every holder is up, holds the manifest and
   can reach the others, every key listed live is readable byte-identical, or is declared
   lost on record (`~lost/<version>`, manifest tombstoned, GET 404, `lost_objects`
   counter). A write whose writer stays cut off beyond `OrphanGrace` may be declared
   lost; inside the fault budget and inside the grace period nothing may be. Your oracles
   must distinguish the three outcomes and fail on "live and unreadable" after a full
   heal and on "declared lost while k verifiable shards of every stripe exist".
3. **Deletes are tombstones** and stay deleted through anti-entropy, heals and rejoins.
4. **No authentication** is a stated limit; do not test for it or add any.
5. **A permanently lost saga coordinator leaves its saga unfinished** (stated limit); E5
   tests a RESTARTED coordinator.
6. **Docker is the only environment.** Tier R (6–9 real `objnode` processes on loopback)
   runs INSIDE the `dev` container like every other gate: processes, not compose
   services. `SIGKILL` for crashes, the harness flipping bytes in shard files for
   bit-rot, and partitions through a peer blocklist in the real transport that is
   compiled in only with the `e2efault` build tag (a default build must contain no trace
   of it — test that). Tier R builds the binary with `-race` and must finish in a few
   minutes on a shared 4-core machine.

## Files you own
- `test/e2e/` (new): tier S scenarios (`RequireDeterministic`, 20–100 nodes, 3 regions,
  clients calling the object API over the simulated network), the tier R process
  harness, one scenario definition shared by both tiers wherever the plan's table marks
  "S, R", the nemesis/healing cycle runner (E7), `FuzzClusterNemesis` (fuzz bytes become
  a whole-cluster fault schedule; safety invariants at every step, convergence after
  heal; inputs capped at 64 KiB; boundary seeds via `f.Add`).
- The `e2efault` blocklist in `internal/noderuntime` (build-tagged file) and the smallest
  hooks it needs.
- A test-support package extracted from `internal/node`'s test helpers, if needed.
- Product fixes anywhere outside the frozen packages, each one: failing test first,
  smallest fix, listed in the notes with the scenario and seed that found it.
- `agent_work/07-e2e/build-notes.md`, `agent_work/07-e2e/hardening/` (fuzz and sweep
  result records), appended lines in `agent_work/gates/panic-allowlist.txt`. Do not edit
  other gate files: write the exact gate change you need (how `test/e2e` joins G1–G5,
  tier R as its own gate step, floors) in the notes as a request and continue.
You may NOT edit `internal/env`, `internal/sim`, `simtest` (simulator change request
procedure: write the need in the notes, stop on that point, finish the rest).

## Requirements beyond the plan item
- **Tests first.** Each scenario is written with its pass criteria (the table's wording
  turned into assertions) before any fix it forces. A scenario that cannot fail is a
  defect: for each of E1–E9 state in the notes one planted fault that makes it fail, and
  keep at least three of those as `TestCanary...` tests behind test-only switches.
- **Invariants at every step of tier S**, not only at the end: no GET ever returns wrong
  bytes; the three-outcome object oracle of fact 2; metadata byte-identical after heal
  with one version per key; every acknowledged metadata write current, superseded or in
  a discard record; identical Merkle roots and membership after heal within the stated
  bound; no orphan shards after the grace periods; shard spread never above `m` per
  region per stripe when regions allow.
- **E7** alternates chaos and heal for many cycles per seed (partitions, crashes,
  restarts, wipes, pauses, bit-rot, link loss, clock steps where the simulator offers
  them, migrations and live traffic in the same run), hundreds of seeds at `DST_SEEDS`
  in the gate. Keep per-seed cost low enough for the builder rules' budgets; cut object
  sizes and cycle length before seeds or checks, and say what you did.
- **E9 and tier R hygiene**: SIGTERM under load ends in-flight requests cleanly, the
  process exits within the deadline with exit status 0 and no goroutine left (the
  binary's own leak report), restart is clean; the harness never leaves a process, port
  or temp directory behind, also when a test fails (verify in a test).
- **Hardening runs (PLAN 2.3, OQ-9).** After the suite is green: every fuzz target in
  the repository for 5 minutes, and 15 minutes each for `FuzzErasureCoding`,
  `FuzzMerkleDiff`, `FuzzOCCConditionalWrite` and `FuzzClusterNemesis`, at most 3 in
  parallel, `-fuzzminimizetime 5s`, through Docker. Record per target: executions, new
  corpus entries, findings. Every crasher is committed under `testdata/fuzz/<Target>/`,
  fixed test-first, and never deleted. Then one large seed sweep: every DST and tier S
  scenario at `DST_SEEDS=5000` where it fits in 30 minutes per package, otherwise the
  largest count that does; record counts, wall time and every failing seed (each one a
  finding to fix or to narrow in writing with the orchestrator's agreement — write the
  request in the notes).
- Standard library only (`pgregory.net/rapid` in tests); no comments in Go (another seat
  writes them); goroutines only where PLAN 2.5 allows, plus the tier R harness itself.

## Measurements to put in the build notes
Per scenario and tier: nodes, seeds, wall time, the bound checked and the measured value
(convergence after heal, repair time and bytes moved per byte damaged in E3, migration
time under traffic in E4, catch-up time in E6, discards and declared-lost counts in E2,
E7 and E8). Hardening: the per-target table and the sweep table. Tier R: start-up time
of the cluster, PUT/GET latency observed, shutdown time under load.

## The gate you must pass before you stop
Commit, then from the worktree root on the host run `agent_work/gates/gate.sh 07-e2e`;
it must end `overall=pass`. Run tier R through Docker by hand and record its output
until the orchestrator adds its gate step. Leave the result files under
`agent_work/07-e2e/gates/` uncommitted.

## Build notes (the next seats work from them)
`agent_work/07-e2e/build-notes.md`: what was built, file by file; for each of E1–E9 the
claim → the test → the measured number, and what the scenario does NOT show; every
product defect found (scenario, seed, failing test, fix); every change in a landed
package and why; the hardening tables; limits and things not done; requests to the
orchestrator.

## Out of scope
Reader-facing prose, code comments, the book chapter (`08-end-to-end-and-hardening`,
another seat), README, docs/, C4 diagrams, PLAN.md, STATE.md, gate scripts.

## Working method
Layers, small commits (the history is read as a tour): (1) test-support extraction and
the tier S cluster fixture with E1; (2) the oracles of fact 2 with their self-tests;
(3) E2, E3, E6, E8 on tier S; (4) E4, E5; (5) E7 runner and `FuzzClusterNemesis`; (6)
the tier R harness, the `e2efault` blocklist, then E1–E6, E8, E9 on tier R; (7) canaries;
(8) hardening runs and the sweep; (9) notes. Run narrow tests for the layer you are on,
not the whole gate, until the end. Final message: commit list and a five-line summary.
Do not claim anything you did not run.
