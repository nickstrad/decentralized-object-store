# Brief — Chunk 7 REVIEW 1: the end-to-end suite, nemesis cycles and fuzz hardening

Seat: Gemini 4 Neon, reviewer (fresh). You are in a git worktree (branch `chunk/07-e2e`).
READ-ONLY review: do not edit code, do not commit, no git write commands. The ONLY file you
create is `agent_work/07-e2e/REVIEW-1.md`. Write it before you stop.

## What you are reviewing
`git diff main..HEAD -- internal cmd agent_work/gates` (an Opus builder's commits; `git
log main..HEAD`). Product Go is comment-free ON PURPOSE (another seat writes comments
later): not a finding. Read:
- the builder's brief `agent_work/07-e2e/objstore-p2-07-e2e-build.md` and rules
  `agent_work/07-e2e/objstore-p2-builder-rules.md`;
- PLAN.md: section 2 (testing architecture), the item "Chunk 7" in 4.2 (deliverable, the
  named tests, the adversarial list), the 2.6 error rows and the section 7 decisions the
  item cites;
- `agent_work/07-e2e/build-notes.md` (claims, measurements, uncovered branches, requests);
- the simulator contract the code builds on: `agent_work/0b-simulator/build-notes.md`
  ("Semantics Wave B builders must know") and `agent_work/0b-simulator/frozen-api.txt`;
- the code and tests in `test/e2e`, with the fixes it forced in `internal/node` and `internal/noderuntime`.
The orchestrator's gate result for this tree is `agent_work/07-e2e/gates/result.json`.
You may run tests through Docker only: `docker compose run --rm -T dev go test ./test/e2e/...`
(also `-race`, `DST_SEEDS=50`, `-rapid.checks=2000` on packages that import rapid).

## Questions (number your findings)
1. Correctness against the plan item: is every part of the Deliverable built, and built
   right? Name protocol or algorithm errors concretely (a state, a message order, an input)
   with a reproduction — a test snippet or a seed where possible.
2. The headline claims: for each claim in the build notes, does the named test really prove
   it? Find tests that cannot fail, assert nothing, use a bound so loose it proves nothing,
   or silently skip (zero iterations, empty tables). Do the canaries fail for the planted
   reason? Does every test named in the plan item exist and test what its name says?
3. Determinism and simulator use: any path where two runs of one seed can differ; any
   misuse of the simulator contract (assuming callbacks run synchronously, ignoring
   `ErrTimeout` ambiguity, relying on unsynced disk data surviving a crash, sharing memory
   across nodes, drawing from the wrong stream).
4. Failure handling: PLAN 2.6 rows — every sentinel produced and tested; malformed or
   hostile input is an error, never a panic or unbounded work; resource bounds (memory,
   message size, retries).
5. Interfaces for the integration chunk (Chunk 6 wires all five protocols into one node on
   the simulator AND on a real UDP/TCP/file runtime): are the small interfaces this chunk
   declares (`Membership`, `Repairer`, step interfaces, …) implementable by the other
   chunks as planned? Anything that will not survive a real runtime?
6. Rules: standard library only; no comments; no goroutine/channel/mutex/wall clock/global
   rand/float in protocol code; panics only for violated invariants, each allowlisted; the
   frozen simulator packages untouched.

## Output: agent_work/07-e2e/REVIEW-1.md
First line exactly one of `VERDICT: PASS`, `VERDICT: NITS`, `VERDICT: FAIL`. Then numbered
findings, each with severity (BLOCKER / MAJOR / NIT / NOTE), file and line, what is wrong, a
concrete reproduction and the smallest fix. FAIL needs at least one BLOCKER or MAJOR: wrong
behaviour, a headline claim its test does not prove, a determinism leak, an interface the
integration cannot use. Do not pad with style remarks. End with what you verified by
running versus by reading.
