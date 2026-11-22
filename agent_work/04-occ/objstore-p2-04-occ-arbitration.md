# Brief — Chunk 4 ARBITRATION: optimistic concurrency control with last-writer-wins, after a second FAIL

Seat: Astra (gpt-6-astra), arbiter (fresh). You are in a git worktree (branch
`chunk/04-occ`). READ-ONLY: do not edit, commit, or run git write commands. The ONLY file
you create is `agent_work/04-occ/ARBITRATION-1.md`. Your verdict is final for this chunk.

## Why you are here
Project rule: a reviewer's second FAIL goes to a third seat. The package is
`internal/occ` of a Go project (standard library only) tested by deterministic
simulation: metadata records with conditional writes (ETags, If-Match / If-None-Match)
and, by the project owner's design ruling, LAST-WRITER-WINS for concurrent versions —
one version per key, the winner is the larger hybrid-logical-clock stamp then node ID,
the loser is discarded, and EVERY such discard must be counted and logged. Losing an
acknowledged write to a concurrent write is the stated design, not a defect; a loss
with NO record is a defect. Governing text: PLAN.md 4.2 "Chunk 4", 7.5, and the LWW
ruling in 9.1; the binding design is Part A of
`agent_work/04-occ/objstore-p2-04-occ-repair1.md`.

History:
- `agent_work/04-occ/REVIEW-1.md` — reviewer (Gemini), FAIL on the first build (a
  different, sibling-keeping design that the owner then ruled out).
- the repair round reworked the package to last-writer-wins.
- `agent_work/04-occ/REVIEW-2.md` — reviewer, FAIL again: every repair item FIXED, the
  winner rule confirmed, but two MAJOR holes in "every loss is recorded" (finding 1: a
  causal clock that claims a discarded dot; finding 2: a node that rejects an incoming
  concurrent version records nothing) and one NIT (finding 3: a crash between making a
  merge durable and emitting the record).
- the orchestrator accepted all three as correct. A second builder round followed:
  brief `agent_work/04-occ/objstore-p2-04-occ-lww-round2.md`, commits
  `git log ..HEAD`, account in the "LWW round 2" section of
  `agent_work/04-occ/build-notes.md`. There is no disagreement about the findings; the
  question is whether the package may land now.
- your seat's earlier independent test harness and its results:
  `agent_work/adversarial/04-occ/` (`ATTACKS.md`, `RESULTS.md`: 60 held, 1 scored broken
  and explained as N1).

## What to decide
1. For each of REVIEW-2's findings 1, 2 and 3 and for the round-2 brief's items 4 and 5
   (quota recount overflow; stale no-op recount): FIXED, PARTLY FIXED or NOT FIXED? Check
   the code and run things; does each regression test fail on the old code
   (`git show :<file>`)? In particular:
   - finding 1: the builder retires the actor epoch when a discard leaves a gap. Is
     there still any honest multi-replica history in which a replica classifies the
     replacement of a version nobody's writer saw as an ordinary causal overwrite and
     records nothing? Does the new property's oracle stay independent of the product's
     own vectors?
   - finding 2: a rejected incoming concurrent version is now recorded, de-duplicated
     through a saved window of the last 256 records. Can a loss still end with no record
     on any surviving node, other than the one stated limit (every node that ever held
     the version lost its disk before passing it on)? Is over-counting after the window
     stated and harmless? Is old news (an incoming version causally below the held one)
     correctly not recorded?
   - finding 3: records persisted with the merge and replayed on restart — at-least-once,
     never zero?
2. Did round 2 introduce a new BLOCKER or MAJOR (wrong behaviour, a determinism leak, a
   weakened test, an edit to the frozen simulator packages `internal/env`, `internal/sim`)?
   Is the winner still a pure function of the two versions, and are replicas holding the
   same versions still byte-identical (discard records must not be part of the replicated
   document)?
3. `RESULTS.md` item N1: the check `partition-two-writer` is still scored BROKEN because
   it expects exactly one discard record where round 2 now records the loss on each node
   that saw it. Confirm or reject that reading from the code and the check's source.
4. Is the guarantee stated in the build notes exactly what the tests prove — no stronger?

Everything runs through Docker from the worktree root: `docker compose run --rm -T dev
go test ./internal/occ/...` (also `-race`, `DST_SEEDS=50`, `-rapid.checks=2000`). The
gate result for this tree is `agent_work/04-occ/gates/result.json`. Leave nothing behind
outside /tmp and your one output file.

## Output: agent_work/04-occ/ARBITRATION-1.md
First line exactly one of: `VERDICT: PASS`, `VERDICT: NITS`, `VERDICT: FAIL`.
PASS: the package may land. NITS: it may land once the listed small items are closed (list
each precisely, with file and the smallest fix). FAIL: a BLOCKER or MAJOR remains — say
exactly which, with a reproduction, and what would close it. Then the per-item table with
one line of evidence each (what you ran or read), then anything new, then what you
verified by running versus by reading. No style remarks.

## Vocabulary and scope (read this)
This is ordinary quality assurance of the project's OWN Go library code, run only inside
its own deterministic simulator and Docker container on this machine. No network
service, third-party system or security tooling is involved. Use plain testing
vocabulary in everything you write — check, scenario, fault, malformed input,
counterexample. Write the output file EARLY (verdict line as a placeholder, then the
table as you fill it) so an interrupted run still leaves a usable record; make sure the
first line is your real verdict before you stop.
