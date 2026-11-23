VERDICT: FAIL

## Findings

### 1. [MAJOR] `FuzzClusterNemesis` runs 9 nodes (3 per region) with `k=4, m=2`, so the migrating bucket `m` can never hold an object
- **File and line:** `test/e2e/e7_test.go:73-78` (`fuzzOptions`); `test/e2e/nemesis_test.go:138-141, 379-388, 459-461`.
- **What is wrong:** `fuzzOptions()` starts from `defaultSimOptions()` (`storage.Layout{K: 4, M: 2}`, 6 shards per stripe) and overrides only `o.nodes = 9` across `simRegions = 3` regions — leaving 3 nodes in each region. `nemesis.run()` creates `bucketM` (`"m"`) routed to `r0`, and `nemesis.traffic()` writes objects to `bucketM` (`nemesis_test.go:140`). Because a routed bucket places shards only inside its own region, every PUT to `bucketM` fails in `internal/storage/placement.go:143` with `storage: placement: 3 members for 6 shards: unavailable`, which `nemesis.settle()` (`nemesis_test.go:177`) silently accepts. As a result, across all 234 executions of the 15-minute `FuzzClusterNemesis` hardening run, `bucketM` held zero objects and zero shards, and every `opMigrate` migrated an empty bucket (never exercising shard copy, Merkle verify over real shards, or source purge under fuzzing). The builder documented this exact 3-node/4+2 incompatibility for Tier R (`build-notes.md` section 6) and switched Tier R E4/E5 to `2+1`, but missed `fuzzOptions()`.
- **Reproduction:** In `e7_test.go:73-78`, `o.nodes = 9` gives 3 nodes in `r0` while `o.cfg.Layout` stays `K: 4, M: 2` (`3 < K+M = 6`). Inspecting `bucketM` puts during `FuzzClusterNemesis/seed#17` (which invokes `opMigrate`) shows every `Put` to `"m"` returns `placement: 3 members for 6 shards: unavailable` and `shardsOutside` / migration steps see 0 objects in `"m"`.
- **Smallest fix:** Set `o.cfg.Layout = storage.Layout{K: 2, M: 1, StripeSize: simStripe}` in `fuzzOptions()` (matching Tier R's `routedRealOptions()`, or use `o.nodes = 12` with `2+1` so a single node fault inside the routed region still leaves `k+m=3` nodes). Re-run `FuzzClusterNemesis` after the fix so the hardening budget exercises non-empty migrations.

### 2. [MAJOR] `nemesis.finishMigration` ignores `env.ErrTimeout` / `env.ErrUnreachable` ambiguity on `StartMigration`
- **File and line:** `test/e2e/nemesis_test.go:420-436` (`finishMigration`), `test/e2e/nemesis_test.go:314, 380-388`.
- **What is wrong:** When `opMigrate` calls `c.StartMigration(coord, bucketM, to)` during chaos and `finishMigration()` runs after `FaultHealAll` + `Converged`, lines 430-435 treat `env.ErrTimeout` and `env.ErrUnreachable` as "no migration started" and return `nil` after clearing `n.mig = nil`:
  ```go
  if start.Err != nil {
      if errors.Is(start.Err, storage.ErrBusy) || errors.Is(start.Err, storage.ErrUnavailable) || errors.Is(start.Err, env.ErrTimeout) || errors.Is(start.Err, env.ErrUnreachable) {
          return nil
      }
      return failf("start migration via %s: %v", start.At.ID, start.Err)
  }
  ```
  This violates the simulator contract (`ErrTimeout` means outcome unknown: the request may have been applied and only the reply dropped or delayed by `opCutRegion`, `opIsolateNode`, `opLossyLink`, `opClogLink`, or a pause/crash of `coord` right after `StartMigration` synced its saga log). Dropping tracking on `ErrTimeout`/`ErrUnreachable` has three concrete consequences:
  1. If the saga is still running after `FaultHealAll`, `finishMigration()` skips awaiting terminal state and `shardsOutside`, and with `n.mig == nil` the next cycle's `opWipeNode` is no longer blocked from wiping the live coordinator (`nemesis_test.go:314`).
  2. If the saga completed and moved `bucketM` to `to`, `n.region` stays stuck on the old region; the next `opMigrate` can choose the bucket's current region and fail line 434 with `node.ErrNoMigration`, or a later `shardsOutside(c, bucketM, n.region)` fails against the stale region.
  3. Any migration started by a timed-out request escapes post-heal verification.
- **Reproduction:** Schedule `opMigrate` at `t` followed immediately by a network cut or `opClogLink`/`opIsolateNode` on `coord` that drops the reply after `coord.StartMigration` commits its log entry. `start.Err` is `env.ErrTimeout`; `finishMigration()` returns `nil` with `n.region` still `"r0"` while `bucketM` migrates to `"r1"`. In the next cycle, `opMigrate` picking `"r1"` fails with `ErrNoMigration` at line 434.
- **Smallest fix:** On `env.ErrTimeout` or `env.ErrUnreachable` in `finishMigration()`, inspect `coord`'s engine (or any live `~reg/` entry in converged metadata) after `FaultHealAll` + `Converged` to locate any saga started on `bucketM`, await its completion and verify `shardsOutside`, and always refresh `n.region` from `rd.Route(bucketM)` after convergence.

### 3. [MAJOR] `scenarioE2` never asserts that object reads or writes allowed by surviving shards actually succeed during the partition
- **File and line:** `test/e2e/scenario_e2_test.go:274-293` (`scenarioE2`).
- **What is wrong:** PLAN 2.7 E2 requires: *"during a region partition both sides keep accepting metadata writes and any object write/read the surviving shards allow; anything else returns `ErrUnavailable`, never wrong bytes"*. After `PartitionRegion("r0")`, `sideB` (`r1+r2`) holds at least `k = 4` shards of every pre-cut object (since spread guarantees at most `m = 2` shards in `r0`) and has ≥ `k+m = 6` nodes after `splitViews` on both tiers (14 on S, 6 on R); on Tier S, `sideA` (`r0`, 7 nodes) also has ≥ 6 nodes after `splitViews`. However, lines 274-293 only check that every PUT returns `nil` or `ErrUnavailable` and every GET returns `nil`, `ErrUnavailable`, or `ErrNotFound`; they never assert `res.putsAcked > 0`, `res.getsOK > 0`, or that surviving-side operations succeed. Because tags and ACLs still succeed on both sides, `res.contested.discarded == 0` (line 319) passes even if `putsAcked == 0` and `getsOK == 0`.
- **Reproduction:** Make `PutObject` and `GetObject` unconditionally return `storage.ErrUnavailable` whenever `len(gossip.Members()) < 21` (or `< 9` on R). All tag and ACL writes succeed, `putsAcked = 0`, `getsOK = 0`, `res.contested.discarded = 10` (tags + ACL), and both `TestE2PartitionAndHealS` and `TestE2PartitionAndHealR` pass.
- **Smallest fix:** In `scenarioE2`, assert that (a) every `sideB` GET of a pre-cut key succeeds (the `p.objects/2` early `sideB` GETs before any overwrite, plus post-split `sideB` GETs), and (b) every post-split PUT issued to a side with `len(side) >= c.Layout().K + c.Layout().M` succeeds (requiring `res.putsAcked > 0` and `res.getsOK > 0`).

### 4. [NIT] `TestCanaryE4AMigrationThatSkipsDeletingTheSourceFails` passes an empty error-match string `""`
- **File and line:** `test/e2e/canary_test.go:196`.
- **What is wrong:** `expectCanaryFailure(t, s, "")` accepts any failure of `scenarioE4`, contradicting `build-notes.md` section 4 (*"passes only when the scenario fails for that reason (the error text is matched)"*).
- **Reproduction:** Running `TestCanaryE4AMigrationThatSkipsDeletingTheSourceFails` logs `caught: no shard of the bucket left in r0: not within 40s: e2e: scenario check failed`, yet `want == ""` would also pass if boot or migration failed for an unrelated reason.
- **Smallest fix:** Pass `"no shard of the bucket left in r0"` as the third argument to `expectCanaryFailure`.

### 5. [NIT] `scenarioE2`/`scenarioE8` never assert `Health.Discards > 0`, and `scenarioE9` never asserts non-zero traffic or in-flight load at SIGTERM
- **File and line:** `test/e2e/scenario_e2_test.go:308-322, 447-450`; `test/e2e/tierr_test.go:364-384`.
- **What is wrong:**
  1. PLAN 2.7 E2 and E8 require that every discarded write is *"counted and logged"*. `checkContests` verifies `RecentDiscards` (logged), and `discardLog` reads `h.Discards` only to check for tail overflow (`> 64`), so `res.discards == 0` would pass if a bug logged discards without incrementing `Health.Discards`.
  2. `scenarioE9` tracks `res.ok`, `res.inflightOK`, and `res.inflightFailed`, and reads back `c.ledger.Acked()` after restart, but never asserts `res.ok > 0` or `res.inflightOK+res.inflightFailed > 0`. If all requests returned 503 before SIGTERM, or none were in flight at SIGTERM, the post-restart read loop would run 0 iterations and E9 would pass vacuously.
- **Smallest fix:** Assert `res.discards > 0` in `scenarioE2` and `scenarioE8`, and assert `res.ok > 0 && res.inflightOK+res.inflightFailed > 0` in `scenarioE9` when `sig == syscall.SIGTERM`.

### 6. [NIT] `FuzzClusterNemesis` checks `s.Err()` instead of `s.Verify()`, and `nemesisStats.declared` is never populated
- **File and line:** `test/e2e/e7_test.go:106`; `test/e2e/nemesis_test.go:75, 513-515`.
- **What is wrong:** `FuzzClusterNemesis` checks `s.Err()` rather than `s.Verify()`, so it would not catch `sim.ErrInvariantNotRun`. Separately, `nemesisStats.declared` is reported in `MEASURE e7/declared_lost` (`e7_test.go:61`) but never set from `len(c.o.declared)` (harmless on passing runs because line 513 fails if `len(c.o.declared) > 0`).
- **Smallest fix:** Use `s.Verify()` in `FuzzClusterNemesis` and assign `n.st.declared = len(c.o.declared)` before line 513 (or remove the unused field).

### 7. [NIT] `internal/noderuntime/e2efault.go` caches reloads by `(ModTime, Size)`, which can miss equal-size updates within one mtime tick
- **File and line:** `internal/noderuntime/e2efault.go:57-59`.
- **What is wrong:** `reload` skips reading the blocklist file when `fi.ModTime().Equal(b.mod) && fi.Size() == b.size`. On Linux, coarse filesystem mtime ticks (~4 ms) can assign identical mtimes to two rapid rewrites of equal-length peer lists, leaving a stale blocklist active. Since `reload` is already throttled to once per 50 ms on a tiny file, reading directly avoids the hazard.
- **Smallest fix:** Replace the `os.Stat` + `(ModTime, Size)` short-circuit with a direct `os.ReadFile(path)` (treating `fs.ErrNotExist` as an empty peer set).

### 8. [NOTE] Assessment of narrowings, `e2efault`, and `CommitSink` coverage
- **E7 fault budget narrowing (`nemesis.metadataElsewhere`, `build-notes.md` section 6 & request 7):**
  - *Already stated by an earlier chunk?* **Yes.** `docs/knowledge/chunk-04-occ.md:27-28` already stated this exact limit: *"The one unrecorded loss: a version whose every holder, its writer included, lost its disk before passing it on. No node ever saw it lose"*, and Chunk 4's multi-region sweep tallied disk-lost writes separately. Because `internal/node` commits manifests and tags to local OCC with a single local fsync (so disconnected nodes stay writable), wiping the sole holder before anti-entropy copies the write necessarily loses it without a discard record.
  - *Stated exactly?* Yes; seed `166963932986536` is pinned in `test/e2e/testdata/seeds.txt`, and `TestCanaryE7WipingTheOnlyCopyOfAnAcknowledgedWriteIsCaught` proves the lineage oracle catches it when `n.loneWipe = true`. Note for the orchestrator / prose seat: cite Chunk 4's prior statement of this limit alongside Chunk 6's manifest persistence.
  - *Hides a defect?* No. `metadataElsewhere` is slightly conservative (requiring another up node to hold the exact version ID), and broken replication is still caught by `Converged` after every heal.
- **Per-scenario seed shares (`seedsFor` in `harness_test.go:66-89`, `build-notes.md` section 5 & request 1):**
  - *Already stated by an earlier chunk?* Only for E5's 78-cell failure matrix (`docs/knowledge/chunk-06-integration.md:251`: seeds spread across cells, not `78 × 500`). Not stated earlier for E1–E4, E6–E8.
  - *Stated exactly?* Yes — fractions, counts at `DST_SEEDS=500` and in the `332`-seed sweep, `-race` reduction to 1 seed + every 8th E5 cell (justified because Tier S has no goroutines), and `E2E_FULL_SEEDS=1` are stated plainly and escalated in Request 1.
  - *Caveats:* Even with shares, `test/e2e` at `DST_SEEDS=500` took 1494 s (~25 min vs the builder rules' ~10 min target), while shrinking E4 to 25 seeds at G5 and 16 seeds in the hardening sweep (and E7 to 199 seeds in the hardening sweep — fewer than G5's 300). The build brief explicitly asked to *"cut object sizes and cycle length before seeds or checks"*; `TestE4MigrationUnderTrafficS` still writes 8 × 128 KiB objects plus ~100 × 64 KiB traffic writes per seed (~5.9 s/seed). Reducing Tier S E4 object sizes (E1 already tests multi-MB and multi-stripe) would buy much higher E4 seed coverage without increasing wall time. Also, at default `go test ./test/e2e` (`DefaultSeeds = 3`), `seedsFor` cuts every scenario to 1 unpinned seed (+ 1 pinned E7 seed).
- **`e2efault` build tag:** Verified that `outboundFilter` in `internal/noderuntime/runtime.go:530` is unexported and never assigned in a default build (always `nil`, so `Send` and `Request` behaviour is unchanged), and `TestDefaultBuildCarriesNoFaultInjection` passes, confirming none of `.e2e-blocklist`, `noderuntime.blockedPeer`, or `noderuntime.BlocklistPath` appear in the default `objnode` binary.
- **`CommitSink` on ACL/quota (`internal/node/objects.go:541-567, 606-623`):** Chunk 6's `PutACL` and `SetQuota` do not invoke `n.committed`, which is why `e2/oracle_discarded` is 12 vs `e2/contested_discarded` 13 (and `e8/oracle_discarded` 7 vs `e8/contested_discarded` 8). `checkContests` verifies ACL discards independently through `Health.RecentDiscards`, and E8 checks quota convergence across all up nodes; no defect is hidden.
- **Rules check:** Standard library only (`go.mod`/`go.sum` untouched); zero comments added in the Go diff; zero panics added (`panic-allowlist.txt` untouched); frozen simulator packages (`internal/env`, `internal/sim`, `internal/sim/simtest`) completely untouched; no forbidden determinism constructs in `internal/node` or `internal/nodetest`.

## Verified by running vs. by reading

- **Verified by running (inside `docker compose run --rm -T dev`, while the orchestrator's G5 run was active on the host at load ~9.8):**
  - `go test -v -count=1 ./test/e2e -run 'TestCanaryE[1-8]|TestLedger|TestLossOracle|TestDeletionOracle|TestOrphanCheck|TestLiveReadable|TestSpreadOracle|TestAckOracle|TestHealthReports'` — all 9 tier S canaries and all 8 oracle/health self-tests passed (25.4 s).
  - `E2E_TIER_R=1 go test -v -count=1 ./test/e2e -run '^TestDefaultBuildCarriesNoFaultInjection$'` — passed (4.1 s).
  - `go test -v -count=1 -tags e2efault ./internal/noderuntime -run Blocklist` — both blocklist tests passed (0.7 s).
  - `go test -v -count=1 ./cmd/objnode` — all 6 tests including `TestLeakReportCountsAGoroutineUntilItEnds` passed (2.1 s).
  - `go test -v -count=1 ./test/e2e -run '^FuzzClusterNemesis$'` — all 18 seed corpus entries passed (6.3 s).
  - `go test -v -count=1 ./test/e2e -run '^TestE7NemesisHealingCyclesS$' -seed=1` — passed (9.4 s).
  - `go test -v -count=1 ./test/e2e -run '^TestE[123468]' -seed=1` — all tier S scenarios E1, E1mb, E2, E3, E4, E6, E8 passed on seed 1 (29.7 s).
  - `go test -v -count=1 ./internal/node -run '^TestAckOracleKeepsIdenticalPayloadsOnTwoKeysApart$'` — passed (0.01 s).
  - `git diff main..HEAD` checks for comments, panics, frozen packages, `go.mod`, and determinism constructs in `internal/node`/`internal/nodetest` — all clean.
- **Verified by reading only (not re-run due to active G5 load on the shared 4-core machine):**
  - Full Tier R suite (`E2E_TIER_R=1 go test ./test/e2e -run 'R$|TierR|DefaultBuild|CanaryE9'`, timing-bound) — read `realcluster_test.go`, `tierr_test.go`, and `hardening/tier-r.txt`.
  - Full `TestE5MigrationEveryFailurePointS` 78-cell matrix, full 500-seed sweep, `-race -count=3`, and 5/15-minute hardening fuzz runs — read `e5sim_test.go`, `hardening/gate-equivalent/e2e-500-seeds.txt`, `hardening/fuzz/summary.tsv`, `hardening/sweep/summary.tsv`, and the orchestrator's `gates/G0-*.log` through `gates/G4-race.log` (`gates/result.json` was not yet written because `G5-dst.log` was still in progress at review time).
