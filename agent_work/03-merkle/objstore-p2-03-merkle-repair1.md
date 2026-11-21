# Brief — Chunk 3 REPAIR (the one repair round): Merkle-tree anti-entropy

Seat: Opus builder (fresh). Worktree: the current directory (branch `chunk/03-merkle`).
You are repairing work a previous builder committed. FIRST read
`agent_work/03-merkle/objstore-p2-builder-rules.md` (binding: no comments in Go, Docker
only, tests first, commit here, never push), then the original brief
`agent_work/03-merkle/objstore-p2-03-merkle-build.md`, `agent_work/03-merkle/build-notes.md`,
PLAN.md 4.2 "Chunk 3" and 7.4, the review `agent_work/03-merkle/REVIEW-1.md` (verdict NITS:
five NIT, two NOTE) and the independent test report
`agent_work/adversarial/03-merkle/ATTACKS.md` with `evidence/run-all.txt` (74 checks: 70
held, 4 BROKEN; you may read and run the harness, you do not edit it). You own
`internal/merkle/` only.

## Must fix (each: a regression test written FIRST that fails on the current code)
1. Review nit 1 — `Worker.Stop()`: after it, no tick, round, deadline or late callback
   does anything; `Start` after `Stop` is defined and tested.
2. Review nit 2 together with BROKEN C8 `single-leaf-reply-size` (a single leaf with
   1,024-byte keys answered with 1,049,541 bytes, over `MaxReplyBytes`): make ONE honest
   statement of the reply-size bound and make every reply kind obey it — inner replies
   (cap the request frontier so the reply fits) and leaf replies. If one leaf's entries
   cannot fit, say what the protocol does (split the leaf across replies, or a stated
   larger hard bound derived from `MaxLeafEntries` × max key size that the integration
   chunk can size its frame limit from). Also bound what `decodeResponse` allocates from
   a small input. A property test checks the stated bound over arbitrary trees and
   requests.
3. BROKEN C12 `batched-round-trip-bound` and C21 `sim-prefix-reply-tight-bound` (three
   round trips where the notes claim at most Depth+1 = 2, when leaf replies are batched
   under the size cap) and C31 `clustered-hash-cost` (49 hashes against the plan's
   16·d·(Depth+1) = 48): the claimed bounds are wrong at the edges. Either tighten the
   code or restate each bound exactly (round trips = levels + extra leaf batches, with
   the formula for the extra batches; the hash bound with its true constant) and make the
   property tests assert the restated bound TIGHTLY, including the reproductions as
   pinned cases. The build notes' claims table must match what the tests assert; review
   note 7 (Depth+1, one per tree level) is part of the same restatement.
4. Review nits 3, 4, 5 — as the reviewer describes (the canary must fail for the planted
   reason; no stats lost at the round deadline; a test that sees `Truncated == true`).
5. Review note 6 — make the "without damage no chunk bytes move" check immune to the
   three-node race the reviewer describes (measure after the initial writes settle).

## Also
- Wire codes: each package has its own block (gossip 80–95, storage 96–111, merkle
  112–127, occ 128–143, saga 144–159). Merkle already sits at 112–113; add a test that
  every merkle code lies in 112–127.
- Re-measure any number in the build notes your changes can move.

## Before you stop
Commit, then from the worktree root on the host run `agent_work/gates/gate.sh 03-merkle`;
it must end `overall=pass`. Then run `agent_work/adversarial/03-merkle/run-all.sh` and
record its SUMMARY line and, for any check still scored BROKEN, why (repaired but the
check pins the old bound, or the claim was restated in writing) — do not edit the
harness. Add a "Repair round" section to `agent_work/03-merkle/build-notes.md`: each item,
the test that now proves it, what changed, API changes, numbers that moved, anything you
chose not to do and why. Everything committed (gate result files may stay uncommitted).
Final message: commit list and a three-line summary. Do not claim anything you did not run.
