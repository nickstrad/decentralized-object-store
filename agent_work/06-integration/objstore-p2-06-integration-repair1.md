# Brief — Chunk 6 REPAIR (the one repair round): integration — the object store and sync engine

Seat: Opus builder (fresh). Worktree: the current directory (branch `chunk/06-integration`).
You are repairing work a previous builder committed. FIRST read
`agent_work/06-integration/objstore-p2-builder-rules.md` (binding: no comments in Go,
Docker only, tests first, commit here, never push), then the original brief
`agent_work/06-integration/objstore-p2-06-integration-build.md` (its numbered facts still
bind you), `agent_work/06-integration/build-notes.md`, and the review
`agent_work/06-integration/REVIEW-1.md` — verdict FAIL: one BLOCKER, eight MAJOR, two NIT,
one NOTE. Read every finding in full: the reviewer's file/line references, reproductions
and "smallest fix" notes are your starting point; a better fix is welcome if you can
prove it. An independent test harness exists at `agent_work/adversarial/06-integration/`
(284 checks, all held; read `ATTACKS.md`; you may run it, you do not edit it).
You own what the build brief gave the builder: `internal/node/`, the HTTP object API
files in `internal/storage/`, `cmd/objnode/`, `internal/testutil/leakcheck/`, the
`cluster` profile of `compose.yaml`, `agent_work/06-integration/`.

## Must fix (each: a regression test written FIRST that fails on the current code)
1. **Finding 1 (BLOCKER) — a write acknowledged just before the migration flip must
   survive the migration.** The coordinator must hold every pre-flip acknowledged write
   before it copies, compares roots and retires the source. The reviewer's fix (every
   live peer must agree on the route AND on a digest of the bucket's live manifests) is
   one way; whatever you choose, prove it with a DST that keeps PUT, overwrite and DELETE
   traffic running on NON-coordinator peers through the whole migration, under loss and
   delay, over the sweep seeds: every acknowledged write is readable after the migration
   (or was superseded or deleted by a later acknowledged write, or is on a discard
   record), in both directions (completed and compensated), and no shard of a live
   version is ever purged. Think through the same hazard for peers that are NOT live
   when the pointer check runs (paused, partitioned) and join later holding a pre-flip
   write: state what happens to that write and test it — it must never end up live in
   metadata with its shards destroyed. If the honest answer is "the migration must not
   retire until X", build X; if a residual case remains, it must degrade to a recorded,
   visible outcome, not to permanent `ErrUnavailable` on a key the metadata calls live.
2. **Finding 6 — shard collection safety, and its invariant.** (a) The continuous GC
   invariant must be independent of the product's own bookkeeping and cluster-wide: an
   oracle built from acknowledged operations, checked on every step, asserting that no
   node ever deletes a shard of a version that is, or can still become, the winner on
   any node. (b) Bit-rotted orphan shards must not leak forever. Re-examine the whole
   collection rule with finding 1 in mind (purge during migration is a collection path
   too) and write the rule down in the notes as one section: every path by which a shard
   can be deleted, and the evidence each requires.
3. **Finding 3 — quota drift.** A last-writer-wins discard of an OBJECT or MANIFEST key
   changes real usage even when no quota key was discarded: recount on those too (or
   whatever closes the reviewer's reproduction), still writing nothing when the stored
   figure is right. The quota DST asserts usage equals a recount from live manifests on
   every node after every heal, not only after quota-key conflicts.
4. **Finding 9 — concurrent migrations and routed bucket creation.** Registrations must
   not be lost to last-writer-wins on one shared key (per-migration keys, or another
   structure where concurrent starts cannot erase each other); close the
   unrouted-placement window on bucket creation. Duelling-migration DST on real
   components: two coordinators, same bucket and different buckets, same instant.
5. **Finding 5 — conditional headers.** An empty or whitespace-only `If-Match` /
   `If-None-Match`, a malformed ETag, a list, a weak validator: decide each per RFC 9110
   and the plan's status table and NEVER degrade a present precondition header to an
   unconditional write (400 or 412, stated per row). Extend the HTTP status table test
   and `FuzzObjectAPI`'s model so a request carrying any precondition header can never
   be applied unconditionally.
6. **Finding 4 — file disk durability.** Fsync the temp file before the rename, fsync
   the containing directory after create/rename/delete, and the ancestors a new
   directory needs; handle short writes; define what a crash at each point leaves and
   test it with a fault-injecting file layer (fail or "crash" between each pair of
   steps, then reopen): the result is always the old bytes or the new bytes, never a
   torn or missing blob the simulator's two-layer disk could not produce. State what is
   NOT covered (power-loss behaviour of the real filesystem cannot be tested here).
7. **Finding 8 — runtime hazards.** No blocking call (DNS lookup, dial, file or socket
   I/O that can stall) on the node loop goroutine: resolve off-loop with a timeout and
   post the result; a panic on the loop must take the whole runtime down cleanly —
   sockets closed, every goroutine exited, `Shutdown` returning, the process exiting
   non-zero in `cmd/objnode` — with a test under the leak check.
8. **Finding 7 — tests that do not test what they claim.** For each lifecycle and
   cancellation test the reviewer names: make it prove its name (a cancellation test
   must cancel mid-flight at a controlled point and show the mutation did NOT apply, or
   applied exactly once and is reported), and fix `call` so an already-cancelled
   mutation is never executed. No sleep as synchronisation.
9. **Finding 2 — the last-writer-wins oracle in `TestClusterPartitionHealDST`** must be
   per key and independent of the product's classifier (the OCC chunk's oracle is the
   model: built from what each acknowledged write held). With the oracle fixed, re-run
   the sweep: if it now finds real failures, they are part of this round.
10. **Findings 10 and 11** — as the reviewer describes: no synchronous callback from
    `migrator.Copy`; every claim-table row backed by a test that can fail; `matrixSeeds`
    so pinned seeds run on all cells; dead code removed; the off-by-one in the notes.

## Structure change (from the review's answer 12c; do it early, as its own commits)
The node's protocol core runs under the simulator but is not covered by the determinism
lint, because the real-runtime files share its package. Split them:
- `internal/node/` keeps ONLY the protocol core (everything the simulator executes): no
  goroutines, channels, mutexes, wall clock, sockets or files — it must pass the
  determinism lint exactly like the five protocol packages (run it by hand:
  `docker compose run --rm -T dev bash -c 'cd agent_work/gates/detlint && go build -o /tmp/dl . && cd /src && /tmp/dl -root /src internal/node'`
  — check the exact tool invocation in `agent_work/gates/gate.sh` and
  `agent_work/gates/detlint/README-tools.txt`).
- `internal/noderuntime/` (new) takes the real runtime: UDP and TCP transports, frame
  codec, file disk, wall clock, the loop goroutine, `Runtime`, service start/stop.
  `cmd/objnode` and the HTTP server use it.
The orchestrator then adds `internal/node` to the lint roots and the coverage floor,
`internal/noderuntime` and `cmd/objnode` to the `-race -count=3` list and the coverage
floor, and a host-side cluster-smoke step to the gate. Keep
`agent_work/06-integration/cluster-smoke.sh` working and runnable from the repository
root with no arguments; make it use ports and a compose project name that cannot collide
with another checkout running the same script at the same time (the gate and CI will
run it).

## Decisions already made (do not reopen)
- The migration matrix shape (all 78 cells on the base seed, the 500 sweep seeds spread
  over cells) is accepted for this chunk; the full product is run in the hardening
  chunk. Fix `matrixSeeds` (pinned seeds on all cells) and keep the claim worded exactly.
- Sweep time: bring the node package's 500-seed sweep under about 10 minutes the way the
  reviewer suggests (shorter contested window and grace in the partition-heal scenario),
  without cutting node count, seeds or invariants — the new tests of this round count
  against the same budget, so cut per-seed cost where it is cheap.
- Tombstones are retained indefinitely (as built). Keep it, state the metadata-growth
  cost in the notes as a limit; the plan's wording is corrected by the orchestrator.
- Last-writer-wins, no siblings: unchanged and binding.

## Before you stop
Re-measure every number in the build notes your changes can move and correct the claims
table. Commit, then from the worktree root on the host run
`agent_work/gates/gate.sh 06-integration`; it must end `overall=pass`. Then run
`agent_work/06-integration/cluster-smoke.sh` and `agent_work/adversarial/06-integration/run-all.sh`
and record both outputs (for any harness check now scored BROKEN say exactly why — a
moved package path or a changed status code it pinned, or a real regression; do not edit
the harness; restore its evidence files after your run). Add a "Repair round" section to
the build notes: each finding, the test that now proves it, what changed, API and layout
changes, numbers that moved, anything you chose not to do and why, and updated "Requests
to the orchestrator". Everything committed (gate result files may stay uncommitted).
Final message: commit list and a five-line summary. Do not claim anything you did not run.
