# Chunk 0b build notes — the deterministic simulator

Builder: Opus 5.5 (fresh seat), branch `chunk/0b-simulator`, 2026-11-21.
Frozen API listing (what Wave B is briefed from): `agent_work/0b-simulator/frozen-api.txt`
(`go doc -all` of `internal/env`, `internal/sim`, `internal/sim/simtest`).

Size: about 3,300 lines of product code, about 3,100 lines of tests.

## What was built

| Package | Files | Contents |
|---|---|---|
| `internal/env` | `env.go`, `errors.go` | `NodeID`, `Region`, `Time` (virtual ns, `Add`/`Sub`); interfaces `Clock`, `Timer`, `Transport`, `Disk`, `Process`; `Reply`; the `Env` bundle with `WithNet`/`WithDisk`/`WithClock`; sentinels `ErrTimeout`, `ErrUnreachable`, `ErrClosed`, `ErrTooLarge`, `ErrRemote`, `ErrNotFound`, `ErrIO`, `ErrInvalidArgument`; wire status codes (`Code`, `CodeTable`, `RemoteError`) |
| `internal/sim` | `sim.go` (loop, nodes, invariants, errors), `queue.go` (indexed heap), `handle.go` (per-incarnation Transport, request serving, `cancel`), `clock.go` (virtual clock and timers), `net.go`, `disk.go`, `process.go`, `fault.go`, `nemesis.go`, `log.go`, `stream.go` (`NewStream`, `fnv64a`), `rand.go` (stream labels, `NodeStream`) | the simulator |
| `internal/sim/simtest` | `seeds.go`, `determinism.go`, `cluster.go` | `-seed` flag, `DST_*` variables, `Seeds`, `Sweep`, `CheckDeterministic`, `RequireDeterministic`, `Report`, `ReplayCommand`, `RunPattern`, `PinnedSeeds`, `Cluster` |
| `internal/sim/simtoy` | `simtoy.go` | the max-register flood toy, its invariants, `Converged`, the chaos scenario (`ChaosRun`, `Scenarios`) |
| `cmd/objsim` | `main.go`, `scenarios.go`, `help.txt` | runs a named scenario from a seed, prints both digests; `-list` |

`internal/buildcheck` (placeholder) is deleted. `internal/prop` is not built (rapid is used).

## Semantics Wave B builders must know (the contract)

Everything here is tested; the test that proves each point is in the claims table below.

**Process model.** A node is built by `sim.Factory = func(env.Env) env.Process`. The `Start`
of a node is itself an event, so a factory or `Start` panic is caught like any other.
`Process` has `Start`, `OnDatagram(from, msg)`, `OnRequest(from, req, reply)` and
`EncodeState() []byte` (canonical bytes for the state digest; must be deterministic). Each
incarnation gets a fresh `env.Env`; after a crash the old incarnation's Clock/Transport/Disk
are closed (`Send` and every disk call return `ErrClosed`; `Request` on a closed env never
calls back; `AfterFunc` returns an inert timer). The node's `Rand` stream (`node/<id>`)
continues across incarnations, so a restarted node does not replay its previous draws.
Messages addressed to a node while it is starting (restart scheduled, `Start` not yet run)
are held and delivered after `Start`.

**Callbacks never run re-entrantly.** Every callback (`AfterFunc` fn, `Request` cb, timeouts,
local errors such as `ErrTooLarge` on `Request`) runs later as its own event, never inside the
call that scheduled it. `Reply` may be called synchronously inside `OnRequest` or later (async
reply after a fan-out is tested). Calling a reply twice panics (a protocol bug; reported as a
`PanicError`).

**Clock.** `Now()` is `global + skew + global*drift/1e6` (integers only). `AfterFunc(d)` waits
`d` of the node's local time: global delay `ceil(d*1e6/(1e6+drift))`. `|DriftPPM| <=
MaxDriftPPM` (100,000). `d <= 0` fires at the current instant after already-queued events.
`Timer.Stop` returns true iff it prevented a pending fire; `Reset` re-arms (also after firing)
and returns whether the timer was pending.

**Datagrams.** `Send` errors synchronously only for `ErrClosed`, `ErrTooLarge`
(`len > MaxDatagram`, default 65,507; nothing is sent) and `ErrUnreachable` (unknown node).
Everything else is silent: partition/one-way block/link loss/`DropPPM` drop, `DuplicatePPM`
delivers a second copy with its own latency, latency is drawn per copy (so reorder is
natural). Partitions and loss are decided at send time; clog is decided at arrival time.

**Request/response.** `Request(to, req, timeout, cb)`: `cb` is called exactly once (unless
the caller's incarnation dies). Outcomes: reply; `ErrTimeout` (lost request, lost response,
partition, paused server, a server that never replies — indistinguishable); `ErrUnreachable`
(server crashed: refused, takes one return trip; or unknown node, immediately);
`ErrTooLarge` (request over `MaxMessage`, default 16 MiB, or a reply over it);
`ErrInvalidArgument` (timeout <= 0). Requests and responses on one directed link are FIFO
(delivery time is at least the link's last stream delivery, ties by seq) and never
duplicated. Per-link loss and partitions apply to both classes; `DropPPM`/`DuplicatePPM`
apply to datagrams only. A reply error crosses as `(Code, message)` through the sim's
`CodeTable` (`Config.Codes`, default `env.DefaultCodes()`); the caller receives an
`*env.RemoteError` that `errors.Is` both the mapped sentinel and `ErrRemote`. Later chunks
register their own sentinels with `env.NewCodeTable(env.CodeEntry{Code: >= CodeAppMin, Err})`
and pass the table in `sim.Config.Codes`.

**Disk.** Named blobs, synchronous calls, two layers. `Write` replaces, `Append` extends,
`Sync(name)` makes that blob's written state (including a delete) durable, `Delete` removes
from the written layer, `List(prefix)` returns sorted names. Reads see the written layer.
Errors: `ErrNotFound`, `ErrIO` (injected), `ErrInvalidArgument` (empty name), `ErrClosed`.
Crash: every unsynced change is lost. `CrashTorn`: additionally, a blob whose only unsynced
changes are appends onto its durable content (or onto nothing: the first `Append` of a new
blob) keeps the durable bytes plus a random prefix (possibly empty, possibly all) of the
appended bytes, drawn from the `disk` stream; a new blob with a non-empty kept prefix becomes
durable, with an empty one it stays absent.
Bit-rot flips one bit of the durable bytes (and of the written bytes if the blob is clean);
the next read returns the bad bytes with no error. The damage mark (`Disk.Damaged()`) tracks
the DURABLE copy: it is set by bit-rot and by wipe (for every blob that existed in either
layer), survives unsynced `Write`/`Delete`, and is cleared only by a `Sync` that removes the
blob or makes bytes durable that do not carry the rot (a full rewrite, or appends onto a blob
that was dirty when it rotted). A crash before the repair is synced leaves the blob rotted and
marked. Wipe removes both layers. Read errors: `FailReads(node, name, n)`
fails the next `n` reads of that blob (`name == ""`: of any blob) with `ErrIO`.

**Process faults.** `Crash` (volatile state, timers, pending calls and held events gone;
durable disk kept), `CrashTorn`, `Restart` (new incarnation, `Start` as an event), `Pause`
(events for the node are held while time passes, then delivered in order on `Resume`),
`WipeRejoin` (crash if up, wipe, restart). `HealAll` = `HealNet` + restart every crashed node
+ resume every paused node + clear read-error injections (bit-rot and wipes stay: that
damage is the protocol's job to repair).

**Topology faults.** `Partition(side, other)` (both directions; `other == nil` means everyone
else), `PartitionOneWay(from, to)`, `Isolate(id)`, `PartitionRegion(r)` (that region against
the rest), `SetLinkLoss(from, to, ppm)`, `Clog(from, to)`/`Unclog` (held traffic is released
in order at the release instant), `HealNet` (clears everything, releases clogs).

**FIFO at a release instant.** A message that arrives at exactly the virtual instant its link
is unclogged or its node resumes is requeued behind the released ones, so release never
reorders a connection (`TestReleaseKeepsFIFOAtTheReleaseInstant`; a mutation that removes the
requeue fails it). A message that already passed its link (held by a paused or starting node)
never re-enters a clog, and a link-released message waits behind the node's released ones, so
`Resume`/`Unclog` in either order and `HealAll` keep each connection in send order
(`TestPropStreamOrderPerConnection`).

**Faults as data.** `sim.Fault{Kind, Node, Peer, Side, Other, Region, Blob, PPM, Count}` with
`Apply`, `InjectAt(t, f)` (logged as a fault event; an invalid fault, e.g. restarting an up
node, is a logged no-op, never an error), `InjectSchedule`, `Encode`, `String`.
`ScheduleFromBytes(data, start, step)` turns fuzz bytes into a schedule (4 bytes per fault;
used by `FuzzSimSchedule`; Chunk 7's `FuzzClusterNemesis` can reuse it). `BitRot` with
`Blob == ""` picks a durable blob from the `disk` stream.

**Nemesis and budget.** `StartNemesis(NemesisConfig{Start, Phases, Chaos, Heal, MaxGap,
Kinds, Budget})`: phase `i` is chaos on `[Start+i*(Chaos+Heal), +Chaos)` with actions spaced
`1..MaxGap` apart, then `HealAll` at the end of the chaos window. `QuietAt()` is the final
heal; liveness bounds are stated from it. Every draw comes from the `nemesis` stream, only
from nemesis-owned state (live/paused/clogged sets, disk blob names for bit-rot targets).
`History()` lists every step with `OutcomeApplied/Rejected/Skipped`. `Budget{MinLive,
MaxDamagedDisks, MaxDamagedPerGroup, GroupOf, Lifted}`: `MinLive` counts up and unpaused
nodes; a disk is "damaged" while it has a damage mark or pending read errors;
`GroupOf(node, blob) string` + `MaxDamagedPerGroup` expresses "no more than m shards of one
stripe" (Chunk 2 maps a shard blob name to its stripe id and returns `""` for every blob that
belongs to no group; those never count). Per node, the blobs counted against groups are the
damage-marked ones, those with pending read errors, and, for a whole-node read error
(`FailReads(node, "", n)`, which is what the nemesis draws), every blob stored on the node.
`Allows` judges a named bit-rot or read error on that blob, a wipe on every stored blob, and an
unnamed bit-rot on each blob it could hit. Zero `MaxDamagedDisks` means no
disk faults at all; nil `GroupOf` means no group limit; `Lifted` disables every check.
`Budget.Allows(s, f)` is the pre-check; `Budget.Check(s)` verifies the current state and is
meant to be registered as an invariant. Manual `Apply`/`InjectAt` ignore the budget (the
"lifted on purpose" path).

**Loop.** `Step`, `RunUntil(t)`, `RunFor(d)`, `RunUntilQuiet`, `RunUntilConverged(deadline,
pred)`. Step budget: `Config.MaxSteps` (default 10,000,000) over the sim's life. Stalls are
`*StallError` (`errors.Is(err, ErrStall)` plus one of `ErrQueueEmpty`, `ErrBudgetExhausted`,
`ErrDeadline`). Every failure is sticky in `s.Err()` and stops the loop: stalls, invariant
violations (`*InvariantError`, `ErrInvariant`), panics anywhere in node code, actions,
invariants, factories or convergence predicates (`*PanicError` with node, seed and stack),
and `s.Fail(err)`. `s.Verify()` returns the sticky error, or `ErrInvariantNotRun` if any
invariant ran fewer times than `steps/every` since it was added.

**Workload.** `s.At(t, name, fn)` schedules a test action; `s.Workload(start, count, maxGap,
fn(i))` schedules `count` actions with gaps drawn from the `workload` stream
(`s.WorkloadRand()` for the action's own draws). `s.Process(id)` returns the live process
(nil when down or not yet started; a paused node's process is still returned, so check
`Status(id).Paused` before driving it).

**Event log and digests.** One 38-byte big-endian record per executed event: time, seq, kind,
disposition (ran, stale, down, held, clogged, requeued, rejected, skipped, failed), node
index, peer index, FNV-64a of the payload as it crossed the wire, taken before the handler
runs (xor the status code; fault encoding for faults;
label for actions), aux (call id / incarnation). `Digests.Events` = SHA-256 of all records;
`Digests.State` = SHA-256 of every node's id, region, up/paused/started flags, incarnation,
`EncodeState()` and both disk layers in sorted name order. The last `Config.LogCap`
(default 2^20) records are kept for `LogTail` and `WriteLog`; the digest always covers all.

**Streams.** `NewStream(seed, label) = rand.New(rand.NewPCG(uint64(seed), fnv64a(label)))`,
labels `net`, `disk`, `nemesis`, `workload`, `node/<id>` (`NodeStream(id)`).

**simtest.** `Sweep(t, fn(t, seed))` runs one subtest `seed=<n>` per seed from `Seeds(t)`:
the `-seed` flag, else `DST_SEED`, else pinned `testdata/seeds.txt` seeds first then
`DST_SEED_BASE` (default 1) + `DST_SEEDS` (default 3). `RequireDeterministic(t, seed,
run)` runs `run(seed)` twice, compares both digests first (`ErrNondeterministic`), then
`Verify`; on any later failure of `t` it prints seed, error, digests, the last 20 log lines,
the artifact path (with `DST_ARTIFACTS`), a `PIN:` line and, last, `REPLAY: go test <pkg>
-run '^Test$/^sub$/^seed=n$' -seed=<n> -count=1`. `run` must build its own `sim.New` from
the seed it is given and return the finished sim (errors live in `s.Err()`). For a node panic
the report also prints the panic stack (after the log tail; REPLAY stays last).
`testdata/seeds.txt` lines are `<seed><space or tab><note>`.

## Claims and the tests that prove them

| Claim | Test(s) |
|---|---|
| Same seed, same process: identical event and state digests at 20, 50, 100 nodes under full chaos | `TestReplaySameProcess` (sweep; 500 seeds × 3 sizes pass) |
| Same seed across processes, with `GOMAXPROCS=1 GOGC=1` and `GOMAXPROCS=4 GOGC=off` | `TestReplayAcrossProcesses` (re-execs the test binary) |
| Pinned digests for seeds 1–3 at each size | `TestGoldenDigests` (`testdata/golden.txt`, regenerate with `-update-golden`); `objsim -scenario=toy-chaos-100 -seed=3` printed the same digests as the golden row |
| The seed drives the run | `TestDifferentSeedsDiffer` |
| Harness catches an unseeded draw / map-order dependency / invariants not being run | `TestCanaryUnseededDraw`, `TestCanaryMapOrder`, `TestCanaryDroppedInvariant` |
| Toy converges after the final heal within 30 s virtual | `TestReplaySameProcess` asserts `Converged`; `RunUntilConverged` stall otherwise |
| Event order is (time, seq), FIFO on ties; past events run now | `TestEventOrdering`, `TestEventScheduledInThePastRunsNow` |
| Timers set/stop/reset/rearm | `TestTimers`, `TestTimerResetAfterFiringRearms` |
| Skew and drift (incl. rounding up) | `TestClockSkewAndDrift`; local time monotonic under random skew/drift/latency: `TestPropTimeMonotonic` |
| Each datagram fault alone | `TestDatagramFaults` (latency, inter-region, drop, duplicate, reorder, one-way both directions, symmetric, region, isolate, link loss 100% and ~50% of 2000, heal), `TestClogHoldsTrafficUntilRelease`, `TestHealNetReleasesClogs`, `TestMessagesAreCopied`, `TestSizeLimits` |
| Each request/response outcome | `TestRequestResponse` (reply, lost request, lost response — server ran, same error — refused, never-replies timeout, error code crossing, late reply discarded, invalid timeout), `TestRequestToUnknownNodeIsUnreachable`, `TestRequestsOnOneConnectionStayInOrder`, `TestUnknownServerErrorArrivesAsErrRemote` |
| Async reply after the server's own fan-out | `TestAsyncReplyAfterFanOut` |
| Release keeps FIFO | `TestReleaseKeepsFIFOAtTheReleaseInstant` |
| Each disk fault alone | `TestDiskFaults` (silent single-bit rot, rot preconditions, wipe, crash drops unsynced write/append/delete, read errors per blob and any blob, closed disk), `TestTornTailKeepsAPrefixOfTheUnsyncedAppend`, `TestTornCrashOnlyTearsAppendOnlyBlobs` |
| Disk matches a reference model under write/append/sync/delete/crash/torn crash/bit-rot/read errors | `TestPropDiskModel` (rapid state machine) |
| Crash/restart/pause/resume/wipe-and-rejoin/HealAll | `TestProcessFaults` |
| Stall detection: queue empty, budget exhausted, deadline | `TestStallDetection` |
| Panics fail the run with seed and node | `TestNodePanicFailsTheRunWithTheSeed`, `TestPanicInActionAndInvariantIsReported`, `TestReplyTwiceIsAProtocolBug` |
| A healed network delivers every message sent after the heal, both classes | `TestPropHealedNetworkDelivers` |
| Streams are independent and exactly as in PLAN 2.1 | `TestPropStreamsIndependent`, `TestStreamDerivationMatchesThePlan`; at sim level `TestPropNetworkDrawsDoNotShiftTheNemesis` (changing `DropPPM` changes net draws and protocol behaviour, the nemesis schedule stays identical) |
| Nemesis alternates chaos/heal, is seeded, respects the budget, can be lifted | `TestNemesisAlternatesChaosAndHeal`, `TestNemesisIsSeeded`, `TestNemesisBudget`, `TestBudgetGroupLimit`, `TestPropNemesisRespectsBudget` (budget checked after every event) |
| Fuzz bytes → schedule: invariants, replay and convergence hold | `FuzzSimSchedule` (30 s, ~48k execs, no failure), `TestScheduleFromBytes` |
| Every env sentinel produced by the simulated transport and disk | timeout/unreachable/closed/too-large/remote/invalid-argument in the net tests; not-found/io/invalid-argument/closed in the disk tests; `TestCodeTableRoundTripsEveryRow` round-trips every row incl. an app row |
| objsim replays by seed | `TestToyScenarioReplaysBySeed`, `TestCommandLine`, `TestFailingScenarioExitsNonZero` |
| simtest seed handling and reporting | `TestSeeds`, `TestSeedErrors`, `TestPinnedSeeds`, `TestSeedsIncludesPinnedFirst`, `TestSweepRunsOneSubtestPerSeed`, `TestReplayCommand`, `TestCheckDeterministic`, `TestRequireDeterministicReportsAFailure` (REPLAY is the last line; artifact line count equals `LogLen`), `TestArtifactErrorIsReported` |

Canary design: the unseeded-draw and map-order bugs are wrappers around the toy process in
`internal/sim/meta_test.go` (they reply/forward a datagram based on `math/rand/v2` global
draws or the first key of a map range). The dropped-invariant bug is a field `dropInvariants`
on `Sim` set only by `PlantDroppedInvariants` in `export_test.go`; with it on, invariants
never run and `Verify` (called by `CheckDeterministic`) reports `ErrInvariantNotRun`.

Bug found by the tests during the build: `TestPropDiskModel` showed a torn crash rebuilt
the durable prefix from the written layer, silently undoing bit-rot on a dirty blob. Fixed
(`disk.crash` now keeps the durable bytes and appends a prefix of the unsynced tail).

## Measurements (all inside the `dev` container, 4-core droplet shared with other builders)

| Measure | Result |
|---|---|
| Events per second at 100 nodes | 232,147 events/s (`BenchmarkChaos100`, whole scenario incl. setup, nemesis, invariants every 100 events, digests; 70.8 ms per 100-node chaos run of ~16k events). Before caching the toy's peer list: 159,393 |
| Default `go test ./...` wall time | 4 s |
| `go test -race ./...` | 23 s |
| `DST_SEEDS=500 go test ./internal/sim/` (whole package incl. meta tests, 500 seeds × 3 sizes × 2 runs, plus the other tests) | 93 s on the final tree (an earlier run of the meta tests alone, before the toy peer-list change, took 172 s) |
| rapid at 2,000 checks (6 properties) | 6 s |

## Gate commands run (through `docker compose run --rm dev`, on the final tree, commit after `sim: test that a crashed server's late reply ...`)

| Command | Result |
|---|---|
| `gofmt -l .` | empty |
| `go vet ./...` | clean |
| `go build ./...` | ok |
| `go test ./... -count=1` | ok (cmd/objsim, env, sim, simtest), 4 s |
| `go test -race ./... -count=1` | ok, 23 s |
| `go test ./internal/sim -run '^$' -fuzz '^FuzzSimSchedule$' -fuzztime 30s -fuzzminimizetime 5s` | PASS, 16,474 execs (earlier runs on the same code: ~48k and ~19k; rate varies with the shared machine and corpus growth), no crasher, nothing added to `testdata/fuzz/` |
| `go test ./internal/sim/ -run 'Laws\|Prop' -rapid.checks=2000 -count=1` | ok, 6 s (rapid is imported only by `internal/sim` tests) |
| `DST_SEEDS=500 go test ./internal/sim/ -count=1` | ok, 93 s |
| `go test -cover ./...` | objsim 97.1%, env 97.9%, sim 94.9%, simtest 94.3%, simtoy 0.0% in-package (exercised from `internal/sim` tests: 88.5% of `./internal/sim/...` with `-coverpkg`) |
| `agent_work/gates/gate.sh` | does not exist in this worktree; not run |

## Uncovered branches (internal/sim, 94.9%; from the package's own tests)

- `New`: default fill of `MaxDatagram`/`MaxMessage` when a non-zero `NetConfig` leaves one
  of them zero — trivial defaulting.
- `toGlobal` clamp for `d > 2^62` — overflow guard for absurd durations.
- `Timer.Reset` on a closed incarnation; `CrashTorn`/`Restart`/`Resume`/`WipeRejoin`
  unknown-node errors (the same `node()` lookup is covered through `Crash`/`Pause`).
- `exec` / `releaseNode` / `unclog`: an event already marked dead while sitting in a held
  list or being requeued — defensive; a dead held event is only produced by a crash, which
  drops the held list wholesale.
- `cancel` of a link-released event (only a timer or a timeout can be cancelled, and those are
  never link-held).
- `eventQueue.remove` of an event not in the heap (defensive); `evKind`/`disposition`/
  `NemesisOutcome` `String` out-of-range or unused branches.
- `nodeName` out-of-range; `LogTail`, `LogLen`, `WriteLog` are covered from the simtest
  tests and `TestEventLogKeepsTheLastLogCapRecords`; the `WriteLog` writer-error branches are
  not.
- `Region`/`LocalTime` for unknown ids, `AddCluster` with `regions < 1` or a failing
  `AddNode`, `AddInvariant` with `every < 1`, `Workload` with `count <= 0` or `maxGap < 1`.
- `RunUntil`/`RunUntilQuiet` loop exits on an already-sticky error; `RunUntilConverged` when
  the predicate panics or `Step` fails mid-loop; `start` when the factory panics or returns nil.
- `Budget.Check` live-count failure as a return path (exercised through `Allows`),
  nemesis `subset`/`pair` with fewer than two nodes, the "apply failed after the budget
  allowed it" outcome, nemesis drawing `HealNet`/`HealAll` (not in `ChaosKinds`).
- `rot` with `Blob == ""` and no durable blob; `ScheduleFromBytes` with no nodes or an empty
  partition side; `Digests` for a paused node.

## Errors intentionally ignored (`_ =`)

- `internal/sim/stream.go` `fnv64a`, `internal/sim/log.go` `record` and `Digests`:
  `hash.Hash.Write` is documented never to return an error.
- `internal/sim/simtoy/simtoy.go` `ChaosRun`: `_ = s.RunUntilConverged(...)` — the error is
  sticky in `s.Err()`, which every caller reads through `Verify`/`CheckDeterministic`.
- `internal/sim/simtest/determinism.go` `packagePath`: a failing `os.Getwd` falls back to `.`
  in the REPLAY line (not `_ =`, but a swallowed error; it only affects a printed path).

## Panics added (lines appended to `agent_work/gates/panic-allowlist.txt`)

- `internal/env/errors.go  mustCodeTable` — the base table is a literal; failure is a bug.
- `internal/sim/handle.go  Sim.serve` (allowlist entry renamed from `serve` in the repair round) — a reply called twice is a protocol bug; the loop turns
  it into a `PanicError` with the seed.

## Notes for detlint (owned by another seat)

- `internal/sim/simtest` reads `os.Getenv` (`DST_*`), `os.Getwd`, reads `testdata/seeds.txt`
  and writes artifacts — by design; it is test-harness code, not sim-reachable protocol code.
- `internal/sim/meta_test.go` deliberately calls `math/rand/v2` global `rand.IntN` and ranges
  over a map (the canaries). `meta_test.go` also uses `os/exec` and `os.Getenv` for the
  cross-process replay. If detlint scans `_test.go` files these need an exemption.
- `internal/sim` uses `runtime/debug.Stack` (panic reports only; never reaches a digest) and
  `crypto/sha256` (digests).
- Maps in `internal/sim` are used for lookup only or iterated through `sortedKeys`;
  `Budget.damage` counts into a map but checks counts in node/name order.

## Things I did not do, and open points

- The PLAN 2.6 sentence "tested by having the simulator deliver requests twice and drop
  responses" conflicts with the brief's "never duplicated". I followed the brief: the
  request class is never duplicated. Idempotency can be tested with a lost response
  (`PartitionOneWay(server→client)`) followed by an application-level retry. If the
  orchestrator wants a transport fault that re-delivers a request, it is a small addition
  to `NetConfig` but it changes the frozen API — decide before the freeze.
- No runtime change of a node's skew/drift (a clock jump). Not asked for; HLC tests in
  Chunk 4 may want it. Adding it later is a simulator change request.
- The rapid fail file `internal/sim/testdata/rapid/TestPropDiskModel/...-100.fail` (from the
  torn-crash bug) is committed per the append-never-delete rule; rapid now reports it as
  "no longer valid" (the model's checks changed shape) and skips it.

## Requests to the orchestrator

1. Review the two open points above (request re-delivery fault; clock jumps) before the API
   freeze.
2. detlint exemptions as listed above.
3. `agent_work/INDEX.md` has no entry for `cmd/objsim` or for the `-update-golden` flag of
   `TestGoldenDigests` (I may not edit INDEX.md).

## Repair round (REVIEW-1: FAIL → this round)

Builder: Opus 5.5 (fresh seat), same branch, 2026-11-21. Exported API of `internal/env`,
`internal/sim`, `internal/sim/simtest` unchanged (API changes: none). `frozen-api.txt`
regenerated; its only difference is that `simtest.TailLines` is now its own `const`
declaration (it moved out of the seed constants block with the file split).
Each finding: regression test committed first and seen failing on the unfixed code, then the
fix in a separate commit.

| Finding | Test that now proves it (failed before the fix) | What changed |
|---|---|---|
| 1 MAJOR stream order under Pause + Clog | `TestReleaseKeepsFIFOAtTheReleaseInstant` subtests: "a request held by a paused node is not re-clogged behind a newer one" (reviewer's path A), "HealAll releases a paused node's held request before the clogged newer one" (path B), "Unclog then Resume at one instant …", "Resume then Unclog at one instant …" (passed before too; kept as the mirror case), "responses held by a paused client stay in order behind a clog" (both release paths). `TestPropStreamOrderPerConnection` (rapid state machine, 2–4 nodes: requests, pause/resume, clog/unclog, HealAll, HealNet, runs of 0–6 ms; after every run no request or response of one ordered pair is reordered or duplicated; after a final HealAll every request is delivered once and answered once). It found the bug in 7 cases on the old code. | `event.arrived` is set once an event passes its link's clog check; an arrived event (held by a paused/starting node, then released) never re-enters `link.held`. In `exec`, a link-released event that pops while its node still has released events pending (`n.relHeld > 0`) is requeued behind them (it keeps its link-release count, so newer arrivals on that link keep waiting). This covers HealAll's existing order (HealNet before resuming), so `HealAll` itself was not reordered. |
| 2 MAJOR budget: ungrouped blobs, read errors | `TestBudgetGroupLimit` subtests on a 3-node cluster with `shard/<id>`, `epoch`, `meta/1`, `saga/1` blobs and `GroupOf` returning `""` for non-shards: "blobs outside any group never count against a group", "a read error on a blob counts against its group", "a read error on a whole node counts against every group it holds", "an unnamed bit-rot is judged on every blob it could hit". `TestPropNemesisRespectsBudget` now wraps the toy so every node also stores two ungrouped blobs, sometimes uses a disk-heavy fault mix (bit-rot, read error, wipe, wipe-rejoin, crash, restart), and registers next to `Budget.Check` an independent check (`budgetHolds`: per-group and per-disk counts computed from `Damaged()`, `Names()` and the test-only `sim.ReadFaults`) after every event. It failed on the old code (a whole-node read error pushed a group over its limit). | `Budget.damage` counts, per node, the union of damage-marked blobs, blobs with pending read errors and, for a whole-node read error, every stored blob (`Disk.affected`); it skips `GroupOf == ""`. `Allows`: named bit-rot or read error → that blob; unnamed read error or wipe → every stored blob (`Disk.stored`, both layers); unnamed bit-rot → each `rotTargets()` blob separately (refused if any one would break the budget). |
| 3 MAJOR damage mark vs unsynced repair | `TestDiskFaults`: "bit-rot is silent …" (an unsynced rewrite keeps the mark, the sync clears it), "a crash before the repair is synced leaves the blob damaged and marked" (reviewer's reproduction), "an unsynced delete of a rotted blob resurrects it marked after a crash", "a synced delete clears the mark", "a wiped blob rewritten but not synced stays marked across a crash", "rot under a dirty blob is repaired by syncing it", "appending onto a rotted blob and syncing keeps the mark". `TestPropDiskModel`: the model tracks durable damage and written-layer damage, gained a `wipe` action, and compares `Damaged()` with the model after every step, across crash and torn crash. | `Write`/`Delete` no longer touch the mark. A blob carries `rot` (its written bytes carry the flipped bit: set by bit-rot on a clean blob, cleared by `Write`/`Delete`, reloaded from the mark on crash). `Sync` clears the mark when it removes the blob or when the bytes made durable do not carry rot. Wipe marks only blobs that exist in either layer (`Disk.stored`; previously also a never-synced write-then-delete leftover). |
| 4 NIT torn first append | `TestTornCrashCanTearTheFirstAppendOfANewBlob` (seeds 1–60: both "absent" and "non-empty strict prefix of the first record" occur; written and durable layers agree). `TestPropDiskModel` models it too. | `Append` on a clean blob always sets `appendOnly` (was `= dExists`); `disk.crash` marks a torn new blob durable when the kept prefix is non-empty. |
| 5 NIT panic stack not printed | `TestReportShowsThePanicStack` (report contains "panic stack:", the panicking function `simtest.explodeInStart` and `simtest_test.go`; REPLAY still last). | `simtest.Report` prints `PanicError.Stack` after the log tail when `s.Err()` is a `*sim.PanicError`. `runtime/debug` stays confined to `internal/sim/sim.go` `Sim.guard`; the stack is stored in `PanicError` only (never logged, hashed, sent or used in a decision). |
| 6 NIT harness polish | `TestPinnedSeeds` (tab between seed and note; a line with surrounding tabs), `TestReportTailHeaderCountsThePrintedLines` (`LogCap` 5, 30 events → "event log tail (5 of 30):" and 5 lines). | `PinnedSeeds` uses `strings.Fields`; the header prints `len(tail)`. `TestTimers` uses `t.Fatal` instead of `panic`. The remaining `panic(` in `internal/sim/loop_test.go` are the planted panics that `TestNodePanicFailsTheRunWithTheSeed` and `TestPanicInActionAndInvariantIsReported` exist to catch. |
| 8 NOTE payload hashed after the handler | `TestEventLogHashesThePayloadAsItCrossedTheWire` (handlers zero their datagram, request and reply buffers; the logged hash must be FNV-64a of the sent bytes). | `Step` takes `fnv64a(e.payload)` before `exec` for network kinds and passes it to `record`. |
| Gate: allowlist name | — | `agent_work/gates/panic-allowlist.txt` second entry now `Sim.serve`. |

Golden digests: `TestGoldenDigests` passed unchanged after every fix; `testdata/golden.txt` is
NOT modified. Why: the toy chaos runs never hit the re-clog or same-instant link/node release
path, the toy never mutates a handler input, never uses `Append`, rewrites its blob with
`Write`+`Sync` (same mark outcome as before), and its budget (`ChaosBudget`) has no `GroupOf`.
`TestReplaySameProcess` and `TestReplayAcrossProcesses` pass.

Rapid fail files: the two new properties wrote fail files while they were failing on the old
code; I deleted them (rapid reports them "no longer valid" once the generator or model changes,
so they would only print a warning). The older `TestPropDiskModel/...-100.fail` from the first
build is left as it was.

Determinism-lint conformance of what I added: no new imports outside the allowed list
(`runtime/debug` stays the single exemption in `sim.go`); every new map range goes through
`sortedKeys` (`Disk.affected`, `Budget.damage`); no floats, no pointer/func/chan passed to a
`fmt` function, no `fmt` function used as a value, no build tags or platform files.

### File moves (pure; a sorted line-by-line comparison of each package's non-import code against the previous commit is identical except `TailLines` leaving the seed const block)

- `internal/sim/simtest/simtest.go` → `seeds.go` (`DefaultSeeds`, `DefaultSeedBase`,
  `SeedsFile`, `seedValue`, `seedFlag`, `init`, `Seed`, `envInt`, `PinnedSeeds`, `Seeds`,
  `Sweep`), `determinism.go` (`TailLines`, `ErrNondeterministic`, `CheckDeterministic`,
  `RequireDeterministic`, `moduleRoot`, `packagePath`, `RunPattern`, `ReplayCommand`,
  `unsafeName`, `writeArtifact`, `Report`), `cluster.go` (`Cluster`). `simtest.go` is gone.
- `internal/sim/rand.go` → `stream.go` gets `NewStream` and `fnv64a` (imports only `hash/fnv`
  and `math/rand/v2`; no domain names); `rand.go` keeps `StreamNet/Disk/Nemesis/Workload` and
  `NodeStream`.
- `internal/sim/handle.go` → `clock.go` gets `node.local`, `node.toGlobal`, `handle.Now`, the
  `timer` type, `handle.AfterFunc`, `timer.arm`, `timer.Stop`, `timer.Reset`.
  `Sim.cancel` stays in `handle.go` (it is also used by request timeouts and touches link
  release counters; see below). `Sim.serve` stays in `handle.go` so the allowlist path holds.

### Extraction notes (tangles that remain; input for a knowledge entry, not fixed now)

- `stream.go` is clean. `rand.go`'s labels name domain streams and `NodeStream` takes
  `env.NodeID`.
- `clock.go`: `local`/`toGlobal` are methods on the domain `node` (which also holds disk,
  process, calls, held events); `timer` holds a `*handle`, the per-incarnation object that also
  implements `env.Transport` and `env.Disk`; `handle.closed` reads `node.inc`/`node.up`;
  `timer.arm` builds an `event` with `kindTimer`, the node index and incarnation; `timer.Stop`
  calls `Sim.cancel` (in `handle.go`), which reaches into `s.links[..][..].relHeld` and
  `s.nodes[..].relHeld`. Times are `env.Time`.
- `queue.go`: the heap only looks at `at`, `seq`, `hi`, but `event` carries domain fields
  (`inc`, `peerInc`, `payload`, `call`, `code`, `msg`, `fault`, `nemesis`, `timer`,
  `released`, `relLink`, `arrived`, `dead`, `until`) and the file defines the domain `evKind`
  and `disposition` enums.
- `sim.go`: `Sim` mixes loop state (`now`, `seq`, `steps`, `q`, `err`, `invariants`,
  `MaxSteps`) with `nodes`, `byID`, `links`, `codes`, `NetConfig`, the four named PRNGs,
  `nextCall` and the log fields. `Step` now also computes the wire hash using
  `evKind.network()` (added this round). `exec` switches on every kind and owns the link-clog
  and node-pause hold/release accounting (`relHeld` on nodes and links, `arrived`), i.e. the
  FIFO-at-release rule is network logic inside the loop. `guard` reads `s.nodes` to name the
  node in a `PanicError` and calls `runtime/debug.Stack`. Invariants, stall errors, `At` and
  `Workload` (kernel-shaped) live in `sim.go` next to node management (`AddNode`, `boot`,
  `start`, `releaseNode`).
- `log.go`: `record` interprets `evKind` (payload hash, fault encoding, action label, call id,
  incarnation); `nodeName` reads `s.nodes`; `Digests` encodes node flags, incarnation,
  `EncodeState()` and the disk layers; `Digests.State` is domain, `Digests.Events` is kernel.
  The log cannot be switched off or replaced (no interface; `logHash` is always SHA-256).
- `nemesis.go`: chaos phases are scheduled as `kindFault` events carrying a `*Nemesis`; the
  loop dispatches them through `exec`.
- `disk.go`: `Disk` keeps an `s *Sim` back-pointer only for `diskRand`.
- `simtest`: `seeds.go` has no `sim` or `env` reference. `determinism.go` depends on `*sim.Sim`
  (`Digests`, `Verify`, `Err`, `Seed`, `Steps`, `Now`, `LogTail`, `LogLen`, `WriteLog`) and on
  `*sim.PanicError`; `cluster.go` depends on `env.NodeID`, `sim.NodeIDs`, `sim.Factory`,
  `Sim.AddCluster`.

### Gate commands run in the repair round (through `docker compose run --rm dev`, on the final code commit `570c9c7`)

| Command | Result |
|---|---|
| `gofmt -l .` | empty |
| `go vet ./...` | clean |
| `go build ./...` | ok |
| `go test -count=1 ./...` | ok (objsim, env, sim, simtest), 5 s |
| `go test -race -count=1 ./...` | ok, 33 s |
| `go test ./internal/sim -run '^$' -fuzz '^FuzzSimSchedule$' -fuzztime 30s -fuzzminimizetime 5s` | PASS, 8,902 execs, 16 new interesting inputs (kept in the Go build cache, nothing added to `testdata/fuzz/`), no crasher |
| `go test -count=1 ./internal/sim/... -rapid.checks=2000` (the brief's form) | `internal/sim/simtest` FAILs with "flag provided but not defined: -rapid.checks", since only `internal/sim` imports rapid; the package result for `internal/sim` was cut off in my log, so I re-ran it alone (next row) |
| `go test -count=1 ./internal/sim -rapid.checks=2000` | ok, 13 s (8 properties incl. the new `TestPropStreamOrderPerConnection`) |
| `DST_SEEDS=500 go test -count=1 ./...` | ok; `internal/sim` 119 s (was 93 s; the machine is shared and the budget property now does more), others under 1 s |
| `go test -count=1 -cover ./...` | objsim 97.1%, env 97.9%, sim 95.2% (was 94.9%), simtest 95.1% (was 94.3%), simtoy 0.0% in-package (exercised from `internal/sim` tests) |
| `agent_work/gates/gate.sh` | does not exist in this worktree; not run |

New code is fully covered (`Disk.affected`, `Disk.stored`, `Budget.damage`, `Budget.Allows`,
`refsOf`, `Sync`, `disk.crash`, `record`: 100%); no new uncovered branch. `exec`
(98.1%) and `Step` (95.5%) keep the uncovered branches listed in the first round.

### Requests to the orchestrator (repair round)

1. The brief's rapid command `go test ./internal/sim/... -rapid.checks=2000` cannot pass in
   `simtest` (it does not import rapid). Use `go test ./internal/sim -rapid.checks=2000`.
2. `runtime/debug` exemption for `internal/sim/sim.go` as announced in the repair brief.

## Post-adversarial round (ATTACKS.md: 8 BROKEN, REVIEW-2 NIT 1 → this round)

Builder: Opus 5.5 (fresh seat), same branch, 2026-11-21. Brief:
`objstore-p2-0b-repair2.md`. All regression tests were committed first in `a577f64`. Each
one failed on the code before the fixes: I ran the package and saw every new test fail. The
cmd test failed to compile because `run` did not take the program name yet. The extended
`TestPropNemesisRespectsBudget` failed only at `-rapid.checks=2000` (after 163 cases: group
`stripe/6` had 2 damaged blobs with a limit of 1). The fixes follow, one commit per item
group.

### Items

| # | Attack / finding | Test that proves it (failed before) | What changed (commit) |
|---|---|---|---|
| 1 | C4 `read-count-overflow`, `whole-read-overflow-budget` | `TestReadFaultCountsSaturate` (named and whole-node counts past `MaxInt` keep failing reads; the whole-node case stays visible to `Budget{}.Check`; counts `0`, `-1` and `MinInt` are rejected and change nothing) | `FailReads` adds with saturation at `math.MaxInt` (`addSat`). A saturated count means "fail every read until `HealAll`". Non-positive counts were already `ErrInvalidArgument`. (`371a1d3`) |
| 1 (audit) | incarnation counter (`uint32`) | `TestIncarnationCounterNeverWraps` (restart, wipe-rejoin and HealAll at `MaxUint32`; uses the test-only hook `SetIncarnation`) | `Restart`/`WipeRejoin` return `ErrInvalidArgument` at incarnation `MaxUint32`. `HealAll` leaves such a node down. Without this the counter would wrap to 0, the value that marks incarnation-free events, so stale timers would fire in a new incarnation. (`371a1d3`) |
| 2 | C6 `nemesis-callback-panic` | `TestPanicInABudgetCallbackFailsTheRun` (a `GroupOf` panic reached through bit-rot, read-error, wipe-disk and crash steps; asserts nothing escapes `RunUntilQuiet`, and both the returned error and `s.Err()` are a `PanicError` with the seed and "nemesis" in the message) | `exec` runs `kindFault` events under `s.guard`: nemesis steps as event `nemesis chaos` / `nemesis heal`, `InjectAt` faults as `fault <kind>`. The guarded step returns `dispFailed`. (`8f42739`) |
| 3 | C2 `huge-latency-range` | `TestHugeLatencyIsDrawnWithoutOverflow` (ranges `{0,MaxInt64}`, `{MaxInt64-1,MaxInt64}`, `{MaxInt64,MaxInt64}`, `{Horizon,MaxInt64}`, 20 seeds each, datagram plus request), `TestLatencyBeyondTheHorizonIsNeverDelivered`, `TestVirtualTimeSaturatesAtTheHorizon` (RunFor, actions, Workload gaps, `ScheduleFromBytes` with a huge step, nemesis windows) | Latency is drawn as `Min + Uint64N(uint64(Max-Min)+1)`. That is the same value and the same draw count as the old `Int64N` for every range that worked before, and it cannot overflow because `0 <= Min <= Max`. Every time sum saturates instead of wrapping (`later`, `scaled` in `clock.go`): delivery, stream delivery, Workload gap, nemesis gap, `ScheduleFromBytes` step. `StartNemesis` rejects with `ErrInvalidArgument` any config where `Start + Phases*(Chaos+Heal)` passes `Horizon` or `Chaos+Heal` overflows. I chose "drawn without overflow" over "reject at configuration": any non-negative range is legal, and a draw that lands past the horizon is a message that is never delivered. (`21c80f6`) |
| 4 | C6 `clock-overflow` | `TestSkewIsBoundedSoLocalTimeNeverOverflows` (`AddNode` rejects `±(MaxSkew+1)`, `MaxInt64` and `MinInt64`; four nodes at `±MaxSkew` × `±MaxDriftPPM` read monotonic local time from 0 to `Horizon`) | New `MaxSkew = 2^61 ns` (about 73 years), checked in `AddNode` (and so in `AddCluster`). The bound holds because with \|drift\| ≤ 10 % and global time ≤ `Horizon`, \|local\| ≤ 1.1·2^62 + 2^61 < 2^63. (`21c80f6`) |
| 5 | C6 `timer-duration-clamp` | `TestTimersBeyondTheHorizonNeverFire` (for drift 0, −100,000 and +100,000 ppm: `AfterFunc(Horizon+1)` and `AfterFunc(MaxInt64)` never fire, `Stop` on such a timer reports it was pending, a `Request` with timeout `MaxInt64` to a partitioned peer never completes, time never passes `Horizon`; a timer due exactly at `Horizon` fires at `Horizon`) | `toGlobal` no longer clamps to 2^62. It saturates at `MaxInt64`, and the sum saturates too. `schedule` does not queue an event due after `Horizon`. (`21c80f6`) |
| 6 | C7 `cli-failure-report` | `TestFailingScenarioPrintsTheLogTailAndReplay` (a failing scenario registered from the test; 5 cases: installed binary, `=`-form flags, a `go run` temp binary, the default seed, a path with a space; checks stdout digests, the error, "event log tail (20 of 31):", a `t=40 seq=` line, "panic stack:", exactly one REPLAY, and that it is the last line), `TestPassingScenarioPrintsNoReplay` | On failure, objsim prints to stderr, in order: `objsim: <err>`, `event log tail (n of N):` with the last 20 records indented two spaces (simtest's shape), `panic stack:` for a `PanicError`, and last `REPLAY: <cmd> -scenario <name> -seed <n>`. `<cmd>` is `os.Args[0]` shell-quoted, or `go run ./cmd/objsim` when the binary sits in a `go-build` temp dir, which is what `go run` produces. Like simtest's REPLAY, that form assumes the module root as working directory. `run` now takes the program name first (package `main`, not frozen). (`1fed710`) |
| 7 | REVIEW-2 NIT 1 | `TestBudgetGroupLimit/a nemesis read error never ambushes a blob written after it` (the reviewer's scenario through the nemesis: 30 seeds, read-error-only nemesis on 3 empty nodes, shards written during chaos, `Budget.Check` after every event; also asserts no applied whole-node read error). `TestPropNemesisRespectsBudget` (extended: a wrapper may write a NEW blob `shard/<k>` on every node every 50–400 ms of local time; `GroupOf` maps it to group `stripe/<k>`) | With `Budget.GroupOf != nil`, a nemesis read error names one blob from `disk.stored()` and is skipped when the node stores none. With `GroupOf == nil`, the draws and behaviour are unchanged. (`60ed873`) |
| 8 | C5 `network-nemesis-dynamic-blobs` | `TestPropNemesisTimingIgnoresInventory` (one seed, two runs whose processes differ only in what they write to disk: 0–3 blobs at start, optional periodic writes; all `ChaosKinds`; budget lifted or drawn with or without groups; every step must have equal `At` and equal `Fault.Encode()` with `Blob` blanked. `Outcome` may differ because the budget reads the disk) | The blob choice for bit-rot and for a named read error draws exactly one `Uint64` from the `nemesis` stream (`Nemesis.index`), even for an empty inventory (then the step is skipped). The mapping is `x & (k-1)` for a power of two and otherwise the high word of `x·k`, which is exactly what `IntN(k)` returns unless `IntN` would resample (probability below k/2^64). This changed golden digests (below). (`60ed873`) |

### The narrowed claim (replaces the "Streams are independent" row's sim-level reading)

The nemesis's schedule — when it acts, on which node, with which kind of fault — depends only on the seed and the nemesis configuration. Which blob a disk fault hits is chosen from the blobs on that node at that instant, so it depends on what the protocol has written.

Two notes on that claim:
- The other fault fields are also independent of the disk: partition sides, region, peer,
  link-loss ppm and read-error count. `TestPropNemesisTimingIgnoresInventory` compares every
  field except `Blob`.
- The outcome (applied/rejected/skipped) of a DISK fault can depend on the disk, because
  `Budget.Allows` judges the blobs that fault would hit. No nemesis-owned state depends on a
  disk-fault outcome. The live, paused and clogged sets change only through process and
  network faults, and those are judged on `MinLive` and on a damage state that the budget
  keeps within limits. So the schedule of later steps does not move. The property checks
  this, because it compares every later step.

The adversarial attack `network-nemesis-dynamic-blobs` stays BROKEN as worded. Its two runs
choose different blob names (`"received"` vs `"base"`) at step 4, which the narrowed claim
allows.

### Contract changes for Wave B (amend "Semantics Wave B builders must know")

- **Time horizon.** `sim.Horizon = 2^62 ns` (about 146 years of virtual time). Global time
  never passes it. `RunUntil`, `RunFor` and `RunUntilConverged` clamp their target or deadline
  to `Horizon`. An event due after `Horizon` is never queued and never runs: a timer, a
  request timeout, a datagram or stream delivery whose latency lands past it, an `At`
  action, an `InjectAt` fault, a nemesis or workload step. So a timer or request timeout
  beyond the horizon never fires early. It never fires at all, and `Stop` on it returns
  true. An event due exactly at `Horizon` runs. Time sums saturate and never wrap.
- **Skew.** `|NodeSpec.Skew| <= sim.MaxSkew` (2^61 ns), else `ErrInvalidArgument` from
  `AddNode`/`AddCluster`. With `|DriftPPM| <= MaxDriftPPM`, a node's local time is monotonic
  and within int64 for every global time in `[0, Horizon]`.
- **Latency.** Any `Range` with `0 <= Min <= Max` is accepted. A draw past the horizon means
  the message is never delivered. On a stream link it also holds every later stream message
  on that link behind it (FIFO).
- **Nemesis.** `StartNemesis` returns `ErrInvalidArgument` if the last heal would come after
  `Horizon` or if `Chaos+Heal` overflows. Under a group budget (`GroupOf != nil`), read errors
  are per blob, never whole-node.
- **Read faults.** `FailReads` counts saturate at `MaxInt`.
- **Incarnations.** `Restart`/`WipeRejoin` return `ErrInvalidArgument` once a node has used
  incarnation `MaxUint32`, and `HealAll` leaves it down.
- **Panics in fault events.** A panic from a caller-supplied callback reached by a fault
  event (`Budget.GroupOf` via `Allows`) is a `PanicError` with the seed, event
  `nemesis chaos` or `fault <kind>`. I audited every user-supplied function the simulator
  calls: `Factory`, the `Process` methods, `AfterFunc` and `Request` callbacks, `At` and
  `Workload` functions, invariants, convergence predicates, `EncodeState` (in `Digests`), and
  `GroupOf`. All now run under `guard`. A `Reply` function and `Budget.Allows`/`Check` called
  directly by a test run in the caller's own context: inside a node handler, action or
  invariant they are guarded by that event. Called outside the loop, a panic is the caller's.
- **objsim.** A failing run prints the log tail, the panic stack and a final `REPLAY:` line.

### Overflow audit (items 1, 3: every counter and accumulator)

- `disk.go`: `failAny` and `failReads[name]` saturate (fixed). The `IntN` arguments
  `len(b.data)-len(b.dur)+1` and `len(b.dur)*8` are bounded by allocated memory.
- `net.go`/`sim.go`: `relHeld` (link and node) counts held events, bounded by memory.
  `lastStream` is a time and is set from saturated sums. `seq`, `steps`, `nextCall` and
  `logTotal` are `uint64` and grow by one per event, bounded by `MaxSteps` (a `uint64`), so
  they cannot wrap within any run. `inc` is `uint32` and is now bounded (above).
- `nemesis.go`: the gap `1 + Int64N(MaxGap)` cannot overflow (`MaxGap <= MaxInt64`), and its
  sum saturates. Phase times are validated. `hist` is bounded by memory.
- `fault.go`: `ScheduleFromBytes` step times saturate. `Count = 1 + b3%4` is small.
  `Fault.Count` from a caller goes through `FailReads`.
- `Range`-typed fields: only `NetConfig.Latency` and `InterRegionLatency`. Both are handled
  by the overflow-free draw; `Min < 0` or `Max < Min` is still rejected.

### API changes (exported additions only; no signature changed)

- `sim.MaxSkew = time.Duration(1 << 61)`: the skew bound item 4 asks for, the counterpart of
  `MaxDriftPPM`.
- `sim.Horizon = env.Time(1 << 62)`: the time horizon the contract now states. Tests and
  Wave B code need it to reason about "never fires". It replaces the unexported 2^62 clamp
  that was in `toGlobal`.

`frozen-api.txt` is regenerated. Its only difference from the previous listing is these two
lines in the `sim` constant block. New behaviour on existing signatures: `Restart` and
`WipeRejoin` can now return `ErrInvalidArgument`, `StartNemesis` rejects configurations past
the horizon, and `AddNode` rejects large skew. Test-only addition:
`SetIncarnation` in `export_test.go`.

### Golden digests

`testdata/golden.txt` changed in `60ed873`, on purpose, because item 8(a) forces it. Rows
20/3, 50/1, 50/2 and 100/1 changed; rows 20/1, 20/2, 50/3, 100/2 and 100/3 did not. Cause:
those toy chaos runs bit-rot a node that holds no durable blob. That step now consumes one
`nemesis` draw where it consumed none. Check: restoring only the "skip without a draw on an
empty inventory" line (and keeping the new `index` mapping) reproduces the old golden file
exactly; I ran `TestGoldenDigests` against the old file in that state and it passed. So the
`index` mapping and the other fixes moved nothing. Items 1–6 do not touch golden: the commit
before `60ed873` passes `TestGoldenDigests` against the old file. The build-notes claim that
`objsim -scenario=toy-chaos-100 -seed=3` prints the golden row still holds for that row,
which did not change. `TestReplaySameProcess` and `TestReplayAcrossProcesses` pass.

### Adversarial harness rerun (`bash agent_work/adversarial/0b-simulator/run-all.sh`, harness unchanged)

`SUMMARY held=58 broken=2 setup_errors=0 elapsed_seconds=17` (was held=52 broken=8). Full
output: `agent_work/0b-simulator/adversarial-rerun/run-all.txt`. The CLI fixture's failing
output and its executed REPLAY are kept next to it. Running the harness rewrites its
tracked evidence files under `agent_work/adversarial/`. I restored them with `git checkout`
and copied the new output into `adversarial-rerun/` instead, because I may not edit that
directory.

Remaining BROKEN lines:
- `BROKEN C5 network-nemesis-dynamic-blobs: seed=1 DropPPM 0 vs 1000000 with OnDatagram writing received changed nemesis step 4: {12ns bit-rot 1 "received" applied} versus {12ns bit-rot 1 "base" applied}`.
  Expected, as the brief says. The claim is narrowed (above), and the times, kinds and nodes
  of both histories are equal.
- `BROKEN C6 clock-overflow: sim: add node 0: skew 9223372036854775807 outside ±2305843009213693952: invalid argument`.
  This is the fix the brief asks for: skew is bounded at `AddNode`. The harness calls
  `must(s.AddNode(...))`, so it reports any rejection as BROKEN and cannot score a
  configuration-time rejection as held. Its own pass condition (local time monotonic or a
  sim error) can no longer be violated, because no accepted skew can wrap
  (`TestSkewIsBoundedSoLocalTimeNeverOverflows`). Request to the orchestrator below.

The other six attacks are HELD: `read-count-overflow`, `whole-read-overflow-budget`,
`nemesis-callback-panic`, `huge-latency-range` (the whole range `[0, MaxInt64]` is now
drawn and the run ends cleanly), `timer-duration-clamp`, and `cli-failure-report`. In
`cli-failure-report` the harness executed the printed REPLAY and it reproduced exit 1 and
both digests.

### Not done, and why

- I did not reject latency ranges at configuration. Drawing without overflow keeps every
  non-negative range legal and makes `huge-latency-range` hold. A range whose draws land past
  the horizon just loses those messages, which is the same as an absurdly late delivery.
- `TestPropNemesisRespectsBudget` finds the old ambush only at 2,000 checks, not at the
  default 100. G2 runs 2,000, and the deterministic ambush subtest in `TestBudgetGroupLimit`
  fails at the default count.
- The rapid fail files written while the new properties failed on the old code were deleted,
  as in the previous round.

### Gate and commands (this round)

`agent_work/gates/gate.sh 0b-simulator` from the worktree root on the host, run on the clean
tree at `4ca8e303cd22ce67b772eb8813984c44282de7e5` (all code commits of this round plus the
frozen-API commit; this notes commit changes only this file):
`gate: overall=pass counts_for_landing=true`. The result files under
`agent_work/0b-simulator/gates/` are left uncommitted.

| Gate | Result |
|---|---|
| G0 tools, gofmt, vet, build, detlint (15 files in 3 packages; no new allowlist line), panics (no new `panic(`; allowlist untouched), deps, comments (baseline `c5117c5`), selfcontained | pass |
| G1 unit + coverage floor | pass. objsim 98.0% (was 97.1%), env 97.9%, sim 95.3% (was 95.2%), simtest 95.1%, simtoy 0.0% in-package (exercised from `internal/sim` tests, as before) |
| G2 rapid, 2,000 checks (seed 84288246893858) | pass, 58 s |
| G3 `FuzzSimSchedule` 30 s | pass, 13,248 execs, 19 new interesting, no crasher |
| G4 race | pass, 35 s |
| G5 500-seed sweep (base 84288246893858) | pass, `internal/sim` 98 s |
| G6 docker | pass |

Before the gate, also run through `docker compose run --rm dev`: `gofmt -l .` (empty),
`go vet ./...`, and `go test -count=1 ./...` (all ok) after each fix commit.
`go test -count=1 ./internal/sim -rapid.checks=2000` was ok in 142 s with the scribbler's
1 ms cadence. I then raised the minimum cadence to 20 ms, and G2 above took 58 s.
`TestGoldenDigests -update-golden` was run once, in `60ed873`.

Per-function coverage of the new and changed code (`go tool cover -func`, measured after
the gate): `addSat`, `FailReads`, `toGlobal`, `withinHorizon`, `StartNemesis`,
`Nemesis.index`, `latency`, `schedule`, `RunFor`, `AddNode`, `HealAll` and `lastIncarnation`
are at 100%, and so are objsim's `run`, `report`, `command` and `quote`. New uncovered
branches: `later`'s negative-overflow return and `scaled`'s negative-saturation return
(80.0% and 85.7%). Both are reachable only with a negative duration near `MinInt64`, for
example a negative `ScheduleFromBytes` step. Also uncovered is `RunUntilConverged`'s clamp of
a deadline past `Horizon` (75.0%, together with the earlier uncovered branches of that
function). The remaining gaps in `Restart` (88.9%), `WipeRejoin` (90.0%), `RunUntil`,
`exec` and `draw` are the unknown-node and defensive branches listed in the first round.

### Requests to the orchestrator (this round)

1. Adversarial harness `clock-overflow` (`agent_work/adversarial/0b-simulator/main.go`, attack
   "clock-overflow"): accept `errors.Is(err, env.ErrInvalidArgument)` from `AddNode` as held.
   The brief directs a configuration-time bound, and the harness's `must` scores that as
   BROKEN. I did not edit the harness.
2. Build notes, main contract and claims table: the sentence "Every draw comes from the
   `nemesis` stream, only from nemesis-owned state" and the row
   `TestPropNetworkDrawsDoNotShiftTheNemesis` should be read with the narrowed claim above.
   That text predates this round. I left it in place and put the corrected contract in this
   section, which takes precedence.
