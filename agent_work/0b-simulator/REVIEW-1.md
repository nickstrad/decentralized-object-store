VERDICT: FAIL

The exported API of `internal/env`, `internal/sim`, and `internal/sim/simtest` is sound and ready to freeze without signature changes, determinism and rule compliance are clean, and the test suite is strong. However, three concrete fault-model and fault-budget bugs in `internal/sim` violate stated guarantees (stream FIFO under pause+clog, stripe-level fault budgets with non-shard blobs and read errors, and crash-after-unsynced-repair hiding silent bit-rot from `Budget`) and should be fixed before Wave B freezes the simulator.

## Findings

### 1. [MAJOR] Stream (TCP) connections reorder requests/responses under `Pause` + `Clog` (`Resume`/`Unclog` and `HealAll`)
- **Where:** `internal/sim/sim.go:578-584`, `internal/sim/process.go:98-109`
- **What is wrong:** PLAN 2.1 and the build brief require the request/response class to be "never duplicated or reordered within a connection". Two sequences that `Nemesis` can generate with default `ChaosKinds()` break this invariant:
  1. **Re-clogging an already-arrived event (`sim.go:578-584`):** When a `kindRequest`/`kindResponse`/`kindRefused` already crossed link `a->b` and was buffered in `b.held` while `b` was paused, and link `a->b` is then clogged with a newer stream event in `l.held`, resuming `b` releases the older event into `exec`, which re-checks `if l.clogged` (`sim.go:580`) and appends the *older* event to `l.held` *behind* the newer one. `Unclog` then delivers `second` before `first`.
  2. **`HealAll` release order (`process.go:98-109`):** `HealAll` calls `s.HealNet()` (`unclog`) before resuming paused nodes (`s.releaseNode(n)`). When `b.held` holds an older request `first` and `l.held` holds a newer request `second`, `unclog` schedules `second` with a lower `seq` than `first` at the same instant `s.now`, and `exec` does not hold link-released events behind `n.relHeld`, delivering `second` before `first`.
- **Reproduction:**
  ```go
  h := twoNodes(t, nil)
  mustNil(t, h.s.Pause("b"))
  h.rec("a").request("b", "first", time.Hour)
  h.run(2 * time.Millisecond) // "first" arrives past link a->b into b.held
  mustNil(t, h.s.Clog("a", "b"))
  h.rec("a").request("b", "second", time.Hour)
  h.run(2 * time.Millisecond) // "second" is held in links[a][b].held
  // Path A:
  mustNil(t, h.s.Resume("b"))
  h.run(2 * time.Millisecond) // "first" re-enters links[a][b].held behind "second"
  mustNil(t, h.s.Unclog("a", "b"))
  // (Or Path B: replace Resume+run+Unclog with h.s.HealAll())
  h.run(time.Second)
  // h.rec("b").requests is ["second", "first"] instead of ["first", "second"]!
  ```
- **Smallest fix:**
  1. Add `arrived bool` to `event` (`queue.go`); guard `sim.go:578` with `if !e.arrived && e.peer >= 0 && e.kind.network()`, and set `e.arrived = true` once past the `l.clogged` check so an event that already reached the node never re-enters `l.held`.
  2. In `process.go:98-109` (`HealAll`), resume paused nodes (calling `releaseNode`) *before* `s.HealNet()`, AND in `sim.go:558-571`, if an event with `e.released && e.relLink` pops while `n.relHeld > 0`, decrement `l.relHeld`, clear `e.released`/`e.relLink`, and requeue it behind `n.relHeld` (covers manual `Unclog`+`Resume` at the same instant too). Add regression subtests to `TestReleaseKeepsFIFOAtTheReleaseInstant`.

### 2. [MAJOR] `Budget` breaks "no more than `m` shards of one stripe" for ungrouped blobs (`GroupOf == ""`) and for `FaultReadError`
- **Where:** `internal/sim/nemesis.go:23-55, 83-94, 312-313`
- **What is wrong:** The brief requires `Budget` to express group limits such as "no more than `m` shards of one stripe" for Chunks 2, 6, and 7. Two bugs break that guarantee:
  1. **Ungrouped blobs collide in `groups[""]` (`nemesis.go:43-49`):** `Budget.damage` never skips `g == ""`. On an integrated node (Chunks 6–7) storing shard blobs alongside metadata, saga logs, and identity-epoch blobs on one `env.Disk`, a `GroupOf` function returning `""` for non-shard blobs causes every damaged non-shard blob to increment `groups[""]`, tripping `MaxDamagedPerGroup` (e.g. wiping one node with 3 non-shard blobs exceeds `m = 2`).
  2. **`FaultReadError` bypasses `MaxDamagedPerGroup` (`nemesis.go:27-49, 91-92`):** `Budget.Allows` sets only `extraNode = n.idx` (with `extra = nil`), and `Budget.damage` checks only `n.disk.Damaged()`, ignoring `failReads` and `failAny`. Because `Nemesis.draw()` (`nemesis.go:312-313`) always leaves `f.Blob == ""` on `FaultReadError` (failing reads on *any* blob on that node), the nemesis can rot `m` shards of stripe S and then inject `FaultReadError` on a node holding an `(m+1)`th shard of stripe S, making `m+1` shards fail simultaneously while both `Budget.Allows` and `Budget.Check` report within budget.
  (Also, if a direct caller passes `FaultBitRot{Node: id, Blob: ""}` to `Budget.Allows`, line 85 checks `extra = [{idx, ""}]` instead of the node's durable `rotTargets()`.)
- **Reproduction:**
  ```go
  // 1) GroupOf returning "" for non-shard blobs:
  // Node a has blobs "epoch", "meta/1", "saga/1" (all GroupOf == "").
  // b := sim.Budget{MaxDamagedDisks: 2, MaxDamagedPerGroup: 2, GroupOf: stripeOf}
  // b.Allows(s, sim.Fault{Kind: sim.FaultWipeDisk, Node: "a"}) -> false (groups[""] == 3 > 2)!

  // 2) FaultReadError exceeding MaxDamagedPerGroup = 1:
  b := sim.Budget{MaxDamagedDisks: 3, MaxDamagedPerGroup: 1, GroupOf: func(env.NodeID, string) string { return "stripe" }}
  mustNil(t, s.BitRot(ids[0], simtoy.Blob)) // stripe has 1 damaged shard
  // ids[1] also holds a shard of "stripe":
  b.Allows(s, sim.Fault{Kind: sim.FaultReadError, Node: ids[1], Count: 2}) // returns true!
  mustNil(t, s.FailReads(ids[1], "", 2))
  b.Check(s) // returns nil, yet both ids[0] and ids[1] fail/corrupt reads of "stripe"!
  ```
- **Smallest fix:** No API change required.
  1. In `Budget.damage` (`nemesis.go:44`), add `if g == "" { continue }`.
  2. In `Budget.damage`, include blobs affected by `n.disk.failAny > 0` (all existing blobs on `n.disk`) and `n.disk.failReads` (keys with count > 0) when tallying `groups[g]`, deduplicating per node; in `Budget.Allows`, for `FaultReadError` add `{n.idx, f.Blob}` (or all existing blobs on `n.disk` when `f.Blob == ""`), and for `FaultBitRot` with `f.Blob == ""` reject if any target in `n.disk.rotTargets()` would exceed `MaxDamagedPerGroup`. Add unit tests to `TestBudgetGroupLimit`.

### 3. [MAJOR] Unsynced `Write`/`Delete` clears `Disk.damaged` before `Sync`, so a crash hides surviving bit-rot/wipe damage from `Damaged()` and `Budget`
- **Where:** `internal/sim/disk.go:88, 106-125, 137, 159-174`
- **What is wrong:** `Write` (`disk.go:88`) and `Delete` (`disk.go:137`) immediately execute `delete(d.damaged, name)`. If a node repairs a bit-rotted shard by calling `Write(shard, clean)` and crashes *before* `Sync(shard)` (the exact repair-crash window tested in Chunks 2, 3, 6, and 7), `disk.crash` reverts `b.data` to the still-rotted `b.dur` without re-marking `d.damaged[name]`. The blob is still silently corrupted on disk, yet `Disk.Damaged()` is empty and `Budget` allows the nemesis to corrupt more shards of the same stripe beyond `MaxDamagedPerGroup`. The same happens for `WipeDisk` -> unsynced `Write` -> `Crash` (blob is still missing, yet unmarked) and `BitRot` -> unsynced `Delete` -> `Crash` (rotted blob resurrects unmarked). Conversely, if `BitRot` hits a dirty blob and `Sync` later commits a clean full overwrite, `Sync` never clears `d.damaged[name]`.
- **Reproduction:**
  ```go
  d := h.rec("a").e.Disk
  orig := bytes.Repeat([]byte{0x55}, 64)
  mustNil(t, d.Write("blob", orig))
  mustNil(t, d.Sync("blob"))
  mustNil(t, h.s.BitRot("a", "blob"))     // b.dur rotted; Damaged() == ["blob"]
  mustNil(t, d.Write("blob", orig))       // unsynced repair clears d.damaged["blob"]
  mustNil(t, h.s.Crash("a"))              // reverts b.data to rotted b.dur
  mustNil(t, h.s.Restart("a"))
  h.run(0)
  got, _ := h.rec("a").e.Disk.Read("blob") // got != orig (still rotted!)
  // h.s.Disk("a").Damaged() == [] (empty!)
  ```
- **Smallest fix:** Remove `delete(d.damaged, name)` from `Write` (`disk.go:88`) and `Delete` (`disk.go:137`), and clear `delete(d.damaged, name)` in `Sync` (`disk.go:115-124`) when `!b.exists` (durable delete) or `!b.appendOnly || !b.dExists` (durable full overwrite or recreation after wipe — not an `Append`-only sync that kept a rotted prefix). Update `TestDiskFaults` (`disk_test.go:108`) to call `d.Sync("blob")` before asserting `Damaged()` is cleared, and add a crash-before-sync test.

### 4. [NIT] `CrashTorn` cannot tear the first unsynced `Append` of a newly created blob
- **Where:** `internal/sim/disk.go:98-100, 163-167`
- **What is wrong:** `Append` sets `b.appendOnly = b.dExists`, so a brand-new log created via `Append` (without an earlier `Write`+`Sync`) has `appendOnly == false` and is always cleanly dropped on `CrashTorn` rather than sometimes leaving a torn first record. Chunk 5's Saga log creating a new saga file with `Append` would only see torn tails starting from the second record.
- **Reproduction:**
  ```go
  d := h.rec("a").e.Disk
  mustNil(t, d.Append("saga/1", []byte("FIRST_RECORD")))
  mustNil(t, h.s.CrashTorn("a")) // across all seeds, Peek("saga/1") is always (_, false)
  ```
- **Smallest fix:** In `Append` (`disk.go:98-100`), set `if !b.dirty { b.appendOnly = true }`; in `disk.crash` (`disk.go:163-167`), when `torn && b.appendOnly && b.exists && len(b.data) > len(b.dur)`, if `keep > len(b.dur)` (or `keep > 0` when `!b.dExists`), also set `b.dExists = true`.

### 5. [NIT] `PanicError.Stack` is captured but never printed on failure
- **Where:** `internal/sim/sim.go:104-106`, `internal/sim/simtest/simtest.go:241-242`
- **What is wrong:** `Sim.guard` records `debug.Stack()` in `PanicError.Stack`, but neither `PanicError.Error()` nor `simtest.Report` prints it. When a Wave B protocol panics inside `simtest.RequireDeterministic`, the test failure shows the panic value, node, and time, but not the source file/line of the panic.
- **Smallest fix:** In `simtest.Report` (`simtest.go:242`), if `var pe *sim.PanicError; errors.As(s.Err(), &pe) && len(pe.Stack) > 0`, append `pe.Stack` to the report output.

### 6. [NIT] Minor test-harness and test polish (`seeds.txt` tabs, `LogTail` header count, `panic` in `clock_test.go`)
- **Where:** `internal/sim/simtest/simtest.go:95, 243`, `internal/sim/clock_test.go:26, 29, 36, 44`
- **What is wrong:**
  1. `simtest.go:95` uses `strings.Cut(text, " ")` instead of `strings.Fields(text)`, so a tab between seed and note in `testdata/seeds.txt` fails parsing.
  2. `simtest.go:243` prints `min(TailLines, int(s.LogLen()))` rather than `len(tail)`, which disagrees with the printed lines if `LogCap < TailLines`.
  3. `clock_test.go:26, 29, 36, 44` calls `panic(...)` inside `TestTimers` instead of passing `t` and calling `t.Fatal(...)`; harmless today (test file), but unnecessary `panic(` under `internal/` if a gate scans `_test.go` files.
- **Smallest fix:** Use `strings.Fields(text)[0]` in `PinnedSeeds`; capture `tail := s.LogTail(TailLines)` and print `len(tail)` in `Report`; pass `t` into `then` in `TestTimers` and use `t.Fatal`.

### 7. [NOTE] Interface fitness across Chunks 1–6 and verdict on the builder's two open points
- **Where:** `internal/env/env.go`, `internal/env/errors.go`, `internal/sim/*.go`, `internal/sim/simtest/simtest.go`
- **Walkthrough (no API freeze changes needed):**
  - **Chunk 1 (SWIM gossip):** Periodic timers with cancel/reset (`Clock.AfterFunc` + `Timer.Stop`/`Reset`), size-limited datagrams (`Transport.Send`, `ErrTooLarge`), full-state sync (`Transport.Request`), `Rand`, asymmetric/region partitions, crash/wipe-rejoin all map directly.
  - **Chunk 2 (Reed-Solomon + chunk store):** `Disk` named blobs (`Read`/`Write`/`Append`/`Sync`/`Delete`/`List`), silent bit-rot, wipe, `Env.WithNet`/`WithDisk` wrappers, region labels (`Env.Region`, `Sim.Region`/`Regions`), and `Budget.GroupOf` (once Finding 2 & 3 are fixed) cover all requirements.
  - **Chunk 3 (Merkle anti-entropy):** Periodic timers, multi-round `Request`/`Reply`, byte-transfer measurement via `Env.WithNet`, bit-rot/partition/loss DST all fit cleanly.
  - **Chunk 4 (OCC metadata):** `NodeSpec.Skew`/`DriftPPM` + `Clock.Now()` cover HLC and skewed-clock LWW tests; `WipeRejoin` keeps the node's `Rand` stream advancing across incarnations so a wiped node drawing a fresh durable identity epoch never collides with its prior incarnation.
  - **Chunk 5 (Sagas):** Append-only log (`Disk.Append`+`Sync`), `CrashTorn`, synchronous crash-before-sync injection via `Env.WithDisk` wrapper calling `s.CrashTorn` inside `Sync` (after which `h.closed()` cleanly returns `ErrClosed`), clock-driven retry backoff, and idempotency testing under lost responses (`PartitionOneWay` + retry) all fit cleanly.
  - **Chunk 6 (Integration + real runtime):**
    - Multiplexing 5 protocols on one `env.Process` and one `env.Transport` is directly supported via `Env.WithNet` / `Env.WithDisk` / `Env.WithClock` and a 1-byte protocol prefix demuxer; `env.NewCodeTable` lets Chunk 6 register all app sentinels (`>= CodeAppMin`) once.
    - **Real-runtime implementability:** Every `env` interface is cleanly implementable with goroutines behind a single node-loop goroutine without races: `Clock.AfterFunc`/`Stop`/`Reset` via `time.AfterFunc` posting `(timer, gen)` tokens to the node loop so cancelled timers never fire late; `Transport.Request` via non-blocking dispatch to worker goroutines posting callback events back to the node loop; `Reply` via a 1-buffered channel so server-side async replies never block the node loop even if the peer disconnected; `Rand` owned exclusively by the node loop.
    - **Two subtle Chunk 6 reminders (not bugs here):** (1) On real OS files, `Disk.Delete` followed by `Disk.Sync(name)` must fsync the parent directory rather than returning `ErrNotFound` for a just-deleted file; (2) `sim.New` only fills default latencies when `cfg.Net == (NetConfig{})`, so callers customizing one field should start from `DefaultNetConfig()`.
- **Builder's two open points:**
  1. **Request re-delivery fault:** Do **not** add before the freeze. The brief's ruling ("never duplicated") is right for the TCP delivery class; real duplicate execution comes from a lost response followed by an application retry, which `PartitionOneWay`/`SetLinkLoss` + retry already models (and tests can also wrap `Env.WithNet` if they ever want forced replay).
  2. **Mid-run clock jump:** Do **not** add before the freeze. Per-node `Skew` and `DriftPPM` satisfy PLAN 2.1, 4.2, and 7.5, and Chunk 4 can inject mid-run backward/forward NTP steps via `Env.WithClock` without changing the frozen API.

### 8. [NOTE] Event log hashes `e.payload` after handler execution
- **Where:** `internal/sim/sim.go:470-471`, `internal/sim/log.go:42-43`
- **Detail:** `s.record(e, disp)` runs after `s.exec(e)`. Because `OnDatagram`, `OnRequest`, and `Request` callbacks receive `e.payload` directly, a handler that mutates its input slice in place causes `log.go:43` to hash the post-handler bytes rather than the wire bytes. This does not break determinism (a deterministic handler mutates deterministically), only payload-hash inspection if a handler mutates its input buffer.

### 9. [NOTE] Kernel separability reading check (Question 7)
- **(i) PRNG stream derivation (`internal/sim/rand.go`):** `NewStream` and `fnv64a` are domain-free, but the file defines domain constants `StreamNet`, `StreamDisk`, `StreamNemesis`, `StreamWorkload` (`rand.go:10-15`) and imports `internal/env` for `NodeStream(id env.NodeID)` (`rand.go:7, 17`).
- **(ii) Virtual clock and timers (`internal/sim/handle.go`):** Lives in the same file and on the same `handle` struct as network (`Send`, `Request`, `serve`, `refuse`, `complete`) and disk (`disk.go` methods on `*handle`). `node.local`/`toGlobal` (`handle.go:19-41`) read `node.skew`/`drift`; `handle.closed` (`handle.go:17`) checks `node.inc`/`up`; `timer.arm` (`handle.go:61`) hard-codes `kindTimer` and `h.n.idx`/`h.inc`; `Sim.cancel` (`handle.go:199-212`) reaches directly into `s.links[e.peer][e.node].relHeld` and `s.nodes[e.node].relHeld`.
- **(iii) Event queue and loop (`internal/sim/queue.go`, `internal/sim/sim.go`):**
  - `queue.go`: `eventQueue` heap operations (`less`, `swap`, `up`, `down`, `push`, `pop`, `remove`, `peek`) only inspect `at`, `seq`, `hi`, but the file also defines domain `evKind` (`queue.go:7-29`), domain `disposition` (`queue.go:31-52`), and embeds network/process/nemesis/timer fields directly in `event` (`queue.go:54-76`: `inc`, `peerInc`, `payload`, `call`, `code`, `msg`, `fault *Fault`, `nemesis *Nemesis`, `timer *timer`, `released`, `relLink`).
  - `sim.go`: `Sim` (`sim.go:136-158`) and `Config` (`sim.go:38-44`) bundle loop state with `NetConfig`, `*env.CodeTable`, `nodes`, `byID`, `links`, all four named PRNGs, `nextCall`, and inline event-log fields (`logHash`, `logBuf`, `logStart`, `logTotal`).
  - **Opaque integers?** No: `Sim.exec` (`sim.go:543-614`) switches directly on every `evKind`, calls `e.kind.network()`/`e.kind.stream()` (`sim.go:616-622`), indexes `s.nodes[e.node]` and `s.links[e.peer][e.node]`, manages link clogs and node pause queues, and invokes `e.nemesis.fire`, `s.Apply`, `s.refuse`, `s.serve`, `s.complete`, `s.start`.
  - **Log switchable/replaceable?** No: `Sim.New` unconditionally initializes `logHash: sha256.New()` (`sim.go:184`) and forces `LogCap <= 0` to `DefaultLogCap` (`sim.go:173-175`), and `Sim.Step` calls `s.record(e, disp)` unconditionally (`sim.go:471`) with no logger interface or off switch.
- **(iv) Event log and digest (`internal/sim/log.go`):** `Sim.record` (`log.go:39-63`) interprets `evKind`, calls `e.fault.Encode()`, and reads `e.payload`, `e.code`, `e.call`, `e.inc`, `e.label`. `Sim.nodeName` (`log.go:65-70`) reads `s.nodes[i].id`. `Sim.Digests` (`log.go:123-154`) directly encodes node/region/flags/incarnation/`proc.EncodeState()` and calls `n.disk.encode(buf)` (and reuses `appendBytes` from `fault.go:74`).
- **(v) Seed plumbing in `simtest` (`internal/sim/simtest/simtest.go`):** Not separated by file. A single file `simtest.go` holds seed parsing/sweep (`seedValue`, `seedFlag`, `init`, `Seed`, `envInt`, `PinnedSeeds`, `Seeds`, `Sweep`, lines 27–141), `Sim` determinism/reporting helpers (`CheckDeterministic`, `RequireDeterministic`, `Report`, `writeArtifact`, lines 143–255), and the cluster builder (`Cluster`, lines 257–264, referencing `env.NodeID`, `sim.NodeIDs`, `sim.Factory`, `s.AddCluster`).

## Checklist summary (Questions 2, 4, 5, 6)
- **Determinism (Q2):** Clean. No non-sorted map iteration reaches outputs or draws (all map iterations in product code go through `sortedKeys`), every draw uses its designated PCG stream matching `NewPCG(uint64(seed), fnv64a(label))`, messages and disk reads/writes are copied across boundaries, clock arithmetic is pure integer (including 128-bit `bits.Mul64`/`Div64` in `toGlobal`), `dropInvariants` exists only in `export_test.go`, and all four replay meta-tests plus all three canaries (`TestCanaryUnseededDraw`, `TestCanaryMapOrder`, `TestCanaryDroppedInvariant`) are non-vacuous and verify the exact expected failure sentinel.
- **Loop and stall semantics (Q4):** Clean. `(time, seq)` min-heap gives total FIFO order on ties; timers are scheduled/cancelled/reset as events; `Step`, `RunUntil`, `RunFor`, `RunUntilQuiet`, `RunUntilConverged`, and `MaxSteps` report stalls (`ErrQueueEmpty`, `ErrBudgetExhausted`, `ErrDeadline`) as sticky `*StallError`s without hanging; panics in nodes, factories, actions, invariants, `EncodeState`, and convergence predicates surface as sticky `*PanicError`s with seed and node.
- **Tests (Q5):** Every named test in the brief exists and asserts what its name claims; every `env`, `sim`, and `simtest` sentinel is produced and checked with `errors.Is`; `CodeTable` round-trips every row including an app entry; `simtest.Report` prints seed, digests, log tail, artifact path, and a working `REPLAY:` command (verified by execution).
- **Rules (Q6):** Clean. Zero comments in product/test `.go` files (only `//go:embed help.txt` in `cmd/objsim/main.go`); stdlib-only in product packages (`pgregory.net/rapid` imported only by `internal/sim/prop_test.go`); zero goroutines, channels, `select`, mutexes, atomics, wall clock, global rand, floats, `%p`, or `maphash` in `internal/env`, `internal/sim`, and `internal/sim/simtoy`; both library panics (`internal/env/errors.go` `mustCodeTable`, `internal/sim/handle.go` `serve`) are justified and listed in `agent_work/gates/panic-allowlist.txt`; all product `_ =` ignores are documented in `build-notes.md`.

## Verification: run vs. read
- **Verified by running (inside Docker `dev` container):**
  - `gofmt -l .` (empty) and `go vet ./...` (clean).
  - `go test -race -count=1 ./...` (all packages passed: `cmd/objsim`, `internal/env`, `internal/sim`, `internal/sim/simtest`).
  - `DST_SEEDS=50 go test -count=1 ./internal/sim/...` (passed in 10.7 s).
  - `go test -cover ./...` (confirmed coverages: `cmd/objsim` 97.1%, `internal/env` 97.9%, `internal/sim` 94.9%, `internal/sim/simtest` 94.3%).
  - `go run ./cmd/objsim -scenario=toy-chaos-100 -seed=3` (matched `internal/sim/testdata/golden.txt` row for `100 3`).
  - Executed a `REPLAY:` command verbatim: `go test ./internal/sim -run '^TestReplaySameProcess$/^nodes=20$/^seed=2$' -seed=2 -count=1 -v` (passed, ran only that subtest).
  - `go doc -all` across `./internal/env`, `./internal/sim`, `./internal/sim/simtest` diffed against `agent_work/0b-simulator/frozen-api.txt` (empty diff).
  - `go list` import and dependency inspection (confirmed stdlib-only product code).
- **Verified by reading (and static grep):**
  - Read all product and test Go files across `internal/env`, `internal/sim`, `internal/sim/simtest`, `internal/sim/simtoy`, and `cmd/objsim`, plus `PLAN.md`, the builder brief, builder rules, `build-notes.md`, `frozen-api.txt`, `panic-allowlist.txt`, `go.mod`, `testdata/golden.txt`, and `testdata/seeds.txt`.
  - Findings 1–9 (including the three MAJOR reproductions) were derived and traced line-by-line from the source (not executed as modified tests, since this review is strictly read-only).
  - Did not re-run `FuzzSimSchedule` for 30 s, `-rapid.checks=2000`, or `DST_SEEDS=500` (relied on `build-notes.md` records plus our 50-seed sweep and `-race` run).
