# Brief — Chunk 4 REPAIR (the one repair round): optimistic concurrency control

Seat: Opus builder (fresh). Worktree: the current directory (branch `chunk/04-occ`). You
are repairing work a previous builder committed. FIRST read
`agent_work/04-occ/objstore-p2-builder-rules.md` (binding: no comments in Go, Docker only,
tests first, commit here, never push), then the original brief
`agent_work/04-occ/objstore-p2-04-occ-build.md`, `agent_work/04-occ/build-notes.md`, and the
review `agent_work/04-occ/REVIEW-1.md` (verdict FAIL: three MAJOR, three NIT). The
reviewer's reproductions and "smallest fix" notes are your starting point; a better fix is
welcome if you can prove it. You own `internal/occ/` only.

## Must fix (each: a regression test written FIRST that fails on the current code)
1. REVIEW finding 1 — work on hostile input must be bounded. (a) decoders stop
   constructing (`NewVersion`, `NewVersionVector`, `NewRecord`) once an error is set;
   (b) `Descends` is a linear merge scan; (c) `maximal` does not do quadratic work on
   forged siblings that share a dot and context, and `Decode` enforces an explicit
   per-record sibling bound (an exported constant with a stated reason; above it
   `ErrMalformed`) — choose the bound from what honest operation can produce (siblings are
   bounded by concurrent writers; say how the bound relates to cluster size) and make sure
   local writes and `Merge` cannot themselves create a record `Decode` would then refuse
   (state what happens at the bound: the honest path must stay total and lose nothing
   silently — if a merge would exceed the bound, say exactly what is done and why that is
   not silent loss, or prove it cannot happen); (d) the test oracle `checkLaws` is fixed so
   the fuzz target cannot stall (dedupe keys, precompute event sets). Add a test that
   decodes and merges a maximum-size hostile message (the reviewer's shapes, at the
   simulator's `DefaultMaxMessage`) within a stated step/time budget measured in
   operations, not wall time, where possible; add the reviewer's 28 KiB input to
   `testdata/fuzz/FuzzOCCDecode/` as a regression seed.
2. REVIEW finding 2 — HLC observation: observe every stamp actually present (records'
   siblings and scalars, in sorted key order), not one global maximum; an out-of-range
   stamp on one key must not stop in-range stamps on others from being observed; an empty
   document observes nothing; `MaxStamp` reports absence instead of a zero stamp; negative
   skew at start-up works. Use both reviewer reproductions as tests. Check what this does
   to `TestPropHLCMonotonic` and the DST scenarios with skew.
3. REVIEW finding 3 — a write that fails to persist must leave no trace: after `Put`,
   `Delete`, `SetScalar`, `UpdateQuota` or an inbound push returns a disk error, local
   reads, ETags, later conditional writes and replication see the state from before the
   call (persist the candidate before installing it, or snapshot and restore — including
   the actor epoch and the HLC if the write advanced them; say which you chose and why).
   Test with a fake `env.Disk` whose `Write` and, separately, `Sync` fail.
4. REVIEW finding 4 — fuzz seeds that really run under plain `go test` (no seed may SKIP;
   assert it in a test if you can do so cheaply).
5. REVIEW finding 5 — typed write helpers on `*Node` that go through commit, and
   `Node.Store()` no longer hands out a mutable store (make the read-only intent
   impossible to get wrong; this API is not frozen, change it as needed and update tests).
6. REVIEW finding 6 — both hardening items.

## Also
- The orchestrator's first gate run of `FuzzOCCDecode` died with `context deadline
  exceeded`; finding 1 explains it. After your fix run that target for 120 s once and
  record execs and that it ends cleanly.
- Convergence and no-silent-loss claims, the sibling bound and the measured numbers in the
  build notes must be re-measured where your changes can move them.

## Before you stop
Commit, then from the worktree root on the host run `agent_work/gates/gate.sh 04-occ`; it
must end `overall=pass`. Add a "Repair round" section to `agent_work/04-occ/build-notes.md`:
each finding, the test that now proves it, what changed, API changes, numbers that moved,
anything you chose not to do and why. Everything committed (gate result files under
`agent_work/04-occ/gates/` may stay uncommitted). Final message: commit list and a
three-line summary. Do not claim anything you did not run.
