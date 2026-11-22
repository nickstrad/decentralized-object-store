# Brief — Chunk 6 BUILD: integration — the object store and sync engine

Seat: Opus builder (fresh). Worktree: /root/Software/decentralized-object-store-work/06-integration
(branch `chunk/06-integration`). Work notes: `agent_work/06-integration/`.

FIRST read `agent_work/06-integration/objstore-p2-builder-rules.md` (binding), then PLAN.md
sections 1, 2 (all of it — 2.5 concurrency and 2.6 errors govern this chunk more than any
other), 2.9, 3, 4.2 "Chunk 6", 5.1, and 7.1, 7.2, 7.4, 7.5, 7.7, 7.8, and the rulings in 9.1.
The plan item "Chunk 6 — Integration: the object store and sync engine" is your
specification: its Deliverable paragraph, its "Tests first" list (the test names are part
of the contract — every named test must exist and test what its name says) and the 2.6
error rows. This brief adds detail; where it differs from PLAN.md wording, this brief
wins (it carries later rulings).

## What this chunk is
Five building blocks have landed, each proven alone on the simulator, each seeing the
others only through small interfaces it declared for itself. You wire them into ONE node
and give that node a real runtime. After this chunk a person can start three nodes with
Docker, PUT an object over HTTP on one and GET it from another, and the same node code
runs unchanged under the simulator with full fault injection.

## What you build on — READ THESE BEFORE DESIGNING
The packages (all landed, all reviewed; their exported APIs are yours to call, NOT to
redesign): `internal/env`, `internal/sim` + `simtest` (FROZEN — a gate rejects edits),
`internal/gossip`, `internal/erasure`, `internal/storage` (chunk store, shard format,
striping, manifest, placement, striped node), `internal/merkle`, `internal/occ`,
`internal/saga` (+ `sagasim`).
For each one read, in this order: its `docs/knowledge/chunk-NN-*.md` entry (written for
you: the non-obvious facts, limits left for this chunk, interfaces to implement, measured
numbers), its book chapter under `book/chapters/`, then
`agent_work/<chunk>/build-notes.md` sections "Requests to the orchestrator", limits and
the repair-round sections. Also `docs/c4/` (the target architecture of this chunk: the
relationships marked planned are exactly the wiring you now build — if you build
something different, say so in the notes; the diagrams are corrected at landing).

Facts that bind your design (each comes from a landed chunk's review or ruling):
1. **Wire-code blocks**: gossip 80–95, storage 96–111, merkle 112–127, occ 128–143 (131
   unassigned), saga 144–159. The node builds ONE `env.CodeTable` from every package's
   rows (a duplicate is rejected). New codes of your own: take 160–191 for `internal/node`
   and the object API; test that the joined table has no collision and every code
   round-trips to its sentinel.
2. **Last-writer-wins, no siblings (owner ruling, PLAN 9.1 and 7.5).** A key always reads
   as ONE version. Concurrent PUTs to one key from two sides of a partition are both
   acknowledged; after heal every node holds the same single winner (hybrid logical clock
   stamp, then node ID) and the loser is discarded — counted, logged and reported through
   `occ`'s `OnDiscard`. You must: (a) expose the discard counter and the recent discard
   records through the node's health/metrics surface and its logs; (b) on a discard of a
   QUOTA key, schedule a recount from the manifests the node holds (after the metadata
   exchange that caused it has settled — the OCC notes explain why the OCC node does not
   schedule it itself) and write only if the stored figure is wrong; (c) on a discard of
   an OBJECT or MANIFEST key, make the losing version's shards garbage — collected after
   the grace period by the shard collector — and NEVER the winner's (a property and a
   DST must show both); (d) never present two versions to a reader: no 300 response, no
   conflict listing. The HTTP layer keeps the full conditional-request contract: `ETag`
   and version ID on every response, `If-Match` / `If-None-Match` on PUT and DELETE,
   412 on a failed precondition.
3. **Deletes need tombstones end to end.** Merkle anti-entropy as landed has no
   tombstones: a pull-only repair would bring a deleted key back. Object deletes are OCC
   tombstone versions (they compete under last-writer-wins like any write); the Merkle
   trees you build over manifests and over shard inventories must make a delete win the
   repair, not lose it. Design this explicitly, state the tombstone retention rule, and
   test "a deleted object stays deleted through anti-entropy, a partition heal and a
   crashed node rejoining" on the simulator.
4. **Callbacks never run inside the call that registered them** (every landed package
   keeps this; so must the node loop), and all protocol state belongs to ONE goroutine
   (PLAN 2.5 rule 3). The protocol packages are single-threaded state machines: in the
   real runtime everything that touches them runs on the node loop; socket readers, HTTP
   handlers and timers only post to its bounded inbound queue.
5. **Membership**: gossip's `Membership` has a different shape from the `Membership` /
   `Peers` interfaces that storage placement, merkle and occ each declared. Write the
   small adapters in `internal/node`; do not change the packages' interfaces unless a
   landed limit forces it (then say so in the notes — the smallest change, with its test).
   Placement must react to membership change the way PLAN 7.2 says (shards placed on the
   gossip view; a node declared dead triggers repair through anti-entropy, not a
   placement stampede).
6. **Saga migration on real components**: implement `saga.MigrationOps` against the real
   stores, obeying the idempotency-key scope rule in `docs/knowledge/chunk-05-saga.md`
   (ownership keyed on the saga ID; step 1 and step 4 copies distinguished), fencing late
   or relayed operations. Then re-run Chunk 5's failure matrix on real components with
   `sagasim.Run` and a `Harness` of yours (`TestClusterMigrationFailurePointsDST`): the
   runner fails a cell whose fault never fired, so your harness must wire `Wrap`. A
   permanently lost coordinator leaves its saga unfinished — a stated limit, no takeover.
7. **Merkle worker lifecycle**: `Worker.Stop` exists; per-bucket workers must be stopped
   when a bucket migrates away or the node shuts down (leak tests). REVIEW-2 of the
   Merkle chunk notes one open edge (a re-entrant `Stop` inside `Start` followed by a
   second `Start`): do not rely on that sequence. Size the TCP frame limit from Merkle's
   stated reply bound and storage's shard request size, with a test.
8. **Storage**: "`ChunkStore.Put` returned nil" means durable; a PUT is acknowledged at
   k+1 durable shards (k=4, m=2 across 3 regions by default; the per-manifest layout is
   honoured on GET); `Window` bounds stripes awaiting quorum, not requests outstanding to
   slow holders. `Get` has no object-size cap yet — the HTTP layer must bound request and
   response sizes and stream where the plan says.
9. **No authentication anywhere** (gossip accepts any datagram; a forged death notice
   evicts a node). That is a stated limit of this learning project: say so in the notes
   and the help text plumbing; do not invent a partial scheme.

## Files you own
- `internal/node/` (new): the node (wiring, adapters, the node loop), the real runtime
  (UDP datagram transport, TCP request/response transport with the frame codec,
  file-backed disk with the same two-layer durability meaning as the simulator's disk —
  write then fsync, a crash loses unsynced data — wall clock, timers), config, health.
- `internal/storage/`: you ADD the HTTP object API files (put/get/head/delete/list,
  bucket create, tags and ACL, quota, metadata, migration start/status, membership and
  health) and may make the smallest changes the wiring needs in the landed files — each
  such change with a test, listed in the notes.
- Minimal, justified changes in `internal/gossip`, `internal/merkle`, `internal/occ`,
  `internal/saga` ONLY where a landed interface cannot carry what integration needs;
  never weaken a landed test; list every such change and why.
- `cmd/objnode/` (new): flags or a config file; `help.txt` holds bare flag names only
  (another seat writes the help prose); graceful shutdown on SIGINT/SIGTERM.
- `internal/testutil/leakcheck/` (new, tiny, stdlib only) as PLAN 2.5 describes.
- `compose.yaml` — the `cluster` profile only: make three nodes in three regions start,
  join and serve, and add `agent_work/06-integration/cluster-smoke.sh` (starts the
  profile, PUTs on one node, GETs from another, checks a conditional PUT gets 412, tears
  down, exits non-zero on any failure). You may not edit the Dockerfile or the `dev`
  service; if you need a change there, request it in the notes.
- `agent_work/06-integration/build-notes.md`; appended lines in
  `agent_work/gates/panic-allowlist.txt`. Do not edit other gate files; if a gate needs a
  change for this chunk (for example to run the cluster smoke script, or coverage floors
  for the new packages), write the exact request in the notes and continue.
You may NOT edit `internal/env`, `internal/sim`, `simtest` (simulator change request
procedure as before: write the need in the notes, stop on that point, finish the rest).

## Requirements beyond the plan item
- **One protocol core, two environments (PLAN 7.1).** The node is constructed from an
  `env.Env`; the simulator supplies one, the real runtime supplies the other. There is no
  `if simulated` anywhere. The full-node DSTs (`TestClusterPutGetDST`,
  `TestClusterBitRotDST`, `TestClusterPartitionHealDST`,
  `TestClusterMigrationFailurePointsDST`, `TestClusterCrashRestartDST`) run the SAME node
  type the binary runs, through `simtest.RequireDeterministic`, at 20+ nodes in 3
  regions, default seeds in `go test ./...` and holding at `DST_SEEDS=500`. Keep the
  package's default test run and its 500-seed sweep inside the builder rules' budgets on
  the shared 4-core machine — if a full-node sweep cannot fit, cut per-seed cost (smaller
  objects, fewer stripes, shorter scenarios), not seeds or checks, and say what you did.
- **Invariants checked during every full-node DST**, not only at the end: no GET returns
  wrong bytes, ever; an acknowledged PUT inside the fault budget stays readable; all
  replicas' metadata converge to byte-identical documents after heal, one version per
  key; every acknowledged metadata write is current, causally superseded, or in a
  discard record (reuse the OCC oracle idea — independent of the product's classifier);
  no orphan shards after the grace period; membership views agree after heal.
- **Concurrency (PLAN 2.5, all of it).** Goroutines exist only in `internal/node`, the
  HTTP server in `internal/storage`, and `cmd/`. Every goroutine has one owner, a
  WaitGroup and a context; `Shutdown(ctx)` returns only when all have exited; bounded
  inbound queues that drop and count datagrams on overflow; no protocol state under a
  lock. Lifecycle tests in `testing/synctest` bubbles; real-socket tests use the leak
  check in `TestMain`; cancellation tests in the middle of put, get, repair and
  migration and during start-up; shutdown under load, double `Close`, `Close` during
  start-up, use after `Close` returns `ErrClosed`. The gate runs these packages with
  `-race -count=3`: no test may be order- or timing-dependent (no sleeps as
  synchronisation; real-runtime tests assert eventual properties with generous timeouts).
- **Runtime conformance**: a small set of scenarios (join, put/get across nodes, one
  conditional write race, one metadata convergence) runs twice — on the simulator and on
  the real loopback runtime — from one scenario definition.
- **HTTP object API**: one status table (PLAN 2.6) tested row by row through real HTTP;
  request bodies and headers bounded; slow or malformed clients cannot pin a goroutine
  or memory without bound (timeouts, max header and body sizes, stated and tested);
  `FuzzObjectAPI` drives the handler layer with arbitrary requests against a reference
  model; `FuzzFrame` covers the TCP frame codec (malformed, truncated, oversized).
- **Idempotent remote operations**: a duplicated request and a lost response must not
  double-apply (property test over the full path).
- **Errors per PLAN 2.6**: sentinels and typed errors, `%w` wrapping, every sentinel
  produced and checked with `errors.Is`; errors cross the network only as codes through
  the joined table; the HTTP mapping is one table.
- **Canaries**: plant at least (a) `TestCanaryNodeSkipsDiscardRecount` — with the quota
  recount on discard disabled behind a test-only switch, the quota DST must fail; (b)
  `TestCanaryDeleteResurrected` — with tombstones ignored by anti-entropy behind a
  test-only switch, the delete-stays-deleted DST must fail; (c)
  `TestCanaryShutdownLeak` — a goroutine started without its WaitGroup must fail the
  leak check.
- Coverage: 85% statement coverage on `internal/node` and on the files you add to
  `internal/storage`; name every uncovered branch and why.

## Measurements to put in the build notes
Full-node DST: convergence time after heal, repair time after bit-rot and after a crashed
node rejoins, discards per contested workload (events and distinct lost versions),
quota overshoot under partition and rounds for the recount to settle, shards collected
for discarded versions (and zero winner shards collected), migration matrix results on
real components (cells × seeds), wall time of the default tests and of the 500-seed
sweep per package. Real runtime: PUT/GET throughput and latency for 1 MiB and 32 MiB
objects on a 3-node loopback cluster, goroutine count at idle and under load, memory at
idle. The cluster smoke script's output.

## The gate you must pass before you stop
Commit, then from the worktree root on the host run `agent_work/gates/gate.sh
06-integration`. It runs, in the dev container: gofmt, vet, build, the determinism lint
(protocol packages only — it does not cover `internal/node` or `internal/storage`; run it
by hand on any protocol-package file you touched), the panic allowlist, the dependency
check (standard library only in product code; `pgregory.net/rapid` in tests only), the
no-comments check, unit tests with the coverage floor, rapid properties at 2,000 checks,
every fuzz target for 30 s, `-race` (with `-count=3` on `internal/node`,
`internal/storage`), and the 500-seed sweep. It must end `overall=pass`. Then run
`agent_work/06-integration/cluster-smoke.sh` on the host and record its output. Record
the gate summary in the build notes. Leave the result files under
`agent_work/06-integration/gates/` uncommitted.

## Build notes (the next seats work from them)
`agent_work/06-integration/build-notes.md`: what was built, file by file; the wiring
(which adapter joins which interface), the node loop and goroutine ownership map; every
design choice with its cost; the claims table (each row: a claim the book will publish →
the test that proves it → the measured number); every change made inside a landed
package and why; limits and things not done; requests to the orchestrator (gate changes,
simulator change requests, plan wording that turned out wrong).

## Out of scope
The end-to-end suite and tier-R process harness (Chunk 7), fuzz hardening budgets,
reader-facing prose, code comments and CLI help text (another seat), the book, README,
docs/, C4 diagrams, PLAN.md, STATE.md.

## Working method for a chunk this size
Tests first, in layers, committing as you go (small logical commits; the history is read
as a tour): (1) the joined code table and adapters with unit tests; (2) the node on the
simulator with `TestClusterPutGetDST` green; (3) metadata, last-writer-wins plumbing,
quota recount, shard collection; (4) tombstones through anti-entropy; (5) migration on
real components and the matrix; (6) the real runtime and frame codec with lifecycle and
leak tests; (7) HTTP API with the status table and fuzz target; (8) `cmd/objnode`, the
cluster profile and the smoke script; (9) measurements and notes. Run the narrow tests
for the layer you are on, not the whole gate, until the end. If you find a defect in a
landed package, write a failing test for it first, fix it minimally, and list it.
Final message: commit list and a five-line summary. Do not claim anything you did not run.
