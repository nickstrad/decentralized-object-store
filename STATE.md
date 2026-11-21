# STATE — decentralized-object-store

Goal: build Nick's "Decentralized Object Store & Local-First Sync Engine"
(spec: docs/project-source.md) — a leaderless, multi-region object store in
Go (SWIM gossip, Reed-Solomon erasure coding, Merkle-tree anti-entropy,
optimistic concurrency control for metadata, Sagas), designed testing-first.
Plan: [PLAN.md](PLAN.md) — signed off by Nick 2026-11-21; his rulings are in
PLAN.md section 9.1 and override the spec where they differ (notably: OCC
replaces the CRDT layer; Docker is the one environment; the book is the
learning artifact; C4 diagrams are kept current).

Status: PHASE 2 (BUILD) RUNNING since 2026-11-21 (orchestrator relaunched 18:32Z; the
chunk runs it found alive were left running, nothing was dispatched twice). Step 0 landed.
Chunk 0a (foundation tooling) is in Astra arbitration after a second Neon FAIL whose
findings are already fixed; Chunk 0b (simulator) is repaired and in Neon re-review.

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
| 0a | Foundation: go.mod, Docker environment, gate scripts and canaries, detlint, orchestration scripts, vendored kb, book site on Pages, CI | on `chunk/0a-foundation`: repaired twice (REVIEW-1 FAIL 11 findings, all fixed; REVIEW-2 FAIL 5 new findings, all fixed in e0409b5); canaries 48/48; adversarial 99 held / 9 narrowed (RESULTS.md); Astra arbitration + Neon prose (README, docs/index.md) running |
| 0b | Deterministic simulator (internal/env, internal/sim, simtest, cmd/objsim) | on `chunk/0b-simulator`: built (Opus), REVIEW-1 FAIL (3 fault-model bugs), repair round done (exit 0, goldens unchanged); Neon re-review running; then rebase on main after 0a lands, gate, Astra adversarial, prose + chapter 01 |
| 1 | SWIM gossip membership (internal/gossip) | not started (needs 0b) |
| 2 | Reed-Solomon erasure coding and chunk store (internal/erasure, internal/storage) | not started (needs 0b) |
| 3 | Merkle anti-entropy (internal/merkle) | not started (needs 0b) |
| 4 | Optimistic concurrency control for metadata (internal/occ) | not started (needs 0b) |
| 5 | Saga engine and bucket migration (internal/saga) | not started (needs 0b) |
| 6 | Integration: node, real runtime, HTTP object API, cmd/objnode | not started (needs 1–5) |
| 7 | E2E suite, nemesis cycles, fuzz hardening | not started |
| 8 | Publishing finish: the assembled book | not started |

## In progress

- 0a (worktree `0a-foundation`): runs `0a-arbitration` (Astra, writes
  agent_work/0a-foundation/ARBITRATION-1.md; verdict is final) and `0a-prose` (Neon, one
  commit: README.md + docs/index.md). After both: close any arbitration nits, commit the work
  notes (agent_work/0a-foundation/, agent_work/adversarial/0a-foundation/ — still untracked),
  update agent_work/INDEX.md, `GATE_STAGE=final agent_work/gates/gate.sh 0a-foundation` on a
  clean tree, `agent_work/gates/prose-check.sh a78cffc HEAD`, canaries, then
  `agent_work/orch/land.sh 0a-foundation chunk/0a-foundation <msg-file>` from main, verify
  the Pages run and the front page, docs/knowledge entry + kb sync.
- 0b (worktree `0b-simulator`, branched from 0a's first commit fde6401): run `0b-rereview`
  (Neon, writes agent_work/0b-simulator/REVIEW-2.md). After 0a lands: `git rebase --onto main
  fde6401`, add the one detlint allowlist line for `runtime/debug` in internal/sim/sim.go
  (panic stack for failure reports only), run the gate; then Astra adversarial (brief still
  to write: /root/briefs/objstore-p2-0b-adversarial.md), run its scripts, Neon prose
  (comments + book/chapters/01-deterministic-simulation.md), manifest entry by the
  orchestrator in a non-prose commit, C4 refresh, final gate, land, tag `freeze-0b`.
- Publish ruling (Nick, 2026-11-21): every chunk's chapter goes LIVE on Pages at that
  chunk's landing; Chunk 8 is assembly and polish only. CHECKED on the 0a scaffold: the site
  is manifest-driven (`book/manifest.json` + `book/chapters/NN-slug.md`), a single chapter
  builds, is linked from the book index and passes site-lint before the rest exists (confirmed
  independently in REVIEW-2). No scaffold fix was needed. Per landing: Neon commits the
  chapter, the orchestrator adds the manifest entry (status "published"), and the landing is
  not done until the chapter URL returns 200. 0a itself has no chapter (PLAN 4.2).
- Simulator ruling (Nick, 2026-11-21): the kernel is extracted to its own repo later, NOT
  now. The repair round did the pure file moves; the kernel-separability reading check is in
  both 0b review briefs; tangles go to docs/knowledge/ as an extraction note at 0b's landing
  (source: build-notes.md "Extraction notes" + REVIEW-2). Never blocks the landing.

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
  chunk/NN-<slug>. Live: 0a-foundation, 0b-simulator. Orchestration scripts (launch.sh,
  wait.sh, land.sh, seat-probe.sh) are on the 0a branch until it lands — run them from that
  worktree.
- Site: https://nickstrad.github.io/decentralized-object-store/ — built by
  .github/scripts/build_site.py through .github/workflows/pages.yml on every
  push to main. Currently the kickoff review site; 0a turns it into the book
  site.
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
