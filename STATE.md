# STATE — decentralized-object-store

Goal: build Nick's "Decentralized Object Store & Local-First Sync Engine"
(spec: docs/project-source.md) — a leaderless, multi-region object storage
and collaborative metadata sync cluster in Go from scratch (SWIM gossip,
Reed-Solomon erasure coding, Merkle-tree anti-entropy, CRDTs, Sagas),
designed testing-first. Plan: [PLAN.md](PLAN.md).

Status: Phase 1 COMPLETE (2026-11-21) — PLAN.md committed and pushed.
STOPPED at the hard gate: waiting for Nick's sign-off on the plan. Nothing
is running. No product code exists. Do not dispatch builders.

Seats (Nick, 2026-11-20, this project only): Fable orchestrates; Opus
writes Go code (logic only), a fresh agent and worktree per chunk,
concurrently where the plan's concurrency map allows; Gemini 4 Neon reviews
Opus and writes ALL reader-facing prose (README, docs, Pages site, code
comments) once, late, per chunk; Astra (gpt-6-astra) adversarially tests
each chunk. Full ruling: /root/briefs/objstore-orchestrator-mission.md.

Done:
- 2026-11-20: spec saved to docs/project-source.md; public repo scaffold
  (e90f7b1).
- 2026-11-21: Phase 1 plan — PLAN.md (testing architecture first, repo
  layout, chunks 0–8, concurrency map, pipeline, technical decisions, 16
  open questions); feasibility probes in agent_work/probes/phase1/ (all
  pass); seat probe agent_work/orch/seat-probe.sh (Neon direct live,
  OpenRouter down until 2026-12-01, Astra live). Commit SHA is in
  /root/runs/objstore-plan-done.txt and `git log`.

In progress:
- Nothing.

Next steps (only after Nick's ruling arrives as a follow-up brief file):
1. Apply Nick's answers to PLAN.md section 9 (open questions) and any edits.
2. Phase 2, Wave A: Chunk 0a (foundation tooling, kb vendoring, Pages
   scaffold + workflow, CI) and Chunk 0b (DST simulator, Opus) — PLAN.md 4.2.
3. Wave B: Chunks 1–5 concurrently after the env/simtest interface freeze.
4. Chunks 6, 7, 8 in order.

Open decisions / blockers:
- C4 RULING (Nick, 2026-11-21, via Twin): this project keeps a C4
  diagrams section on its GitHub Pages site, up to date per landing,
  designed with Nick's c4-ask tool; designing the initial diagrams is
  part of kickoff and they go to Nick for buy-in together with the plan
  sign-off. Full ruling: /root/briefs/objstore-c4-ruling-2026-11-21.md. A bounded diagram-design run
  follows this entry; diagrams land in docs/c4/ labelled TARGET
  architecture, awaiting Nick's buy-in.
- BOOK RULING (Nick, 2026-11-20, via Twin): in addition to the per-chunk
  Neon write-ups, Neon assembles a concise Leanpub-style BOOK as the
  project's canonical learning artifact - per-chunk write-ups become book
  chapter drafts, Chunk 8 assembles the book, Pages presents the book.
  Full ruling: /root/briefs/objstore-book-ruling-2026-11-20.md. Applies
  at Phase 2 mission time, together with the plan sign-off.
- Plan sign-off — owner: Nick (relayed by Twin). Gates everything.
- PLAN.md section 9, OQ-1 to OQ-16 — owner: Nick. No defaults applied.
- Claude sign-in on this VM ends between 2026-11-25 and 2026-11-28 (OQ-14).

Facts a resuming agent needs:
- GitHub Pages is already enabled (source: GitHub Actions) but no workflow
  is committed yet, so https://nickstrad.github.io/decentralized-object-store/
  serves nothing until Chunk 0a lands. Not yet recorded in the README.
- Run opencode and codex headless with stdin closed (`</dev/null`).
- Vendored kb, go.mod, gate scripts: not created yet (Chunk 0a).

Resume: read this file, then PLAN.md and
/root/briefs/objstore-orchestrator-mission.md, then any newer
/root/briefs/objstore-*.md ruling; `git log` for what has landed. Branch
main, no worktrees, no background runs (`pgrep -af 'claude -p|codex exec|opencode run'`
should show nothing for this project). Orchestrator progress:
/root/runs/objstore-orchestrator-progress.txt.
