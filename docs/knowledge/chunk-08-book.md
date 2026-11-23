# Chunk 8 — the publishing finish: findings, pitfalls

What was done is in `agent_work/08-book/` (style sheet, briefs, the editors' and writers'
reports, the acceptance check `REVIEW-1.md`) and `agent_work/adversarial/08-book/RESULTS.md`
(the independent check). This entry holds what a later editor must know and would not guess.

## Findings

- A claims audit against the code is worth its cost even after every chapter passed its own
  review. 13 of 381 claim groups said more than the evidence, two of them guarantees:
  "every square submatrix is invertible" (true only of k-by-k submatrices of the coding
  matrix) and "permanent damage within the budget never makes a GET unavailable" (true only
  once a write is on all k+m shards; a PUT acknowledged at quorum, k+1 by default, may hold
  fewer). Both have executable counterexamples in
  `agent_work/adversarial/08-book/claims_test.go`.
- Numbers with no kept record cannot be defended later. Two fuzz execution counts in the
  gossip chapter were in no log or result file and were removed. Keep the log, or do not
  print the number.
- "Every failure prints a replay line" is true of single-seed scenarios run through
  `simtest.RequireDeterministic`. Sweep-level checks (a floor across many seeds) fail in
  the parent test and have no single seed to replay; rerun the sweep.

## Pitfalls

- `compose.yaml` fixes the Compose project name, so every checkout on one machine shares one
  project. A `docker compose down` in any clone stops the containers of all of them: a
  cleanup step in the README command check stopped a running gate this way. Anything that
  works in a second clone sets `COMPOSE_PROJECT_NAME` first and never removes volumes (the
  Go cache volumes are named and shared by design).
- `agent_work/gates/readme-commands.sh` runs every block of the README fenced exactly as
  `sh`, top to bottom in one shell on a clean clone. Long runs belong in blocks fenced as
  `text`. A wait loop in the README needs a bound, or the check can only time out.
- A Go test file placed under `agent_work/` without its own `go.mod` becomes a package of
  the root module and runs in `go test ./...`. Independent harnesses get a nested module
  with a `replace` to the repository root.
- An edit for length drops detail. Compare the numbers in each chapter before and after
  (a sorted list of numeric tokens is enough) and read what disappeared.
- `prose-check.sh` rejects a prose commit that carries anything but reader-facing files and
  Go comments. The three prose commits of the publishing finish also carried the prose
  seat's work reports (`agent_work/08-book/*-REPORT.md`), so the check ends with four
  findings of that one kind. Commit a prose seat's report separately, without the trailer.
  The check was run only after the landing here; run it before, while history can change.
- Publishing ahead of a running final gate (owner's ruling): merge by hand from the main
  checkout (`land.sh` needs a gate result), leave the gate's worktree alone, and afterwards
  give `prose-check.sh` the old main and the branch tip, since a range with a merge in it
  is itself a finding. The gate result is then committed straight to main.
