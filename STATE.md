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

Status: PHASE 2 (BUILD) RUNNING since 2026-11-21. LANDED: Step 0, 0a, 0b and ALL FIVE
protocol chunks (1–5); book chapters 1–6 are live. IN FLIGHT: Chunk 6 (integration), arbitration fix finished by
Fable; Astra confirm NITS closed; Neon second-eyes FAIL (four majors) fixed by Fable; focused Neon confirm and prose running. Then Chunks 7 and 8 in sequence.

Seats (this project only): Fable orchestrates, gates and lands; Opus writes
product Go (logic only, no comments), fresh agent and worktree per chunk, at
most 3 at once; Gemini 4 Neon reviews Opus and writes all reader-facing prose
once, late, per chunk (book chapter, comments, README, site copy, CLI
help); Astra (gpt-6-astra) writes independent test harnesses per chunk and
arbitrates an Opus/Neon standoff. Mission: /root/briefs/objstore-phase2-mission.md.
Chunk 6 only (Nick, 2026-11-22, two rulings): Fable is the builder of the arbitration fix,
and that fix lands only after two independent passes — Astra's confirm and a Neon
second-eyes review-plus-probing pass (see the Chunk 6 row for the landing rule).

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
| 6 | Integration: node (`internal/node`), real runtime (`internal/noderuntime`), HTTP object API (`internal/storage/httpapi`), cmd/objnode | Built; REVIEW-1 FAIL (1 blocker, 8 majors) → repair → REVIEW-2 FAIL (all 11 fixed; 2 new majors, 4 nits) → fix round → Astra ARBITRATION-1 FAIL (the six fixed; ONE major left: an acknowledged write can end live and unreadable after orphan collection plus an unwritten shard). RULING (Nick, 2026-11-22, /root/briefs/objstore-chunk6-fable-finish-ruling-2026-11-22.md, THIS fix round only): Fable finishes the fix as builder; pipeline is full gate → Astra confirm pass → Neon prose → land; NO Neon review or re-review this round; if the confirm pass finds a defect Fable repairs it and one more confirm pass runs; never Opus again for this task. DONE under it: Opus `fix3` was stopped mid-run; Fable verified its work (absence records: a holder records, bindingly, that it holds no verifiable shard only after every other holder of the stripe answered and fewer than k verify; loss is declared only on more than m such records for one stripe) and added three changes with tests (branch tip 614c439; account: build-notes.md section 14). The first confirm run (`06-integration-confirm`) was stopped by Astra's provider filter with no verdict (ARBITRATION-2.md placeholder removed); its one finished scenario (wiped holder plus a held shard write) is now a test and the residual is stated in section 14; a write-refusal closure was built and withdrawn (it refused the first writes of new clusters). CONFIRM PASS DONE: Astra ARBITRATION-2.md verdict NITS — the major is closed; its one required notes correction is made (b0efdd4, `NITS CLOSED` line appended). AMENDING RULING (Nick, 2026-11-22 ~07:38 CT, /root/briefs/objstore-neon-second-eyes-ruling-2026-11-22.md): Fable stays builder, but the landing now needs TWO independent passes over the fix — Astra's confirm (done) AND a Neon second-eyes pass (independent code review plus its own probing; it must NOT see ARBITRATION-2.md). LANDING RULE for Chunk 6: gate green + Astra NITS closed + NEON-SECOND-EYES.md with no open blocker or major. A Neon blocker or major: Fable fixes it, the gate reruns, ONE focused confirm on the fix (Astra or Neon), no full new cycle unless the findings demand it; nits ride with the landing. SECOND-EYES DONE: NEON-SECOND-EYES.md verdict FAIL — four majors (false absence record on a local disk read error; a finishing rebuild ignoring its holder's absence record; the loss oracle failing migrated versions; an untested safeguard) and two nits; all six accepted and FIXED by Fable on the chunk branch (tests-first commits ending 29b0c12; account: build-notes.md section 15; Neon's 20 scenarios are now `internal/node/secondeyes_*_test.go`, originals under `agent_work/adversarial/06-integration/neon-second-eyes/`). RUNNING: (1) the ONE focused confirm, fresh Neon, run `06-integration-neon2confirm` (`/root/runs/objstore-p2-06-integration-neon2confirm.{log,pid,exit}`; brief /root/briefs/objstore-p2-06-integration-neon2confirm.md) in the isolated worktree `/root/Software/decentralized-object-store-work/06-integration-neon2eyes` (branch `review/06-integration-neon2eyes`, reset to the chunk tip); it writes `agent_work/06-integration/NEON-CONFIRM.md` THERE — copy it to the chunk worktree and commit; then remove that worktree and branch. PASS/NITS → continue to landing; FAIL → Fable fixes, and per the ruling the findings decide whether more than one further confirm is needed. (2) in the chunk worktree: Neon code comments (`06-integration-prose-comments`) and a small chapter revision (`06-integration-prose-chapter-rev`); the chapter draft, the sagas-chapter update and the landing docs (C4 redraw, docs/knowledge/chunk-06-integration.md, INDEX, manifest — drafted by a subagent, to be checked and to gain section 15 facts and the NEON record rows) sit UNCOMMITTED in the chunk worktree. No gate is running: the build-stage gate was stopped when the code changed (G0–G3 had passed on the old tree). NEXT: commit prose by path (trailer `Prose-By: Gemini 4 Neon`) and the four landing-doc commits; rerun the harness (`agent_work/adversarial/06-integration/run-all.sh`) and update RESULTS.md; status prose (README + docs/index.md); rebase on main; `GATE_STAGE=final` gate + prose-check + G7 cluster smoke; land only with gate green AND NEON-CONFIRM clean. Then prose (chapter 07-object-store-and-sync-engine, comments, cmd/objnode help.txt, Chunk 5 chapter update with real-component evidence), landing docs (C4: planned relationships become as-built; knowledge; INDEX; manifest), final gate, land. Harness: 662 held, 0 broken. Gate tooling changes for this chunk are ON THE CHUNK BRANCH (G7 cluster check required by land.sh; `internal/node` in the lint roots; floors and race list; fuzz retry + `fuzz-stall` canary) and were judged sound in REVIEW-2 and ARBITRATION-1; landing them triggers the full canary workflow |
| 7 | E2E suite, nemesis cycles, fuzz hardening | not started (needs 6) |
| 8 | Publishing finish: the assembled book (assembly and polish only) | not started |

## In progress

- Chunk 6: see the board row for the exact next step. Its build brief
  bound: wire-code blocks; last-writer-wins handling in the node (discard counter on the
  health surface, quota recount on discard, the loser's shards collected and never the
  winner's); tombstones through anti-entropy; the saga failure matrix on real components;
  PLAN 2.5 concurrency rules; three canaries; a `cluster` compose smoke script.
- Chunk 6 landing extras beyond the usual: Chunk 5's chapter (book/chapters/06-sagas.md)
  is updated by Neon with the real-component evidence; the C4 relationships still marked
  planned become as-built (or are corrected to what was built); chapter
  `07-object-store-and-sync-engine`; the builder's gate-change requests (cluster smoke in
  the gate, coverage floors for new packages) are the orchestrator's to apply, with a
  canary each.

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

- CI: GREEN on main since 631b44e (2026-11-22). ci.yml runs gates, prose provenance and
  the 33 fast canaries (`CANARY_FAST=1`) on every push in about 18 minutes; the full
  canary set runs from .github/workflows/canaries.yml on tooling changes, weekly and on
  demand (hours; check `gh run list --workflow "Gate canaries"` — the first full run was
  still in progress when this was written). Both tooling changes were reviewed by Neon
  (agent_work/ci-canaries/REVIEW-2.md, NITS closed).
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
  chunk/NN-<slug>. Live: 06-integration. Branches of landed chunks are kept.
- Gate stages: `agent_work/gates/gate.sh <chunk>` (build stage, on a chunk branch, before
  prose) and `GATE_STAGE=final ...` (after prose, and the only form that runs on main).
- Site: https://nickstrad.github.io/decentralized-object-store/ — built by
  .github/scripts/build_site.py through .github/workflows/pages.yml on every push to
  main; the book index lists chapters 1–6.
- C4 set: docs/c4/ (c4-sdk bundle model.json, nine diagram files; 07 is
  07-component-occ.md). Every protocol package is drawn as built; relationships between
  packages and to the object API and node runtime stay planned until Chunk 6 lands.
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
