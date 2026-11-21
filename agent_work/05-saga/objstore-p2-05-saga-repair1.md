# Brief — Chunk 5 REPAIR (the one repair round): the Saga engine and bucket migration

Seat: Opus builder (fresh). Worktree: the current directory (branch `chunk/05-saga`). You
are repairing work a previous builder committed. FIRST read
`agent_work/05-saga/objstore-p2-builder-rules.md` (binding: no comments in Go, Docker only,
tests first, commit here, never push), then the original brief
`agent_work/05-saga/objstore-p2-05-saga-build.md`, `agent_work/05-saga/build-notes.md`,
PLAN.md 4.2 "Chunk 5" and 7.8, and the review `agent_work/05-saga/REVIEW-1.md` (verdict
FAIL: two MAJOR, six NIT, one NOTE). The reviewer's reproductions and "smallest fix" notes
are a starting point; a better fix is welcome if you can prove it. You own
`internal/saga/` (including `sagasim`) only. An independent test harness
(`agent_work/adversarial/05-saga/`, 125 checks, all held) exists; you may read and run it,
you do not edit it.

## Must fix (each: a regression test written FIRST that fails on the current code)
1. Finding 1 — the exported matrix runner must work on a cluster that was warmed up
   before the migration starts: times relative to the simulator's current time, the
   bound/settle/policy overridable through `RunOptions` (zero = today's defaults), fault
   hooks scoped to the matrix saga. Prove it with a test harness whose `Build` advances
   virtual time well past the old absolute deadline before returning, run over every cell.
2. Finding 2 — the model store reloads and copies buckets of any size the tests can
   create (no hidden 32-object cliff): a dedicated bounded object-list codec with a stated
   bound that the write path enforces too, so nothing is ever persisted that cannot be
   read back. Round-trip, restart and copy tests above the old limit; move the nemesis
   scenario off the cliff edge.
3. Finding 8 — the matrix runner fails a cell whose fault was never injected (every fault
   kind, torn and double variants), so a later harness that forgets to wire the hooks
   cannot pass silently; nil report pointers are safe.
4. Findings 3, 4, 5, 6, 7 — as the reviewer describes.
5. Finding 9 (NOTE) — write the idempotency-key scope rule into the build notes' section
   for the integration chunk, with the reviewer's point about step 1 vs step 4 copies.

## Also
- Wire codes: the project gives each package its own block so the integrated node can
  join the tables (a duplicate code is rejected): gossip 80–95, storage 96–111, merkle
  112–127, occ 128–143, **saga 144–159**. Saga currently uses 112–114 and sagasim 120–123,
  which collide with merkle: move saga to 144–146 and sagasim to 152–155 (or as fits
  inside 144–159), update goldens that carry codes, and test that every code lies in the
  block.
- Re-measure any number in the build notes your changes can move.

## Before you stop
Commit, then from the worktree root on the host run `agent_work/gates/gate.sh 05-saga`; it
must end `overall=pass`. Then run `agent_work/adversarial/05-saga/run-all.sh` and record
its SUMMARY line (if a check now fails only because it pinned an old code number or old
runner behaviour, say exactly which and why — do not edit the harness). Add a "Repair
round" section to `agent_work/05-saga/build-notes.md`: each finding, the test that now
proves it, what changed, API changes, numbers that moved, anything you chose not to do
and why. Everything committed (gate result files may stay uncommitted). Final message:
commit list and a three-line summary. Do not claim anything you did not run.
