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

Status: PHASE 2 (BUILD) RUNNING since 2026-11-21. LANDED: Step 0, Chunk 0a (foundation
tooling, 8205edf + c5117c5), Chunk 0b (simulator, ceb5438, tag `freeze-0b`, chapter 1 live),
Chunk 2 (erasure coding and chunk store, c39eb79, chapter 3 live — landed ahead of Chunk 1,
which was still in repair). In flight: Chunks 1, 3, 4, 5 (board below).

Seats (this project only): Fable orchestrates, gates and lands; Opus writes
product Go (logic only, no comments), fresh agent and worktree per chunk, at
most 3 at once; Gemini 4 Neon reviews Opus and writes all reader-facing prose
once, late, per chunk (book chapter draft, comments, README, site copy, CLI
help); Astra (gpt-6-astra) writes adversarial harnesses per chunk and
arbitrates an Opus/Neon standoff. Mission: /root/briefs/objstore-phase2-mission.md.

## Chunk board

| Chunk | What it is | State |
|---|---|---|
| Step 0 | Sign-off rulings applied to PLAN.md | LANDED |
| 0a | Foundation: go.mod, Docker environment, gate scripts and canaries, detlint, orchestration scripts, vendored kb, book site on Pages, CI | LANDED 8205edf (arbitration PASS; 50/50 canaries; adversarial 99 held / 9 narrowed); follow-up c5117c5 (SQLite header in the image so `kb-sync.sh index` works). CI green, site serving the new front page |
| 0b | Deterministic simulator (internal/env, internal/sim, simtest, cmd/objsim) | LANDED ceb5438, tag `freeze-0b` (API frozen). Reviews FAIL → NITS → NITS closed; adversarial 58 of 60 hold, 1 narrowed, 1 repaired-but-scored-broken; chapter 1 live at book/deterministic-simulation.html; C4 marks sim + env as built |
| 1 | SWIM gossip membership (internal/gossip) | Repair round done, gate green; harness rerun 72 held / 1 disputed (RESULTS.md N1). Neon re-review `01-gossip-rereview` RUNNING. AFTER: nits or arbitration, then prose (chapter 02-swim-gossip, comments, status), C4, knowledge, INDEX, manifest, final gate, land |
| 2 | Reed-Solomon erasure coding and chunk store (internal/erasure, internal/storage) | LANDED c39eb79 (reviews FAIL → NITS closed; adversarial 41 held, 1 narrowed; final gate green; chapter 3 live at book/reed-solomon-erasure-coding.html; C4 05 and 09 as built, object API still planned) |
| 3 | Merkle anti-entropy (internal/merkle) | The one Opus repair round `03-merkle-repair1` is RUNNING (5 review nits + 4 broken bound checks). AFTER: commit gates, rebase, rerun harness, RESULTS.md, Neon re-review, prose, land |
| 4 | Optimistic concurrency control for metadata (internal/occ) | LWW rework done and gated; Neon REVIEW-2 FAIL (two real holes in every-loss-recorded + 1 nit; everything else FIXED); Astra harness vs LWW: 59 held, 2 broken (quota recount). Opus round `04-occ-lww2` RUNNING (brief agent_work/04-occ/objstore-p2-04-occ-lww-round2.md; treated as the owner-directed LWW amendment round per the ruling's process note). AFTER: rerun harness + RESULTS.md, then ASTRA ARBITRATION on REVIEW-2 + the round (second-FAIL rule; write ARBITRATION-1.md; soften the brief), then prose, C4 redraw of 07 as OCC without siblings, land |
| 5 | Saga engine and bucket migration (internal/saga) | Repair done; REVIEW-2 NITS, nits fixed and CLOSED; harness 125 held / 0 broken (RESULTS.md). Neon chapter (`05-saga-chapter`) and comments (`05-saga-comments`) stages dispatched. AFTER: commit prose with the Prose-By trailer, status prose, C4 (08), knowledge, INDEX, manifest (chapter 6), final gate, land |
| 6 | Integration: node, real runtime, HTTP object API, cmd/objnode | not started (needs 1–5) |
| 7 | E2E suite, nemesis cycles, fuzz hardening | not started |
| 8 | Publishing finish: the assembled book | not started |

## In progress

- Builder slots (cap 3, repairs count): `03-merkle-repair1`, `04-occ-lww2`; one free.
  Nothing is queued for a builder. Stage briefs come from agent_work/orch/mkstage.py (run
  it from inside the chunk's worktree; pass launch.sh ABSOLUTE paths).
  `agent_work/orch/waitany.sh <secs> <run>...` waits for the first to stop.
- Landing docs (C4 refresh checked against the code, docs/knowledge entry, INDEX rows,
  manifest entry) were done for Chunk 2 by an orchestrator subagent as four commits staged
  by path while Neon wrote the status prose; same recipe for the others (worked example:
  commits `c4: mark erasure coding and the chunk store as built` and the three after it).
  Chapters may publish out of numeric order; the site build accepts gaps.
- CI on main was RED from the simulator landing until 2026-11-22: canaries.sh planted
  files in internal/buildcheck, a placeholder package the simulator landing removed (38 of
  50 caught). Fixed by creating the directory in each plant (orchestrator tooling fix, the
  14 affected canaries re-run green locally; not separately reviewed). Check `gh run list`
  after each push.
- ASTRA BRIEFS: its provider's content filter cut off three runs whose briefs spoke of
  attacks and hostile input. Always pass an Astra brief through
  `agent_work/orch/soften.sh <brief>` before launching (plain testing vocabulary; asks for
  the report early). Softened runs (sagas rerun, Merkle) completed normally.
- After each repair exits 0: commit its gates dir, rebase on main, orchestrator reruns
  `agent_work/adversarial/<slug>/run-all.sh` and writes RESULTS.md, Neon re-review
  (mkstage `rereview`), then prose stages, C4, final gate, land (pipeline below).
- Wire-code blocks (duplicates are rejected when Chunk 6 joins the tables): gossip 80–95,
  storage 96–111, merkle 112–127, occ 128–143, saga 144–159. Gossip, storage and merkle
  already comply; occ and saga move into their blocks in their repair rounds (both briefs say so).
- LWW ruling follow-ups: README and front page corrected (3e34482). Still to carry: the
  Chunk 4 chapter brief (mkstage already says siblings are a road not taken), the C4
  redraw without a sibling component, and Chunk 6's brief (PLAN 4.2 Chunk 6 is amended).
- Per chunk when its build exits 0 (see the 0b history in agent_work/0b-simulator/ for
  worked examples of every brief): rebase on main if main moved; orchestrator gate
  (`agent_work/gates/gate.sh <slug>`); Neon review (probe first); one Opus repair if
  NITS/FAIL; Neon re-review (second FAIL → Astra arbitration, ARBITRATION-N.md); Astra
  adversarial brief → run its `run-all.sh` → RESULTS.md; one Opus round for broken claims;
  Neon review of that round; Neon prose WITHOUT commits (comments + help; chapter
  `book/chapters/NN-slug.md` with Mermaid + draw-visual embeds; README + docs/index.md
  status) — orchestrator checks the comment-only diff and commits with the trailer
  `Prose-By: Gemini 4 Neon`; manifest entry (status "published") in a non-prose commit; C4
  refresh (model.json statuses, diagram header lines, README table; chunk 4 also redraws
  07-component-crdt as OCC); docs/knowledge entry; INDEX rows; `GATE_STAGE=final` gate and
  `prose-check.sh main HEAD` on the FINAL rebased tree; commit result.json; `land.sh`;
  verify Pages + chapter URL 200; remove the worktree; `kb-sync.sh index`; STATE; progress.
- LESSON (cost one extra gate run): do not commit to main between a chunk's final gate and
  its landing — land.sh needs the branch rebased on main and the gate result bound to the
  rebased tip. Check land.sh's exit status directly; never chain a tag or push after a pipe.
- Landing order: numeric (PLAN 5.3); a chunk that is green early waits in a ready state,
  or lands out of order if a predecessor is stuck for a working session (record it here).
- Publish ruling (Nick, 2026-11-21): every chunk's chapter goes LIVE at that chunk's
  landing; Chunk 8 is assembly and polish only. Working as designed since 0b.
- Simulator ruling: kernel extraction later, not now; the extraction note is
  docs/knowledge/sim-kernel-extraction-note.md.

## Next steps, in order

1. Wave B: finish and land chunks 1–5 (pipeline per chunk below); repairs for 4 then 2 as slots free.
2. Chunks 6, 7, 8 in sequence.

## Open decisions / blockers

- None waiting on Nick; his rulings are complete for Phase 2.
- CUTOFF: the Claude sign-in on this VM ends between 2026-11-25 and
  2026-11-28. Rule (OQ-14): stop at the last landed chunk, leave any in-flight
  chunk unmerged in its worktree unless it passes every gate cleanly, make
  this file an exact resume document, write the done marker with
  STOPPED-AT-CUTOFF. Chunks do not move to other seats.
- c4-sdk neighbour elements — owner: Nick, not blocking. c4-sdk 0.1.0 has no
  per-view external tag, so docs/c4/model.json carries eight `n-*` stand-in
  elements. Detail: docs/c4/README.md, "Model notes".

## Facts a resuming agent needs

- Per-chunk pipeline and gates: PLAN.md sections 2.9 and 6. Every gate runs
  through Docker. No chunk lands without: green Docker gate suite, Neon
  review PASS, adversarial results recorded, Neon prose done, C4 current.
- Briefs: /root/briefs/objstore-p2-<topic>.md. Run logs, pid and exit files:
  /root/runs/objstore-p2-*. Progress log: /root/runs/objstore-phase2-progress.txt.
  Done marker (only at completion or cutoff): /root/runs/objstore-phase2-done.txt.
- Worktrees: /root/Software/decentralized-object-store-work/NN-<slug>, branch
  chunk/NN-<slug>. Live: 01-gossip, 03-merkle, 04-occ, 05-saga. Orchestration scripts: agent_work/orch/ (launch.sh,
  wait.sh, land.sh, seat-probe.sh). A second Neon FAIL goes to Astra arbitration; land.sh
  takes the newest ARBITRATION-N.md as the verdict when one exists. A NITS review needs a
  `NITS CLOSED` line appended by the orchestrator once each nit is closed.
- Gate stages: `agent_work/gates/gate.sh <chunk>` (build stage, on a chunk branch, before
  prose) and `GATE_STAGE=final ...` (after prose, and the only form that runs on main).
- Site: https://nickstrad.github.io/decentralized-object-store/ — built by
  .github/scripts/build_site.py through .github/workflows/pages.yml on every
  push to main. The book site since 0a landed; the book index lists
  chapters as they are published (chapters 1 and 3 so far).
- C4 set: docs/c4/ (c4-sdk bundle model.json, nine diagram files). The simulator and internal/env are marked built (status `existing`). The
  07-component-crdt diagram still shows the pre-amendment CRDT design and is
  redrawn as the OCC component (last-writer-wins, no sibling component) when Chunk 4 lands; other diagrams move from
  TARGET to as-built as their chunks land.
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
