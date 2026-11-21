# STATE — decentralized-object-store

Goal: build Nick's "Decentralized Object Store & Local-First Sync Engine"
(spec: docs/project-source.md) — a leaderless, multi-region object storage
and collaborative metadata sync cluster in Go from scratch (SWIM gossip,
Reed-Solomon erasure coding, Merkle-tree anti-entropy, CRDTs, Sagas),
designed testing-first: DST, property tests, fuzzing, race validation, and
E2E tests guide the build.

Status: Phase 1 (planning) — Fable orchestrator commissioned 2026-11-20.
HARD GATE: no product building until Nick signs off on PLAN.md.

Seats (Nick, 2026-11-20, this project only): Fable orchestrates; Opus
writes Go code (logic only), concurrently per chunk where the dependency
graph allows; Gemini Neon reviews Opus and writes ALL reader-facing prose
(README, docs, Pages site, code comments) once, late, per chunk; Astra
(gpt-6-astra) adversarially tests each chunk. Full ruling: the
orchestrator mission at /root/briefs/objstore-orchestrator-mission.md.

Done:
- 2026-11-20: Twin read Nick's Google Doc
  (1z9iL9sjn2K_k30sD2fjmDBJ-ZmZAUwtE9PlyLpFxnzk) and saved the faithful
  spec to docs/project-source.md; repo created PUBLIC at
  github.com/nickstrad/decentralized-object-store with this scaffold.

In progress:
- Phase 1: Fable orchestrator producing PLAN.md — testing architecture
  first, then repo layout, chunk plan with a concurrency map, per-chunk
  pipeline, technical decisions, and open questions for Nick.

Next steps:
- Nick signs off on PLAN.md (Twin relays), then Phase 2 build per the
  plan: foundation/DST chunk first, paradigm chunks with Neon write-ups
  publishing to GitHub Pages as each lands, E2E phase, publishing finish.

Open decisions / blockers:
- All Phase 2 work gated on Nick's plan sign-off.
- GitHub Pages: enabled via the Actions workflow that lands with the
  foundation chunk; site URL will be
  https://nickstrad.github.io/decentralized-object-store/

Resume: read this file, then PLAN.md and
/root/briefs/objstore-orchestrator-mission.md; git log for what has
landed. Orchestrator progress: /root/runs/objstore-orchestrator-progress.txt.
