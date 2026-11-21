VERDICT: NITS

All REVIEW-1 findings (1–6 and 8) and the gate-hygiene allowlist fix are FIXED with non-vacuous regression tests, no new BLOCKER or MAJOR was introduced, `agent_work/0b-simulator/frozen-api.txt` matches `git show fc37722` apart from `simtest.TailLines` becoming its own `const` declaration, `internal/sim/testdata/golden.txt` is unchanged, `PanicError.Stack` cannot affect replay, the kernel-prep file moves are mechanically verified pure moves, and the exported API of `internal/env`, `internal/sim`, and `internal/sim/simtest` is ready to freeze without signature changes. One non-blocking NIT remains around whole-node `FaultReadError` interacting with dynamically created grouped blobs when `MaxDamagedDisks > MaxDamagedPerGroup`.

## REVIEW-1 findings

| Finding | Status | Evidence |
|---|---|---|
| 1 (MAJOR) Stream FIFO under `Pause` + `Clog` (`Resume`/`Unclog`, `HealAll`) | FIXED | `internal/sim/queue.go:74` (`arrived`), `internal/sim/sim.go:563-566,586-593`; proved by 5 subtests in `TestReleaseKeepsFIFOAtTheReleaseInstant` (`process_test.go:235-306`, 4 fail on `fc37722` where arrived events re-entered `l.held` and `HealAll`/`Unclog`+`Resume` delivered `second` before `first`) and `TestPropStreamOrderPerConnection` (`prop_test.go:654-750`). |
| 2 (MAJOR) `Budget`: ungrouped blobs (`GroupOf == ""`), `FaultReadError`, unnamed `FaultBitRot` | FIXED | `internal/sim/disk.go:182-190,240-256` (`stored`, `affected`), `internal/sim/nemesis.go:27-50,91-108`; proved by 4 new subtests in `TestBudgetGroupLimit` (`nemesis_test.go:197-261`, all fail on `fc37722`) and `TestPropNemesisRespectsBudget` (`prop_test.go:255-366` with independent `budgetHolds`). |
| 3 (MAJOR) Unsynced `Write`/`Delete` clearing `Disk.damaged` before `Sync` | FIXED | `internal/sim/disk.go:18,88,115-128,140,174,192-196,229` (`blob.rot` tracks written-layer rot, `Sync` clears `damaged` only on durable delete or clean durable write, `crash` reloads `rot` from `damaged`); proved by 7 subtests in `TestDiskFaults` (`disk_test.go:108-200`, fail on `fc37722`) and `TestPropDiskModel` (`prop_test.go:372-590`). |
| 4 (NIT) `CrashTorn` cannot tear first unsynced `Append` of a new blob | FIXED | `internal/sim/disk.go:98-100,166-172` (`appendOnly = true` on clean blob; `keep > 0` sets `dExists = true`); proved by `TestTornCrashCanTearTheFirstAppendOfANewBlob` (`disk_test.go:354-378`, `partial == 0` on `fc37722`) and `TestPropDiskModel` (`prop_test.go:476,537-544`). |
| 5 (NIT) `PanicError.Stack` captured but never printed | FIXED | `internal/sim/simtest/determinism.go:124-127` prints `pe.Stack` before `REPLAY:`; proved by `TestReportShowsThePanicStack` (`simtest_test.go:285-308`, fails on `fc37722`). |
| 6 (NIT) `seeds.txt` tabs, `LogTail` header count, `panic` in `clock_test.go` | FIXED | `internal/sim/simtest/seeds.go:88` (`strings.Fields`), `determinism.go:119-120` (`len(tail)`), `clock_test.go:16-58` (`t.Fatal`); proved by `TestPinnedSeeds` (`simtest_test.go:100-104`), `TestReportTailHeaderCountsThePrintedLines` (`simtest_test.go:310-323`), and `TestTimers` (`clock_test.go:12-71`). |
| 8 (NOTE) Event log hashes `e.payload` after handler execution | FIXED | `internal/sim/sim.go:470-475` (`wire := fnv64a(e.payload)` before `exec`), `internal/sim/log.go:39-44`; proved by `TestEventLogHashesThePayloadAsItCrossedTheWire` (`net_test.go:437-473`, fails on `fc37722` where zeroed buffers were hashed). |
| Gate hygiene: panic allowlist method name | FIXED | `agent_work/gates/panic-allowlist.txt:3` renamed `serve` to `Sim.serve`, matching `internal/sim/handle.go:79-85`. |

## New findings

### 1. [NIT] Whole-node `FaultReadError` (`Blob == ""`) injected before new grouped blobs are written can later trip `Budget.Check` when `MaxDamagedDisks > MaxDamagedPerGroup`
- **Where:** `internal/sim/nemesis.go:103-108,327-328`, `internal/sim/disk.go:250-254`
- **What is wrong:** `Nemesis.draw()` (`nemesis.go:327-328`) always leaves `f.Blob == ""` on `FaultReadError`, setting `n.disk.failAny += count`, which applies prospectively to *any* blob later written to `n` until `count` reads consume it or `HealAll` clears it. `Budget.Allows` (`nemesis.go:107`) checks only `n.disk.stored()` at injection time, whereas `Budget.Check` (`disk.go:250-254`) checks `n.disk.stored()` at every subsequent step. On a cluster where `GroupOf != nil` and `MaxDamagedDisks > MaxDamagedPerGroup` (the normal multi-stripe erasure-coding setup in Chunks 2 and 6), if the nemesis injects `FaultReadError` on a node that does not yet hold a shard of stripe `S` and a workload `PUT` or repair subsequently writes a shard of `S` onto that node, `Budget.Check` fails with `ErrBudget` (and `S` genuinely has more than `MaxDamagedPerGroup` unreadable/rotted shards) even though every nemesis action passed `Budget.Allows`. (`TestPropNemesisRespectsBudget` does not hit this because `simtoy` writes its single grouped blob `simtoy.Blob` on every node in `Start()` before chaos starts and never introduces a new group name.)
- **Reproduction:**
  ```go
  h := newHarness(t, sim.Config{Seed: 1}, spec("a", "r"), spec("b", "r"), spec("c", "r"))
  b := sim.Budget{MaxDamagedDisks: 2, MaxDamagedPerGroup: 1, GroupOf: stripeOf}
  // Neither a nor b holds a shard of "stripe" yet (e.g., before workload PUT):
  fA := sim.Fault{Kind: sim.FaultReadError, Node: "a", Count: 2}
  fB := sim.Fault{Kind: sim.FaultReadError, Node: "b", Count: 2}
  // Both pass Allows and Apply:
  assert(b.Allows(h.s, fA))
  mustNil(t, h.s.Apply(fA))
  assert(b.Allows(h.s, fB))
  mustNil(t, h.s.Apply(fB))
  // Now workload PUT writes "shard/a" on a and "shard/b" on b:
  mustNil(t, h.rec("a").e.Disk.Write("shard/a", []byte("s0")))
  mustNil(t, h.rec("b").e.Disk.Write("shard/b", []byte("s1")))
  // b.Check(h.s) now returns ErrBudget: group "stripe" has 2 damaged blobs, limit 1!
  ```
- **Smallest fix:** No API change required. In `Nemesis.draw()` (`nemesis.go:327-328`), when `n.cfg.Budget.GroupOf != nil`, target a specific existing blob from `s.nodes[i].disk.stored()` (mirroring how `FaultBitRot` picks from `rotTargets()`, returning `f, false` if empty), so a nemesis read error never ambushes future writes of new stripes; keeping `Blob == ""` when `GroupOf == nil` also leaves `testdata/golden.txt` untouched.

### 2. [NOTE] Regression-test quality, panic-stack isolation, invariants, and freeze check
- **Where:** `internal/sim/prop_test.go:605-750`, `internal/sim/sim.go:9,104-106,433-446`, `internal/sim/simtest/determinism.go:124-127`, `agent_work/0b-simulator/frozen-api.txt`, `internal/sim/testdata/golden.txt`
- **Detail:**
  - **`TestPropStreamOrderPerConnection`:** Non-vacuous and able to fail on `fc37722` (the repair git history also captured a real rapid failure before the fix). It drives random 2–4 node clusters with `request`, `pause`, `resume`, `clog`, `unclog`, `healAll`, `healNet`, and `run` (0–6 ms, covering both in-flight arrivals and same-instant 0 ms steps). After every `run` and after a final `HealAll` + drain, `checkOrder` asserts strictly increasing per-`(from, to)` sequence numbers for both inbound requests and inbound responses (catching both reordering and duplicates), zero reply errors, and exact delivery/reply counts for every ordered pair.
  - **`PanicError.Stack` replay isolation:** `runtime/debug` is imported only by `internal/sim/sim.go:9` and called only in `Sim.guard` (`sim.go:436`) to populate `PanicError.Stack`. `PanicError.Error()` (`sim.go:104-106`), `Sim.record` (`log.go:39-63`), `Sim.Digests` (`log.go:123-154`), and every loop/network/disk/nemesis decision never read `PanicError.Stack`. The only non-test reader is `simtest.Report` (`determinism.go:124-127`) during failure reporting. It cannot affect replay.
  - **API, golden digests, and determinism rules:** `go doc -all` across `./internal/env`, `./internal/sim`, and `./internal/sim/simtest` matches `agent_work/0b-simulator/frozen-api.txt` byte-for-byte; its only diff against `fc37722` is `TailLines = 20` becoming `const TailLines = 20`. `internal/sim/testdata/golden.txt` has zero diff against `fc37722`. All product imports are within the allowed stdlib set (with `runtime/debug` confined to `sim.go`), all new map ranges go through `sortedKeys`, zero comments or floats exist in product packages, and `panic(` under `internal/` exists only in the two allowlisted product functions plus intentional test panics in `loop_test.go` and `simtest_test.go`.
  - **Freeze check:** No interface changes are needed before freezing the API for Wave B.
- **Reproduction:** N/A (verification note).
- **Smallest fix:** None.

### 3. [NOTE] Pure file moves and kernel-separability reading check
- **Where:** `internal/sim/stream.go`, `internal/sim/rand.go`, `internal/sim/clock.go`, `internal/sim/handle.go`, `internal/sim/queue.go`, `internal/sim/sim.go`, `internal/sim/log.go`, `internal/sim/simtest/{seeds,determinism,cluster}.go`
- **Detail:**
  - **Pure file moves (`570c9c7`):** Mechanically verified by comparing sorted non-package, non-import lines of `handle.go`+`rand.go` against `handle.go`+`clock.go`+`rand.go`+`stream.go` (exact match) and `simtest.go` against `seeds.go`+`determinism.go`+`cluster.go` (exact match apart from `TailLines` becoming `const TailLines = 20`). Zero behaviour or exported-signature changes.
  - **Kernel-separability check:**
    - **(i) Stream derivation (`stream.go`, `rand.go`):** `stream.go` imports only `hash/fnv` and `math/rand/v2` and has zero domain references. `rand.go` holds domain stream names (`StreamNet/Disk/Nemesis/Workload`) and `NodeStream(env.NodeID)`.
    - **(ii) Virtual clock and timers (`clock.go`):** Still refers to domain `node` (`local`/`toGlobal` read `n.skew`/`n.drift`, `arm` reads `h.n.idx`), domain `handle` (shared with `Transport`/`Disk`, `handle.closed()` checks `node.inc`/`up`), `event`/`kindTimer`, `env.Time`/`env.Timer`, and `Sim.cancel` in `handle.go` (which decrements `s.links[..][..].relHeld` and `s.nodes[..].relHeld`).
    - **(iii) Event queue and loop (`queue.go`, `sim.go`):** `eventQueue` heap operations only touch `at`/`seq`/`hi`, but `queue.go` defines domain `evKind`, `disposition`, and domain fields on `event` (`inc`, `peerInc`, `payload`, `call`, `code`, `msg`, `fault *Fault`, `nemesis *Nemesis`, `timer *timer`, `released`, `relLink`, `arrived`, `dead`, `until`). `sim.go` bundles loop state with `NetConfig`, `*env.CodeTable`, `nodes`, `byID`, `links`, four named PRNGs, `nextCall`, and inline event-log state. **Opaque integers?** No: `Step` calls `e.kind.network()`, and `exec` switches directly on every `evKind`, indexes `s.nodes`/`s.links`, enforces link-clog and node-pause/unstarted hold/release (`relHeld`, `arrived`), and dispatches directly to `Nemesis`, `Apply`, `serve`, `refuse`, `complete`, `start`, and `timer`. **Event log off or replaced?** No: `New` hardcodes `logHash: sha256.New()` and clamps `LogCap <= 0` to `DefaultLogCap` (`sim.go:173-175,184`), and `Step` unconditionally pre-hashes network payloads and calls `s.record` (`sim.go:470-475`) with no interface or disable switch.
    - **(iv) Event log and digest (`log.go`):** `Sim.record` switches on domain `evKind` and calls `e.fault.Encode()`; `nodeName` reads `s.nodes`; `Sim.Digests` combines kernel `Events` digest with domain `State` digest (`n.id`, `n.region`, node flags, `n.inc`, `n.proc.EncodeState()`, `n.disk.encode`).
    - **(v) Seed plumbing in `simtest` (`seeds.go`, `determinism.go`, `cluster.go`):** Seed handling (`seeds.go`) is completely apart from the cluster builder (`cluster.go`) and imports only stdlib — zero `sim` or `env` references. `determinism.go` is also apart from `cluster.go` and `internal/env`, depending only on `*sim.Sim` and `*sim.PanicError`.
  - **Completeness of builder's "Extraction notes" (`build-notes.md:363-396`):** Strong and accurate on the main tangles; additions it missed:
    1. `clock` / `sim.go` / `handle.go`: `MaxDriftPPM` (`sim.go:19`), `NodeSpec.Skew`/`DriftPPM` validation (`sim.go:238-240`), and `Sim.LocalTime` (`sim.go:342-348`) remain in `sim.go`; `handle.Request` (`handle.go:66`) calls `from.toGlobal(timeout)` directly; `timer.arm` relies on sentinel `peer: -1` and `inc: h.inc`.
    2. `sim.go`: `Config` and `New` (`sim.go:38-44,160-193`) couple loop construction to `NetConfig` defaulting/validation, `env.DefaultCodes()`, and unconditional `LogCap <= 0 -> DefaultLogCap` clamping; `PanicError.Node` is typed as domain `env.NodeID` (`sim.go:97`); domain node/region/budget sentinels and cluster APIs (`NodeSpec`, `Factory`, `NodeStatus`, `NodeIDs`, `AddCluster`, `Regions`, `Region`, `Process`, `Status`, `Disk`, `LiveCount`) live in `sim.go`; `evKind.network()`/`stream()` sit in `sim.go:625-631` away from `evKind` in `queue.go`; `Sim.Workload` (`sim.go:364-382`) directly uses domain-named `s.workRand`.
    3. `log.go`: `Digests` reuses `appendBytes` from `fault.go:74` (`log.go:130,131,148`) and calls `s.guard` (`log.go:146`, mutating `s.err` on `EncodeState` panic); `record.bytes()` packs `node`/`peer` into `uint16` (`log.go:32-33`, limiting a generic kernel to `< 65535` actors with `-1` as `0xffff`); `LogTail`/`LogLen`/`LogTruncated`/`WriteLog` are methods on `*Sim` rather than on a standalone log type.
    4. Cross-file utility coupling: `fault.go` owns `appendUvarint`/`appendBytes` (used by `disk.go` and `log.go`), and `disk.go` owns `sortedKeys` (used by `nemesis.go` and `export_test.go`).
    5. `simtest`: `determinism.go` is already free of `internal/env` and cluster builders and needs only a small interface over the 9 `*sim.Sim` methods (plus a stack-extractor interface) to become kernel-generic; `determinism.go:133` references `SeedsFile` from `seeds.go`.
- **Reproduction:** N/A (reading check).
- **Smallest fix:** None now (notes only for future kernel extraction).

## Verification: run vs. read
- **Verified by running (inside Docker `dev` container only, via `docker compose run --rm -T dev ...`):**
  - `gofmt -l .` (empty) and `go vet ./...` (clean).
  - `go test -count=1 ./...` (all packages passed: `cmd/objsim`, `internal/env`, `internal/sim`, `internal/sim/simtest`).
  - `go test -race -count=1 ./...` (all packages passed, `internal/sim` 23.1 s).
  - `DST_SEEDS=50 go test -count=1 ./internal/sim/...` (passed, `internal/sim` 14.9 s, `simtest` 0.04 s).
  - `go test -count=1 ./internal/sim -rapid.checks=2000` (passed in 18.5 s, covering all rapid properties including `TestPropStreamOrderPerConnection`, `TestPropNemesisRespectsBudget`, and `TestPropDiskModel`).
  - `go test -count=1 -cover ./...` (`cmd/objsim` 97.1%, `internal/env` 97.9%, `internal/sim` 95.2%, `internal/sim/simtest` 95.1%).
  - `go test -v -count=1` on all new/updated regression tests, `TestReplayAcrossProcesses`, and `TestGoldenDigests` (all subtests executed and passed).
  - `go doc -all` across `./internal/env`, `./internal/sim`, `./internal/sim/simtest` diffed against `agent_work/0b-simulator/frozen-api.txt` (exact match).
  - `go list -f '{{.ImportPath}}: {{.Imports}}'` across `./internal/env`, `./internal/sim`, `./internal/sim/simtest`, `./internal/sim/simtoy` (confirmed allowed stdlib imports and `runtime/debug` confined to `internal/sim`).
  - Read-only `git diff fc37722..HEAD` on `frozen-api.txt`, `panic-allowlist.txt`, `testdata/golden.txt`, and all modified files, plus a read-only Python comparison of sorted non-import lines before/after commit `570c9c7` confirming pure file moves.
- **Verified by reading (and static grep):**
  - Read `REVIEW-1.md`, `objstore-p2-0b-repair.md`, `objstore-p2-0b-simulator-build.md`, `build-notes.md`, `frozen-api.txt`, `git log --stat fc37722..HEAD`, all diffs from `fc37722..HEAD`, and full current files `internal/sim/{sim,queue,disk,nemesis,process,log,handle,clock,net,rand,stream}.go` and `internal/sim/simtest/{seeds,determinism,cluster}.go`.
  - Reasoned through old `fc37722` behaviour from the diffs to verify non-vacuity of every regression test (read-only review; did not check out `fc37722` or modify files).
  - Static grep for `panic(`, `runtime/debug`/`debug.Stack`/`.Stack`, `for ... range`, comments (`//`, `/*`), and `float32`/`float64` across `internal/`.
  - Did not run `FuzzSimSchedule` for 30 s or `DST_SEEDS=500` (relied on `DST_SEEDS=50`, `-rapid.checks=2000`, `-race`, and the builder's recorded 500-seed and fuzz runs).
