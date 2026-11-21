# STATE — decentralized-object-store

Goal: build Nick's "Decentralized Object Store & Local-First Sync Engine"
(spec: docs/project-source.md) — a leaderless, multi-region object storage
and collaborative metadata sync cluster in Go from scratch (SWIM gossip,
Reed-Solomon erasure coding, Merkle-tree anti-entropy, CRDTs, Sagas),
designed testing-first. Plan: [PLAN.md](PLAN.md).

Status: Phase 1 COMPLETE (2026-11-21) — PLAN.md committed and pushed.
STOPPED at the hard gate: waiting for Nick's sign-off on the plan. Nothing
is running. No product code exists. Do not dispatch builders. The initial
C4 diagram set is landed in docs/c4/ and waits for Nick's buy-in (see Done).

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

- 2026-11-21: initial C4 diagram set designed with c4-sdk and LANDED at
  b11d841 (docs/c4/; TARGET architecture, awaiting Nick's buy-in):
  model.json (c4-sdk bundle, schemaVersion 1), nine diagram files (01
  system context; 02 and 03 container views; 04–09 component diagrams for
  gossip, erasure, merkle, crdt, saga, storage), 25 diagrams/*.mmd terminal
  sources, README.md index. Validated: model.json decodes with c4-sdk
  0.1.0 `decodeArchitecture` (ok, 0 errors, 0 warnings) and all 9 diagrams
  build a scene; all Mermaid blocks parse (Mermaid 11); all 25 draw-visual
  embeds are rendered and pass `render.py --check` at 80 columns. Results,
  repeat commands and model notes: docs/c4/README.md. Marker:
  /root/runs/objstore-c4-done.txt (status COMPLETE).

In progress:
- Nothing running.

Next steps (only after Nick's ruling arrives as a follow-up brief file):
1. Apply Nick's answers to PLAN.md section 9 (open questions) and any edits.
2. Phase 2, Wave A: Chunk 0a (foundation tooling, kb vendoring, Pages
   scaffold + workflow, CI) and Chunk 0b (DST simulator, Opus) — PLAN.md 4.2.
3. Wave B: Chunks 1–5 concurrently after the env/simtest interface freeze.
4. Chunks 6, 7, 8 in order.

Open decisions / blockers:
- C4 RULING (Nick, 2026-11-21, via Twin): this project keeps a C4
  diagrams section on its GitHub Pages site, up to date per landing,
  designed with Nick's c4-sdk tool (/root/Software/c4-sdk, npm
  c4-sdk@0.1.0); designing the initial diagrams is part of kickoff and
  they go to Nick for buy-in together with the plan sign-off. Full ruling:
  /root/briefs/objstore-c4-ruling-2026-11-21.md. The diagrams are landed in
  docs/c4/ (see Done), labelled TARGET architecture. C4 buy-in — owner:
  Nick; Phase 2 needs it as well as the plan sign-off.
- c4-sdk neighbour elements — owner: Nick. c4-sdk 0.1.0 has no per-view
  external tag, so model.json carries eight `n-*` elements that stand for
  neighbouring containers in component views. Keep the workaround or
  change c4-sdk. Detail: docs/c4/README.md, "Model notes".
- PLAN.md section 3 (repo layout) does not list docs/c4/ yet, and the
  per-chunk landing gate has no C4-accuracy check yet; apply both with
  Nick's other plan edits (ruling items 1 and 5).
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
