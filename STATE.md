# STATE — decentralized-object-store

Goal: build Nick's "Decentralized Object Store & Local-First Sync Engine"
(spec: docs/project-source.md) — a leaderless, multi-region object store in
Go (SWIM gossip, Reed-Solomon erasure coding, Merkle-tree anti-entropy,
optimistic concurrency control for metadata with last-writer-wins resolution, Sagas),
designed testing-first.
Plan: [PLAN.md](PLAN.md) — signed off by Nick 2026-11-21; his rulings are in
PLAN.md section 9.1 and override the spec where they differ (notably: OCC
replaces the CRDT layer; SIBLINGS ARE OUT — concurrent versions settle by
last-writer-wins on the hybrid logical clock, every discard counted and logged
(LWW ruling, 2026-11-21 16:14 CT, /root/briefs/objstore-lww-ruling-2026-11-21.md,
folded into PLAN 9.1, 7.5 and Chunk 4); Docker is the one environment; the book is the
learning artifact; C4 diagrams are kept current).

Status: PHASE 2 (BUILD) RUNNING since 2026-11-21. LANDED: Step 0, 0a, 0b, all five protocol
chunks (1–5) and Chunk 6 (integration, f64aa11); book chapters 1–7 are live. IN FLIGHT: Chunk 7
(end-to-end suite and hardening): built, review FAIL, being finished under the seat-swap ruling
(see its row). Then Chunk 8 (publishing finish), normal seats.

Seats (this project only): Fable orchestrates, gates and lands; Opus writes
product Go (logic only, no comments), fresh agent and worktree per chunk, at
most 3 at once; Gemini 4 Neon reviews Opus and writes all reader-facing prose
once, late, per chunk (book chapter, comments, README, site copy, CLI
help); Astra (gpt-6-astra) writes independent test harnesses per chunk and
arbitrates an Opus/Neon standoff. Mission: /root/briefs/objstore-phase2-mission.md.
Chunk 6 only (Nick, 2026-11-22, two rulings): Fable is the builder of the arbitration fix,
and that fix lands only after two independent passes — Astra's confirm and a Neon
second-eyes review-plus-probing pass (see the Chunk 6 row for the landing rule).
Chunk 7's finish only (Nick, 2026-11-22): Neon is the builder, Fable the reviewer, Opus the
adversarial seat; Astra has no Chunk 7 role. Chunk 8 returns to the seats above.

## Chunk board

| Chunk | What it is | State |
|---|---|---|
| Step 0 | Sign-off rulings applied to PLAN.md | LANDED |
| 0a | Foundation: go.mod, Docker environment, gate scripts and canaries, detlint, orchestration scripts, vendored kb, book site on Pages, CI | LANDED 8205edf + c5117c5 |
| 0b | Deterministic simulator (internal/env, internal/sim, simtest, cmd/objsim) | LANDED ceb5438, tag `freeze-0b` (API frozen); chapter 1 |
| 1 | SWIM gossip membership (internal/gossip) | LANDED 3074279; chapter 2; review FAIL → repair → NITS closed; harness 72 held, 1 pins removed behaviour |
| 2 | Reed-Solomon erasure coding and chunk store (internal/erasure, internal/storage) | LANDED c39eb79; chapter 3; review FAIL → repair → NITS closed; harness 41 held, 1 narrowed |
| 3 | Merkle anti-entropy (internal/merkle) | LANDED 2a86f4c; chapter 4; review NITS → repair → PASS; harness 72 held, 2 restated (cost bounds; PLAN 2.2/7.4 updated) |
| 4 | Optimistic concurrency control for metadata, last-writer-wins (internal/occ) | LANDED 9acc7ea; chapter 5; review FAIL → LWW rework → FAIL → round 2 → Astra arbitration NITS closed; harness 60 held, 1 expects exactly one record per loss |
| 5 | Saga engine and bucket migration (internal/saga) | LANDED 4892ab9; chapter 6; review FAIL → repair → NITS closed; harness 125 held |
| 6 | Integration: node (`internal/node`), real runtime (`internal/noderuntime`), HTTP object API (`internal/storage/httpapi`), cmd/objnode | LANDED f64aa11; chapter 7; REVIEW-1 FAIL → repair → REVIEW-2 FAIL → fix → ARBITRATION-1 FAIL (one major) → fix finished by Fable (owner ruling) → ARBITRATION-2 NITS closed → owner-ordered Neon second-eyes pass FAIL (four majors, two nits) → fixed by Fable → focused Neon confirm NITS closed (its one nit was fixed AFTER the confirm; covered by tests and the final gate only); harness 662 held, 0 broken; final gate G0–G7 green at 500 seeds. Records: agent_work/06-integration/ (build-notes sections 14 and 15, NEON-SECOND-EYES.md, NEON-CONFIRM.md) |
| 7 | E2E suite, nemesis cycles, fuzz hardening (`test/e2e`, tier S on the simulator and tier R with real `objnode` processes; `internal/nodetest`) | Built by Opus (exit 0; notes agent_work/07-e2e/build-notes.md; build-stage gate G0–G7 pass, record in agent_work/07-e2e/gates/). Builder reports no product defect. Neon REVIEW-1 FAIL (agent_work/07-e2e/REVIEW-1.md): three majors, all coverage holes in the suite (the `FuzzClusterNemesis` migrating bucket is always empty at 3 nodes per region with k=4, m=2; `finishMigration` treats `ErrTimeout`/`ErrUnreachable` as nothing started; `scenarioE2` never requires a partition-time read or write to succeed), four nits, one note. **OWNER RULING (Nick, 2026-11-22, /root/briefs/objstore-chunk7-seat-swap-ruling-2026-11-22.md): SEATS SWAPPED FOR THIS CHUNK'S FINISH ONLY — Neon finishes as BUILDER in worktree 07-e2e (may write code there), Fable REVIEWS (verdict agent_work/07-e2e/FABLE-REVIEW.md; one fix round back to Neon; a remaining dispute is decided by the orchestrator and recorded), a fresh Opus runs the ADVERSARIAL pass (mutation/canary checks on the three repaired holes first).** The Astra harness run was stopped; its partial output (agent_work/adversarial/07-e2e/, two harness findings: nemesis `settle` accepts any error on GET/tag/delete; `newRealCluster` accepts a node that failed to start) is on record, does NOT gate, and is NOT re-dispatched. The Opus repair round is cancelled. DONE: Neon finish stages 1 and 2 (exit 0; build notes sections 11 and 12; branch tip 81888eb) — all REVIEW-1 findings and both recorded harness findings closed, and a PRODUCT DEFECT found and fixed (f70e2c2: a read short of k shards answered not found/404 instead of unavailable/503). Fable review: round 1 FAIL (one major, two minors, three nits) → Neon's one fix round (exit 0) → round 2 **PASS** (agent_work/07-e2e/FABLE-REVIEW.md; all six closed, both reviewer mutations killed). For the owner's CI ruling Neon also added a short mode (under `go test -short` the 78-cell migration failure matrix runs every eighth cell on one seed; default and sweeps unchanged), and the orchestrator added gate step G5R (the scenarios on real `objnode` processes, with a canary), `GATE_SHORT` for the fast lane, and PLAN 2.9's seed-share wording. Branch rebased on main c1910f6; tip 796e127. Fast lane on this tree: 2 min 26 s locally, green. Opus adversarial pass DONE (exit 0; agent_work/adversarial/07-e2e-opus/, ATTACKS.md and RESULTS.md): 74 held, 9 broken — F1 MAJOR, a SECOND PRODUCT DEFECT (a migration whose coordinator is frozen longer than the orphan grace right after its copy completes and loses acknowledged objects; reproduced by the orchestrator), F2 MAJOR (the suite's printed REPLAY command does not replay seeds outside shard 0), F3–F5 minor; all accepted. Branch tip after records: see `git log chunk/07-e2e`. IN FLIGHT: Neon's fix round for F1–F5, run `07-e2e-advfix` (`/root/runs/objstore-p2-07-e2e-advfix.{log,pid,exit}`, brief agent_work/07-e2e/objstore-p2-07-e2e-advfix.md; a product change in internal/node — expect hours). NEXT, in order: Fable reviews the fix round (append round 3 to FABLE-REVIEW.md; it is product code, so read migrate.go/inventory.go in full and mutate the new rule; a FAIL here is decided by the orchestrator per the seat-swap ruling, with at most one more Neon round); re-run `agent_work/adversarial/07-e2e-opus/run-all.sh` and update its RESULTS.md (F1/F2 lines must read HELD); a short fresh Opus confirm on the F1 fix only if the review leaves doubt; then the full build-stage gate (`agent_work/gates/gate.sh 07-e2e`, over two hours, tree must be clean); prose (Neon: chapter `08-end-to-end-and-hardening` — it must tell both product defects — and comments); landing docs (C4 check, knowledge entry incl. the new migration rule, INDEX rows for ci-fast.sh already on main plus the Opus drivers, manifest); rebase on main; final gate; land; check the fast-lane CI run on the landing push is green and a few minutes. LANDING RULE: full gate green + Fable review PASS/NITS + Opus adversarial with no unaddressed blocker or major; then prose (Neon: chapter `08-end-to-end-and-hardening`, comments; Fable's review covers them), landing docs, final gate, land. Still the orchestrator's: the builder's gate requests (build-notes section 9: tier R as its own gate step after G6 with a canary; confirm or change the per-scenario seed shares — PLAN 2.9 wording must match). OPEN for Nick, not blocking and untouched by the ruling (builder's request 7): metadata is acknowledged with one copy on the writer's disk, so a write acknowledged by a cut-off node is lost without a record if that disk is wiped before it replicates; the orchestrator treats this as the limit OCC already states (Chunk 4) and E7's fault budget is narrowed to match; the alternative is a product change (acknowledge only after one peer holds the write) |
| 8 | Publishing finish: the assembled book (assembly and polish only) | not started |

## In progress

- Chunk 7: see the board row. Its chapter is `08-end-to-end-and-hardening`. Chunk 6 left it:
  docs/knowledge/chunk-06-integration.md (what to reuse, the loss-declaration rule and its
  stated residuals), the 78-cell × 500-seed migration matrix on real components (not yet run
  anywhere), and a possible distinct HTTP status for a lost object (a PLAN table change, not
  made).

## Per-chunk pipeline (as run for chunks 1–5; worked examples in each agent_work/<slug>/)

1. Build exits 0 → commit `agent_work/<slug>/gates/`, rebase on main. The builder's own
   build-stage gate (`counts_for_landing=true`) is accepted; the final gate is re-run.
2. In parallel: Neon review (`mkstage.py <slug> review base=main n=1`) and Astra harness
   (`mkstage.py <slug> adversarial`, THEN `agent_work/orch/soften.sh <brief>` — Astra's
   provider filter cuts off briefs that speak of attacks; softened briefs run normally).
   Commit the harness when Astra stops.
3. One Opus repair round takes the review findings and the broken checks together
   (brief written by hand; examples `objstore-p2-*-repair1.md`). Then: commit gates, rebase,
   orchestrator re-runs `agent_work/adversarial/<slug>/run-all.sh`, writes RESULTS.md (the
   harness rewrites its evidence files — commit them with RESULTS.md BEFORE any rebase),
   Neon re-review (`mkstage.py <slug> rereview base=<sha of the review-1 record commit> n=2`;
   check the generated brief names a real base). NITS → small Opus nit-fix brief → append a
   `NITS CLOSED` line. Second FAIL → Astra arbitration (ARBITRATION-1.md; land.sh takes it
   as the verdict; example agent_work/04-occ/objstore-p2-04-occ-arbitration.md).
4. Prose (Neon, no commits; orchestrator commits by path with trailer `Prose-By: Gemini 4
   Neon`): chapter and comments in parallel once code is final (comments only after the
   last code change); then, after rebasing on current main, status prose (README +
   docs/index.md) while an orchestrator subagent writes the landing docs as four commits
   staged by path: C4 refresh CHECKED AGAINST THE CODE, docs/knowledge entry, INDEX row,
   manifest entry (worked example: the four commits before each landing merge).
5. `GATE_STAGE=final agent_work/gates/gate.sh <slug>` and `agent_work/gates/prose-check.sh
   main HEAD` on the final rebased tree; commit result.json; write the tour message;
   `agent_work/orch/land.sh <slug> chunk/<slug> <message-file>`; verify the chapter URL
   returns 200; remove the worktree (KEEP the branch); STATE; progress line.
- LESSON: do not commit to main between a chunk's final gate and its landing.
- LESSON: a 30 s fuzz run of `FuzzSimSchedule` timed out once in a final gate under
  machine load (`context deadline exceeded`, no failing input); the rerun on the same
  commit passed. Record such a rerun in the result commit message; investigate if it repeats.
- Helpers: `agent_work/orch/launch.sh <run> <opus|neon|astra> <worktree> <ABSOLUTE brief>`,
  `waitany.sh <secs> <run>...`, `mkstage.py` (run inside the worktree), `soften.sh`.

## Open items (none blocks the build)

- CI — OWNER RULING (Nick, 2026-11-22, /root/briefs/objstore-ci-fast-ruling-2026-11-22.md):
  push-triggered CI runs the FAST LANE ONLY. ci.yml runs `agent_work/gates/ci-fast.sh`
  (gates G0, G1, G6 in the dev container: build, static checks, unit tests with coverage
  floors, pinned seeds plus the default three-seed sweep) and prose provenance; about 1.5
  minutes locally on main. A red fast lane is a real blocker. Everything long is on demand
  and NOT deleted (commands in the ci-fast.sh header): the full gate, fuzz budgets, seed
  sweeps, and the gate canaries (canaries.yml is now started by hand only; no push, PR or
  weekly trigger). The full local gate on vm-1 is still the landing authority, unchanged.
  The Chunk 7 branch adds a short mode (`GATE_SHORT=1`, `go test -short`) so the
  end-to-end suite does not push the fast lane past a few minutes; it arrives with the
  landing. Check the landing push's CI time.
  Known and left alone: the full gate cannot finish on a GitHub runner (its host-side
  cluster check cannot write into the container-owned result directory — the cause of the
  last red CI runs on main); it runs on vm-1 as root.
- docs/c4 diagrams 1 and 2 still draw a "Sync client" person marked "not to be built"
  (OQ-7 decided node-level sync only). Removing it renumbers both views — owner: Nick,
  or do it in Chunk 8's final C4 pass.
- Book chapters are accurate but dense (close to compressed specifications). Style
  verdict is Nick's; Chunk 8 is the consistency edit.
- OCC's LWW round 2 was run as the owner-directed amendment round (ruling's process
  note) before arbitration rather than after; recorded in the progress log.
- c4-sdk neighbour elements — owner: Nick, not blocking (docs/c4/README.md, "Model notes").
- CUTOFF: the Claude sign-in on this VM ends between 2026-11-25 and 2026-11-28. Rule
  (OQ-14): stop at the last landed chunk, leave any in-flight chunk unmerged in its
  worktree unless it passes every gate cleanly, make this file an exact resume document,
  write the done marker with STOPPED-AT-CUTOFF. Chunks do not move to other seats.

## Facts a resuming agent needs

- Rulings in force: OCC replaces CRDTs; SIBLINGS ARE OUT — last-writer-wins with every
  discard counted and logged (/root/briefs/objstore-lww-ruling-2026-11-21.md, PLAN 9.1);
  every chunk's chapter goes live at its landing, Chunk 8 is assembly only
  (/root/briefs/objstore-publish-ruling-2026-11-21.md); simulator kernel extraction is
  later, not now (docs/knowledge/sim-kernel-extraction-note.md).
- Wire-code blocks: gossip 80–95, storage 96–111, merkle 112–127, occ 128–143 (131
  unassigned), saga 144–159, node and object API 160–191.
- Briefs: /root/briefs/objstore-p2-<topic>.md. Run logs, pid and exit files:
  /root/runs/objstore-p2-*. Progress log: /root/runs/objstore-phase2-progress.txt.
  Done marker (only at completion or cutoff): /root/runs/objstore-phase2-done.txt.
- Worktrees: /root/Software/decentralized-object-store-work/NN-<slug>, branch
  chunk/NN-<slug>. Live: 07-e2e. Branches of landed chunks are kept.
- Gate stages: `agent_work/gates/gate.sh <chunk>` (build stage, on a chunk branch, before
  prose) and `GATE_STAGE=final ...` (after prose, and the only form that runs on main).
- Site: https://nickstrad.github.io/decentralized-object-store/ — built by
  .github/scripts/build_site.py through .github/workflows/pages.yml on every push to
  main; the book index lists chapters 1–7.
- C4 set: docs/c4/ (c4-sdk bundle model.json, nine diagram files; 07 is
  07-component-occ.md). Every protocol package is drawn as built; redrawn as built at the Chunk 6 landing (a node core implements every package's
  interfaces; no protocol package calls another).
- Neon routes: direct (google/gemini-4-neon via opencode) is primary;
  OpenRouter fallback is out of credits until 2026-12-01. Probe with
  agent_work/orch/seat-probe.sh before each chunk's first Neon stage. Neon (opencode) cannot
  read files outside its worktree: copy each brief into agent_work/<chunk>/ and launch with
  that path. Run
  opencode and codex headless with stdin closed (`</dev/null`).

## Resume

Read this file, PLAN.md, then /root/briefs/objstore-phase2-mission.md; `git
log` and `git worktree list` for what has landed and what is in flight. The
orchestrator is one long headless Fable run (/root/runs/objstore-phase2-run.sh,
log /root/runs/objstore-phase2.log). To tell whether builders are alive:
`pgrep -af 'claude -p|codex exec|opencode run'` and the pid files under
/root/runs/objstore-p2-*.pid. If the orchestrator died, relaunch the run
script; it resumes from this file.
