# Brief — Chunk 0a REVIEW: foundation tooling (gates, lint, Docker, kb, site, CI)

Seat: Gemini 4 Neon, reviewer (fresh). You are in the git worktree
/root/Software/decentralized-object-store-work/0a-foundation (branch `chunk/0a-foundation`).
READ-ONLY review: do not edit, commit, or run git write commands. The ONLY file you create is
`agent_work/0a-foundation/REVIEW-1.md`.

## What you are reviewing
`git diff a78cffc..HEAD` (six commits; `git log --oneline a78cffc..HEAD`). This chunk is the
project's tooling, built by the orchestrator seat (Claude). It contains no product code. Skip
the vendored `kb/` tree (a `git archive` of another repository — only confirm the root module
does not import it) and skim the test fixtures.

Read first: PLAN.md sections 2.9 (landing gates), 2.10 (the prose rule), 3 (repo layout),
4.2 "Chunk 0" (the 0a deliverable and its canary list), 7.6, 7.9, 7.10, and 9.1 (rulings).

## Questions your review must answer (number your findings)
1. `agent_work/gates/gate.sh`: does each gate G0–G6 do what PLAN 2.9 says? Can any check pass
   vacuously or be skipped silently (an empty package list, a command whose failure is
   swallowed, a pipe hiding an exit status, a `grep` that matches nothing)? Is
   `counts_for_landing` true only for a clean, full, in-container run at a known commit?
2. `agent_work/gates/canaries.sh`: does every canary prove the check it names, or could it be
   red for an unrelated reason (for example a compile error instead of the planted rule)?
   Which gate or check has NO canary? PLAN 4.2 lists eight required canaries — are all there?
3. `agent_work/gates/detlint/` (nested stdlib-only Go module: detlint, nocomments, astdiff,
   paniccheck, importcheck): read the rule implementations. Name concrete Go snippets that
   violate a determinism rule in PLAN 2.1/2.5 and would NOT be flagged. Check the allowlist
   handling cannot be used to silence a whole package by accident.
4. `agent_work/gates/prose-check.sh`: does it prove (a), (b), (c) of PLAN 2.10? Name a commit
   sequence that breaks the prose rule and passes.
5. `Dockerfile`, `compose.yaml`, `.github/workflows/ci.yml`: is Docker really the one
   environment — does anything in the gate path depend on the host? Is the image pinned?
   Would the CI job behave differently from a local run in a way that matters?
6. `.github/scripts/build_site.py`, `agent_work/gates/site_lint.py`, `book/manifest.json`:
   correctness of manifest validation and link checking; anything that would publish
   `docs/knowledge/`, `agent_work/`, `STATE.md` or `kb/`.
7. `agent_work/kb-sync.sh`, `.kb/`: correctness; anything that breaks on a clean clone.
8. `agent_work/orch/launch.sh`, `wait.sh`: can a launch report success for a dead or misplaced
   run; can wait report success wrongly?

You may run things to check a suspicion (Docker is available: `docker compose run --rm dev
<cmd>`; the full gate is `agent_work/gates/gate.sh 0a-foundation`; canaries are
`agent_work/gates/canaries.sh`, about two minutes). Do not leave files behind outside /tmp.

## Output: agent_work/0a-foundation/REVIEW-1.md
First line exactly one of: `VERDICT: PASS`, `VERDICT: NITS`, `VERDICT: FAIL`.
Then numbered findings, each with: severity (BLOCKER / MAJOR / NIT), file and line, what is
wrong, a concrete reproduction or snippet, and the smallest fix. FAIL means at least one
BLOCKER or MAJOR: a gate that can be green on a tree that violates it, or a check that does
not check what it claims. Do not pad with style remarks; do not restate the code. End with a
short list of what you verified by running it versus by reading.

