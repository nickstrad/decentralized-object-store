# Chunk 5 — the saga engine and the bucket migration: findings, limits, pitfalls

What was built, the log format, the 78-cell matrix table and every measurement are in
`agent_work/05-saga/build-notes.md`; the outside harness is in
`agent_work/adversarial/05-saga/RESULTS.md`. This entry holds what a later builder must know
and would not guess.

## Findings

- Retire the source before promoting the target. With the other order, a crash between the
  two made the retried catch-up copy the sealed source over the already-live target, and
  writes acknowledged there were lost: about 2.6% of nemesis seeds, 39 of 500 when the old
  order is put back. Seeds 26, 53, 13 and 16 are pinned in
  `internal/saga/testdata/seeds.txt`.
- Recovery truncates a torn tail and syncs before it appends anything; otherwise the partial
  bytes become a corrupt middle record. A strict prefix of a frame is a torn tail; a whole
  frame with a bad checksum is `ErrLogCorrupt`, also when it is the last record.
- Wire codes: saga owns block 144–159. `internal/saga` uses 144–146 (`CodePermanent`,
  `CodeSagaNotFound`, `CodeLogCorrupt`), `sagasim` 152–155. Project blocks: gossip 80–95,
  storage 96–111, merkle 112–127, occ 128–143, saga 144–159.
- Measured in the `dev` container, virtual time, 500 seeds unless noted. Matrix: every cell
  ended in its expected state in all 1,508 runs (three sweeps plus pinned seeds); worst
  time from begin to terminal state 5.58 s against a bound of 20 s. Nemesis with 40 seeded
  objects: 2,245 migrations, 1,781 Completed, 464 Compensated, settled at most 2.85 s after
  the last heal, no acknowledged write lost. Duel across a 2–6 s partition: one `moved`
  and one `superseded` every time, done at most 3.34 s after the heal. Wall time of the
  `internal/saga` sweep: about 56 s.

## Rules for real `MigrationOps` (integration chunk)

- A `Call`'s key is `{Saga, Step}` of the step that makes the call, not of the operation.
  Step 4 (`delete-source`) calls `Discard`, `Unclaim`, `Copy` and `Root` with `Step: 4`;
  step 0 calls `Claim`/`Unclaim` with `Step: 0`, step 1 `Copy`/`Discard` with `Step: 1`.
- So ownership, claim, registration, discard, seal and retire checks key on `c.Key.Saga`
  alone, or step 4's cleanup of a superseded target is refused as "not yours".
- An idempotency cache for `Copy` keys on the whole `c.Key`. Step 4's catch-up copy is a
  second, different copy; answered from step 1's result, it loses the writes that reached
  the source between the two.
- Every operation is idempotent per key. An operation that relays (the copy) needs a fence,
  so that a late execution cannot land after its compensation; the model uses a per-bucket
  generation. `Pointer` returns one agreed value or an error.
- Saga IDs must be unique across coordinators (the pointer stamp is begin time, then saga
  ID); prefix them with the node ID. Source registration and sealing are serialized on one
  node in the model and must be made consistent across the source region's nodes.

## Re-running the failure matrix on real components

- Implement `sagasim.Harness.Build`, then call `sagasim.Run(h, cell, RunOptions{})(seed)`
  for every cell of `sagasim.Cells()`. Failures land in `s.Err()`, wrapped with `ErrCheck`.
- `Build` may advance virtual time to warm the cluster up: the migration begins 20 ms after
  the time at which `Build` returns, and the bound and the reported time count from there.
- `RunOptions.Bound`, `.Settle` and `.Policy` override `MatrixBound` (20 s), `MatrixSettle`
  and `MatrixPolicy()`; the injected failure counts follow the policy's `MaxAttempts`.
- `Build` must apply `BuildOptions.Wrap` and `BuildOptions.Configure`. A cell fails if its
  fault never fired (crash point, failure, torn crash, second crash of a double cell), so a
  harness that drops either one cannot pass by accident.

## Stated limits

- No log replication and no takeover: a permanently lost coordinator leaves its saga
  unfinished. Its log blob `saga/<id>` decodes with `DecodeLog`, and `Resume` gives step,
  attempt and state. Its source registration also blocks any other migration of that bucket
  at `delete-source` (`ErrBusy`, retried forever).
- A compensation that never succeeds retries forever, with backoff capped at `MaxDelay`;
  so do the pivot and later steps. `Status.Err` is a `*StepError`; the log grows by about
  20 bytes per attempt.
- A corrupt log is not repaired: `Status` reports `ErrLogCorrupt` and the log is left as
  found. The nemesis excludes bit-rot and wipes. Terminal logs are never deleted.

## Pitfalls

- A list codec with a field cap gave the model store a cliff at 32 objects: it wrote larger
  buckets and could not reload or copy them. The outside harness hit it first and the
  failures were misread as a set-up mistake. The store now has stated bounds
  (`MaxObjects` 4096 and others) and refuses a write past them with `env.ErrTooLarge`.
- A one-shot `ErrPermanent` whose abort record is torn away is retried and may succeed.
  That is correct; an injected refusal that must stay refused has to be sticky.
