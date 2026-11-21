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

| `orch/launch.sh <run> <seat> <workdir> <brief>` | Launch one headless stage (Opus, Sonnet, Neon direct or OpenRouter, Astra) detached in a worktree; writes log, pid and exit files; checks 15 s in that the process is alive in the right directory | 0a; every stage since | Fable orchestrator, 2026-11-21 | Neon needs `--auto`, a bounded reasoning variant and a final `NEON-DONE` line or it can stop early with exit 0 |
| `orch/wait.sh <run> [seconds]` | Wait for a launched stage: 0 on exit 0, 1 on nonzero, 3 when it died with no exit file or was never launched, 4 on deadline; prints the log tail | 0a | Fable orchestrator, 2026-11-21 | |
| `orch/land.sh <chunk> <branch> <msg-file>` | Merge a chunk to main and push, only on a counted gate result bound to the branch tip, a passing review (or arbitration) and recorded adversarial results; enforces the simulator freeze once tag `freeze-0b` exists | 0a | Fable orchestrator, 2026-11-21 | Arbiter exercised it on throwaway histories: uncounted, unknown-commit, foreign-commit and diff-failure cases all refused |
| `kb-sync.sh vendor\|index\|status\|search` | Vendor the kb tool into `kb/`, rebuild the in-repo index over `docs/knowledge/`, report drift | 0a | Fable orchestrator, 2026-11-21 | Builds and runs kb inside the dev container; works on a clean clone |

## Gate scripts

| Script | Purpose | Chunk | Author seat | What it proved / last result |
| --- | --- | --- | --- | --- |
| `gates/gate.sh <name>` | Landing gates G0–G6 through Docker; writes `agent_work/<name>/gates/result.json` | 0a | Fable orchestrator, 2026-11-21 | See each chunk's `gates/result.json` |
| `gates/canaries.sh [regexp]` | Plants one violation at a time in a throwaway copy and requires the owning check to go red with the expected message | 0a | Fable orchestrator, 2026-11-21 | 2026-11-21: 50 of 50 caught (`0a-foundation/gates/canaries.json`) |
| `gates/fuzz-short.sh` | Runs every native fuzz target for a short budget (gate G3) | 0a | Fable orchestrator, 2026-11-21 | |
| `gates/prose-check.sh <base> [head]` | Prose provenance over a commit range: no comments before the prose commit beyond the base, prose commits change only comments and reader-facing files, nobody else touches prose | 0a | Fable orchestrator, 2026-11-21 | |
| `gates/ci-prose.sh <before> [head]` | The same for everything a push put on main: landings as branches, direct commits as ranges; whole history since the bootstrap commit when the boundary is unknown | 0a | Fable orchestrator, 2026-11-21 | |
| `gates/site_lint.py [root]` / `--selftest` | Builds the site into a temp directory and checks links, anchors, chapter titles, unlisted chapters, private paths and machine references | 0a | Fable orchestrator, 2026-11-21 | 22 self-test fixtures |
| `gates/detlint/` (nested module) | `detlint` (determinism lint), `nocomments`, `astdiff` (comment-only diff), `paniccheck`, `importcheck`; usage in `README-tools.txt` | 0a | Fable orchestrator, 2026-11-21 | Unit tests over fixture modules in `testdata/` |
| `0a-foundation/arbitration-controls.sh` | Positive and negative controls for the comment baseline, the direct-container gate path and the CI push boundary, in a throwaway repository | 0a | Fable orchestrator, 2026-11-21 | 2026-11-21: 11 of 11 OK |

## Probes

| Script | Purpose | Chunk | Author seat | What it proved / last result |
| --- | --- | --- | --- | --- |
| `probes/phase1/run.sh` | Runs every Phase 1 feasibility probe below and prints PASS/FAIL per plan claim | Phase 1 (PLAN.md Appendix A) | Fable orchestrator, 2026-11-21 | 2026-11-21, go1.26.8: all PASS |
| `probes/phase1/simdet/main.go` | 100-line discrete-event loop: named PCG streams from one seed, `(time, seq)` heap, drop/duplicate/reorder, asymmetric partition; prints a SHA-256 of the event log and final state; `-leak` adds one map iteration | Phase 1 (plan 2.1) | Fable orchestrator, 2026-11-21 | Same seed gives the same digest across separate processes at 50 and 100 nodes (6 of 6); different seeds differ; 200,000 events in 0.29 s; with `-leak`, 6 runs give 6 digests, so the cross-process replay check catches map-order leaks |
| `probes/phase1/toolchain/probe_test.go` | Native fuzz target, and a `testing/synctest` goroutine-lifecycle test (an hour of virtual ticks, cancel, wait) | Phase 1 (plan 2.3, 2.5, 7.6) | Fable orchestrator, 2026-11-21 | Fuzz short run, `-race`, synctest and darwin/arm64 cross-build + vet all work on this VM |

The probes are a nested Go module (`probes/phase1/go.mod`), outside the product module.

## Adversarial scripts and harnesses

| Script | Purpose | Chunk | Author seat | What it proved / last result |
| --- | --- | --- | --- | --- |
| `adversarial/0a-foundation/run-all.sh` + `a01`–`a07-*.sh`, `harness.py` | 108 attacks on the gate tooling over six published claims: hygiene bypasses, entropy the lint misses, smuggled comments and dependencies, comment-only-diff evasions, forged or partial gate results, publishing private or broken pages | 0a | Astra (gpt-6-astra), run by the orchestrator, 2026-11-21 | Run 1: 62 held, 46 broken. After repair: 99 held, 9 broken, each narrowed in writing and accepted in arbitration (`adversarial/0a-foundation/RESULTS.md`) |
