# Brief — Chunk 0b REVIEW 3: the simulator's post-adversarial round

Seat: Gemini 4 Neon, reviewer (fresh). You are in the git worktree
/root/Software/decentralized-object-store-work/0b-simulator (branch `chunk/0b-simulator`).
READ-ONLY review: do not edit code, do not commit, no git write commands. The ONLY file you
create is `agent_work/0b-simulator/REVIEW-3.md`. Write it before you stop.

## Context
- `agent_work/0b-simulator/REVIEW-2.md`: NITS — one open nit (finding 1: a whole-node
  read-error fault ambushing blobs written later).
- An adversarial pass then broke 8 of 60 attacks: `agent_work/adversarial/0b-simulator/
  ATTACKS.md` (exact reproductions) and `RESULTS.md` (the orchestrator's re-run after the
  repair: what now holds, what was narrowed).
- One Opus round answered both: brief `agent_work/0b-simulator/objstore-p2-0b-repair2.md`
  (items 1–8), commits `git log 110bd69..HEAD`, diff
  `git diff 110bd69..HEAD -- internal cmd agent_work/gates`, account in
  `agent_work/0b-simulator/build-notes.md` section "Post-adversarial round", regenerated
  `agent_work/0b-simulator/frozen-api.txt`.
- Product Go is comment-free ON PURPOSE. Not a finding.

## What to decide
1. For each of the brief's items 1–8: FIXED, PARTLY FIXED, NOT FIXED, or (item 8 only)
   CORRECTLY NARROWED. Check code and tests, not the notes. Does each regression test fail
   on the old code (`git show 110bd69:<file>`)? For item 8: does the nemesis really consume
   the same number of draws per fault whatever the blob inventory, and does
   `TestPropNemesisTimingIgnoresInventory` have the power to fail?
2. Anything new that is BLOCKER or MAJOR: a determinism leak; a changed exported signature
   (diff `frozen-api.txt` against `git show 110bd69:agent_work/0b-simulator/frozen-api.txt`
   — additions must be listed under "API changes" in the notes and be the smallest
   possible); golden digests moved without a stated cause; a validation that now rejects
   configurations the five later protocol chunks legitimately need (large but sane
   latencies, skews, timeouts — check the chosen bounds and the stated time horizon).
3. Freeze check, last time: this API is frozen when the chunk lands. Anything you would
   still change first? A necessary interface change is at least MAJOR — exact signature.

Run through Docker only: `docker compose run --rm -T dev go test ./...` (also `-race`;
`DST_SEEDS=50 go test ./internal/sim/...`; `go test ./internal/sim -rapid.checks=2000`).

## Output: agent_work/0b-simulator/REVIEW-3.md
First line exactly one of `VERDICT: PASS`, `VERDICT: NITS`, `VERDICT: FAIL`. Then the item
table with status and one line of evidence each, then new findings, numbered, with
severity (BLOCKER / MAJOR / NIT / NOTE), file and line, what is wrong, a concrete
reproduction and the smallest fix. FAIL needs a BLOCKER or MAJOR. End with what you
verified by running versus by reading.
