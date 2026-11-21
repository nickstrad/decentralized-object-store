# Brief — Chunk 1 REPAIR (the one repair round): SWIM gossip membership

Seat: Opus builder (fresh). Worktree: the current directory (branch `chunk/01-gossip`).
You are repairing work a previous builder committed. FIRST read
`agent_work/01-gossip/objstore-p2-builder-rules.md` (binding: no comments in Go, Docker
only, tests first, commit here, never push), then the original brief
`agent_work/01-gossip/objstore-p2-01-gossip-build.md`, `agent_work/01-gossip/build-notes.md`,
PLAN.md 4.2 "Chunk 1" and 7.7, the review `agent_work/01-gossip/REVIEW-1.md` (verdict FAIL:
three MAJOR, four NIT, one NOTE) and the adversarial evidence
`agent_work/adversarial/01-gossip/evidence/run-all.txt` (73 attacks: 69 held, 4 BROKEN;
harness code in that directory — you may read and run it, you do not edit it). The
reviewer's reproductions and "smallest fix" notes are a starting point; a better fix is
welcome if you can prove it. You own `internal/gossip/` only.

## Must fix (each: a regression test written FIRST that fails on the current code)
1. Review finding 1 — dead and left entries must really leave every view: in a cluster of
   several nodes they are purged on every survivor and STAY absent (multi-node DST past
   3 × `DeadRetention`, for a crash and for a leave), with no periodic re-gossip of them.
   Design this together with items 4 and 5 below — they pull against each other
   (forgetting a dead member vs. never letting old news resurrect it vs. finding the
   other side after a long partition). Write the resulting rule down in the build notes
   as one coherent "lifetime of a dead entry" section.
2. Review finding 2 — bounded relay state and work under a ping-req flood (prune on
   arrival, a hard cap with a counter, no relay kept for a ping that was not sent, no
   quadratic ack scan).
3. Review finding 3 — the view can never grow past what one message can carry
   (`MaxUpdates`), `piggyback` respects the same bound, and no callback runs inside the
   call that registered it (audit every callback-taking method and every early-error
   path, not only `syncWith`).
4. Adversarial BROKEN C4 `retention-monotonicity` and C23 `stale-replay-after-purge`
   (seed 901): after `dead(x, 9)` is purged, a replayed `alive(x, 0)` is accepted, so a
   member's incarnation goes backwards and a dead verdict is overturned by old news. The
   build notes present "after purge, alive(0) is accepted as new" as intended (a wiped
   node rejoining at incarnation 0 must get back in). Resolve the tension and state the
   exact guarantee: either keep a bounded, compact floor (highest incarnation seen per
   purged ID, with a stated cap and eviction rule, and the wiped-node rejoin still works
   because a live node refutes upward past the floor), or narrow the published claims
   (incarnation monotonic only within the retention window) and pin the limit with a
   test. Prefer the fix if it is bounded and simple; whichever you choose, the claims
   table must say exactly what holds.
5. Adversarial BROKEN C19 `partition-heal-retained-false` (seed 1401) and
   `default-retention-long-partition` (seed 1405): after a partition longer than
   `DeadRetention`, two halves that joined through `Join(seeds)` (no `Config.Seeds`)
   never find each other again. The control with a configured seed heals in 1.75 s. At
   minimum, seeds given to `Join` are remembered and used by the heal path exactly like
   `Config.Seeds`. Beyond that decide and state: what a cluster needs for a guaranteed
   re-merge after an arbitrarily long partition (a reachable seed on the other side),
   and what happens without one. PLAN 7.7's claim must end up true as worded in your
   notes, with a DST for the long partition (longer than `DeadRetention`) that heals.
6. Review findings 4, 5, 6, 7 — as the reviewer describes (finding 6: no fuzz seed may
   SKIP under plain `go test`). Finding 8: correct the build notes.

## Also
- Wire codes: the project gives each package its own block (gossip 80–95, storage
  96–111, merkle 112–127, occ 128–143, saga 144–159). Gossip already sits in 80–83; add a
  test that every gossip code lies in 80–95.
- The package's 500-seed sweep took about 12 minutes in the gate on the shared machine.
  If your new multi-node DSTs add to it, keep the default-seed run and the sweep inside
  the builder rules' budgets (cut per-seed cost, not coverage; say what you did).
- Re-measure every number in the build notes your changes can move (detection time,
  convergence after heal, message cost per node per period).

## Before you stop
Commit, then from the worktree root on the host run `agent_work/gates/gate.sh 01-gossip`;
it must end `overall=pass`. Then run `agent_work/adversarial/01-gossip/run-all.sh` and
record its SUMMARY line and any attack still scored BROKEN (with why: repaired but the
driver expects the old behaviour, or narrowed in writing) — do not edit the harness. Add
a "Repair round" section to `agent_work/01-gossip/build-notes.md`: each finding and broken
attack, the test that now proves it, what changed, API changes, numbers that moved,
anything you chose not to do and why. Everything committed (gate result files may stay
uncommitted). Final message: commit list and a three-line summary. Do not claim anything
you did not run.
