# Brief — Chunk 4, LWW amendment round 2: close the holes in "every loss is recorded"

Seat: Opus builder (fresh). Worktree: the current directory (branch `chunk/04-occ`).
FIRST read `agent_work/04-occ/objstore-p2-builder-rules.md` (binding: no comments in Go,
Docker only, tests first, commit here, never push), then PLAN.md 4.2 "Chunk 4", 7.5 and
the LWW ruling in 9.1, `agent_work/04-occ/build-notes.md` (the package as it is), the
previous brief `agent_work/04-occ/objstore-p2-04-occ-repair1.md` (Part A is the binding
design), the review `agent_work/04-occ/REVIEW-2.md` (FAIL: two MAJOR, one NIT — everything
else FIXED; read "New findings" closely, the reproductions are exact) and the independent
test report `agent_work/adversarial/04-occ/ATTACKS.md` with `evidence/run-all.txt` (61
checks: 59 held, 2 BROKEN; read and run the harness, do not edit it). You own
`internal/occ/` only.

The owner's rule this round defends: an acknowledged write may be lost to a concurrent
write, but NEVER invisibly — every such loss is counted and logged. The review found two
honest schedules where a loss leaves no record. Both must be closed in the code, not
narrowed in the notes.

## Must fix (each: a regression test written FIRST that fails on the current code)
1. REVIEW-2 finding 1 — a version's causal clock must never claim a dot its writer did
   not see. After a node's own version is discarded, its later writes currently inflate
   their context over the discarded dot, so another replica still holding the lost
   version classifies its replacement as a causal overwrite and records nothing. The
   reviewer's fix (retire the actor epoch when a discard leaves a gap) is one option;
   an exact context that does not inflate over gaps is another. Choose, justify, and
   prove: the reviewer's `next2` reproduction as a test; `TestDotsAreNeverReused` extended
   past two writes; and a property over random multi-replica histories with an
   INDEPENDENT oracle (the existing `oracle` style: built from what each write actually
   held, never from the product's vectors) asserting that the product's causal/concurrent
   classification equals the oracle's for every replacement on every replica.
2. REVIEW-2 finding 2 — a node that receives a concurrent version and rejects it in
   favour of the one it holds has discarded an acknowledged write and must record it
   (counter, log line, `OnDiscard`), exactly as when it drops a version it held. Otherwise
   a writer that crashes for good, is wiped, or loses its state blob after delivering its
   version takes the only record with it. Design points you must settle and state:
   (a) repeated delivery — anti-entropy re-sends the same losing version until the sender
   catches up; the same (key, losing version) must not be counted again and again on one
   node: bounded de-duplication, with its bound and what happens beyond it stated;
   (b) the same loss is now recorded on several nodes — say so, make each log record
   carry the losing version ID so records can be joined, and restate what the counter
   means (discard events observed by this node) versus distinct lost versions;
   (c) an incoming version that is causally BELOW the held one (old news) is not a loss
   and records nothing — test it; (d) persist-failure rollback still leaves no trace.
   Tests: the reviewer's two-node reproduction for crash, wipe-and-rejoin and state-blob
   rot (after the heal at least one surviving node's record names the lost version); and
   `TestPropEveryLossRecorded` and the multi-region DST strengthened so that wipes and
   permanent crashes of a writer AFTER its version reached any peer are in the schedule
   and the every-loss-recorded check still passes on the surviving nodes. State the one
   remaining limit exactly (a write acknowledged by a node whose disk is lost before the
   version reached any other node is lost with that disk and has no record: disk loss,
   not a concurrency loss) and test that this is the ONLY unrecorded case the oracle
   allows.
3. REVIEW-2 finding 3 — the record must survive a crash between making the merge durable
   and publishing the log line and callback: persist the pending discard records with
   the state (a bounded outbox drained on open and after each publish) so a restarted
   node emits them; make the readable tail durable the same way or state why not. Test
   with a disk that lets the sync succeed and then a restart before publish.
4. Independent test report, BROKEN C23 `recount-overflow` (two objects of sizes
   MaxUint64 and 1 recount to bytes=0 with no error): totals saturate or return a typed
   error — never wrap. Audit every sum over sizes and counts in the quota path.
5. Independent test report, BROKEN C17 `recount-stale-noop` (the recount hook's result
   equals the value read first, but the stored value changed in between: the no-op path
   skips the staleness check and the stale figure stands): the "write nothing when
   already right" shortcut must compare against the CURRENT stored version under the
   same check-and-set discipline as a write (retry on change, bounded).

## Also
- The canaries must still bite: add one for item 2 (a planted "reject incoming loser
  silently" switch must fail the suite) and keep `TestCanaryDiscardNotRecorded`.
- Rewrite the affected parts of `agent_work/04-occ/build-notes.md` so the guarantee is
  stated exactly as the tests now prove it (the book chapter is written from these
  notes): where a loss is recorded, what the counter counts, the one remaining limit.
  Re-measure discards per partitioned workload (events and distinct lost versions) and
  anything else that moved. Add a "LWW round 2" section: each item, the test that proves
  it, what changed, API changes, what you chose not to do and why.

## Before you stop
Commit, then from the worktree root on the host run `agent_work/gates/gate.sh 04-occ`; it
must end `overall=pass`. Then run `agent_work/adversarial/04-occ/run-all.sh` and record
its SUMMARY line (and why, for any check still scored BROKEN). Everything committed (gate
result files may stay uncommitted). Final message: commit list and a three-line summary.
Do not claim anything you did not run.
