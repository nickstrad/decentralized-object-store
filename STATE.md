# STATE — decentralized-object-store

Goal: build Nick's "Decentralized Object Store & Local-First Sync Engine"
(spec: docs/project-source.md) — a leaderless, multi-region object store in
Go (SWIM gossip, Reed-Solomon erasure coding, Merkle-tree anti-entropy,
optimistic concurrency control for metadata, Sagas), designed testing-first.
Plan: [PLAN.md](PLAN.md) — signed off by Nick 2026-11-21; his rulings are in
PLAN.md section 9.1 and override the spec where they differ (notably: OCC
replaces the CRDT layer; Docker is the one environment; the book is the
learning artifact; C4 diagrams are kept current).

Status: PHASE 2 (BUILD) RUNNING since 2026-11-21. Step 0 and Chunk 0a (foundation
tooling) are LANDED (8205edf, plus the kb-index fix c5117c5). Chunk 0b (simulator) is
reviewed (NITS), attacked (52 held, 8 broken) and in its one post-adversarial Opus round.

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
| 0b | Deterministic simulator (internal/env, internal/sim, simtest, cmd/objsim) | on `chunk/0b-simulator`, rebased on main: REVIEW-1 FAIL → repair → REVIEW-2 NITS (API ready to freeze; 1 nit); Astra 52 held / 8 broken; Opus round `0b-repair2` running for the 8 + the nit |
| 1 | SWIM gossip membership (internal/gossip) | not started (needs 0b) |
| 2 | Reed-Solomon erasure coding and chunk store (internal/erasure, internal/storage) | not started (needs 0b) |
| 3 | Merkle anti-entropy (internal/merkle) | not started (needs 0b) |
| 4 | Optimistic concurrency control for metadata (internal/occ) | not started (needs 0b) |
| 5 | Saga engine and bucket migration (internal/saga) | not started (needs 0b) |
| 6 | Integration: node, real runtime, HTTP object API, cmd/objnode | not started (needs 1–5) |
| 7 | E2E suite, nemesis cycles, fuzz hardening | not started |
| 8 | Publishing finish: the assembled book | not started |

## In progress

- 0b (worktree `0b-simulator`): run `0b-repair2` (Opus; brief
  /root/briefs/objstore-p2-0b-repair2.md). When it exits 0:
  1. `bash agent_work/adversarial/0b-simulator/run-all.sh`; write
     `agent_work/adversarial/0b-simulator/RESULTS.md` (held/broken per claim; the
     `network-nemesis-dynamic-blobs` attack stays broken as worded and is NARROWED: nemesis
     timing/kind/node depend only on seed and nemesis config, the target blob on the node's
     inventory).
  2. `agent_work/gates/gate.sh 0b-simulator` (build stage) on a clean tree.
  3. Neon re-review of that round (REVIEW-3; a FAIL here is a second FAIL → Astra arbitration).
  4. Neon prose: all comments in internal/env, internal/sim, simtest, cmd/objsim, the CLI
     help, and `book/chapters/01-deterministic-simulation.md` (trailer `Prose-By: Gemini 4
     Neon`). Then the orchestrator adds the manifest entry in a NON-prose commit
     (`book/manifest.json`: n=1, slug deterministic-simulation, status "published"; the
     chapter's first heading must equal the manifest title).
  5. C4: move the simulator diagram(s) in docs/c4/ from TARGET to as-built.
  6. `GATE_STAGE=final agent_work/gates/gate.sh 0b-simulator`, `agent_work/gates/prose-check.sh
     main HEAD`, commit results, docs/knowledge entry (include the kernel EXTRACTION NOTE:
     build-notes "Extraction notes" + REVIEW-2 finding 3), INDEX rows.
  7. `agent_work/orch/land.sh 0b-simulator chunk/0b-simulator <msg>` from main; `git tag
     freeze-0b && git push origin freeze-0b`; verify the Pages run and that
     book/deterministic-simulation.html returns 200 with the chapter title (publish ruling:
     the landing is not done until the chapter is live).
- Publish ruling (Nick, 2026-11-21): every chunk's chapter goes LIVE on Pages at that
  chunk's landing; Chunk 8 is assembly and polish only. The 0a scaffold was checked and
  needed no fix: the site is manifest-driven and a single chapter publishes on its own.
- Simulator ruling (Nick, 2026-11-21): kernel extracted to its own repo later, NOT now.
  Pure file moves done in 0b's first repair; reading check done in both reviews; the
  extraction note goes into docs/knowledge/ at 0b's landing. Never blocks the landing.

## Next steps, in order

1. Land 0a, then 0b (interface freeze at 0b's landing).
2. Wave B: chunks 1, 2, 4 first, then 3 and 5 as builder slots free (cap 3).
3. Chunks 6, 7, 8 in sequence.

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
  chunk/NN-<slug>. Live: 0b-simulator. Orchestration scripts: agent_work/orch/ (launch.sh,
  wait.sh, land.sh, seat-probe.sh). A second Neon FAIL goes to Astra arbitration; land.sh
  takes the newest ARBITRATION-N.md as the verdict when one exists. A NITS review needs a
  `NITS CLOSED` line appended by the orchestrator once each nit is closed.
- Gate stages: `agent_work/gates/gate.sh <chunk>` (build stage, on a chunk branch, before
  prose) and `GATE_STAGE=final ...` (after prose, and the only form that runs on main).
- Site: https://nickstrad.github.io/decentralized-object-store/ — built by
  .github/scripts/build_site.py through .github/workflows/pages.yml on every
  push to main. The book site since 0a landed; the book index lists
  chapters as they are published (none yet).
- C4 set: docs/c4/ (c4-sdk bundle model.json, nine diagram files). The
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
