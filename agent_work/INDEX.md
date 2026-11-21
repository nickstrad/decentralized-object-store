# agent_work index

Every script produced by any agent on this project lives in agent_work/
(Go or bash) and is indexed here: name, purpose, chunk, author seat, and
what it proved or its last result. Adversarial scripts and harnesses are
kept permanently under adversarial/ and indexed in their own section.

## Orchestration scripts

| Script | Purpose | Chunk | Author seat | What it proved / last result |
| --- | --- | --- | --- | --- |
| `../.github/scripts/build_site.py [out_dir]` | Builds the kickoff review site into `_site/` for the Pages workflow (`.github/workflows/pages.yml`): index, PLAN.md, and the docs/c4 set as HTML with Mermaid from CDN. Python, needs `pip install markdown`; lives under `.github/` because the workflow runs it | Kickoff (pre-Chunk 0 scaffold; Chunk 0 supersedes it) | Fable orchestrator, 2026-11-21 | 2026-11-21: builds 12 pages plus model.json; all 9 Mermaid diagrams render in headless Chromium |
| `orch/seat-probe.sh [neon\|astra\|all]` | One tiny call per route to check that the Neon routes (direct primary, OpenRouter fallback) and Astra (`gpt-6-astra`) answer; never prints secrets | Phase 1; reused before every Neon/Astra stage | Fable orchestrator, 2026-11-21 | 2026-11-21: Neon direct LIVE, Neon OpenRouter DOWN (out of credits until 2026-12-01), Astra LIVE. Both CLIs need stdin closed (`</dev/null`) or they hang / return nothing |

## Probes

| Script | Purpose | Chunk | Author seat | What it proved / last result |
| --- | --- | --- | --- | --- |
| `probes/phase1/run.sh` | Runs every Phase 1 feasibility probe below and prints PASS/FAIL per plan claim | Phase 1 (PLAN.md Appendix A) | Fable orchestrator, 2026-11-21 | 2026-11-21, go1.26.8: all PASS |
| `probes/phase1/simdet/main.go` | 100-line discrete-event loop: named PCG streams from one seed, `(time, seq)` heap, drop/duplicate/reorder, asymmetric partition; prints a SHA-256 of the event log and final state; `-leak` adds one map iteration | Phase 1 (plan 2.1) | Fable orchestrator, 2026-11-21 | Same seed gives the same digest across separate processes at 50 and 100 nodes (6 of 6); different seeds differ; 200,000 events in 0.29 s; with `-leak`, 6 runs give 6 digests, so the cross-process replay check catches map-order leaks |
| `probes/phase1/toolchain/probe_test.go` | Native fuzz target, and a `testing/synctest` goroutine-lifecycle test (an hour of virtual ticks, cancel, wait) | Phase 1 (plan 2.3, 2.5, 7.6) | Fable orchestrator, 2026-11-21 | Fuzz short run, `-race`, synctest and darwin/arm64 cross-build + vet all work on this VM |

The probes are a nested Go module (`probes/phase1/go.mod`), outside the product module.

## Adversarial scripts and harnesses

(none yet — the first adversarial pass runs against Chunk 0 in Phase 2)
