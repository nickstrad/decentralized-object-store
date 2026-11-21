# Brief — Chunk 0b RE-REVIEW: the deterministic simulator after its one repair round

Seat: Gemini 4 Neon, reviewer (fresh). You are in the git worktree
/root/Software/decentralized-object-store-work/0b-simulator (branch `chunk/0b-simulator`).
READ-ONLY review: do not edit code, do not commit, no git write commands. The ONLY file you
create is `agent_work/0b-simulator/REVIEW-2.md`. Write it before you stop.

## Context
- First review: `agent_work/0b-simulator/REVIEW-1.md` — FAIL: three MAJOR (1–3), three NIT
  (4–6), three NOTE (7–9). It judged the exported API ready to freeze.
- The repair brief: `agent_work/0b-simulator/objstore-p2-0b-repair.md` (what was asked,
  including pure file moves for a later kernel extraction).
- The repair: `git log fc37722..HEAD` (commits by a fresh Opus builder), diff
  `git diff fc37722..HEAD -- internal cmd agent_work/gates`. Its account:
  `agent_work/0b-simulator/build-notes.md`, section "Repair round" (with "File moves" and
  "Extraction notes"), and the regenerated `agent_work/0b-simulator/frozen-api.txt`.
- Product Go is comment-free ON PURPOSE (another seat writes comments later). Not a finding.

## What to decide
1. For each REVIEW-1 finding 1–6 and 8: FIXED, PARTLY FIXED or NOT FIXED. Check the code
   and the new tests, not the notes. For each fix: does the regression test really fail on
   the old behaviour (read it; where cheap, convince yourself by reasoning about the old
   code in `git show fc37722:<file>`)? Is `TestPropStreamOrderPerConnection` able to fail,
   and does it cover requests AND responses, pause/resume, clog/unclog and heal?
2. Did the repair introduce a new BLOCKER or MAJOR: a determinism leak, a fault that no
   longer does what it claims, a changed exported signature (compare `frozen-api.txt` with
   `git show fc37722:agent_work/0b-simulator/frozen-api.txt` — the only expected difference
   is `TailLines` becoming its own const declaration), or golden digests that moved
   (`internal/sim/testdata/golden.txt` must be unchanged)?
3. The panic stack: `runtime/debug` is now imported by `internal/sim/sim.go` to capture the
   stack of a recovered node panic for the failure report. Confirm the stack never reaches
   the event log, a digest, a message or any decision (it must not affect replay).
4. File moves: confirm they are pure moves within a package (no behaviour or exported
   change). Then repeat the kernel-separability READING check as NOTES only (never
   blockers): the files holding (i) stream derivation, (ii) virtual clock and timers,
   (iii) event queue and loop, (iv) event log and digest, (v) seed plumbing in `simtest` —
   which still refer to network, disk, node, region or nemesis types? Does the loop treat
   event kind/node/peer as opaque integers? Would it work with the event log off or
   replaced? Is seed handling apart from the cluster builders? Say whether the builder's
   "Extraction notes" list is complete; add what it missed.
5. Freeze check: this API is frozen when the chunk lands and five protocol builders start
   on it at once. Is there anything you would still change BEFORE the freeze? An interface
   change you consider necessary is at least MAJOR — give the exact signature.

Run tests through Docker only: `docker compose run --rm -T dev go test ./...` (also
`-race`; `DST_SEEDS=50 go test ./internal/sim/...`; `go test ./internal/sim
-rapid.checks=2000`). Do not use the host Go.

## Output: agent_work/0b-simulator/REVIEW-2.md
First line exactly one of `VERDICT: PASS`, `VERDICT: NITS`, `VERDICT: FAIL`. Then a table of
the REVIEW-1 findings with status and one line of evidence each (file:line or test name),
then new findings, numbered, each with severity (BLOCKER / MAJOR / NIT / NOTE), file and
line, what is wrong, a concrete reproduction and the smallest fix. FAIL needs a BLOCKER or
MAJOR. NOTES do not affect the verdict. End with what you verified by running versus by
reading.
