# STATE — decentralized-object-store

Goal: build Nick's "Decentralized Object Store & Local-First Sync Engine"
(spec: docs/project-source.md) — a leaderless, multi-region object store in
Go (SWIM gossip, Reed-Solomon erasure coding, Merkle-tree anti-entropy,
optimistic concurrency control for metadata, Sagas), designed testing-first.
Plan: [PLAN.md](PLAN.md) — signed off by Nick 2026-11-21; his rulings are in
PLAN.md section 9.1 and override the spec where they differ (notably: OCC
replaces the CRDT layer; Docker is the one environment; the book is the
learning artifact; C4 diagrams are kept current).

Status: PHASE 2 (BUILD) RUNNING since 2026-11-21. LANDED: Step 0, Chunk 0a (foundation
tooling, 8205edf + c5117c5), Chunk 0b (simulator, ceb5438, tag `freeze-0b`, book chapter 1
live). Wave B is building: Chunks 1, 2 and 4 (Opus), then 3 and 5 as slots free.

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
| 1 | SWIM gossip membership (internal/gossip) | Opus build running (`01-gossip-build`, worktree `01-gossip`) |
| 2 | Reed-Solomon erasure coding and chunk store (internal/erasure, internal/storage) | Opus build running (`02-erasure-build`, worktree `02-erasure`) |
| 3 | Merkle anti-entropy (internal/merkle) | not started; brief ready, waits for a builder slot (cap 3) |
| 4 | Optimistic concurrency control for metadata (internal/occ) | Opus build running (`04-occ-build`, worktree `04-occ`) |
| 5 | Saga engine and bucket migration (internal/saga) | not started; brief ready, waits for a builder slot (cap 3) |
| 6 | Integration: node, real runtime, HTTP object API, cmd/objnode | not started (needs 1–5) |
| 7 | E2E suite, nemesis cycles, fuzz hardening | not started |
| 8 | Publishing finish: the assembled book | not started |

## In progress

- Wave B builds (Opus, headless, dispatched 2026-11-21 after the freeze): `01-gossip-build`,
  `02-erasure-build`, `04-occ-build`; briefs /root/briefs/objstore-p2-<slug>-build.md (also
  copied into each worktree's agent_work/<slug>/). Briefs for 03-merkle and 05-saga are
  written (same folder; generator agent_work/orch/mkbriefs.py) and are dispatched as slots
  free: `git worktree add -b chunk/<slug> ../decentralized-object-store-work/<slug> main`,
  copy the brief and /root/briefs/objstore-p2-builder-rules.md into agent_work/<slug>/,
  commit, `agent_work/orch/launch.sh <slug>-build opus . <brief>`.
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

1. Wave B: finish and land chunks 1, 2, 4; dispatch 3 and 5 as builder slots free (cap 3).
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
  chunk/NN-<slug>. Live: 01-gossip, 02-erasure, 04-occ. Orchestration scripts: agent_work/orch/ (launch.sh,
  wait.sh, land.sh, seat-probe.sh). A second Neon FAIL goes to Astra arbitration; land.sh
  takes the newest ARBITRATION-N.md as the verdict when one exists. A NITS review needs a
  `NITS CLOSED` line appended by the orchestrator once each nit is closed.
- Gate stages: `agent_work/gates/gate.sh <chunk>` (build stage, on a chunk branch, before
  prose) and `GATE_STAGE=final ...` (after prose, and the only form that runs on main).
- Site: https://nickstrad.github.io/decentralized-object-store/ — built by
  .github/scripts/build_site.py through .github/workflows/pages.yml on every
  push to main. The book site since 0a landed; the book index lists
  chapters as they are published (chapter 1 so far).
- C4 set: docs/c4/ (c4-sdk bundle model.json, nine diagram files). The simulator and internal/env are marked built (status `existing`). The
  07-component-crdt diagram still shows the pre-amendment CRDT design and is
  redrawn as the OCC component when Chunk 4 lands; other diagrams move from
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
