# Chunk 0b build notes — the deterministic simulator

Builder: Opus 5.5 (fresh seat), branch `chunk/0b-simulator`, 2026-11-21.
Frozen API listing (what Wave B is briefed from): `agent_work/0b-simulator/frozen-api.txt`
(`go doc -all` of `internal/env`, `internal/sim`, `internal/sim/simtest`).

Size: about 3,300 lines of product code, about 3,100 lines of tests.

## What was built

| Package | Files | Contents |
|---|---|---|
| `internal/env` | `env.go`, `errors.go` | `NodeID`, `Region`, `Time` (virtual ns, `Add`/`Sub`); interfaces `Clock`, `Timer`, `Transport`, `Disk`, `Process`; `Reply`; the `Env` bundle with `WithNet`/`WithDisk`/`WithClock`; sentinels `ErrTimeout`, `ErrUnreachable`, `ErrClosed`, `ErrTooLarge`, `ErrRemote`, `ErrNotFound`, `ErrIO`, `ErrInvalidArgument`; wire status codes (`Code`, `CodeTable`, `RemoteError`) |
| `internal/sim` | `sim.go` (loop, nodes, invariants, errors), `queue.go` (indexed heap), `handle.go` (per-incarnation Clock/Transport/Disk), `net.go`, `disk.go`, `process.go`, `fault.go`, `nemesis.go`, `log.go`, `rand.go` | the simulator |
| `internal/sim/simtest` | `simtest.go` | `-seed` flag, `DST_*` variables, `Seeds`, `Sweep`, `CheckDeterministic`, `RequireDeterministic`, `Report`, `ReplayCommand`, `RunPattern`, `PinnedSeeds`, `Cluster` |
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
changes are appends onto its durable content keeps the durable bytes plus a random prefix
(possibly empty, possibly all) of the appended bytes, drawn from the `disk` stream.
Bit-rot flips one bit of the durable bytes (and of the written bytes if the blob is clean);
the next read returns the bad bytes with no error. `Write`/`Delete` of a rotted or wiped name
clears its damage mark. Wipe removes both layers. Read errors: `FailReads(node, name, n)`
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
requeue fails it).

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
stripe" (Chunk 2 maps a shard blob name to its stripe id). Zero `MaxDamagedDisks` means no
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
index, peer index, FNV-64a of the payload (xor the status code; fault encoding for faults;
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
the seed it is given and return the finished sim (errors live in `s.Err()`).

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

- `internal/sim/rand.go` `fnv64a`, `internal/sim/log.go` `record` and `Digests`:
  `hash.Hash.Write` is documented never to return an error.
- `internal/sim/simtoy/simtoy.go` `ChaosRun`: `_ = s.RunUntilConverged(...)` — the error is
  sticky in `s.Err()`, which every caller reads through `Verify`/`CheckDeterministic`.
- `internal/sim/simtest/simtest.go` `packagePath`: a failing `os.Getwd` falls back to `.`
  in the REPLAY line (not `_ =`, but a swallowed error; it only affects a printed path).

## Panics added (lines appended to `agent_work/gates/panic-allowlist.txt`)

- `internal/env/errors.go  mustCodeTable` — the base table is a literal; failure is a bug.
- `internal/sim/handle.go  serve` — a reply called twice is a protocol bug; the loop turns
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
