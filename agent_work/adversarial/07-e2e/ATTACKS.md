# Chunk 7 independent robustness checks

Status: first complete driver run is in progress. Three C7 oracle checks have confirmed
a shared error-validation gap; whole-system scenario results are still being collected. The initial 18 scenario checks
and all 78 migration matrix cells passed; all nine custom eight-cycle schedules passed.
All writes are confined to this directory; no product changes or commits.
Initial reviewed HEAD: `fd6dc9612520010a9610873410c58495b7d51637`.

## Claims and check map

The build notes present their scenario claims as nine subsections rather than a single
claims table. C1–C9 follow those subsections; C10–C13 cover the other notes and the
binding PLAN.md §4.2 Chunk 7 scenarios.

| Claim | Scope | Checks |
|---|---|---|
| C1 | Multi-region reads preserve bytes, sizes and ETags; regional spread ≤ m | 3 fresh simulated seeds with zero, one, prime, stripe boundary and multi-MB sizes; real-process size variant |
| C2 | Partitioned writes resolve, convergence within bounds, acknowledged writes accounted for | 3 fresh simulated seeds plus real processes, 4 objects of 65537 bytes, 3-second extra partition dwell, lineage and discard checks |
| C3 | Up to m damaged shards recover correct bytes and bounded repair volume | 3 fresh simulated seeds and real files, 20 objects, two damaged objects in each phase, 131379-byte bodies |
| C4 | Migration under traffic moves routing and all live shards | 3 fresh simulated seeds plus real processes, 5 objects, 65553-byte bodies, routing probes and live reads |
| C5 | Failure-point compensation and restart recovery preserve source and clean destination | All 78 matrix cells with independently assigned seeds 741001–741078, plus both real-process kill cases |
| C6 | Crash/restart/wipe preserves acknowledged data, raises incarnation, refills shards | 3 simulated seeds and real processes, 5 writes/wave, 65539-byte bodies, changed kill timing; 2 stale-node restarts after deletion/GC |
| C7 | Budgeted fault/heal cycles preserve safety, availability and convergence | 3 explicitly constructed schedules × 3 seeds × 8 cycles, with changed clock skew/drift; unexpected terminal-error controls |
| C8 | Disconnected conditional metadata edits converge and losing ETags fail | 3 fresh simulated seeds plus real processes, 4 objects, changed detection dwell and skew |
| C9 | Shutdown under load completes cleanly and restart preserves acknowledged writes | Real SIGTERM and SIGINT with a fresh workload seed |
| C10 | Intended real nodes start; cleanup on failure; default binary excludes blocklist functionality | A planted failed launch, failed-test subprocess cleanup, and default/tagged binary comparison |
| C11 | Invalid inputs reject without damaging later valid operations | Empty/oversized keys and reserved metadata tags on 2 simulated seeds; malformed quota JSON on real HTTP |
| C12 | Long soak checks memory, goroutines, tombstones and orphan shards | 30 fixed-key put/delete waves, ≥900 virtual seconds, samples at waves 5/15/30, all-node disk enumeration |
| C13 | Real disk exhaustion refuses writes and preserves acknowledged data after restart recovery | Fill container-private ≤128 MiB tmpfs until verified ENOSPC, release it, restart all nodes, check old data and new writes |

## Earlier chapter claims retested on the whole system

- Chapter 1, deterministic replay: every simulated scenario, matrix cell, malformed-input
  run and custom fault schedule executes twice and compares event and state digests.
- Chapter 2, membership/restart: C2 and C6 check membership convergence and a higher
  incarnation after restart, with all protocols active.
- Chapter 3, reconstruction from verifiable shards: C1 and C3 check bytes through the
  object API with boundary-size bodies and damaged stored shards.
- Chapter 4, metadata anti-entropy: C2/C8 compare metadata digests and Merkle roots
  after disconnected edits; C3 observes repair traffic with the complete node stack.
- Chapter 5, conditional writes and LWW: C8 checks stale losing ETags reject after heal,
  a single winner is shared, and losing edits appear in discard logs.
- Chapter 6, durable compensation: C5 repeats every matrix cell on fully composed nodes.
- Chapter 7, retained metadata and cleanup: C12 measures indefinite tombstone/system-key
  retention described in that chapter while requiring all obsolete shard files to disappear.

## Independence and reproducibility

`fixture/` copies `test/e2e/*.go` as reusable cluster adapters, scenario definitions,
and supporting oracles. `realcluster_test.go` changes only root discovery to find the
product's `cmd/objnode/main.go` above this nested module. `independent_test.go` is the
new check suite. Drivers select only `TestIndependent*`; inherited tests are not silently
counted as new evidence. Some checks deliberately reuse the builder's scenarios with
new seeds and sizes. These are parameter variation, not independent reimplementations
of their oracles. The fixed-key soak, malformed-input checks, ENOSPC check, and schedule
construction are new. The real process adapter builds `objnode -race -tags e2efault`.
New real-process checks limit concurrent PUTs to one per cluster, below the builder's
four-slot limit, to reduce unrelated one-second shard timeouts on this shared VM.
Each driver refreshes the copied support from current product test sources, so repairs to
the target suite take effect on rerun. It records source hashes in `evidence/*.sources.sha256`.
`fixture-sources.sha256` records the initial reviewed snapshot. Drivers are sequential;
do not run them concurrently while refreshing the same fixture directory.

From the worktree root:

```
bash agent_work/adversarial/07-e2e/run-all.sh
```

Every driver runs via `docker compose run --rm -T dev`; no host Go is used.
`evidence/*.json` holds full Go test events, `.stderr` holds Docker/build diagnostics,
and `evidence/run-all.txt` collects one result per leaf check. Test failure is exit 1;
missing tests, skipped checks, compilation errors and setup errors are exit 2.

## Tolerances

No statistical population claims are made: these are finite deterministic examples.
Byte equality, convergence, discard accounting and orphan absence have zero tolerance.
Bounds inherited from the scenario definitions: simulator convergence/repair 30 seconds,
migration 40 seconds, refill 60 seconds; custom fault cycles convergence and repair each
60 seconds; real convergence/repair 90 seconds, migration/refill 120 seconds, shutdown
20 seconds. Repair-volume bounds: rebuilt 0.9–2 times damage; simulated storage traffic
at most (k+m) times damage + 64 KiB, with bucket size checked against that bound.
The soak allows heap growth of 128 MiB between waves 5 and 30 and at most two additional
harness goroutines. Heap includes simulator event history, client ledger and test oracles;
it is not a measurement of product-only memory. Retained tombstones and system metadata
are counted, not assumed to be reclaimable: `internal/node/records.go` explicitly says
they persist indefinitely.

## Results and limitations

The focused driver `a09-oracle-contract.sh` produced 3 HELD and 3 BROKEN checks.
The three C7 BROKEN checks are one underlying test-oracle defect, not evidence that product
GETs actually returned corrupt data.

### C7: unexpected errors are silently accepted for GET, tag and delete

Source: `test/e2e/nemesis_test.go`, `(*nemesis).settle`. The build notes §3 E7 say
all pending calls must end with success or an allowed error. PUT validates its errors;
ordinary GET merely increments counters, delete processes success only, and tag has no
switch case. Therefore the stated terminal-error check is absent for these operations.

Minimal reproduction for each of GET, tag and delete (seed 801001): construct a fresh
simulated cluster and nemesis, set its pending list to one already completed `Call`
with the corresponding `Op`, `Bucket="w"`, `Key="k00"`, `Err=storage.ErrCorrupt`, and
`done=true`; call `settle()`. It returns nil for each. No network fault or actual product
corruption is necessary to expose this oracle omission. The identical PUT control is
correctly rejected. These are planted unexpected results for valid operations, not
correct input-validation errors misclassified as failures.

Exact command, from the worktree root:

```
bash agent_work/adversarial/07-e2e/a09-oracle-contract.sh
```

To run just one smallest reproduction:

```
docker compose run --rm -T dev bash -c 'cd /src/agent_work/adversarial/07-e2e && go test ./fixture -count=1 -run "^TestIndependentTerminalErrors$/^C7$/^unexpected-get-error$/^seed=801001$"'
```

Substitute `unexpected-tag-error` or `unexpected-delete-error` for the other two checks.
Recommended correction: validate non-nil terminal errors separately for every operation,
and keep planted-result self-checks so the claim is executable. Product code is untouched.

Both wire checks (seeds 801011 and 801013) held: nine malformed channel/node requests
through each of 21 nodes (189 requests per seed), followed by a valid bucket/write.
Every malformed request returned an error and the valid operation succeeded.

The simulator has static skew/drift, not dynamic clock steps. The soak is 15+ minutes of
virtual time, not an overnight real-process memory study. Historic fuzz execution totals
and historic seed-sweep wall times are not independently reproducible facts from this
short suite. Reused scenario oracles can share blind spots with the builder. Real-process
schedules depend on wall time and port allocation even with fixed workload seeds.

## Initial-run harness correction

The matrix's Go process completed all 78 cells successfully (376.659 seconds), but the
initial aggregate missed their lines because the driver file was edited while its shell
was waiting for Docker. The shell resumed at a stale file offset and reported an unmatched
quote. This is a harness-development setup error, not a product failure. `bash -n` passes
for the final driver; `evidence/matrix-first-results.txt` recovers the results from the
complete Go JSON log. The finalized suite will be rerun without editing active scripts.

### Initial real-process provisioning timeout

The first C4 real migration variant (seed 781004, five 65553-byte objects) stopped during
initial object loading: PUT `e4/m03` received 503 because only 2 of 3 shards were durable;
a shard RPC timed out at the runtime's default one second. Migration had not started.
This matches the load limit documented in build notes §6, not a counterexample to migration
correctness. The final independent real workloads use one concurrent PUT slot (rather
than four). The first-run failure is retained in evidence; the final run tests this setup
change. No timeout or product setting was changed, and failure responses are not silently
retried or counted as successful writes.

### C10: real cluster construction accepts a failed node launch

The initial C1 size run (seed 781001) hit a concrete port collision: node 8's HTTP
address 127.0.0.1:45293 duplicated node 7's transport address. Node 8 logged bind failure
and exited. `newRealCluster` nevertheless returned; its convergence predicate only checks
running processes. A later zero-byte GET then got a connection reset at that transport port.
This is a target harness defect, not corruption in the object store.

A focused behavioral reproduction uses three real node launch attempts. A small executable
wrapper exits 73 only for the r2 launch and execs the real race-built objnode for the other
two. The unmodified constructor returns a cluster with two live nodes instead of rejecting
startup. The wrapper is solely a planted launch failure for testing the harness, not a mock
of any library protocol. Seed 821001 reproduced this on the first focused run.

```
docker compose run --rm -T dev bash -c 'cd /src/agent_work/adversarial/07-e2e && go test ./fixture -count=1 -run "^TestIndependentStartup$"'
```

Recommended correction: require every intended process to be alive and ready during initial
construction, verify node identity on each HTTP endpoint, and allocate distinct ports (with
bounded retry on real bind failures). General post-fault convergence may still compare only
survivors. New workload checks explicitly reject an incomplete fixture as SETUP.

### Other harness corrections before the final run

- The first reserved-tag check created no object, so the API correctly returned not-found
  before validating the tag. The corrected check first creates the object; both simulated
  seeds now reject all four reserved tags as invalid and preserve readable object bytes.
- The first late-restart check tested survivor convergence before the simulator had run the
  restarted process's Start event. Its first GET therefore saw the old manifest during
  catch-up and returned unavailable. The corrected barrier requires all 21 nodes ready
  before comparing metadata. This is not evidence of resurrection or lost data.
