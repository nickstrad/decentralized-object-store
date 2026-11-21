# Brief — Chunk 0a RE-REVIEW: foundation tooling after its one repair round

Seat: Gemini 4 Neon, reviewer (fresh). You are in the git worktree
/root/Software/decentralized-object-store-work/0a-foundation (branch `chunk/0a-foundation`).
READ-ONLY review: do not edit, commit, or run git write commands. The ONLY file you create is
`agent_work/0a-foundation/REVIEW-2.md`.

## Context
The first review of this chunk is `agent_work/0a-foundation/REVIEW-1.md`: FAIL with eleven
numbered findings (3 BLOCKER, 7 MAJOR, 1 NIT). The builder (the orchestrator seat) answered
with one repair round, three commits: `git log e62416a..HEAD`, diff `git diff e62416a..HEAD`
(most of the file count is fixtures under `agent_work/gates/testdata/` — skim those).
The full chunk is `git diff a78cffc..HEAD`. Skip the vendored `kb/` tree.

Plan sections that govern: PLAN.md 2.9 (gates), 2.10 (prose rule), 4.2 "Chunk 0" (0a
deliverable and canaries), 7.6, 7.9, 7.10, 9.1.

## What to decide
For EACH of the eleven findings in REVIEW-1.md, by number: is it FIXED, PARTLY FIXED, NOT
FIXED, or NARROWED IN WRITING (the tool's header comment or the plan now states the limit
plainly, and the limit is acceptable for a tooling chunk)? Check the code, not the commit
messages. Then: did the repair introduce any NEW blocker or major defect (a gate that can now
be green on a tree that violates it, or a check that fails on a correct tree)?

Deliberate, documented limits that are NOT defects (do not re-raise them):
- `detlint` lints product files of the simulator and protocol packages and their in-module
  import closure (computed by `gate.sh`), not `_test.go` files; test-side nondeterminism is
  caught by the replay meta-tests in gate G5.
- comments inside `testdata/` trees and generated-file headers are outside the comment rule.
- `prose-check.sh` lets a non-prose commit DELETE a comment together with its code, and lets
  a prose commit edit `cmd/*/help.txt` (CLI help is prose).
- a result file is bound to its commit by `agent_work/orch/land.sh`, not by the gate itself.

One additional question (new binding rule from the project owner): every chunk's book
chapter must go live on the site at that chunk's landing. Confirm by reading
`.github/scripts/build_site.py` and `book/manifest.json` that the site is manifest-driven —
adding `book/chapters/NN-slug.md` plus one manifest entry is all it takes for a chapter to be
built, linked from the book index, and published on the next push to main — and say if
anything would prevent a single chapter from publishing before the rest of the book exists.

You may run things: `docker compose run --rm dev <cmd>`; the full gate is
`agent_work/gates/gate.sh 0a-foundation` (it rewrites agent_work/0a-foundation/gates/ — that
is fine); canaries are `agent_work/gates/canaries.sh` (about three minutes). Leave nothing
else behind outside /tmp.

## Output: agent_work/0a-foundation/REVIEW-2.md
First line exactly one of: `VERDICT: PASS`, `VERDICT: NITS`, `VERDICT: FAIL`.
Then a table of the eleven findings with status and one line of evidence each (file:line),
then any new findings, numbered, each with severity (BLOCKER / MAJOR / NIT), file and line,
what is wrong, a concrete reproduction, and the smallest fix. FAIL means at least one
BLOCKER or MAJOR remains or was introduced. End with what you verified by running versus by
reading.
