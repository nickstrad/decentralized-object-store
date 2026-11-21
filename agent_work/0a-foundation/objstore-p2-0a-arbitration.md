# Brief — Chunk 0a ARBITRATION: foundation tooling, after a second FAIL

Seat: Astra (gpt-6-astra), arbiter (fresh). You are in the git worktree
/root/Software/decentralized-object-store-work/0a-foundation (branch `chunk/0a-foundation`).
READ-ONLY: do not edit, commit, or run git write commands. The ONLY file you create is
`agent_work/0a-foundation/ARBITRATION-1.md`. Your verdict is final for this chunk.

## Why you are here
Project rule: a reviewer's second FAIL goes to a third seat. The chunk is the landing-gate
tooling of a Go project tested by deterministic simulation (no product code yet). History:
- `agent_work/0a-foundation/REVIEW-1.md` — reviewer (Gemini), FAIL, eleven findings.
- one repair round: `git log e62416a..9ce04e9`.
- `agent_work/0a-foundation/REVIEW-2.md` — reviewer, FAIL again: all eleven original
  findings FIXED (one "partly"), plus FIVE NEW findings (2 BLOCKER, 2 MAJOR, 1 NIT), three
  of them in code the repair round added.
- the builder (the orchestrator seat) accepts all five as correct and answered with commit
  `e0409b5` (`git show e0409b5`). There is no disagreement about the findings; the question
  is whether the chunk may land now.
- your own earlier adversarial pass and its results: `agent_work/adversarial/0a-foundation/`
  (`ATTACKS.md`, `RESULTS.md`: 99 held, 9 broken, each of the nine narrowed in writing).

Governing plan text: PLAN.md 2.9 (gates G0–G6), 2.10 (prose rule), 4.2 "Chunk 0", 5.1–5.3,
6 (pipeline).

## What to decide
1. For each of the five NEW findings in REVIEW-2.md: is it now FIXED by `e0409b5`, PARTLY
   FIXED, or NOT FIXED? Check the code, and run things. In particular:
   - New 1 (`agent_work/orch/land.sh`): can it still merge on a result with
     `counts_for_landing=false`, an unknown or foreign `sha`, or a failing `git diff`?
   - New 2 (comment baseline): with comments already on main (a later chunk's situation),
     do `gate.sh` at `GATE_STAGE=build`, `canaries.sh` and `prose-check.sh <base> <head>`
     stay green on a correct branch, and still go red on a comment the branch itself adds
     before its prose commit? Build the reviewer's reproduction in a throwaway repo under
     /tmp (copy the tree without `.git` and `kb`, `git init`, make a commented base commit
     on `main`, branch from it). Is the baseline mechanism itself a hole — can an ordinary,
     honest invocation end up comparing a tree with itself?
   - New 3 (coverage canary): does it still catch once `internal/sim` is large?
   - New 4 (`.github/workflows/ci.yml`, new `agent_work/gates/ci-prose.sh`): direct
     pushes, landing merges, an "evil" merge, a first push.
   - New 5 (NIT): the `for k = range m` case; the builder left the repeated site-lint
     self-test as is (runtime only) — acceptable or not.
2. Did `e0409b5` introduce a new BLOCKER or MAJOR (a gate green on a violating tree, or red
   on a correct one)?
3. Are the nine narrowed adversarial claims in `RESULTS.md` acceptable as narrowed?

Everything runs through Docker from the worktree root: `docker compose run --rm -T dev
<cmd>`; `agent_work/gates/gate.sh 0a-foundation`; `agent_work/gates/canaries.sh [regexp]`
(a full run is about six minutes; another full run may be in progress — prefer filtered
runs, e.g. `canaries.sh '^(clean|landed-prose|comment|coverage)$'`). Leave nothing behind
outside /tmp and your one output file.

## Output: agent_work/0a-foundation/ARBITRATION-1.md
First line exactly one of: `VERDICT: PASS`, `VERDICT: NITS`, `VERDICT: FAIL`.
PASS: the chunk may land. NITS: it may land once the listed small items are closed (list
each precisely, with file and the smallest fix). FAIL: a BLOCKER or MAJOR remains — say
exactly which and what would close it. Then the per-finding table with one line of evidence
each (what you ran or read), then anything new, then what you verified by running versus by
reading. No style remarks.
