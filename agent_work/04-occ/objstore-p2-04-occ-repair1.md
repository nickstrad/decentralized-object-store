# Brief — Chunk 4 REPAIR (the one repair round) + OWNER RULING: last-writer-wins replaces siblings

Seat: Opus builder (fresh). Worktree: the current directory (branch `chunk/04-occ`). You
are repairing and reworking code a previous builder committed. FIRST read
`agent_work/04-occ/objstore-p2-builder-rules.md` (binding: no comments in Go, Docker only,
tests first, commit here, never push), then PLAN.md 4.2 "Chunk 4", 7.5 and the "LWW ruling"
paragraph in 9.1 (PLAN.md on this branch already carries the ruling), then
`agent_work/04-occ/build-notes.md` and the review `agent_work/04-occ/REVIEW-1.md` (verdict
FAIL: three MAJOR, three NIT). The original build brief
`agent_work/04-occ/objstore-p2-04-occ-build.md` is background only: wherever it or the
build notes speak of siblings, Part A below overrides them. You own `internal/occ/` only.

This round does two things in one pass: Part A, an owner design ruling that changes what
the package does; Part B, the review findings, restated for the new design. Do Part A's
design first — it removes most of finding 1's surface.

## Part A — the owner's ruling (binding): no siblings, last-writer-wins, loss on the record

The project owner ruled on 2026-11-21 that concurrent versions are NOT kept as siblings.
The rule the chunk now proves: **replicas always converge to one single version per key,
and every concurrent write that loses is counted and logged — never invisible.**

A1. **One version per key.** A record holds exactly one version (a value or a tombstone)
    or is absent. There is no sibling set anywhere: not in memory, not in the encoding, not
    in the API. `*ConflictError`, `ErrConflict` and its wire code go away (keep the wire
    code number unassigned rather than renumbering the others, and say so in the notes);
    `Get` returns the one version or `ErrNotFound`. `Decode` accepts exactly one version
    per record and nothing else, so no sibling bound is needed and the quadratic
    `maximal` path of review finding 1 is deleted, not patched.
A2. **The winner is a pure function of the two versions.** When two different versions of
    one key meet (in `Merge`, an inbound push, a digest reply, anywhere), the winner is the
    larger under ONE total order: hybrid logical clock stamp `(wall, logical)`, then node
    ID, then — only so the order is total on forged input where two different versions
    carry an identical stamp — version ID bytes. Nothing else may enter the decision: not
    arrival order, not which side is local, not the version vectors. Because it is a
    maximum under a total order, merge is commutative, associative, idempotent and total
    on EVERY decoded state, hostile ones included, and two replicas holding the same set
    of versions are byte-identical. The same rule serves scalar fields; whether records
    and scalars now share one implementation is your call (say which and why).
A3. **Causality must agree with the clock on the honest path.** A local write that replaces
    the version its node holds must carry a stamp strictly greater than that version's
    stamp (the HLC already observes what the node has seen; make this an explicit, tested
    invariant: `TestPropCausalWriteAlwaysWins` — a write that saw version X always beats X
    under the A2 order, under any clock skew the simulator allows). State what happens
    when the held version carries a stamp beyond `MaxClockLead` (a forged or wildly skewed
    far-future stamp): an accepted, acknowledged local write must never lose to the
    version it just replaced on its own node — either the write takes a stamp above it or
    the write is refused with a typed error; choose, justify, test. Interacts with review
    finding 2.
A4. **Version vectors stay, for detection only.** Keep the dot + causal context on each
    version. They no longer decide the winner; they classify each replacement as either
    *causal* (the winner's event set includes the loser's: the winner's writer had seen
    the loser — an ordinary overwrite, nothing lost) or *concurrent* (neither includes the
    other: an LWW discard — an acknowledged write lost to a concurrent write). If the
    larger-stamp version is causally BELOW the smaller-stamp one (possible only on forged
    input, by A3), the A2 order still decides; classify that as a discard too and say so.
A5. **Every LWW discard is counted and logged.** Each time a node drops a version in a
    concurrent replacement it (a) increments a monotonic per-node counter exposed on the
    store and the node (e.g. `LWWDiscards()`), and (b) emits one log record naming the
    key, the losing version ID, its stamp and writer, and the winning version ID and
    stamp. Use the logging seam the package already has or `internal/env` offers; if there
    is none, add an injectable sink in the node config (deterministic: no wall time in it)
    and keep a bounded in-memory tail readable by tests. Also offer an optional callback
    hook (`OnDiscard(key, winner, loser)`) for Chunk 6. The counter and the log are local
    observations, NOT part of the replicated document — the document's bytes must not
    depend on how many discards a node saw. Counter overflow saturates. A failed persist
    (finding 3) must not leave a counted discard for a merge that did not take effect.
A6. **Preconditions are unchanged as an API contract.** `If-Match: <etag>` holds iff the
    key's one version has that ETag (a tombstone has an ETag); `If-None-Match: *` holds
    iff the key is absent or a tombstone; a failed precondition returns
    `ErrPreconditionFailed` and changes nothing. The ETag is now a deterministic function
    of the single version (equal on every node holding that version). An unconditional
    write simply replaces what the node holds, as a causal successor of it (the node has
    seen it): that is an ordinary overwrite, not a discard. Two writers racing on ONE node
    with the same ETag: exactly one wins, the other gets `ErrPreconditionFailed`. Across a
    partition both are acknowledged; after heal every replica holds the same single A2
    winner and the loser's discard is on at least one node's record.
A7. **Deletes** are tombstone versions and compete under A2 like any write: a delete can
    discard a concurrent put and the reverse; either way it is counted and logged.
A8. **Quota counters** stay check-and-set (`If-Match`, bounded retry, never applying a
    stale read). Partition-side concurrent usage updates now settle by A2, so the stored
    usage can be wrong after a heal: keep the recount hook, drive it from the discard of
    a quota key (A5's callback) instead of from "siblings present", and make the recount
    write nothing when the stored value is already what the recount computed (otherwise
    recounting nodes would discard each other's identical recounts forever — prove it
    settles in the quota DST). Overshoot under partition remains a stated, measured
    trade-off.
A9. **The wiped-node epoch guarantee stays** (it still protects classification: a reused
    actor counter must not make a concurrent discard look causal). Re-read
    `TestWipedNodeTakesAFreshEpoch` under the new design: after the heal one version wins
    by A2 and, if the wiped node's new write loses or wins against `(A,2)`, the discard is
    recorded as concurrent — and with the hazard switch on it is misclassified as causal
    (a loss with no record). Keep the test able to see the hazard.

### Tests for Part A (write them FIRST; rename or replace the sibling tests)
- unit: conditional put/delete tables without sibling rows; `TestETagDeterministic`;
  `TestLWWWinnerTable` (stamp, node tie-break, version-ID tie-break on forged equal stamps,
  tombstone vs value both ways); `TestLWWDiscardCountedAndLogged`;
  `TestCausalOverwriteIsNotADiscard`; decode table rejects any multi-version record.
- property (rapid): `TestPropMergeLaws` (commutative, associative, idempotent, and the
  result is the single A2 maximum, on honest AND arbitrary decoded states);
  `TestPropConvergence` (random, duplicated, reordered delivery → identical bytes, ETags);
  `TestPropCausalWriteAlwaysWins` (A3); `TestPropEveryLossRecorded` — replaces
  `TestPropNoSilentLoss`: random conditional writes on several replicas with random
  merges; every acknowledged version is, at the end, (i) the key's current version on all
  replicas, or (ii) causally superseded by an acknowledged version whose writer saw it, or
  (iii) named as the loser in at least one node's discard log — with an independent
  oracle, not the product's own classifier; `TestPropQuotaNoStaleApply`;
  `TestPropHLCMonotonic`. Drop `TestPropSiblingsOnlyWhenConcurrent`; its replacement is
  `TestPropNoDiscardWithoutConcurrency` (causally ordered writes never count a discard).
- fuzz: `FuzzOCCConditionalWrite` (headline), `FuzzOCCMergeLaws` (laws + single winner on
  arbitrary decoded A, B, C), `FuzzOCCDecode`.
- DST: `TestMultiRegionConvergenceDST` (byte-identical metadata after heal; exactly one
  version per key; the every-loss-recorded check across all nodes' discard logs; total
  discards reported); `TestOfflineRegionDST` (the long-offline region's writes either win
  or are recorded as discarded — assert both kinds occurred, i.e. the scenario really
  contested keys); `TestConditionalWriteRaceDST` (A6); the quota DST (A8).
- canaries: replace `TestCanaryMergeDropsSibling` with `TestCanaryDiscardNotRecorded` (a
  planted bug that discards without counting/logging must fail the suite) and
  `TestCanaryMergePrefersLocal` (a planted arrival-order-dependent winner must fail
  convergence).

## Part B — the review findings, as they stand after Part A
Each needs a regression test written first that fails on the current code (or, where
Part A deletes the code in question, a test of the new behaviour that covers the hazard).
1. Finding 1 — bounded work on hostile input. (a) decoders stop constructing once an
   error is set; (b) `Descends`/event-set inclusion is a linear merge scan; (c) moot by
   A1 — prove it: a test decodes and merges maximum-size hostile messages (many keys, big
   contexts; at the simulator's `DefaultMaxMessage`) within a budget counted in
   operations, not wall time, where possible, and work is linear in message size; (d) the
   test oracle `checkLaws` cannot stall (dedupe keys, no per-pair map building). Add the
   reviewer's 28 KiB input shape to `testdata/fuzz/FuzzOCCDecode/` as a seed (it must now
   be REJECTED as malformed — a multi-version record).
2. Finding 2 — HLC observation: observe every stamp actually present, in sorted key
   order, not one global maximum; an out-of-range stamp on one key must not stop in-range
   stamps on other keys from being observed; an empty document observes nothing;
   `MaxStamp` reports absence instead of a zero stamp; negative skew at start-up works.
   Both reviewer reproductions become tests. Reconcile with A3.
3. Finding 3 — a write that fails to persist leaves no trace: after `Put`, `Delete`,
   `SetScalar`, `UpdateQuota` or an inbound push returns a disk error, local reads,
   ETags, later conditional writes, replication, the discard counter and the discard log
   show the state from before the call (say whether you persist-then-install or
   snapshot-and-restore, including the actor epoch and the HLC). Test with a fake
   `env.Disk` whose `Write` and, separately, `Sync` fail.
4. Finding 4 — fuzz seeds that really run under plain `go test` (no seed may SKIP).
5. Finding 5 — typed write helpers on `*Node` that go through commit; `Node.Store()` no
   longer hands out a mutable store.
6. Finding 6 — `Node.peers()` sorts before drawing; the quota DST asserts a quota key was
   really contested (a discard on it was recorded) before the recount.

## Also
- Wire codes: the project now gives each package its own block so the integrated node can
  join the tables (a duplicate code is rejected): gossip 80–95, storage 96–111, merkle
  112–127, **occ 128–143**, saga 144–159. Move this package's codes into 128–143 (the
  retired conflict code's slot stays unassigned), update goldens that carry codes, and
  test that every code lies in the block.
- The orchestrator's first gate run of `FuzzOCCDecode` died with `context deadline
  exceeded`; finding 1 explains it. After your changes run that target for 120 s once and
  record execs and that it ends cleanly.
- Rewrite `agent_work/04-occ/build-notes.md` so it describes the package AS IT NOW IS
  (it is the source the book chapter is written from): the LWW design and its trade-off
  in plain words — an acknowledged write can be lost to a concurrent write under
  partition; the system guarantees convergence and a record of the loss, not
  preservation — the claims table restated (no claim may mention siblings as the design),
  measured numbers re-measured (discards per partitioned workload, quota overshoot,
  merge cost), then a "Repair round" section: each ruling item and finding, the test that
  proves it, API changes, anything you chose not to do and why.

## Before you stop
Commit, then from the worktree root on the host run `agent_work/gates/gate.sh 04-occ`; it
must end `overall=pass`. Everything committed (gate result files under
`agent_work/04-occ/gates/` may stay uncommitted). Final message: commit list and a
three-line summary. Do not claim anything you did not run.
