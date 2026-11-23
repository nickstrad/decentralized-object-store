# Brief — a per-seed guard in the Merkle lying-peer scenario fails a seed on a correct product

Seat: Claude Opus, builder (fresh agent). You are in a git worktree, branch `chunk/07-e2e`;
it is your current directory. Work only here. Small and bounded: one test file, expect
well under an hour. No product code changes.

## The finding

The landing gate's 500-seed sweep (seed base taken from the commit) failed in
`internal/merkle`, a package that landed earlier and has passed about ten such sweeps:

```
--- FAIL: TestLyingPeerDST/honest_replicas_converge_beside_a_liar_and_never_store_its_bytes/seed=214339956433005
    dst_test.go:621: the liar was never engaged: failed 23 invalid 0
REPLAY: go test ./internal/merkle -run '^TestLyingPeerDST$/^honest_replicas_converge_beside_a_liar_and_never_store_its_bytes$/^seed=214339956433005$' -seed=214339956433005 -count=1
```

Reproduced by the orchestrator (deterministic). On this seed the honest replicas converge
inside the bound, no round exceeds its round-trip bound and no invalid byte is stored — every
safety claim of the scenario holds. What fails is the guard at the end of the subtest,
`if failed == 0 || invalid == 0`, which demands of EVERY seed that the liar's bad chunk
bytes were fetched at least once. The liar picks its lie per reply at random, so on a rare
seed (this is the first in several thousand) no honest worker reaches the chunk-fetch stage
with it before convergence. A guard that fails without a defect breaks the suite's rule
that a failing seed means a broken claim.

## What to do (in `internal/merkle/dst_test.go` only; read the whole scenario first)

1. First confirm the diagnosis from the code and the replay, not from this brief: which
   lie kinds the liar has, how one is chosen, and why `invalid` can stay 0 while `failed`
   is 23. If you find instead that the product skipped something it should have done on
   this seed, STOP and report that — do not change the guard.
2. Keep the guard's purpose (the scenario must not pass vacuously, never meeting a lie)
   and make it exact:
   - per seed, require only what every seed must show by construction (the liar was
     contacted and its replies rejected: `failed > 0`, and anything else that is certain);
   - move the "bad chunk bytes were actually fetched and rejected" requirement
     (`invalid > 0`) to the sweep: summed over the seeds of the run, guarded by a minimum
     seed count so a single-seed replay or the default three-seed run cannot false-fail.
     State the floor you choose and the measured rate behind it (run
     `DST_SEEDS=500 go test -count=1 ./internal/merkle -run '^TestLyingPeerDST$'` with two
     different `DST_SEED_BASE` values and report how many seeds have `invalid == 0`).
   - if a seed can cheaply be made to engage every lie kind by construction instead (a
     deterministic schedule of lie kinds rather than a random pick), that is better than a
     statistical floor: say which you chose and why. Do not change what the liar can do.
3. Pin the seed: add the PIN line to `internal/merkle/testdata/seeds.txt` so the sweep
   always replays it.
4. Prove the guard still bites: in a scratch copy under `/tmp` (never in the tree), make
   the liar honest (or make honest nodes never contact it) and show the scenario FAILS on
   the guard, at default seeds and at `DST_SEEDS=40`.
5. Look for the same pattern in the other DST scenarios of this file (a per-seed
   requirement on something only probable). Fix any you find the same way; list them.

## Verify (through Docker, one run at a time; record command, result, time)

`docker compose run --rm -T dev gofmt -l internal/merkle`; `go vet ./internal/merkle`;
`go test -count=1 -cover ./internal/merkle` (coverage must stay at or above 85%);
the REPLAY command above (must pass);
`DST_SEEDS=500 DST_SEED_BASE=214339956432692 go test -count=1 ./internal/merkle` (the gate's
failing sweep; must pass); the same with `DST_SEED_BASE=1`;
`go test -race -count=1 ./internal/merkle`.

## Rules

- Go standard library and the test-only rapid dependency already present; no sleeps or wall
  clock; no dropped errors; frozen simulator packages are not edited.
- This file already carries landed comments. Do NOT add, edit or remove any comment (prose
  belongs to another seat and a check enforces it); if a comment next to the guard becomes
  untrue because of your change, leave it and report it in your final message.
- Do not touch anything outside `internal/merkle/dst_test.go` and
  `internal/merkle/testdata/seeds.txt`.
- Git: one commit, subject `merkle: <imperative>`, body saying what the change proves and
  the measured numbers. Never push, merge, rebase, reset, stash or switch branches. Finish
  with `git status` clean.
- Machine: 4 cores, 7 GB, shared. Do not claim anything you did not run.

Final message: the commit, the diagnosis in two sentences, the measured rate, the mutation
result, any comment that is now untrue, any other scenario with the same pattern.
