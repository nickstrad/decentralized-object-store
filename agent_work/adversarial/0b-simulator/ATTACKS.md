# Chunk 0b adversarial results

Target: `d8f7a0ab4283fb66cfc06b186eb72bb5ac867300`, including the repair round.
All execution used `docker compose run --rm -T dev` from the supplied worktree.
All authored files are beneath `agent_work/adversarial/0b-simulator/`; no product
changes, commits, git writes, or edits to the project-root state file were made.

Run from the worktree root:

```sh
bash agent_work/adversarial/0b-simulator/run-all.sh
```

The runner executes seven independent drivers, captures their exact verdict lines in
[evidence/run-all.txt](evidence/run-all.txt), and exits 1 when a claim is broken, 2 on
setup failure, or 0 if everything holds. Each `aNN-*.sh` also runs independently with
no arguments. Temporary binaries and the CLI copy are removed by traps/context
managers. Evidence files intentionally remain. The single nested module uses a module
path beneath the product's import path, satisfying Go's `internal` rule, and replaces
the product dependency with `../../..`. It adds no third-party dependencies.

`main.go` contains the custom protocol and C1–C6 attacks; `reportfixture/` is an
intentionally failing Go test used to exercise simtest's actual reporting and replay.
`reporting.py` orchestrates C7, captures subprocess output, and copies the production
CLI runner byte-for-byte into a temporary package in this same module. The copy gets
one registered failing scenario because the shipped CLI exposes only successful toy
scenarios. Its failure-report verdict concerns the production runner, not a modified
runner or an invalid-command usage error.

## Broken attacks and minimal API reproductions

Use `a04-c4.sh`, `a05-c5.sh`, `a06-c6.sh`, or `a07-reporting.sh` to reproduce the
corresponding findings. The huge-latency C2 attack lives in `a06-c6.sh` with the other
arithmetic boundary probes. Node setup in these sequences means a started node with
an otherwise inert process, zero latency, and explicit positive size limits.

1. **C4 read-count-overflow — integer overflow disables named read faults.** Seed 1:
   write blob `x`, call `FailReads("0", "x", math.MaxInt)` and then
   `FailReads("0", "x", 1)`. Both return nil. `Read("x")` succeeds, though pending
   injected errors were never consumed. The count wraps negative. This needs only
   two fault injections, not billions of events.
2. **C4 whole-read-overflow-budget — the same overflow hides a faulty disk.** Seed 1:
   write and sync `x`, call those same two injections with blob name `""`.
   Before consuming any read, `Budget{MaxDamagedDisks:0}.Check(s)` returns nil and
   `Read("x")` succeeds. This is a second observable consequence of the same unchecked
   addition, not an independent root cause.
3. **C5 network-nemesis-dynamic-blobs — broad schedule-independence claim fails.**
   Seed 1, nodes `0`, `1`, `2`, each with a synced blob `base`. Node 1's datagram handler
   writes and syncs `received`. Deliver a datagram from 0 to 1 before starting a
   bit-rot-only nemesis, or change only `DropPPM` to 1,000,000 and drop it. Nemesis
   configuration: Start=2ns, Phases=2, Chaos=100ns, Heal=10ns, MaxGap=3ns, Lifted=true.
   History index 4 at 12ns targets node 1's `received` with drop off, versus `base`
   with drop on. The executable sends one datagram before the nemesis. Both executions are individually
   deterministic. Named PRNG independence holds; the *fault schedule* depends on the
   protocol's durable blob inventory. The builder contract explicitly includes blob
   names as an input, so this is an overbroad published claim needing qualification or
   a design change, not proof that the PRNG streams share state.
4. **C6 nemesis-callback-panic — fault-event callback escapes the loop.** Seed 43:
   write and sync `x` on one node; start a bit-rot-only nemesis with Start=1ns,
   Phases=1, Chaos=2ns, Heal=1ns, MaxGap=1ns, and a budget allowing one disk and one
   group member whose `GroupOf` panics `"group-probe"`. `RunUntilQuiet` panics directly;
   `s.Err()` stays nil. The harness catches the escaped panic to report the result.
   Normal node, action, invariant, predicate, timer and encoding panics are captured.
5. **C2 huge-latency-range — accepted latency range fails during delivery.** Seed 1:
   one node, `Latency=Range{Min:0, Max:math.MaxInt64}`, positive size limits. Schedule
   an action that calls `Send("0", []byte{1})`. `RunUntilQuiet` produces a seeded
   `PanicError: invalid argument to Int64N`; `Max-Min+1` overflows. This is a network
   configuration/delivery failure. The C6 promise to capture an action panic holds here.
6. **C6 clock-overflow — local virtual time goes backwards.** Seed 1: add node 0 with
   `Skew=math.MaxInt64`, start it at global 0, read its clock, `RunUntil(1)`, read again.
   Values: 9223372036854775807 then -9223372036854775808, without an error. The global
   clock remains monotonic. The node's accepted skew plus global time is unchecked.
7. **C6 timer-duration-clamp — large timers fire early.** Seed 1: on a started node
   with zero skew/drift, call `AfterFunc(time.Duration(1<<62)+1, callback)` and
   `RunUntil(1<<62)`. The callback has already run. `toGlobal` silently clamps the
   valid duration to `1<<62`; it does not reject it. Values are extreme (roughly
   146 years), but within the exported signed-duration range.
8. **C7 cli-failure-report — no log tail or replay command.** Seed 23: register a
   scenario `adversarial-fail` that schedules one action panicking `cli-replay-fixture`;
   invoke the copied production CLI as `objsim -scenario adversarial-fail -seed 23`.
   It exits 1 and prints seed and both digests, but neither a log tail nor `REPLAY:`.
   [The exact captured output](evidence/cli-original.txt) contains all four output
   lines. Repeating the same arguments reproduces the failure and digests; there is
   no emitted replay command to execute. `reporting.py` includes the complete small
   scenario and constructs/removes the fixture automatically.

## Interpretation, tolerance, and limits

- HELD means the executed attack held, not proof over all API sequences. C1 tests a
  custom protocol rather than the toy: 20 seeds at 1, 2, 3, 7, 31 and 100 nodes, faults
  inside handlers and an invariant, zero-delay timers, requests, torn crashes,
  wipe/rejoin and final healing. Separate-process checks vary GOMAXPROCS/GOGC.
- Statistical attacks use 40 seeds and 1,000 trials each. At configured 30% loss or
  duplication, tolerance is 400 deliveries out of 40,000 (one percentage point,
  approximately 4.36 binomial standard deviations). Disabled faults require exactly
  40,000 deliveries. Tests check both request and response loss, including server
  execution count, not only caller timeouts. No independence between different attack
  samples is assumed; identical stream draws explain identical counts across modes.
- FIFO checks use synchronous server replies: asynchronous replies can be emitted in
  a different order by the protocol itself. Such application-level reply ordering is
  not misclassified as transport reordering. All zero-latency configurations explicitly
  set size limits, avoiding the all-zero NetConfig's default-latency behavior.
- Partition matrices test all directed datagram paths, including same-node paths,
  under one-way, region, isolate and symmetric faults. Request loss in each direction
  and pause/clog compositions are tested separately. A partition is judged at send time
  per the contract; an already-in-flight packet arriving after partition is not a leak.
  HealNet tests use zero configured random drop: HealNet clears topology faults, not
  the static NetConfig's drop setting.
- Empty queues are failures when waiting for convergence. Quiet termination itself is
  valid per PLAN 2.1, so an empty `RunUntilQuiet` is not called a defect. Livelocks tested
  are event-producing ones; preempting an infinite loop *inside* a callback is outside
  this single-threaded simulator's documented execution model.
- Disk testing covers silent bit flips, durable marks through unsynced mutations,
  synced repair, whole/named read faults, wipes, torn prefixes including first append,
  ungrouped metadata, and 15,000 mixed budget-checked steps. It is not an exhaustive
  disk state-space proof. Group budgets were checked after permitted faults and after
  repair/crash/heal operations; manual injections that intentionally ignore budgets
  were not labeled failures of budget enforcement.
- Very large node counts, every possible fuzz schedule, arbitrary callback nontermination,
  and cross-architecture replay were not tested: finite runtime and memory, and this
  task's single Docker platform, bound the campaign. Extreme latency/skew/duration
  probes address arithmetic limits without waiting in real time.
- The built-in CLI has no exposed failing scenario, so direct failure reproduction
  requires the documented temporary registration fixture. CLI runner/help bytes are
  copied unchanged. simtest's printed command is executed as printed from the module
  root; environment and source remain identical, and both digests and failure match.
- The mandated knowledge lookup was attempted before environment work. `kb search`
  and `kb --global search` both failed because the CLI is unconfigured, before issuing
  any search ID. No search feedback could be submitted and no store was initialized.
  Project-root `STATE.md` is stale relative to git; this task's explicit write boundary
  forbids correcting it. The local [STATE.md](STATE.md) records this handoff instead.

## Executed attacks by claim

Each line below records the attack and its observed result. Numerical checks are exact
unless the statistical tolerance above applies.

Latest complete execution: `SUMMARY held=52 broken=8 setup_errors=0 elapsed_seconds=17`. Exit status: **1**, expected for observed broken claims.

### C1

- HELD C1 custom-replay: 120 custom callback/timer/invariant/restart/wipe schedules replayed twice with equal logs and digests
- HELD C1 separate-process-runtime: 120 custom fingerprints equal across three fresh processes with varied GOMAXPROCS and GOGC
- HELD C1 equal-time-timer-storm: six runs of 10000 timers at one instant gave identical digests

### C2

- HELD C2 stream-loss-rate-request: 40 seeds x 1000 requests success=27905 timeout=12095 server deliveries=27905 within 400 of expected 28000 successes
- HELD C2 stream-loss-rate-response: 40 seeds x 1000 requests success=27905 timeout=12095 server deliveries=40000 within 400 of expected 28000 successes
- HELD C2 rate-drop: 40 seeds x 1000 sends delivered=27905 expected=28000 tolerance=400 deliveries
- HELD C2 rate-duplicate: 40 seeds x 1000 sends delivered=52095 expected=52000 tolerance=400 deliveries
- HELD C2 rate-link-loss: 40 seeds x 1000 sends delivered=27905 expected=28000 tolerance=400 deliveries
- HELD C2 rate-off: 40 seeds x 1000 sends delivered=40000 expected=40000 tolerance=0 deliveries
- HELD C2 delay-reorder: 200 deliveries stayed within 10..100ns and included reordered datagrams
- HELD C2 stream-fifo-no-duplicates: 50 seeds x 200 requests and replies stayed FIFO despite 100% datagram drop and duplication
- HELD C2 lost-request: seed=23 produced one deferred callback with the expected error and server-delivery status
- HELD C2 lost-response: seed=23 produced one deferred callback with the expected error and server-delivery status
- HELD C2 refused: seed=23 produced one deferred callback with the expected error and server-delivery status
- HELD C2 no-reply: seed=23 produced one deferred callback with the expected error and server-delivery status
- HELD C2 oversize-request: seed=23 produced one deferred callback with the expected error and server-delivery status
- HELD C2 oversize-reply: seed=23 produced one deferred callback with the expected error and server-delivery status
- HELD C2 invalid-timeout: seed=23 produced one deferred callback with the expected error and server-delivery status
- HELD C2 datagram-size-boundary: 64-byte datagram delivered and 65-byte datagram rejected
- BROKEN C2 huge-latency-range: seed=1 valid latency [0,MaxInt64] send failed: sim: seed 1: panic in action send at node "" t=0: invalid argument to Int64N

### C3

- HELD C3 one-way-heal: all 16 directed paths matched the partition matrix and all 16 post-heal datagrams arrived
- HELD C3 region-heal: all 16 directed paths matched the partition matrix and all 16 post-heal datagrams arrived
- HELD C3 isolate-heal: all 16 directed paths matched the partition matrix and all 16 post-heal datagrams arrived
- HELD C3 symmetric-heal: all 16 directed paths matched the partition matrix and all 16 post-heal datagrams arrived
- HELD C3 clog-unclog-resume: 50 seeds held traffic and released requests and replies in order through pause/clog composition
- HELD C3 clog-resume-unclog: 50 seeds held traffic and released requests and replies in order through pause/clog composition
- HELD C3 clog-heal-all: 50 seeds held traffic and released requests and replies in order through pause/clog composition

### C4

- BROKEN C4 whole-read-overflow-budget: seed=1 FailReads(0,empty,MaxInt) then +1: Read=<nil> and zero-damage Budget.Check=<nil>
- HELD C4 group-budget-read-faults: whole/named read errors, unnamed rot and wipe were rejected beside an existing damaged shard; ungrouped metadata allowed
- HELD C4 silent-single-bit-rot: 200 seeds silently flipped exactly one durable bit and marked the blob
- HELD C4 crash-durability-repair: crash discarded unsynced write/append/delete/new blob while preserving synced rot and its mark until repair sync
- HELD C4 torn-prefix: 200 seeds retained only append prefixes, covering all nine tail lengths and first-append creation
- HELD C4 wipe-and-read-errors: exactly two injected read errors then success; wipe removed both layers and marked two lost blobs
- HELD C4 budget-fault-sequences: 15000 mixed fault/repair/crash/heal steps stayed within checked group and disk budgets
- BROKEN C4 read-count-overflow: seed=1 FailReads(0,x,MaxInt), FailReads(0,x,1), Read(x) returned <nil> instead of IO or overflow rejection

### C5

- HELD C5 named-stream-draws: 600000 comparisons unchanged by draws on an unrelated stream
- HELD C5 node-stream-isolation: 1000 extra node-0 draws did not shift node-1
- HELD C5 network-nemesis-static: 50 bit-rot schedules unchanged by network drop changes with identical blob inventories
- BROKEN C5 network-nemesis-dynamic-blobs: seed=1 DropPPM 0 vs 1000000 with OnDatagram writing received changed nemesis step 4: {12ns bit-rot 1 "received" applied} versus {12ns bit-rot 1 "base" applied}

### C6

- BROKEN C6 nemesis-callback-panic: seed=43 nemesis bit-rot Budget.GroupOf panic escaped RunUntilQuiet=group-probe; Sim.Err=<nil>
- HELD C6 empty-stall: seed=37 returned the expected sticky stall with seed before the 200-step cap
- HELD C6 deadline-stall: seed=37 returned the expected sticky stall with seed before the 200-step cap
- HELD C6 zero-timer-stall: seed=37 returned the expected sticky stall with seed before the 200-step cap
- HELD C6 self-send-stall: seed=37 returned the expected sticky stall with seed before the 200-step cap
- HELD C6 actions-stall: seed=37 returned the expected sticky stall with seed before the 200-step cap
- HELD C6 panic-factory: seed=41 panic became a sticky PanicError naming the seed
- HELD C6 panic-start: seed=41 panic became a sticky PanicError naming the seed
- HELD C6 panic-handler: seed=41 panic became a sticky PanicError naming the seed
- HELD C6 panic-timer: seed=41 panic became a sticky PanicError naming the seed
- HELD C6 panic-action: seed=41 panic became a sticky PanicError naming the seed
- HELD C6 panic-invariant: seed=41 panic became a sticky PanicError naming the seed
- HELD C6 panic-predicate: seed=41 panic became a sticky PanicError naming the seed
- HELD C6 panic-encode: seed=41 panic became a sticky PanicError naming the seed
- BROKEN C6 clock-overflow: seed=1 skew=MaxInt64 RunUntil(0)->RunUntil(1) local clock went from 9223372036854775807 to -9223372036854775808 with no error
- BROKEN C6 timer-duration-clamp: seed=1 AfterFunc((1<<62)+1) fired at 4611686018427387904 one ns early
- HELD C6 global-time-past-events: past events and negative RunFor did not move global time backwards

### C7

- HELD C7 simtest-report-fields: seed=23 deliberate panic report checked for seed, both digests, truncated tail, stack and replay command
- HELD C7 simtest-exact-replay: seed=23 executed the printed command verbatim from the module root and compared failure and both digests
- HELD C7 cli-failure-digests: seed=23 copied production CLI with a registered panic scenario exited 1 twice with matching digests
- BROKEN C7 cli-failure-report: seed=23 production run(-scenario adversarial-fail -seed 23) emitted log-tail=False REPLAY=False; see cli-original.txt and reporting.py fixture
