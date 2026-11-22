# OCC independent test report

Completed the requested checks against `internal/occ` in this worktree. The full run
produced **59 HELD, 2 BROKEN, 0 setup errors** (61 check lines across 34 claims).
The final run took **51 seconds**, returned exit 1, and left no temporary build directories.
The two counterexamples concern quota recounts. All 34 existing claim regression groups
passed; their narrower tests did not cover the two failing boundaries below.

Run from the worktree root:

```sh
bash agent_work/adversarial/04-occ/run-all.sh
```

Exit 1 is expected while the two counterexamples remain. Exit 0 means all checks held;
exit 2 means setup did not complete. Each driver runs without arguments. Docker runs all
Go compilation and execution; the host only orchestrates and parses JSON. Temporary build
and copied test-fixture directories are created here and removed by traps. No commits,
git writes or changes outside this directory were made.

## Claim inventory and coverage

C1–C34 follow the builder's claims table in its original order, starting with vector
comparison. These concise labels retain that ordering; the complete source statements are
in `../../04-occ/build-notes.md`, under “Claims and the tests that prove them”.
`claims.json` records the exact existing tests selected for each claim. Every row also has
a `builder-regression` result in the evidence. Those are reruns, explicitly **not** new
independent tests. Most claims receive multiple existing tests plus new API checks; the two
private canary switches are exercised only through their existing multi-scenario tests.

| Claim | Statement | New independent checks and observed outcome |
|---|---|---|
| C1 | Vector normalization, partial order and join | `vector-model` HELD |
| C2 | Precondition matrix and atomic refusal | `precondition-table-delete-false` HELD; `precondition-table-delete-true` HELD |
| C3 | Single-replica model and unique write ETags | `etag-sequence` HELD; `single-replica-model` HELD |
| C4 | Deterministic version ETags and parsing | `etag-sequence` HELD; `encoding-copy-isolation` HELD |
| C5 | Total LWW winner including stamp and ID ties | `decoded-permutations` HELD |
| C6 | Discard counting, logging, callbacks and bounded tail | `discard-accounting` HELD; `saturating-count-and-dot-rotation` HELD; `partition-two-writer` HELD |
| C7 | Causal overwrites do not discard | `discard-accounting` HELD; `single-replica-model` HELD |
| C8 | Discarded dots never reissued | `etag-sequence` HELD; `saturating-count-and-dot-rotation` HELD; `crash-wipe-epochs` HELD |
| C9 | Every acknowledged loss has a seen successor or log | `partition-loss-oracle` HELD; `partition-two-writer` HELD |
| C10 | Causal successor beats observed version under skew | `clock-boundaries` HELD; `hlc-observe-many` HELD |
| C11 | Merge laws and independent LWW maximum | `decoded-permutations` HELD |
| C12 | Duplicate and reordered delivery converges in bytes and ETags | `decoded-permutations` HELD; `partition-loss-oracle` HELD |
| C13 | Far-future key locality and clock exhaustion | `clock-boundaries` HELD |
| C14 | HLC observation, negative skew and monotonicity | `clock-boundaries` HELD; `hlc-observe-many` HELD |
| C15 | Persist rollback leaves no visible trace | `persist-rollback` HELD |
| C16 | Quota CAS, retries and arithmetic bounds | `quota-retry-recount` HELD |
| C17 | Recount convergence, stale retries and no-op when correct | `quota-retry-recount` HELD; `recount-stale-noop` BROKEN; `quota-partition-measurement` HELD |
| C18 | Canonical document encoding | `decoded-permutations` HELD; `encoding-copy-isolation` HELD |
| C19 | Malformed documents, prefixes, state blobs and results rejected | `decode-corpus` HELD; `repeated-key-large-input` HELD |
| C20 | Repeated-version input rejected early | `decode-corpus` HELD; `repeated-key-large-input` HELD |
| C21 | Decode and merge operation bounds | `repeated-key-large-input` HELD |
| C22 | Decode safety, round trips and total store merge | `decode-corpus` HELD |
| C23 | Typed metadata keys, accessors, recount and durable node writes | `typed-roundtrips` HELD; `recount-overflow` BROKEN |
| C24 | Error codes 128-143 unique, 131 absent, round trips | `wire-code-table` HELD |
| C25 | Crash persistence, corrupt state epoch and read-error recovery | `read-errors-and-bitrot` HELD; `crash-wipe-epochs` HELD |
| C26 | Wipe changes actor epoch and classifies concurrency | `crash-wipe-epochs` HELD |
| C27 | Wire malformed and client operation semantics | `malformed-node-wire` HELD |
| C28 | Peer choice independent of membership ordering | `peer-permutation-digests` HELD |
| C29 | Multi-region fault workload converges with accounted losses | `partition-loss-oracle` HELD |
| C30 | Offline region changes win, are superseded or logged | `partition-loss-oracle` HELD |
| C31 | Same-node and partition-side conditional write races | `same-node-wire-race` HELD; `partition-two-writer` HELD |
| C32 | Partition quota overshoot and recount bounds | `quota-partition-measurement` HELD |
| C33 | Missing discard canary detected | Existing canary scenarios only (HELD) |
| C34 | Arrival-order canary detected | Existing canary scenarios only (HELD) |

## Counterexamples and minimal reproductions

### C17 — unchanged-result recount does not validate its stale read

```sh
CHECK=recount-stale-noop bash agent_work/adversarial/04-occ/a01-core.sh
```

Observed, exit 1:

```text
BROKEN C17 recount-stale-noop: seed=4701 initial quota=5; recount hook writes quota=7 then returns 5; calls=1 final bytes=7 without retry
```

Smallest sequence (the store helper supplies a clock, node ID and deterministic RNG):

```go
s := store("a", 0)
s.WriteQuota("q", occ.Usage{Bytes: 5}, occ.IfNoneMatch())
calls := 0
want := occ.Usage{Bytes: 5}
s.RecountQuota("q", 3, func(string) (occ.Usage, error) {
    calls++
    if calls == 1 {
        _, p, _ := s.ReadQuota("q")
        s.WriteQuota("q", occ.Usage{Bytes: 7}, p)
    }
    return want, nil
})
// Returns Usage{Bytes:5}, wrote=false, nil; stored usage is 7; calls is 1.
```

The executable checks every intermediate error. The first read is 5; the callback changes
the quota to 7 and returns the correct recount 5. `recountQuota` compares the callback result
with its *old* read, then returns without validating the ETag. This is the same synchronous
callback-interleaving model used by the builder's stale-read tests, not an unsupported
multi-goroutine store call. A retry should reread 7 and write 5 (or return a stale-read error
if its attempt bound is exhausted). It instead reports success while leaving 7 stored.
The failure does not depend on RNG; seed 4701 supplies the store configuration.

### C23 — typed object recount silently wraps the total bytes

```sh
CHECK=recount-overflow bash agent_work/adversarial/04-occ/a01-core.sh
```

Observed, exit 1:

```text
BROKEN C23 recount-overflow: seed=4701 two objects of sizes MaxUint64 and 1 recount as bytes=0 objects=2 without error
```

Smallest sequence:

```go
s := store("a", 0)
s.PutObjectMeta("b", "x", occ.ObjectMeta{Size: math.MaxUint64}, occ.Unconditional())
s.PutObjectMeta("b", "y", occ.ObjectMeta{Size: 1}, occ.Unconditional())
u, err := s.RecountObjects("b")
// u == Usage{Bytes:0, Objects:2}; err == nil.
```

Both individual metadata sizes are accepted by the exported typed API and fit its uint64
representation. The sum does not. `RecountObjects` uses unchecked `u.Bytes += m.Size`.
The check accepts an error or saturation rather than requiring an impossible exact uint64
sum. Returning zero without an error violates recount accuracy and could write a false
quota total. This is specifically a representational boundary; it does not imply that a
physically stored object of that size was created. Seed 4701 is incidental.

## Required plan scenarios

- **Acknowledged write disappearance:** `partition-loss-oracle`, 40 seeds 4701–4740,
  six nodes in three regions, 100 operations each, 3% drop and 33% duplication, clock
  skew from -1.4 to +2.1 seconds and drift from -3800 to +5700 ppm. The workload records
  the exact held version and its transitive observed predecessors at every successful
  write. It does not use the product's version-vector classifier as its oracle. At heal,
  every acknowledgement is current, observed by a later acknowledged write, or in a hook
  record. **3,419 acknowledgements, 1,054 distinct logged losing versions**, no unexplained
  loss; per-node counters summed exactly to hook/log events. The original long-offline
  and full fault-mixture scenarios also passed for seeds 4701–4708.
- **Same versions / different bytes:** 1,500 decoded triples in all six orders, with
  repeated deliveries, matched an independently coded stamp/ID maximum. Network scenarios
  also compared canonical bytes after healing. Copy-isolation and round-trip checks held.
- **Wipe actor reuse:** 20 seeds 4701–4720 each did crash/restart, then isolated wipe/rejoin.
  Crash retained actor and acknowledged version; wipe changed actor and the ensuing contest
  was logged as concurrent. A separate scenario checked transient read errors and bit-rot.
- **Decoded merge laws / arrival order:** decoded triples, duplicates, all permutations,
  20,000 random/mutated input strings, plus the existing arbitrary-context merge tests.
  Successfully decoded strings must round-trip byte-for-byte and merge without failure.
- **ETag collisions:** 12,000 same-value writes over seven keys had distinct ETags and dots;
  5,000 mixed CAS/model steps also required every success to change the ETag. This is finite
  sampling, not a proof of SHA-256 collision resistance.
- **Skew, overflow, future-pinned key:** negative clocks, `sim.MaxSkew`, logical carry at
  `MaxUint32`, terminal `MaxInt64/MaxUint32` and a normal unrelated key all held. A correct
  `ErrClockExhausted` refusal preserving state is HELD. Another 10,000 HLC observations
  verified strict monotonicity and non-mutating `ErrClockAhead` rejection. The existing skew
  winner scenarios explicitly demonstrate that clock-ahead writes may win despite real-time
  ordering; that is the declared design.
- **Measured discards:** the 40-seed totals above count unique logged IDs separately from
  per-replica discard events. Ten isolated two-writer seeds accepted both writes and logged
  exactly one loser per seed. A 90-replacement check observed 90 hooks/logs, a 64-entry tail,
  no duplicate re-count and no causal-overwrite count. At counter saturation, a new discard
  still produced a hook and tail record while the counter stayed `MaxUint64`.
- **Quota overshoot:** four isolated writers each accepted ten 25-byte allocations under a
  250-byte local limit; further allocations were correctly refused. Aggregate usage was
  1,000 bytes: **750 bytes overshoot**, exactly `(4-1)*250`. Healing produced five discard
  events (replica events, not five distinct acknowledged losses). Recount corrected usage,
  converged, and subsequent unchanged recounts wrote nothing. This finite scenario does not
  establish a universal overshoot bound for arbitrary workloads.
- **Stale ETag acceptance:** the full absent/live/tombstone matrix for both put and delete,
  12,000 explicit stale refusals, the 5,000-step independent model, and two wire clients
  competing with one ETag all held. On one node exactly one wire request succeeded; across
  a partition both succeeded and their concurrent loss was logged, as specified.

## Evidence, bounds and limitations

- `evidence/run-all.txt` is the consolidated result. `core.txt`, `sim.txt`, and `builder.txt`
  are per-driver results; `builder.jsonl` preserves exact test and subtest outcomes and
  measurements. `*.stderr` captures Docker/setup diagnostics.
- All new property checks use deterministic seed 4701 unless a range is stated. Acceptance
  tolerance is **zero** mismatches. There are no confidence-interval or probabilistic-rate
  claims. Network probabilities are configured inputs, not measured accuracy claims.
- Existing properties run 200 cases with rapid seed 4701; existing DST runs use seeds
  4701–4708. Their original public test entry points are compiled in Docker, then run in
  a temporary directory here with copied fixtures, keeping any failure artifacts in scope.
- Large repeated-key inputs at 2, 8 and 16 MiB were rejected. Independent checks cannot
  inspect the private operation counters. Existing instrumented tests separately verified
  decode at most four counted operations per byte, merge at most one per combined input
  byte, and no more than 1.5x growth in counted work per byte from 2 to 16 MiB. These
  counters are not a proof of total wall-clock complexity or allocation complexity.
- The private canary switches were not reimplemented or toggled through unsafe access.
  Their existing tests passed, including deterministic and simulated detection scenarios.
  No product edits were made to inject additional canaries.
- No native Go fuzz campaign or race-detector campaign was run here; the new byte corpus,
  exported-API model checks, simulator sweep and existing regression run are the evidence.
  These are finite checks, not an exhaustive schedule/state-space proof. Disk failure hooks
  and original wire result/state decoding tests complement the independent API checks.
- The initial supplemental test launch lacked relative fixtures because its working
  directory was moved for write isolation. That was a harness setup error, corrected by
  copying fixtures into its temporary directory. It is not counted as a product failure;
  final regression groups all passed. The knowledge-store lookup also failed because the
  local index was absent; no environment repair was attempted.

The worktree root STATE.md was deliberately left unchanged because this brief forbids
writes outside this harness directory. `STATE.md` here records the local handoff.
