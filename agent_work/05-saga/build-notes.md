# Chunk 5 build notes — the saga engine and the bucket-migration workflow

Builder: Opus (fresh seat). Branch `chunk/05-saga`. Everything below was run inside the
`dev` container (`docker compose run --rm dev ...`). Product code: `internal/saga`
(engine, log, resume state machine, policy, migration workflow, errors) and
`internal/saga/sagasim` (the model cluster, the exported matrix runner and the nemesis
and duel scenario drivers). No other package was touched.

## What was built

### `internal/saga` — the engine

- **Log.** One blob per saga, `<prefix><id>` (default prefix `saga/`), append-only. Each
  record is a frame: magic byte `0xA5`, payload length (u32 BE), CRC-32C of magic+length,
  CRC-32C of the payload, then the payload (kind, step as zigzag varint, attempt, time,
  state, definition name, data). Frame header is 13 bytes; payload at most `MaxRecord`
  (1 MiB). Record kinds: `begin` (definition, begin time, input), `intent` (step,
  attempt), `done` (step, attempt, output), `abort` (compensate-from step, cause),
  `comp-intent`, `comp-done`, `end` (Completed or Compensated).
- **Decoding rule (`DecodeLog`).** Reads whole records and returns the valid prefix
  length. A tail that is a strict prefix of a frame (header incomplete, or header complete
  and checksummed but payload incomplete) is a **torn tail**: no error, `valid < len`.
  Anything else that is wrong is `ErrLogCorrupt`: a bad magic byte, a header checksum
  mismatch, a length of 0 or over `MaxRecord`, a payload checksum mismatch, a payload
  that does not decode, has trailing bytes, is non-canonical (re-encoding must give the
  same bytes) or holds an out-of-range field. A torn append can only leave a prefix of
  what was written, so a whole frame with a bad checksum is never mistaken for a torn
  tail; a damaged last record is corrupt, exactly like a damaged middle record.
- **Write path.** Every record: apply it to a copy of the in-memory snapshot (so an
  illegal transition can never reach disk), `Append`, call the test hook, `Sync`, then
  adopt the new snapshot. The intent record is synced before the step's `Do` is called.
- **`Resume(records, steps, pivot, maxAttempts)`** is a pure state machine shared by
  recovery and the engine (the engine's in-memory state *is* a `Snapshot` advanced by the
  same `apply`). It validates every transition (any illegal sequence is
  `ErrLogCorrupt`) and plans the next action: `ActDo`, `ActAbort`, `ActCompensate`,
  `ActEnd`, or `ActNone` for a terminal saga.
  - An intent without an outcome means "outcome unknown": the step runs again as the
    next attempt with the same idempotency key.
  - Attempt counts are durable (one intent per attempt), so the retry bound holds
    across crashes: before the pivot, `attempt >= MaxAttempts` with no outcome plans an
    abort.
  - Abort compensates from the failing step itself when it is before the pivot (a step
    that failed may have half-acted, e.g. a partial copy), and from `pivot-1` when the
    pivot failed definitively (`ErrPermanent` from the pivot means it did not apply).
- **Engine (`New`, `Begin`, `Recover`, `Status`, `IDs`, `Stats`, `EncodeState`,
  `LogName`, `Frozen`).** Single-threaded; acts only through `env.Clock`, `env.Rand`
  (jitter) and `env.Disk`; never blocks. Steps are callback-style
  (`Do(Call, done(out, err))`, `Compensate(Call, done(err))`). A `Call` carries the
  idempotency `Key{Saga, Step}`, the attempt number, the begin time, the input and the
  outputs of completed steps (for a compensation: of every completed step).
  - Each attempt arms a step timeout (`Policy.StepTimeout`); a missing answer becomes a
    retryable `env.ErrTimeout`. Every callback carries a token, so a late or duplicate
    answer is ignored (tested: `a step that answers twice or late is heard once`).
  - Retry: `Policy.Backoff(attempt, rand)` = `BaseDelay·2^(attempt-1)` capped at
    `MaxDelay`, plus jitter in `[0, d·JitterPPM/10^6]` from `env.Rand`; integer
    arithmetic, overflow-safe (saturates).
  - Before the pivot: retryable failures retry up to `MaxAttempts`, then abort;
    `ErrPermanent` aborts at once. The pivot: retried without bound on retryable errors
    (an unknown outcome must not be compensated), aborted only on `ErrPermanent`. After
    the pivot: every error, `ErrPermanent` included, is retried without bound (forward
    only). Compensations: retried without bound until they succeed, in reverse order.
  - `Config.Hook(Point) bool` is called after each append and before its sync; returning
    true **freezes** the engine (all timers stopped, all callbacks ignored). The tests
    use it to crash the coordinator at an exact record, with or without a torn tail.
    Production passes nil.
  - A log write failure suspends that saga (`Status.Suspended`, `Status.Err`); it
    resumes from its log on the next `Recover` (restart).
  - `Recover` lists the prefix, and per saga: unreadable (`ErrIO`) → retried later on a
    backoff timer (status reports `ErrSagaNotFound` "recovery retrying" meanwhile);
    corrupt or unknown definition → recorded as broken, the log is left byte-for-byte as
    found, `Status` returns the error; no whole record → the blob is deleted; a torn
    tail → the blob is rewritten to the valid prefix and synced **before** anything is
    appended (otherwise the garbage would become a corrupt middle record); then the saga
    resumes. A second `Recover` in the same incarnation is a no-op.
- **Errors.** `ErrPermanent`, `ErrSagaNotFound`, `ErrLogCorrupt`, `ErrRootMismatch`;
  `*StepError{Saga, Step, Attempt, Compensation, Err}` (the brief's four fields plus
  `Compensation`). Wire codes via `saga.Codes()`: `CodePermanent` = 144,
  `CodeSagaNotFound` = 145, `CodeLogCorrupt` = 146 (`CodeAppMin+80..82`, inside the
  saga block 144–159; moved in the repair round). Invalid
  arguments wrap `env.ErrInvalidArgument`; oversized records `env.ErrTooLarge`; a frozen
  engine's `Begin` `env.ErrClosed`.

### `internal/saga` — the migration workflow (`Migration(ops) Definition`)

Five steps, pivot at index 3, input `MigrationInput{Bucket, From, To}` (length-prefixed
codec, `DecodeMigrationInput`, `Validate`: non-empty, `From != To`, each field at most
1024 bytes, the codec's own limit). Ops are five small
interfaces declared here (`Allocator`, `Copier`, `Verifier`, `Router`, `Retirer`,
combined as `MigrationOps`); nothing from storage/merkle/occ/gossip is imported.

| # | step | Do | Compensate |
|---|---|---|---|
| 0 | `allocate` | `Claim`: register this saga at the source, claim the target | `Unclaim`: release the target claim, deregister at the source |
| 1 | `copy` | `Copy` source → target; output = root of what was copied | `Discard` the copied objects |
| 2 | `verify` | `Root(to)` must equal the copy's root, else `ErrRootMismatch` (retryable) | nothing to undo |
| 3 | `flip` (pivot) | `Flip` the routing pointer to `To` with stamp (begin time, saga ID) | none (pivot) |
| 4 | `delete-source` | see below; output `moved` or `superseded` | none (forward only) |

`delete-source`: read the **merged** pointer (`Pointer` must return one agreed value or
an error). If it is not `To`, this migration lost a duel: `Discard` + `Unclaim` its
target, output `superseded`. If it is `To`: `Seal` the source (refused with a retryable
error while any other saga is registered there; reports whether the source is already
retired by this saga); if already retired → `Promote` the target; else catch-up `Copy`,
second Merkle comparison `Root(from) == Root(to)` (else `ErrRootMismatch`, retried),
`Retire` the source, `Promote` the target, output `moved`. Retire comes **before**
Promote: with the other order a crash between them made the retried catch-up copy the
sealed source over the already-live target and lose writes acknowledged there (found by
the 500-seed sweep, see "Bugs found").

Why the duel resolves: every migration of a bucket registers at the source before it
copies, and the winner may seal the source only when it is the sole registrant. A loser
deregisters when it compensates or when it sees the merged pointer name someone else.
So the source is never retired while a competing migration could still flip, and the
pointer (LWW over all replicas, flips retried until every replica has them) converges
to one value that exactly one migration reads as its own.

### `internal/saga/sagasim` — the model cluster and the matrix runner

- **Stores** (one location per store node): per bucket a status (live, staging,
  sealed, retired), the claiming owner, the sealer, source registrations, objects; plus
  a pointer replica per bucket (LWW by stamp, then writer, then location). State is one
  blob, rewritten and synced on every mutation. Ops over `env.Transport` request/
  response with errors through the code table (`sagasim.Codes()` adds `ErrBusy` 152,
  `ErrMoved` 153, `ErrUnsettled` 154, `ErrMalformed` 155 to the saga rows). Every
  operation is idempotent per saga ID. A release of a claim that still holds objects
  leaves them as visible orphans, so every compensation is necessary (the canary relies
  on this). Copies are fenced by a per-bucket generation (a late dump reply is refused
  once a discard, release, claim, promotion or newer copy happened).
- **Coordinator** process hosts `saga.Engine` with the migration definition over
  `sagasim.Ops`; **Client** writes objects through the pointer and records acks.
- **Checks**: `CheckAt(bucket, loc)` = every pointer replica names `loc`, `loc` holds
  the bucket live with no registrations and the original objects (exact root when there
  is no client), every other store holds nothing of the bucket (no staging, no orphan
  objects, no registrations); `CheckWrites` = every acknowledged write is present at the
  final location and nothing nobody wrote is; `CheckDuel`; invariant
  `sagasim/one-live-location` (never two live copies), every 20 events.
- **Exported for Chunk 6**: `Harness` / `Cluster` / `BuildOptions`, `Cells()`, `Cell`
  (`Name`, `Want`), `Run(h, cell, RunOptions) func(seed) *sim.Sim` (failures land in
  `s.Err()`, wrapped with `ErrCheck`; the migration begins 20 ms after the simulator's
  time when `Build` returns, so `Build` may warm the cluster up first; `RunOptions`
  overrides `Bound`, `Settle` and `Policy`), `MatrixPolicy()`, `ModelHarness()`, and the
  scenario drivers `NemesisRun(NemesisParams, *NemesisReport)` and
  `DuelRun(DuelParams, *DuelReport)`. Chunk 6 implements `Harness.Build` with real
  components and calls `Run` for every cell; fault injection wraps the
  `saga.Definition` it is handed, so it works for any step implementation.

## The failure-point matrix

78 cells. Injection is generic (a wrapper around the definition's steps plus the engine
hook):
- `fail-before-action`: the step refuses with `ErrPermanent` without acting (sticky for
  steps up to the pivot, once after it).
- `fail-after-action`: the step acts, then its answer is replaced by a retryable "lost
  response" (`MaxAttempts` = 3 times before the pivot, so retries run out after the
  step acted; twice from the pivot on).
- `fail-mid-compensation`: the pivot refuses, and the named step's compensation acts and
  then fails 6 times before succeeding.
- `crash-before-action`: the coordinator freezes right after appending the step's intent
  (before its sync) and is crashed, restarted 500 ms later.
- `crash-after-action`: same, after the step acted, at the append of its `done`.
- `crash-mid-compensation`: the pivot refuses; freeze at the append of the step's
  `comp-done` (compensation acted, not yet durable).
- **torn**: crash kinds use `CrashTorn` instead of `Crash`; fail kinds add a torn crash
  at the next log append after the failure. **double**: the first append after the
  restart (i.e. during recovery's first action) is torn-crashed again.
Mid-compensation cells exist for steps 0–2 only (flip and delete-source are never
compensated). Each run also asserts no step or compensation runs once the `end` record
is durable, compensations succeed in non-increasing step order, and the terminal state
passes `CheckMoved` (Completed, output `moved`) or `CheckCompensated`.

Measured over three 500-seed sweeps (bases 1, 7700000, 31337000000) plus the pinned
seeds: 1,508 runs per cell, 117,624 runs in all, every cell ended in its expected state
every time. Time is virtual time from `begin` to the terminal state.

| step | failure kind | untorn | torn | untorn, double | torn, double | worst time |
|---|---|---|---|---|---|---|
| allocate | fail-before-action | Compensated (1508/1508) | Compensated (1508/1508) | — | — | 0.66 s |
| allocate | fail-after-action | Compensated (1508/1508) | Compensated (1508/1508) | — | — | 1.23 s |
| allocate | fail-mid-compensation | Compensated (1508/1508) | Compensated (1508/1508) | — | — | 5.54 s |
| allocate | crash-before-action | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | 2.30 s |
| allocate | crash-after-action | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | 2.43 s |
| allocate | crash-mid-compensation | Compensated (1508/1508) | Compensated (1508/1508) | Compensated (1508/1508) | Compensated (1508/1508) | 1.92 s |
| copy | fail-before-action | Compensated (1508/1508) | Compensated (1508/1508) | — | — | 0.94 s |
| copy | fail-after-action | Compensated (1508/1508) | Compensated (1508/1508) | — | — | 1.82 s |
| copy | fail-mid-compensation | Compensated (1508/1508) | Compensated (1508/1508) | — | — | 5.58 s |
| copy | crash-before-action | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | 2.30 s |
| copy | crash-after-action | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | 2.49 s |
| copy | crash-mid-compensation | Compensated (1508/1508) | Compensated (1508/1508) | Compensated (1508/1508) | Compensated (1508/1508) | 1.92 s |
| verify | fail-before-action | Compensated (1508/1508) | Compensated (1508/1508) | — | — | 1.18 s |
| verify | fail-after-action | Compensated (1508/1508) | Compensated (1508/1508) | — | — | 1.71 s |
| verify | fail-mid-compensation | Compensated (1508/1508) | Compensated (1508/1508) | — | — | 4.89 s |
| verify | crash-before-action | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | 2.30 s |
| verify | crash-after-action | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | 2.44 s |
| verify | crash-mid-compensation | Compensated (1508/1508) | Compensated (1508/1508) | Compensated (1508/1508) | Compensated (1508/1508) | 1.79 s |
| flip | fail-before-action | Compensated (1508/1508) | Compensated (1508/1508) | — | — | 1.29 s |
| flip | fail-after-action | Completed (1508/1508) | Completed (1508/1508) | — | — | 2.22 s |
| flip | fail-mid-compensation | n/a | n/a | n/a | n/a | — |
| flip | crash-before-action | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | 2.30 s |
| flip | crash-after-action | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | 2.40 s |
| flip | crash-mid-compensation | n/a | n/a | n/a | n/a | — |
| delete-source | fail-before-action | Completed (1508/1508) | Completed (1508/1508) | — | — | 1.86 s |
| delete-source | fail-after-action | Completed (1508/1508) | Completed (1508/1508) | — | — | 2.45 s |
| delete-source | fail-mid-compensation | n/a | n/a | n/a | n/a | — |
| delete-source | crash-before-action | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | 2.30 s |
| delete-source | crash-after-action | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | Completed (1508/1508) | 2.53 s |
| delete-source | crash-mid-compensation | n/a | n/a | n/a | n/a | — |

Bound: `sagasim.MatrixBound` = 20 s (measured worst 5.58 s, `allocate`
fail-mid-compensation torn). Partial records: in the base-1 sweep 24,196 of 39,000 runs
recovered from at least one partial log record; each `-torn` cell saw a partial record
in 449–499 of its 500 seeds (the rest kept all or none of the record, which recovery
also handles).

## Measurements

- **Nemesis** (`TestMigrationUnderNemesisDST`, 3 chaos phases of 4 s + 2 s heal, every
  chaos kind except bit-rot, wipe-disk, wipe-rejoin; budget MinLive 3, MaxDamagedDisks 1;
  coordinator c1, stores a/b/c in three regions, client writing 24 objects): migrations
  are chained a→b→c→a through the whole chaos window. Over 1,508 runs: 6,787 migrations,
  5,306 Completed, 1,481 Compensated (pre-pivot retries exhausted under partitions/
  pauses); 2–9 migrations per run. Settle time after the last heal: max 2.85 s, mean
  0.93 s; bound `nemesisConverge` = 20 s. No acknowledged write lost (mean 17.9 acked
  writes per run).
- **Retries per step under the nemesis** (`Do` calls summed over the 6,787 migrations;
  later steps run only for migrations that reached them): allocate 8,051, copy 8,179,
  verify 5,973, flip 9,234, delete-source 12,449. Per migration that is about 1.19
  allocate calls, and flip and delete-source carry the most retries (they wait out
  partitions without a bound).
- **Duel** (`TestDuelingMigrationsDST`, c1 migrates a→b, c2 migrates a→c, partition
  {c1,b} | {c2,c} for 2–6 s, begins staggered by up to 300 ms): 1,508 runs, every one
  ends with exactly one location holding the bucket, the pointer agreeing at every
  replica, the loser's target empty. Winners: b 761, c 747; outcomes always one `moved`
  and one `superseded` (no run needed a compensation). Finish after the heal: max 3.34 s,
  mean 1.71 s (base-1 sweep); bound `duelAfterHeal` = 10 s, `duelConverge` = 90 s.
- **Repair-round re-measure** (base-1 500-seed sweep after the repair; the nemesis now
  seeds 40 objects, so with its writes the bucket ends with 41–64 objects, mean 57.9):
  500 runs, 2,245 migrations, 1,781 Completed, 464 Compensated, 2–7 per run; settle
  after the last heal max 2.85 s, mean 0.94 s; mean 17.8 acked writes; `Do` calls
  allocate 2,657, copy 2,643, verify 2,004, flip 3,072, delete-source 4,220. Duel: winners
  b 254, c 246, always one `moved` and one `superseded`, finish after heal max 3.34 s,
  mean 1.71 s. Matrix: 39,000 runs, every cell 500/500 in its expected state, 24,196 runs
  recovered from a partial record, worst 5.58 s (`copy` fail-mid-compensation torn).
  The figures above this bullet are the pre-repair three-base sweep with 8 seeded
  objects and are kept for comparison.
- **Wall time**: `go test ./internal/saga/...` at default seeds about 1 s; the
  500-seed sweep of `internal/saga` 43–49 s per base (about 52 s wall including the
  container); rapid at 2,000 checks per property under 1 s each.

## Claims and the tests that prove them

| claim | test |
|---|---|
| records round-trip; every strict prefix is a torn tail; a damaged middle or last record is `ErrLogCorrupt`; a non-canonical or oversized record is refused | `TestLogAppendRecover` |
| recovery truncates a torn tail before appending; a torn first record leaves no saga; a torn crash at every record boundary recovers and finishes | `TestLogAppendRecover` (sub-tests) |
| a damaged middle record fails recovery with `ErrLogCorrupt`, the log stays as found, nothing runs | `TestLogCorruptMiddleRecordFailsRecovery` |
| unreadable logs are retried on a timer | `TestRecoverRetriesReadErrors`, `TestEngineEdges` |
| the record-level state machine accepts exactly the legal sequences and plans the right next action | `TestStateMachineTable` (36 rows + 4 sub-tests) |
| backoff doubles to the cap, saturates, jitter is seeded and bounded; the engine waits exactly the backoff; a silent step times out | `TestRetryBackoffSchedule` |
| compensations run in reverse order, retried before the next starts, forward steps never run after compensation begins | `TestCompensationOrder` |
| one idempotency key per (saga, step) for every attempt and across a crash; keys unambiguous | `TestIdempotencyKey` |
| the pivot is retried past the attempt bound; only `ErrPermanent` aborts it; nothing after the pivot is compensated | `TestPivotSemantics` |
| `*StepError` fields; `ErrPermanent` stops retry; `ErrSagaNotFound`; codes round-trip | `TestStepErrorFields`, `TestPermanentStopsRetry`, `TestSagaNotFound`, `TestCodesRoundTrip` |
| terminal state equals a reference model for random outcomes and up to 4 crashes (hook-placed torn/untorn and timed) | `TestPropTerminalState` |
| recovering twice equals recovering once (same disk, same status, same first action) for any byte prefix of any valid log | `TestPropRecoveryIdempotent` |
| no step or compensation runs after the end record is durable, including late answers and crashes after the end | `TestPropNoStepAfterTerminal` |
| arbitrary bytes: a valid prefix or `ErrLogCorrupt`, never a panic; recovery keeps the valid prefix and finishes | `FuzzSagaLogRecover` |
| the workflow's paths (moved, superseded, already retired, root mismatch, bad input, retried ops and compensations) | `TestMigrationWorkflow`, `TestMigrationInputCodec`, `TestModelClusterReportsBadInput` |
| every cell ends Completed with a verified root and flipped pointer, or Compensated with zero orphans and the pointer unchanged | `TestMigrationFailurePointsDST` |
| migrations under the nemesis end terminal, the bucket in exactly one place, no acknowledged write lost | `TestMigrationUnderNemesisDST` |
| two coordinators across a partition: one location, pointer agreed, loser cleaned up | `TestDuelingMigrationsDST` |
| skipping the copy compensation (or the allocate one) makes a cell fail; skipping verify's no-op one does not | `TestCanarySkippedCompensation` |
| model store operations, wire, sentinels (`ErrBusy`, `ErrMoved`, `ErrUnsettled`, `ErrMalformed`), copy fencing | `sagasim` tests |

Canary: `saga.PlantSkippedCompensation(cfg, step)` (in `export_test.go`; sets an
unexported `Config` field) makes the engine log a compensation as done without calling
it. With step 1 skipped, the cell `flip-fail-before-action-untorn` fails `ErrCheck`
(orphan objects at the target) on every seed tried.

## Bugs the tests found (and their regressions)

1. **Lost acknowledged writes** (500-seed nemesis sweep, about 2.6% of seeds): promote
   before retire let a retried catch-up overwrite a live target. Fixed by retiring
   first; reintroducing the old order fails 39 of 500 seeds. Seeds 26, 53 (scenario as
   first written) and 13, 16 (scenario as moved into `sagasim.NemesisRun`) are pinned in
   `internal/saga/testdata/seeds.txt`.
2. **Matrix expectation**: a one-shot `ErrPermanent` whose abort record was torn away
   was retried and succeeded — correct engine behaviour; the injection was made sticky
   for steps up to the pivot (a definitive refusal stays refused).
3. **Canary had no teeth**: releasing a claim deleted copied objects, so the copy's
   compensation was redundant; release now leaves objects as orphans.
4. Found by review, not by a seed: late two-hop copies (fenced) and equal-stamp pointer
   ties (ordered by location).

## Stated limitations (for the prose and the adversarial pass)

- **OQ-13, lost coordinator.** No log replication, no takeover. An operator finds, on
  the coordinator's disk, the blob `saga/<id>`: decodable with `saga.DecodeLog`, and
  `saga.Resume` gives the step, attempt and state; its last record is not `end`. The
  world is in a state the log describes: before the pivot, possibly a claimed target
  with partial or full copies and a registration at the source; after the pivot, the
  pointer flipped and possibly a sealed source. A lost coordinator holding a source
  registration also blocks any other migration of that bucket from sealing the source
  (the other migration retries `delete-source` forever with `ErrBusy`).
- **A compensation that fails forever** keeps the saga in Compensating, retrying with
  backoff capped at `MaxDelay`; `Status.Err` is a `*StepError` with
  `Compensation: true` and the attempt count; the log grows by one `comp-intent` per
  attempt (about 20 bytes). Same for the pivot and later steps. Attempts are capped at
  2^31−1 by the record format (after that the saga is suspended).
- **Bit-rot or wipe of the coordinator's log** is not repaired: the saga is reported
  `ErrLogCorrupt` and left alone. The nemesis excludes bit-rot and wipes for that reason.
- Saga IDs must be unique across coordinators (the stamp tie-break relies on them);
  Chunk 6 should prefix them with the node ID. Terminal logs are never garbage-collected.
- The model's source is one node, so source-side registration and sealing are
  serialized there; in the real store they must be made consistent across the source
  region's nodes (Chunk 6).
- **For Chunk 6's real steps**: every operation must be idempotent per key, and any
  operation that relays (like the copy) must be fenced so a late execution cannot land
  after its compensation. Over the simulated network the per-link FIFO order protects
  the direct requests; the two-hop copy needed the explicit fence.
- **Idempotency-key scope for Chunk 6's `MigrationOps`** (review finding 9). A `Call`'s
  key is `{Saga, Step}` of the step that makes the call, not of the operation. Step 4
  (`delete-source`) calls `Discard`, `Unclaim`, `Copy` and `Root` with `Step: 4`, while
  step 0 calls `Claim`/`Unclaim` with `Step: 0` and step 1 calls `Copy`/`Discard` with
  `Step: 1`. So: ownership, claim, registration, discard, seal and retire checks must
  key on `c.Key.Saga` alone (the model's stores do: owner, sealer and registrations are
  saga IDs), or step 4's cleanup of a superseded target is refused as "not yours". Any
  idempotency cache for `Copy` must key on the whole `c.Key` (or `c.Key` plus attempt
  where a fresh run is wanted): step 4's catch-up copy is a second, different copy that
  must not be answered from step 1's cached result, or writes that reached the source
  between the two copies are lost.

## Errors intentionally ignored (`_ =`)

- `internal/saga/engine.go` `retryPending`: `_ = g.load(id)` — the error is recorded where
  it matters (a broken saga in `broken`, an unreadable one re-queued by `later`); there
  is no caller to return it to from a timer.
- `internal/saga/sagasim/matrix.go` `crash`: `_ = s.CrashTorn(id)`, `_ = s.Crash(id)`,
  `_ = s.Restart(id)` — a no-op when the node is already in that state is fine; the cell
  check fails anyway if the crash point was never reached.
- `Timer.Stop()`/`Reset()` results (booleans) are not used except in `later`.

## Panics, lint exemptions

No `panic(` added; no line in `panic-allowlist.txt`. No detlint exemption needed
(map ranges are only collect-then-sort; `fmt` never gets a pointer-typed argument).

## Uncovered branches

`internal/saga` 95.2%, `internal/saga/sagasim` 87.5% (own tests). Uncovered in
`internal/saga`: `write` when `apply` refuses a record the engine built (an engine bug;
unreachable), `AppendRecord` failing inside `write` (the record was already sized by
`Begin`), the `Stop()` branch of `later` with a pending retry timer, `cause` truncation
over 200 bytes, `retryPending` after a freeze, `Status` of a saga pending re-read,
`validRecord`'s step/attempt/state range errors on encode (the decoder's ones are
covered), `decodePayload`'s out-of-range branch and a few `flag`/`EncodeState`
combinations, and the compensate closures of `flip` and `delete-source` (never called: nothing at or after the pivot is compensated). In `sagasim`: error branches of the client and the checks that a correct
run never takes.

## Requests to the orchestrator

- Wire code block: done in the repair round per the allocation (saga 144–159): saga
  144–146 (`CodeAppMin+80..82`), sagasim 152–155 (`CodeAppMin+88..91`).
- No simulator change request.

## Gate run

`agent_work/gates/gate.sh 05-saga` from the worktree root on the host, at commit
`cac1008aa54dba532eac6e28dc7fb5b3215584cf` (all code and these notes up to this section),
stage build, comment baseline `9508d35`, rapid/DST seed 222930286585165 from the SHA:

```
G0  tools/gofmt/vet/build/detlint/panics/deps/comments/selfcontained  pass
G1  unit           pass    9s   (internal/saga 95.2%, internal/saga/sagasim 87.6%)
G2  property       pass   79s   rapid 2000 checks (internal/saga 2.1 s)
G3  fuzz           pass   72s   FuzzSagaLogRecover 30 s (plus the simulator's target)
G4  race           pass   50s   (internal/saga 6.6 s under -race)
G5  dst            pass  184s   500 seeds from base 222930286585165 (internal/saga 67.9 s)
G6  docker         pass
gate: overall=pass counts_for_landing=true
```

Gate wall time 413 s. Result files are under `agent_work/05-saga/gates/` (not committed).
The only commit after the gated one adds this section to the notes.

## Repair round (REVIEW-1, verdict FAIL)

Each finding got a regression test first (commit `075c7aa`, tests only), then the fix
(commit `12287b6`). For `internal/saga` the new tests failed on the old code with the
expected messages (run with the old `dst_test.go`, which the new one needs the new API
for). The `sagasim` tests use new API fields so they could not compile against the old
code; a throwaway probe in a temporary worktree at `7d7c7c9` (not committed) showed the
old behaviour: a 25 s warm-up made `Run` stall with `sim.ErrDeadline` at t=25 s; a
harness that drops `Wrap` passed `delete-source-fail-after-action-untorn`; a 33-object
migration ended Compensated with `remote error (code 123): sagasim: objects: sagasim:
malformed message`; `NemesisRun(p, nil)` panicked with a nil dereference.

| finding | test that proves the fix | what changed |
|---|---|---|
| 1 MAJOR, absolute matrix times | `TestRunnerOnAWarmedUpCluster` (all 78 cells on a harness whose `Build` runs to t=25 s; checks start after the warm-up, elapsed within the bound, settle exactly `MatrixSettle`), `TestRunOptionsOverrideBoundSettleAndPolicy`, `TestRunnerFaultsTouchOnlyTheMatrixSaga`, `TestRunnerSurvivesAHarnessWithoutASim` | `Run` begins at `s.Now()+20ms` and bounds/reports relative to it; `RunOptions.Bound/Settle/Policy` (zero = `MatrixBound`, `MatrixSettle`, `MatrixPolicy()`); the injected lost-response count and the mid-compensation failure count use the effective policy's `MaxAttempts`; the hook and the step wrapper act only on `matrixSaga` (other sagas pass through untouched and uncounted); a nil sim is replaced by a fresh one before `Fail`, a nil cluster with no error is an error; a sim already failed by `Build` is returned as is |
| 2 MAJOR, 32-object cliff | `TestObjectListCodecHasNoCliff` (0, 1, 33, 300, 4096 objects round-trip; 4097 refused; the largest possible list fits in `sim.DefaultMaxMessage`; malformed/over-bound input is `ErrMalformed`), `TestStoreReloadsAndCopiesABucketAboveTheOldLimit` (300+33 objects, restart, dump), `TestStoreWritePathEnforcesTheBound` (every bound reached exactly, the next write refused with `env.ErrTooLarge`, the state at every bound reloads), `TestBuildRefusesAnUnstorableBucket`, `TestMigrationOfABucketAboveTheOldLimit` (33 and 300 objects with a target crash/restart, Completed and `CheckMoved`), `TestMigrationUnderNemesisDST` (now 40 seeded objects; asserts the final bucket holds more than 32) | new `sagasim/codec.go`: a length-prefixed reader/writer for the store state (version byte 2) and the object list, independent of the RPC field codec. Stated bounds: `MaxObjects` 4096 per bucket, `MaxKey` 128 bytes, `MaxValue` 512 bytes, `MaxName` 1024 bytes (bucket, saga ID, location, writer), `MaxRegs` 64 registrations per bucket, `MaxBuckets` 64 buckets and 64 pointers per store. The largest list is about 2.6 MB, under the 16 MiB message limit, so a full bucket can always be dumped. The write path refuses past every bound with `env.ErrTooLarge` (put of a new key, register, claim of a new bucket, flip of a new pointer, any over-long request field); the encoder also refuses (so `persist` would fail rather than write something unreadable), and `Build` refuses `Objects > MaxObjects` with `env.ErrInvalidArgument` |
| 3 NIT, spurious `ErrNotFound` | `TestBeginWriteFailureOnSimDiskIsNotNotFound` (sim disk, append fails / sync fails: `ErrIO`, not `ErrNotFound`, no log left, a later `Begin` works) | `Begin`'s cleanup ignores `env.ErrNotFound` from `Delete`/`Sync` |
| 4 NIT, abort without an intent | three new rows in `TestStateMachineTable` | `RecAbort` requires an open intent |
| 5 NIT, aliased `Call` buffers | `TestStepCannotCorruptTheEngineSnapshot` | `Engine.call` passes `bytes.Clone` of the input and a deep copy of the outputs |
| 6 NIT, `Validate` vs codec | `TestMigrationInputValidateMatchesTheCodec` | `Validate` refuses fields over 1024 bytes |
| 7 NIT, property never exhausts | `TestPropTerminalState` now draws, per step, a 1-in-6 chance of `MaxAttempts+2` failures (pre-pivot: expected Compensated from that step; at or after the pivot: still Completed) and up to 3 post-pivot `ErrPermanent` answers (expected Completed), and fails unless both were reached during the run | `scenario.slow` removed; `postPerm`, `exhausts` added (test only) |
| 8 NIT, unfired faults, nil reports | `TestRunnerFailsACellWhoseFaultNeverFired` (every cell under a harness that drops `Wrap` and under one that drops the hook: each cell whose fault depends on the dropped part fails with `ErrCheck`, the rest pass), `TestRunnerFailsADoubleCellWithoutASecondCrash` (all 26 double cells with the second crash suppressed), `TestScenarioDriversAcceptNilReports` | `cellState.injected`: a cell fails unless its crash point fired, its pivot refusal fired (mid-compensation kinds), its failure fired (fail kinds), its torn crash happened (torn fail kinds) and its second crash happened (double cells); `NemesisRun`/`DuelRun` use a local report when given nil |
| 9 NOTE, key scope | written under "Stated limitations" above | notes only |
| wire codes | `TestCodesLieInTheSagaBlock` in both packages (every code in 144–159, exact values, no duplicates, the joined table builds) | saga 144–146, sagasim 152–155 |

**API changes (all additive except the codes):** `RunOptions.Bound`, `.Settle`, `.Policy`;
`Report.Start`, `Report.CompFails`; `NemesisParams.Objects`; `NemesisReport.Stored`;
constants `MaxObjects`, `MaxKey`, `MaxValue`, `MaxName`, `MaxRegs`, `MaxBuckets`; code
values of `saga.Code*` and `sagasim.Code*`; the store's on-disk state format (version byte
2; nothing persisted by the old format needs reading). Behaviour: `Run` on a harness whose
`Build` leaves the sim at t=T begins at T+20 ms (T=0 for the model, so the model's timings
are unchanged); a harness that does not wire `Wrap` or the hook now fails the cells it
cannot inject.

**Numbers that moved:** coverage `internal/saga` 95.2% → 95.5%, `sagasim` 87.6% → 91.3%;
nemesis figures (more objects; see "Repair-round re-measure" under Measurements); matrix
and duel figures re-measured on base 1 and unchanged in substance (worst 5.58 s, the
bound stays 20 s). Default `go test ./internal/saga/...` about 3 s (both packages); the
base-1 500-seed DST sweep of `internal/saga` 55.9 s (59 s wall with the container).

**Not done, and why:**
- The decoder does not require object keys to be sorted and unique; every writer keeps
  them sorted (`opPut` inserts in order, seeds are generated sorted), and the root is
  computed over the list as stored, so an unsorted list could only come from a bug the
  checks would already catch.
- A pointer flip refused for exceeding `MaxBuckets` at the pivot would be retried
  forever; no test creates more than one migrated bucket per store, and the bound
  exists so the state stays readable, not as a product limit.
- `TestRunnerFaultsTouchOnlyTheMatrixSaga` drives the hook and the step wrapper
  directly with a foreign saga ID; there is no end-to-end run with a second saga on the
  coordinator because the model cluster seeds one bucket.
- I did not re-run the bases 7700000 and 31337000000 sweeps; the pre-repair table keeps
  those counts.

**Adversarial harness** (`agent_work/adversarial/05-saga/run-all.sh`, not edited; its
rewritten `evidence/run-all.txt` was restored so the harness tree is unchanged):
`SUMMARY held=125 broken=0 setup_errors=0 elapsed_seconds=22`. No check pinned an old code
number or old runner behaviour.

**Repair-round gate run.** `agent_work/gates/gate.sh 05-saga` from the worktree root on
the host, at commit `b91a797d4e84fc1f7a3101b2ad1b95f465c6b063` (all repair code and the
notes above), stage build, comment baseline `8eb934d`, rapid/DST seed 203523358543492:

```
G0  tools/gofmt/vet/build/detlint/panics/deps/comments/selfcontained  pass
G1  unit           pass    7s   (internal/saga 95.5%, internal/saga/sagasim 91.3%)
G2  property       pass   91s   rapid 2000 checks (internal/saga 2.4 s)
G3  fuzz           pass   73s   2 fuzz targets, 30 s each
G4  race           pass   66s   (internal/saga 9.0 s, sagasim 8.9 s under -race)
G5  dst            pass  228s   500 seeds from base 203523358543492 (internal/saga 73.5 s, sagasim 2.4 s)
G6  docker         pass
gate: overall=pass counts_for_landing=true
```

Gate wall time 8 min 4 s. Also run before the gate, all in the dev container:
`gofmt -l .` (empty), `go vet ./...`, `go build ./...`, `go test -count=1 ./...` (all
packages ok). No `panic(` added, no panic-allowlist line, no detlint exemption. Errors
intentionally ignored: unchanged from the list above. The only commit after the gated one
adds this paragraph.
