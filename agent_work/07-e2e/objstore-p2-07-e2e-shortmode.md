# Brief — Chunk 7: a short mode for the migration failure matrix (for the CI fast lane)

Seat: Gemini 4 Neon, **BUILDER** (fresh agent). You are in a git worktree, branch
`chunk/07-e2e`, and it is your current directory. Work only here. This is a small, bounded
change: expect well under an hour.

## Why

Owner ruling, 2026-11-22: push-triggered CI runs quick tests only (build, static checks,
unit tests, a small fixed-seed smoke of the simulation and end-to-end tests), a few minutes
per push. Long runs stay available on demand and remain the landing gate. CI will run
`go test -short ./...`.

Measured on this branch (`go test -count=1 -v ./test/e2e`, default seeds): 309 s wall on 4
cores. `TestE5MigrationEveryFailurePointS` is about 970 of roughly 1,090 CPU-seconds: 78
cells, three seeds each, 234 runs. Everything else in the package together is about 120
CPU-seconds and stays as it is.

## What to build

1. In `test/e2e/e5sim_test.go`: when `testing.Short()` is true, the matrix runs a smoke
   only — the cells the race build already keeps (every eighth cell, `i%8 == 0`), each on
   ONE seed (the first of the seeds that cell would get without `-short`). Without `-short`
   nothing changes: same cells, same seeds, same order, in the default run, under
   `DST_SEEDS`, under `-seed`, and under `E2E_FULL_SEEDS=1`. An explicit `-seed=N` or
   `E2E_FULL_SEEDS=1` wins over `-short` (a replay command must always replay).
2. Put the choice in one small pure function (cell index, cell count, short, race, the
   seeds) so it can be tested without running a simulation, and test it first: without
   short it returns exactly what the code returns today for several indexes; with short it
   returns one seed for kept cells and none for the others; with short plus an explicit
   seed or full-seeds it returns what the non-short path returns; a kept cell never gets
   zero seeds.
3. The trimmed run must say so: one `t.Logf` line on the parent test naming how many cells
   and seeds ran out of how many (so a reader of a `-v` log sees it is the smoke).
4. Do not add `testing.Short()` anywhere else. Do not change any other test, any seed
   share, or any product code.

## Verify (through Docker only, one run at a time; record command, result, wall time)

- `docker compose run --rm -T dev gofmt -l .` (empty) and `go vet ./...`
- `docker compose run --rm -T dev go test -count=1 ./test/e2e -run 'TestE5'` — the
  default: must still run all 78 cells × 3 seeds (count the `seed=` subtests in a `-v` run
  and state the number: 234).
- `docker compose run --rm -T dev go test -short -count=1 -v ./test/e2e -run 'TestE5'` —
  state cells and runs.
- `docker compose run --rm -T dev go test -short -count=1 ./test/e2e` — state wall time.
- `docker compose run --rm -T dev go test -short -count=1 ./test/e2e -run 'TestE5MigrationEveryFailurePointS' -seed=1`
  — must run every cell on seed 1 (state the count).
- `GATE_ONLY=G0 agent_work/gates/gate.sh 07-e2e`, then
  `git checkout -- agent_work/07-e2e/gates` if that changed tracked files.

## Rules

- `agent_work/07-e2e/objstore-p2-builder-rules.md` applies (read "Opus" as "you"): Go
  standard library only, NO comments in `.go` files, no dropped errors.
- Do not touch: PLAN.md, STATE.md, README.md, docs/, book/, kb/, .github/, Dockerfile,
  compose.yaml, agent_work/gates/, agent_work/orch/, agent_work/adversarial/,
  agent_work/INDEX.md, REVIEW-1.md, FABLE-REVIEW.md.
- Git: one commit for the code and its test, subject `e2e: <imperative>`, body saying what
  the test proves, ending with the trailer line `Built-By: Gemini 4 Neon`; one commit
  appending a short section **"14. Short mode for the fast lane (Neon)"** to
  `agent_work/07-e2e/build-notes.md` with the numbers above. Never push, merge, rebase,
  reset, stash or switch branches. Finish with `git status` clean.
- Machine: 4 cores, 7 GB, shared. Do not claim anything you did not run.

Final message: the two commits, the measured numbers, anything not done. Then the marker
line your launcher asks for.
