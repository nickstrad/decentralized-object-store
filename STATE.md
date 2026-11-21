# STATE — decentralized-object-store

Goal: build Nick's "Decentralized Object Store & Local-First Sync Engine"
(spec: docs/project-source.md) — a leaderless, multi-region object store in
Go (SWIM gossip, Reed-Solomon erasure coding, Merkle-tree anti-entropy,
optimistic concurrency control for metadata, Sagas), designed testing-first.
Plan: [PLAN.md](PLAN.md) — signed off by Nick 2026-11-21; his rulings are in
PLAN.md section 9.1 and override the spec where they differ (notably: OCC
replaces the CRDT layer; Docker is the one environment; the book is the
learning artifact; C4 diagrams are kept current).

Status: PHASE 2 (BUILD) RUNNING since 2026-11-21 (halted 16:41Z for the shared-simulator
question, resumed 16:58Z on Nick's word). Step 0 landed. Chunk 0a (foundation tooling) is
built on its branch and in review; Chunk 0b (simulator) is being built by Opus.

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
| 0a | Foundation: go.mod, Docker environment, gate scripts and canaries, detlint, orchestration scripts, vendored kb, book site on Pages, CI | built on `chunk/0a-foundation`; gate green, 22/22 canaries caught; Neon review + Astra adversarial running |
| 0b | Deterministic simulator (internal/env, internal/sim, simtest, cmd/objsim) | Opus build running (re-dispatched clean 2026-11-21 17:02Z; the killed run had written no code) |
| 1 | SWIM gossip membership (internal/gossip) | not started (needs 0b) |
| 2 | Reed-Solomon erasure coding and chunk store (internal/erasure, internal/storage) | not started (needs 0b) |
| 3 | Merkle anti-entropy (internal/merkle) | not started (needs 0b) |
| 4 | Optimistic concurrency control for metadata (internal/occ) | not started (needs 0b) |
| 5 | Saga engine and bucket migration (internal/saga) | not started (needs 0b) |
| 6 | Integration: node, real runtime, HTTP object API, cmd/objnode | not started (needs 1–5) |
| 7 | E2E suite, nemesis cycles, fuzz hardening | not started |
| 8 | Publishing finish: the assembled book | not started |

## In progress

- 0a: runs `0a-review` (Neon) and `0a-adversarial` (Astra) in worktree `0a-foundation`.
  After them: repair if needed, run Astra's scripts and record RESULTS.md, Neon prose
  (README.md, docs/index.md), full gate, prose-check, land, verify Pages.
- 0b: run `0b-build` (Opus) in worktree `0b-simulator`, branched from 0a's first commit
  (fde6401: go.mod + Docker). Brief: /root/briefs/objstore-p2-0b-simulator-build.md.
  When it exits: rebase onto main (0a landed), gate, Neon review, then the pipeline.
- Simulator ruling (Nick, 2026-11-21): the simulation kernel is extracted to its own repo
  later, NOT now. 0b proceeds as briefed. At 0b's review add the kernel-separability
  reading check (streams, clock/timers, queue/loop, event log/digest, seed plumbing must
  not refer to network, disk, node, region or nemesis types; loop treats kind/node/peer as
  opaque integers and works with the event log off or replaced; simtest seed handling apart
  from cluster builders). Record tangles in docs/knowledge/ as an extraction note; do not
  block the landing on it. Source: /root/briefs/objstore-p2-resume-note-2026-11-21.md.

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
  chunk/NN-<slug>. None exist yet.
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
