# Saga robustness test report

Completed against product commit `3bf50eda7ac020e03354fec2968284c909c17cf2`.
All authored changes are under `agent_work/adversarial/05-saga/`; no product
changes, git writes, commits, or external services were involved.

## Reproduce

From the worktree root:

```sh
bash agent_work/adversarial/05-saga/run-all.sh
```

The four no-argument drivers run codec, engine, migration, and supplemental
checks through `docker compose run --rm -T dev`. The single nested Go module
imports the product via `replace ... => ../../..`; it adds no dependencies.
Each check prints one HELD/BROKEN line. Drivers return 0 for all held, 1 for a
failed check, 2 for setup failure. `run-all.sh` collects the check lines and its
summary in `evidence/run-all.txt`. Temporary container output is deleted on exit.
The simulators use in-memory disks; no simulated cluster survives the process.

The first resumed run was 118 held / 0 broken / 0 setup errors in 12 seconds.
The final expanded full run (including the exact ErrBusy fence assertion) was:

```text
SUMMARY held=125 broken=0 setup_errors=0 elapsed_seconds=15
```

The evidence file is authoritative for the most recent runtime. These are
125 check groups, not 125 individual simulations. No remaining BROKEN finding
requires a minimal reproduction.

## Correction of the inherited results

The previous 33-held/85-broken output did not describe the current source.
Its matrix failures used 33 initial objects; the existing partial source already
used 1/8/32. The model's `encodeObjs` emits two fields per object, while
`decodeFields` caps the count at 64. Thus 33 objects cannot round-trip through
the store's state/wire decoder. This explains missing pointers, early
compensations, and unreachable injection points without invoking an engine
failure. The same old evidence used 100 acknowledged copy-time writes, whereas
the current scenario uses 8 initial objects and 16 new writes. The chaos scenario
now uses 8 initial objects and at most 24 new writes.

I compared the fixture to `sagasim.ModelHarness`, verified the limits in the
codec, reran all 78 matrix cells, and added a startup control to every matrix
fixture: run the startup events and require `CheckCompensated` to confirm the
initial pointer, source data and empty target before the migration begins.
All matrix checks pass at supported sizes. Every other formerly BROKEN group
also passed when rerun from the current source, including chaos, same-target
contention, concurrent writes, invalid input and permanent coordinator loss.
The inherited `evidence/migration-rerun.txt` is historical, not current evidence.

The engine's inherited crash loop enumerated positions 2..12, but its baseline
trace has only nine appends. I narrowed it to positions 2..9, asserted that each
first crash happened, and required both crashes for all pre-end positions.
An end record fully retained by a torn crash legitimately needs no recovery
append, so that final position only requires the first crash. The earlier
22-schedule coverage wording is withdrawn; the final check covers 16 schedules.

## Claims and checks

C1–C20 follow the builder's claims table in order. C21 is the stated coordinator
loss limitation. Plan section 4.2's required scenarios map to these same IDs.
Every row below was exercised successfully in this run; the limits column is
part of the conclusion, not an assertion of exhaustive proof.

| ID | Claim | Checks run and observed result | Sample / tolerance |
|---|---|---|---|
| C1 | Records round-trip; strict prefixes are torn; corruption, noncanonical and oversized records are refused | `roundtrip-prefixes`, `bit-corruption`, `canonical-size-limits`, `checksummed-length-limits` all held; malformed inputs returned the expected sentinel | 100 random traces and every byte prefix; every single-bit change of a complete four-record trace; valid-CRC zero/oversized lengths; exact comparisons |
| C2 | Recovery truncates tails; partial begin creates no saga; interrupted appends recover | `all-prefix-recovery`, C11 crash schedules and C16 torn cells all held | Every byte prefix of the fixture's complete log; durable recovered log must decode with no tail |
| C3 | Middle corruption stops recovery without mutation or execution | `corrupt-middle-recovery`, `corrupt-last-and-unknown-definition` held | Every byte of a middle intent changed separately; damaged last record and unknown definition; bytes unchanged, zero calls |
| C4 | Unreadable logs are retried on timers | `read-fault-recovery`, `permanent-read-fault` held | 1/2/17 transient failures completed once; 10,000 injected failures remained pending at 1 virtual second with unchanged bytes |
| C5 | State machine accepts legal transitions and plans the next action | `legal-sequences-and-deletions`, `retry-bound-and-pivot-plans`, C11 terminal grid held | All prefixes of 1..12-step successful traces; each removed intent/done rejected; 1..19 attempts at/before pivot |
| C6 | Backoff doubles, caps and saturates; jitter is reproducible/bounded; silent steps time out | `backoff-extremes`, `jitter-bound`, `silent-timeout-bound`, `exact-backoff-log-times` held | 395 extreme-delay draws; 10,000 bounded jitter draws; nine calls exactly at 0/1/3/7/15/23/31/39/47 ms; zero timing tolerance |
| C7 | Compensations retry in reverse order with no later forward action | `reverse-compensation`, `forever-compensation`, C16 mid-compensation cells held | Six failing-step positions, two failed compensations before each success; perpetual failure observed for 1 second (>100 attempts) with StepError and no false completion |
| C8 | Idempotency keys remain stable and unambiguous | `key-collision-grid`, `lost-response-key`, `idempotent-effect-after-lost-response` held | 707 binary key pairs; 21+ attempts across restart; timeout and crash each caused two calls but one effect in a key-deduplicating step |
| C9 | Pivot retries beyond bound; permanent pivot refusal aborts; post-pivot work only goes forward | `pivot-and-postpivot`, `permanent-pivot-vs-after`, C16 flip/delete cells held | 30 retryable failures at pivot and after pivot; permanent failures before/at/after boundary; no post-pivot compensation |
| C10 | Error fields, permanent errors, missing IDs and wire sentinels | `error-wire-roundtrip`, `permanent-and-status-errors`, C9 permanent cases held | Every saga sentinel wrapped in StepError and encoded/decoded; exact saga/step/compensation fields and errors.Is |
| C11 | Terminal outcomes agree with a reference rule; crashes do not strand supported runs | `terminal-reference-grid`, corrected `crash-and-double-crash-grid`, C16 held | 180 pivot/failure/retry cases against an independent terminal rule; 16 hook schedules with verified injections; crash loop checks terminality, matrix checks exact expected state |
| C12 | Repeated recovery is equivalent | `double-recover-prefixes`, `compensating-prefix-recovery` held | Every byte prefix of complete forward log; 101 compensating prefixes; exact final bytes and action/order comparisons |
| C13 | Nothing runs after durable end, including late callbacks/restarts | `duplicate-late-end` and every matrix cell held | Immediate duplicate success and delayed failure callbacks; terminal restart; exactly four forward calls in the dedicated fixture |
| C14 | Arbitrary bytes yield a prefix or corruption, never panic; valid prefixes recover | `random-byte-decoding`, C1 prefix tests, C2 recovery tests held | 50,000 deterministic byte strings of length 0..511, half given the frame magic; exact error/range checks |
| C15 | Workflow paths and input codec behave correctly | `input-boundaries`, `input-truncation-garbage`, `workflow-branch-oracle`, C16 held | Field lengths 1/127/128/1024/1025; all seven truncated encodings plus trailing byte; independently specified moved/superseded/already-retired/persistent-root-mismatch/transient-root-mismatch operation traces |
| C16 | Failure matrix ends moved and clean or compensated and clean | All 78 named cells held | 40 seeds each, 3,120 runs; 1/8/32 objects; exact state/root/pointer/orphan checks; 20-second virtual terminal bound |
| C17 | Chaos migrations terminate and preserve acknowledged writes; writes may arrive during copy | `nemesis-dense-false`, `nemesis-dense-true`, `writes-during-copy` held | 300 chaos runs, four phases each; 100 delayed-copy runs with 16 writes and 300 ms extra response delay; exact acknowledged-byte preservation; settle within 20 virtual seconds |
| C18 | Competing coordinators converge to one location and clean the loser | Two `duel-stagger-*` groups and `same-target-contention` held | 300 partition duels, 10–20 seconds partition, 0/2 seconds max stagger; finish within 10 seconds after heal; 100 same-target duels, one completion and one compensation |
| C19 | Cleanup canaries detect omissions | Three `skipped-compensation-*` groups held | 10 seeds per skipped step: omission of allocate/copy cleanup detected; omission of verify no-op passed; startup control rules out invalid initial fixtures |
| C20 | Model operations, wire errors, malformed input and copy fencing | `model-wire-sentinels`, `unknown-source-target`, `late-copy-fence`, `malformed-store-requests` held | All sentinels round-trip; unknown bucket/target and identical locations compensate; 20 paused-source schedules reject stale copy with ErrBusy; six malformed requests preserve cluster state |
| C21 | Permanent coordinator loss leaves inspectable unfinished work, without takeover | `coordinator-lost` held | Seed 993001; crash after copy action before durable done; still nonterminal after one virtual minute; durable log decoded and resumed as a snapshot |

### Required plan scenarios

- Crash/double crash leaving an orphan or partial pointer: all 78 C16 cells,
  plus C11 recovery crash schedules; cleanup and pointer agreement held.
- Lost response with duplicate execution: C8 stable keys and deduplicating side
  effect fixture, C16 fail-after-action at every step. The engine retries calls;
  the step implementation must make effects idempotent.
- Compensation failing indefinitely: C7 forever-compensation remains observable
  and retrying; it does not promise terminality while failures persist.
- Corruption in the middle: C1 bit mutations and C3 actual engine recovery.
- Crash during recovery: C11 second append crash and C16 double cells.
- Writes at the source during copy: C17 deliberate response delay plus concurrent
  workload, and two chaos schedules.
- Two sagas on one bucket: C18 different-target partition duels and same-target
  contention, with unique saga IDs.

## Seeds and reproducibility

Seeds are constants in the source, not wall-clock values. Matrix seed i is
`951001 + 7919*i`, i=0..39; object counts rotate 1/8/32. Chaos uses
`970001 + 101*i`, i=0..149, in each density. Partition duels use
`980001 + 103*i`, i=0..149, in each stagger group. Same-target seeds are
990001..990100; copy/write seeds 991001..991100. Supplemental checks use
1001001..1001300 as named in source, including fence seeds 1001100..1001119.
The codec uses PCG seeds 91001/4 and 91002/9; jitter uses 912/1 and 913/2.
Run any driver independently to repeat its group without modifying parameters.

## Limits and untested cases

- This is Chunk 5's model, not the real distributed store. Chunk 6 integration,
  real sockets, real storage failures and source-region coordination are outside
  scope. Product scenario runners and invariant checkers are reused for the
  matrix/chaos/duels; the supplemental workflow traces, timing oracle, direct
  operation schedules and engine fixtures provide independent checks, but are
  not a second complete implementation of the simulator or migration engine.
- The 32-object model codec boundary is a fixture restriction for this report.
  Behavior above it is not reported as 78 separate engine defects. The public
  model builder does not establish an unlimited-size guarantee here. This report
  does not prove that unsupported fixture sizes are rejected early or cleanly.
- No statistical probability, distribution, or universal liveness claim follows
  from these deterministic samples. Tolerance is zero for data/state equality;
  the only timing slack is the explicitly listed virtual deadline. “Forever”
  compensation was observed for a finite interval, supported by inspection of
  the retry path; the 2^31−1 attempt-format limit was not reached experimentally.
- Step idempotency is a caller responsibility. The engine cannot prevent an
  arbitrary non-idempotent external step from applying an effect twice. Our
  side-effect fixture explicitly models key deduplication outside the engine.
- No guarantee is made for permanently lost coordinators, damaged/wiped logs,
  shared saga IDs across coordinators, or failures that never heal. Coordinator
  log corruption is detected, not repaired. Terminal log collection is absent.
- State-machine checks cover selected legal/illegal trace families, not all
  possible record combinations. Random byte decoding does not exhaust all valid
  CRC-framed malformed payloads. Recovery prefixes cover representative running
  and compensating traces, not every possible workflow history.
- The skipped-cleanup canary uses a public Step wrapper because the product's
  unexported skip flag is only exposed inside its own test binary.

## Handoff

The requested harness and report are complete. Re-run `run-all.sh` after product
changes and use `evidence/run-all.txt` for the latest verdict. No product repair
is requested by this run. Root `STATE.md` was read but not edited because the
brief explicitly restricts writes to this directory.
