# PLAN — Decentralized Object Store & Local-First Sync Engine (Go)

Status: Phase 1 plan, awaiting Nick's sign-off. Nothing in Phase 2 starts before that.
Spec: [docs/project-source.md](docs/project-source.md). Current state: [STATE.md](STATE.md).
Section 9 lists the open questions; every recommendation marked **(OQ-n)** is a proposal,
not a decision.

Contents: 1 North Star · 2 Testing architecture · 3 Repo layout · 4 Chunk plan ·
5 Concurrency map · 6 Per-chunk pipeline · 7 Key technical decisions · 8 Risks ·
9 Open questions for Nick · Appendix A (research and probe evidence)

---

## 1. North Star

A leaderless, multi-region object store with a local-first metadata sync layer — a small S3
plus a small Figma-style sync — written in Go on the standard library alone, in which every
node can accept reads and writes without asking a leader or a quorum of a consensus group.
Membership comes from SWIM gossip, durability from Reed-Solomon striping, self-healing from
Merkle-tree anti-entropy, conflict-free metadata from CRDTs, and multi-step cluster changes
from compensating Sagas. The repository's claim is that these properties are **proven, not
asserted**: a deterministic simulator runs 20–100 nodes through partitions, packet loss,
bit-rot and crashes in milliseconds from one `int64` seed; property tests and fuzzers check
the algebra; the race detector and lifecycle tests check the real runtime; end-to-end tests
run real processes. A reader gets a commit history that works as a guided tour (one landing
per primitive, each message naming what its tests prove), an architecture write-up per
primitive that states the benefit, the trade-off accepted and the evidence, and a failing
seed they can replay with one printed command.

---

## 2. Testing architecture (governing section)

Everything in sections 3–7 follows from this section. A chunk is briefed from its tests:
the brief names what must be proven and with which test kind, the tests are written first,
and the code counts as done only when the landing gates in 2.9 are green.

### 2.1 The DST harness (`internal/sim`, built in Chunk 0)

**Shape.** Protocol code is written as single-threaded state machines that never touch a
socket, a wall clock, an OS file or a goroutine. They see the world through four interfaces
in `internal/env`: `Clock`, `Rand`, `Transport`, `Disk` (7.1). The simulator supplies one
implementation of each for every node and is the only thing that calls protocol code.
This is the "environment interface over a sans-IO core" design from the DST canvas on this
machine (Appendix A): the cheapest design that gives exact replay, and it needs no source
rewriting or runtime tricks.

**Seeded PRNG discipline.**
- One root `int64` seed per run. Nothing else is an input.
- Generator: `math/rand/v2` `*rand.Rand` over `rand.NewPCG` (**OQ-3**: the spec says
  `*math/rand.Rand`; v2's PCG is a specified algorithm with a stable stream, and its type is
  still `*rand.Rand`).
- Named streams: the root seed forks one independent stream per purpose, keyed by a label —
  `net`, `disk`, `nemesis`, `workload`, and `node/<id>` for each node's own protocol
  randomness. Adding a draw to the network model does not shift disk faults or node choices.
  Derivation: `NewPCG(uint64(seed), fnv64a(label))`.
- No other entropy: no `time.Now`, no global `rand.*`, no `crypto/rand`, no ranging over a
  map where order can reach an output, no floats in decisions, no goroutines. Enforced two
  ways: `detlint` (2.9, a static check over the sim-reachable packages) and the
  cross-process replay test below, which catches what a lint cannot. The Phase 1 probe
  showed a single map iteration gives 6 different digests in 6 runs, and that the replay
  check sees it.

**Virtual clock.** Time is an `int64` count of virtual nanoseconds from a fixed epoch. The
clock never moves on its own: the loop pops the next event and jumps the clock to its
timestamp. Timers are events. Per-node skew and drift are supported (a node's `Clock.Now`
is the global time passed through that node's offset and rate), because LWW metadata is
only as good as the clocks under it (Chunk 4 needs this).

**Single-threaded discrete-event loop.** One priority queue ordered by `(time, seq)`, where
`seq` is a global insertion counter, so equal-time events run in FIFO order and the order is
total. One event runs to completion before the next is popped. Event kinds: message
delivery, timer fire, disk completion, fault action, workload action, invariant check. The
loop offers `Step`, `RunUntil(t)`, `RunUntilQuiet`, and a step budget; "queue empty and
system not converged" and "budget exhausted" are reported as stall failures, not hangs.

**Event log and state digest.** Every executed event is appended to a canonical binary log
(time, seq, kind, node, peer, payload hash). A run's fingerprint is `SHA-256(event log)`
plus `SHA-256(canonical encoding of every node's state and disk)`. "Bit-identical" in this
plan always means these two digests.

**Fault-injecting network.** Messages cross the simulated network as encoded bytes, never
as Go pointers, so wire encoding is exercised in every simulation and two nodes can never
share memory. Two delivery classes, matching the two real transports (7.1):

| Class | Real transport | Faults the simulator injects |
|---|---|---|
| datagram | UDP | drop, duplicate, delay and reorder, size limit (oversize is dropped) |
| request/response | TCP | delay, lost request, lost response (caller cannot tell which), connection refused, timeout; never duplicated or reordered within a connection |

Topology faults apply to both classes: symmetric partition, **asymmetric** partition (A→B
dropped while B→A flows), per-link loss rate, region-level partition (nodes carry a region
label), isolate-one-node, and "clog" (hold a link's traffic and release it later). All
rolls come from the `net` stream.

**Fault-injecting disk.** An in-memory blob store per node with two layers, *written* and
*durable*; `Sync` moves written to durable. Faults, all rolled from the `disk` stream:
- **bit-rot**: flip bits in a durable blob silently — the next read returns the bad bytes
  with no error;
- **wipe**: the node loses its whole disk;
- **crash**: unsynced writes are lost; optionally a torn tail (a prefix of the last unsynced
  append survives), which the Saga log must tolerate;
- **read error**: a read returns an I/O error.

**Process faults.** Crash (volatile state gone, durable disk kept), restart, pause (the node
stops while time passes), wipe-and-rejoin.

**Nemesis and fault budget.** A nemesis is a schedule of fault actions drawn from the
`nemesis` stream, recorded in the event log as data. It alternates chaos phases with healing
phases. It works inside a **fault budget**: by default it never damages more than `m` shards
of one stripe at once, never leaves fewer than the configured minimum of live nodes, and so
on, so that "the system is stuck" is a real failure rather than an expected outcome. Tests of
the failure boundary lift the budget on purpose. After the last chaos phase the nemesis
heals everything and the test requires convergence within a stated bound of virtual time
(liveness).

**Invariant checking.** Safety invariants are functions over the whole cluster, run after
every event (or every N events for expensive ones, with N in the test). Each primitive
registers its own (2.2).

**Deterministic replay meta-tests (spec Commit 0 verification).**
1. `TestReplaySameProcess`: the same chaos schedule twice with the same seed in one process
   gives identical event-log and state digests.
2. `TestReplayAcrossProcesses`: the test binary re-executes itself twice and compares
   digests. This is the one that catches map order and anything keyed off per-process state.
3. `TestGoldenDigests`: digests for a few pinned seeds are stored in `testdata/`. A change
   to the simulator that shifts the schedule must update them in the same commit, on
   purpose, with the reason in the commit message.
4. `TestDifferentSeedsDiffer`: a sanity check that the seed actually drives the run.
5. **Canaries (testing the tester):** planted bugs the harness must catch — an unseeded
   draw, a map-order dependency, a dropped invariant. Each later chunk adds one or two
   planted protocol bugs (for example SWIM with indirect probes switched off) that its DST
   must fail on. A test suite that cannot fail proves nothing.
6. Every DST scenario in every later chunk runs through `simtest.RequireDeterministic`,
   which runs it twice and compares digests before checking anything else.

**Seed handling.**
- `internal/sim/simtest` registers a `-seed` flag in every test package that imports it,
  and also reads `DST_SEED`. The flag is for replaying one package; the variable works with
  `go test ./...` (a flag unknown to some package would fail there).
- `DST_SEEDS=N` sets how many seeds a scenario sweeps; `DST_SEED_BASE` sets where the sweep
  starts. Plain `go test` uses a fixed base and a small N, so the default run is fully
  deterministic and never flaky. The landing gate sets the base from the commit SHA and
  N=500 (same commit, same seeds). The hardening phase uses fresh bases and large N.
- Every failing simulation test prints, as its last lines, the seed, the two digests, the
  tail of the event log, and the exact command:
  `REPLAY: go test ./internal/gossip -run '^TestSWIMConvergenceDST$' -seed=<n> -count=1`
- With `DST_ARTIFACTS=<dir>` the full event log of a failing seed is written to a file.
- Every seed that ever failed is appended to that package's `testdata/seeds.txt` with a
  one-line note and runs forever after as a regression ("append, never delete"). It is
  also recorded in the chunk's work notes (`agent_work/<chunk>/`).
- A standalone runner, `cmd/objsim -scenario=<name> -seed=<int64>`, runs any named scenario
  outside `go test` and prints the digests — the spec's "driven by `-seed=<int64>`".

### 2.2 Property tests

**Laws and invariants per primitive.**

| Primitive | Properties (each is a named test) |
|---|---|
| GF(2^8) | field axioms: associativity, commutativity, distributivity, identities, every non-zero element has an inverse, `a·a⁻¹ = 1`; exp/log tables agree with bitwise multiplication (exhaustive, 65,536 pairs) |
| Reed-Solomon | **round-trip**: for any payload, any valid `(k, m)`, any erasure set of size ≤ m, decode returns the exact bytes; **MDS**: every `k×k` submatrix of the coding matrix is invertible (exhaustive for small `k+m`, sampled above); **failure boundary**: more than `m` losses returns `ErrTooFewShards`, never a panic and never wrong bytes; **corruption**: up to `m` corrupted shards are detected by checksum and treated as erasures (7.3); systematic: the first `k` shards are the data |
| Merkle | **completeness**: diff returns exactly the keys whose hashes differ or that exist on one side (zero missed, zero spurious); **efficiency**: hashes exchanged ≤ `c·d·log N` for `d` differing keys, and round trips ≤ tree depth; root equality ⇔ map equality; incremental update equals rebuild from scratch; insertion order does not change the root |
| Gossip | **convergence**: after faults stop, all live nodes hold identical membership views within `c·log₂N` protocol periods; incarnation numbers never decrease; a node never declares itself dead; no eviction under a single asymmetric link; a crashed node is declared dead by all within a bound; a dead verdict is only overturned by a higher incarnation |
| CRDTs | commutativity, associativity, idempotency of `Merge` for each type; `Merge` is an upper bound (`a ≤ a⊔b`); every operation is an inflation; **strong eventual consistency**: any two replicas that have received the same set of updates, in any order, with any duplication, encode to identical bytes; PN-Counter value equals increments minus decrements with no double counting; OR-Set add-wins; LWW total order with deterministic tie-break; encode/decode round-trip |
| Saga | **terminal-state invariant**: every saga ends Completed (all forward effects present) or Compensated (no forward effect remains), for every failure point; recovery from any log prefix is idempotent (recovering twice equals once); compensations run in reverse order of completed steps; no step runs after the saga reaches a terminal state |
| Simulator | replay (2.1); a healed network delivers every message sent after the heal; virtual time never goes backwards |

**Generator design — one generator, three drivers (stdlib only, OQ-4).** A small in-repo
package, `internal/prop` (built in Chunk 0, about 300 lines), gives generators a byte tape:
`prop.Check(t, func(g *prop.G) { ... g.IntN(n) ... g.Bytes(max) ... })`. Values are drawn
from the tape, so the same property function runs under three drivers:
1. **random**: the tape is filled from a seeded PCG stream (`PROP_SEED`, `PROP_RUNS`);
2. **fuzz**: the tape is the `[]byte` input of a `testing.F` target, so every property is
   also a fuzz target with no extra code, and Go's coverage guidance searches it;
3. **replay**: a saved tape reproduces a failure exactly.

Generators are built for the hard cases, not uniform noise: payload sizes are drawn from
`{0, 1, primes, k−1, k, k+1, shard-size boundaries, large}`; CRDT states are generated by
running random operation sequences on several replicas and merging some of them (so the
states are reachable ones) and separately as arbitrary decoded bytes (so `Merge` is also
checked on states no honest replica would produce); Merkle maps are generated as a base map
plus a mutation script of inserts, deletes and bit flips.

**How failures shrink or replay.** On failure `prop` shrinks the tape — delete spans, zero
bytes, lower bytes — re-running the property each time and keeping any change that still
fails (the internal-reduction technique, which shrinks every generator for free). It then
prints the minimal counterexample, the seed, and
`REPLAY: PROP_SEED=<n> go test ./internal/crdt -run '^TestORSetLaws$' -count=1`, and writes
the shrunk tape to `testdata/prop/<Test>/<hash>`; saved tapes run first on every later run.
Under the fuzz driver, Go's own minimizer and `testdata/fuzz/` do the same job. `prop` has
its own meta-test: a planted failing property must shrink to a known minimal case.

Stateful tests (the simulator, the Saga engine, the chunk store) use the same tape to
generate operation sequences against a simple reference model and compare after every step.

### 2.3 Fuzz targets (Go native fuzzing, `testing.F`)

| Target | Package | Asserts |
|---|---|---|
| `FuzzGF256` | erasure | field laws on arbitrary bytes |
| `FuzzErasureCoding` (spec) | erasure | round-trip over arbitrary payload, `(k, m)`, loss and corruption sets; clean error beyond `m`; no panic |
| `FuzzShardDecode` | storage | arbitrary bytes as a shard file: rejected or accepted, never a panic, never a checksum bypass |
| `FuzzMerkleDiff` (spec) | merkle | completeness and the hash-count bound over arbitrary maps and mutation scripts |
| `FuzzMerkleWire` | merkle | arbitrary bytes as diff-protocol messages never panic or loop |
| `FuzzCRDTLaws` (spec) | crdt | the three semilattice laws for all four types on arbitrary states A, B, C |
| `FuzzCRDTDecode` | crdt | decode never panics; decode∘encode is the identity; merging a decoded hostile state keeps the laws |
| `FuzzGossipWire` | gossip | decode never panics; round-trip; an arbitrary inbound packet never breaks a node's invariants |
| `FuzzSagaLogRecover` | saga | arbitrary, truncated or corrupted log bytes: recovery returns a valid prefix or a typed error, never a panic |
| `FuzzSimSchedule` | sim | fuzz bytes become a fault schedule; simulator invariants and replay hold |
| `FuzzFrame` | node | the TCP frame decoder on arbitrary bytes |
| `FuzzObjectAPI` | storage | arbitrary method, path, headers and body against the HTTP handler: a valid status, no panic |
| `FuzzClusterNemesis` | test/e2e | fuzz bytes become a whole-cluster nemesis schedule; safety invariants hold, convergence after heal (Chunk 7) |

**Seed-corpus policy.** Each target adds boundary cases with `f.Add` in code (empty, one
byte, prime lengths, `k=1`, `m=0`, maximum `k+m`, all-parity loss, and so on). Every crasher
the fuzzer finds is committed under `testdata/fuzz/<Target>/` and is never deleted; it runs
as an ordinary test from then on. The fuzzer's generated cache is not committed. Multi-MB
payloads are covered by property and table tests (a length drawn from the tape, content
from a PRNG), because byte-level fuzzing of megabytes is slow; fuzz inputs are capped at
64 KiB.

**Run budgets.** Go runs one fuzz target per invocation, so `agent_work/gates/fuzz-short.sh`
loops over every target.
- Per-chunk landing gate: 30 s per target touched by the chunk, `-fuzzminimizetime 5s`
  (the default minimize time looks like a hang — a finding already in the machine's kb).
- Chunk 7 hardening: 15 minutes per target, 60 minutes each for the three spec-named
  targets and `FuzzClusterNemesis`, run in parallel across cores; results and any new
  corpus entries recorded per target (**OQ-9** for the budget).

### 2.4 Unit tests

- Table-driven with `t.Run` subtests; one table per function or behaviour; row names read
  as sentences.
- Black-box by default (`package x_test`); white-box only where an unexported invariant is
  the point of the test.
- No sleeps and no wall clock. Time comes from the simulator or a `testing/synctest` bubble.
- Golden vectors where the math has known answers: GF(2^8) products for the chosen
  polynomial, SHA-256 trees for small fixed inputs, wire encodings (a golden file pins each
  wire format so an accidental change is visible).
- **"Enough" per package:** every exported function and method has a test; every sentinel
  and typed error has a test that produces it and checks it with `errors.Is`/`errors.As`;
  every decoder has a malformed-input table; boundary rows for zero, one, maximum and
  maximum-plus-one of each parameter; statement coverage reported per package with a floor
  of 85% on the five primitive packages and `internal/sim`, and any uncovered branch named
  in the chunk's build notes with the reason. Coverage is a floor that flags gaps, not the
  measure of done — the properties are.

### 2.5 Concurrency validation

**Where concurrency is allowed.** The five primitive packages, `internal/sim` and
`internal/prop` contain no `go` statement, no channel, no mutex and no `sync/atomic`
(`detlint` enforces it). They are single-threaded by construction, which is why the
simulator can prove things about them. Goroutines exist only in the real-runtime layer:
`internal/node` (socket readers, the node's event loop, timers), `internal/storage`'s HTTP
server, and `cmd/`.

**Ownership and lifecycle rules for that layer.**
1. Every goroutine has one owner, started through a `sync.WaitGroup` (`wg.Go`) the owner
   holds, with a `context.Context` it must obey.
2. `Close`/`Shutdown(ctx)` returns only after every owned goroutine has exited. Nothing
   starts a goroutine without a matching stop.
3. All protocol state belongs to one goroutine, the node loop. Other goroutines talk to it
   through its inbound channel. No protocol state is shared under a lock.
4. A channel is closed by its single sender-side owner only.
5. Blocking calls take a context; no unbounded queue — inbound queues are bounded and
   overflow drops datagrams (counted), which is what UDP does anyway.

**How tests prove them.**
- `go test -race ./...` is a hard landing gate for every chunk, and the node, storage and
  e2e packages also run `-race -count=3`.
- Lifecycle tests run in `testing/synctest` bubbles: virtual time makes them instant, and
  the bubble fails the test if a goroutine is still blocked at the end — leak detection
  without a dependency (probe: an hour of ticks runs in about a second under `-race`).
- Tests on real sockets use a small stdlib leak check (`internal/testutil/leakcheck`:
  goroutine stacks before and after, with a settle loop) in `TestMain`.
- Context cancellation tests: cancel in the middle of put, get, repair and migration, and
  during startup; assert the call returns an error matching `context.Canceled` or
  `DeadlineExceeded`, that all goroutines exit, and that any partial state left behind is
  the kind anti-entropy or shard garbage collection removes (asserted, not assumed).
- Shutdown tests: shutdown under load, double `Close`, `Close` during startup, use after
  `Close` returns `ErrClosed`.
- **Runtime conformance:** a small set of protocol scenarios runs twice, once on the
  simulator and once on the real loopback runtime, to show the same code runs on both. The
  real run asserts only eventual properties with generous timeouts.

### 2.6 Error handling, per layer

**Rules that apply everywhere.**
- Sentinel errors (`var ErrX = errors.New(...)`) for conditions a caller tests for; typed
  errors (structs with `Unwrap`) where a caller needs fields to branch; callers use
  `errors.Is`/`errors.As` only — never string matching.
- Wrap with `%w` and context naming the operation and the object:
  `fmt.Errorf("erasure: decode stripe %d: %w", i, err)`. One wrap per layer crossing.
- Several independent failures (for example four peers refusing a shard) are combined with
  `errors.Join`, so each cause stays reachable with `errors.Is`.
- **No panics in library packages.** A panic is allowed only for a violated internal
  invariant (a bug). Every `panic(` under `internal/` must appear in
  `agent_work/gates/panic-allowlist.txt` with its justification, and the gate fails
  otherwise. At the node boundary the real runtime recovers, logs, and stops that node. In
  the simulator a panic is never recovered silently: it fails the test and prints the seed.
- Errors are never dropped. An ignored error needs an explicit `_ =` and a line in the
  build notes; `go vet` plus a small check in the gate script looks for bare calls to
  functions returning `error` in the primitive packages.

**Per layer.**

| Layer | Sentinels / typed errors | Notes |
|---|---|---|
| `erasure` | `ErrInvalidConfig`, `ErrTooFewShards`, `ErrShardSize`; `*ShardError{Index, Err}` | decoding is total: any input gives bytes or one of these |
| `storage` (chunk store) | `ErrNotFound`, `ErrCorrupt`, `ErrExists`; `*ObjectError{Op, Bucket, Key, Err}` | a checksum mismatch is `ErrCorrupt`, never silently returned bytes |
| `merkle` | `ErrMalformed`, `ErrDepth`; peer failures surface as transport errors | a failed round is abandoned whole; the next round is the retry |
| `crdt` | `ErrMalformed` (decode only) | `Merge` has no error return — it is total by construction, and that is a tested property |
| `gossip` | `ErrMalformed`, `ErrNotMember`, `ErrClosed` | a bad packet is counted and dropped, never fatal |
| `saga` | `*StepError{Saga, Step, Attempt, Err}`, `ErrPermanent`, `ErrSagaNotFound`, `ErrLogCorrupt` | steps classify failures as retryable or permanent; only the engine retries |
| object API | maps the above to HTTP status in one table (`ErrNotFound`→404, `ErrUnavailable`→503, `ErrCorrupt`→500 with repair triggered, quota→507 …) | the table is tested row by row |
| `env`/transport | `ErrTimeout`, `ErrUnreachable`, `ErrClosed`, `ErrTooLarge` | the same sentinels from the simulated and the real transport |

**Crossing the network boundary.** Go error values never cross. A response carries a
`uint8` status code and a short message. One table maps sentinel → code on the way out and
code → sentinel on the way in; a test round-trips every row
(`errors.Is(fromWire(toWire(err)), sentinel)`); an unknown code maps to `ErrRemote`. A
timeout is created by the caller from its own clock and means "outcome unknown" — the
request may have been applied — so every remote operation is idempotent, and that is tested
by having the simulator deliver requests twice and drop responses.

**Where retry and compensation are allowed.**
- Allowed: the Saga engine retrying a retryable step with bounded attempts and backoff, and
  retrying compensations until they succeed; a reader fetching a different shard when one
  is missing or corrupt; anti-entropy, whose next periodic round is its retry; a SWIM
  indirect probe after a direct probe fails.
- Required of every retry: the delay comes from `env.Clock` and any jitter from `env.Rand`;
  the attempt is a visible event in the simulator's log; the attempt count is bounded or
  the loop is a named periodic protocol round.
- Forbidden: retry loops inside protocol handlers that swallow the error; retries timed by
  the wall clock or unseeded jitter; "try again until it works" in tests; anything that
  would make two runs of one seed differ.

**How tests assert error behaviour.** Tables carry `wantErr` (checked with `errors.Is`) or
`wantErrAs` (checked with `errors.As` and field comparison). Every error path in the table
above has at least one test that reaches it through the public API. Fuzz targets assert
"an error or a correct result, never a panic". DST scenarios assert what the client sees
during chaos — for example that a GET with more than `m` shards unreachable returns
`ErrUnavailable` rather than hanging or returning wrong bytes.

### 2.7 End-to-end tests (designed now, built in Chunk 7)

E2E tests use the system's real packages only — no mocks of this repo's own code. Two tiers
share one scenario definition where they can:
- **Tier S (simulated cluster, deterministic):** full nodes with every primitive wired, 20
  to 100 nodes in 3 regions, clients calling the object API over the simulated network.
- **Tier R (real processes):** `cmd/objnode` built and started as 6–9 OS processes in 3
  region groups on loopback, driven over HTTP; real UDP, TCP and files. Crashes are
  `SIGKILL`; bit-rot is the harness flipping bytes in shard files; partitions use a peer
  blocklist in the transport that is compiled in only with the `e2efault` build tag.

| # | Scenario | Tier | Pass criteria |
|---|---|---|---|
| E1 | Multi-region put/get | S, R | objects of 0 B, 1 B, prime sizes and multi-MB written through a node in each region are read back byte-identical through every other node; shards are spread so no region holds more than `m` of a stripe |
| E2 | Partition and heal | S, R | during a region partition both sides keep accepting metadata writes and any object write/read the surviving shards allow; anything else returns `ErrUnavailable`, never wrong bytes; after heal all nodes reach identical membership, identical CRDT bytes and identical Merkle roots within the stated bound; no acknowledged write is lost |
| E3 | Bit-rot detection and repair | S, R | with up to `m` shards per stripe corrupted on disk, every GET still returns correct bytes; background anti-entropy restores every corrupted shard within the bound; bytes moved for repair are proportional to the damage, not the bucket |
| E4 | Bucket migration, success | S, R | bucket moves from region A to B under live traffic; ends with verified equal Merkle roots, routing pointer on B at every node, zero shards of the bucket left in A, every object readable |
| E5 | Bucket migration, rollback | S (every failure point), R (kill target mid-copy, kill coordinator mid-saga) | the saga ends Compensated: pointer still A, zero orphan shards in B, source intact; a restarted coordinator resumes from its log and reaches the same end |
| E6 | Node crash and restart | S, R | `SIGKILL` during writes: acknowledged objects survive; the restarted node rejoins with a higher incarnation, catches up by anti-entropy, and serves reads; a wiped node is refilled |
| E7 | Nemesis and healing cycles | S (long, many seeds) | alternating chaos and heal for many cycles: safety invariants hold at every step, availability holds within the fault budget, full convergence after each heal; seeds swept in the hundreds per gate and far more in hardening |
| E8 | Local-first metadata sync | S, R | two regions (and a client replica, **OQ-7**) edit tags, ACL sets, quotas and object metadata while disconnected; after reconnect all replicas hold identical bytes and every concurrent add is present |
| E9 | Shutdown and cancellation | R | SIGTERM under load: in-flight requests finish or fail cleanly, the process exits within the deadline with no goroutine left, restart is clean |

Tier S scenarios run under `RequireDeterministic`. Tier R runs under `-race` builds of the
binary and is kept small enough to finish in a few minutes.

### 2.8 What each test kind is for

| Question | Answered by |
|---|---|
| Is the algebra right? | property tests and fuzzing |
| Does the protocol hold up under faults and bad timing? | DST, many seeds, invariants each step |
| Is the edge-case behaviour and the error contract right? | table-driven unit tests |
| Does the real runtime manage goroutines safely? | `-race`, synctest lifecycle tests, leak checks |
| Does it all work as one system? | E2E tiers S and R |
| Are the claims stronger than the tests? | Astra's adversarial pass; broken claims are fixed or narrowed in writing |

### 2.9 Landing gates per chunk (ordered; all must be green to merge)

`agent_work/gates/gate.sh <chunk>` runs G0–G6 and writes a machine-readable result file to
`agent_work/<chunk>/gates/`. Each gate script has a canary proving it fails on a planted
violation.

| # | Gate | Command / check |
|---|---|---|
| G0 | Hygiene | `gofmt -l` empty; `go vet ./...`; `go build ./...`; `detlint` (banned calls and constructs in sim-reachable packages); panic allowlist; **zero-dependency check** — root `go.mod` has no `require`, `go list -deps ./...` is standard library plus this module |
| G1 | Unit | `go test ./...` (fixed seeds); per-package coverage report against the floor |
| G2 | Property | `PROP_RUNS=2000 go test -run 'Laws|Prop' ./...`; saved tapes replay first |
| G3 | Fuzz, short | 30 s per target the chunk touches; committed corpus replays |
| G4 | Race | `go test -race ./...`; `-count=3` on node, storage, e2e |
| G5 | DST seeds | pinned seeds + 500 seeds based on the commit SHA per scenario; replay meta-tests; planted-bug canaries fail as required |
| G6 | Portability | `GOOS=darwin GOARCH=arm64 go build ./... && go vet ./...` locally (7.6) |
| G7 | Adversarial | Astra's harnesses run by the orchestrator; `RESULTS.md` records held/broken per claim; every broken claim is repaired (and re-run) or narrowed in writing |
| G8 | Review | Neon verdict PASS, or NITS with each nit closed |
| G9 | Prose | Neon prose pass and write-up done; comment-only-diff check (the Go AST is unchanged by the prose commit); prose-provenance check (2.10); self-containment check (no `/root/`, no brief or machine references in reader-facing files); `site-lint`; G0–G6 re-run on the final tree |
| G10 | Acceptance | orchestrator reads the diff, the evidence and the write-up against the brief; then merge, push, verify the Pages deploy serves the write-up |

### 2.10 The prose rule, made checkable

Opus writes Go with **no comments** other than machine directives (`//go:build`,
`//go:embed`, `// Output:` in examples). A check at the end of the build stage enforces it.
Opus explains intent in `agent_work/<chunk>/build-notes.md` (operational, not shipped as
reader copy), which Neon uses as source material. Every comment in the final tree and every
reader-facing markdown file is then written in the Neon prose stage, in commits carrying a
`Prose-By: Gemini 4 Neon` trailer. The provenance check verifies that (a) the pre-prose
tree had no comments, (b) the prose commits change only comments and reader-facing
markdown, and (c) no later commit touches either without the trailer. Identifier names,
error strings, test names and log messages are code and are Opus's (**OQ-8**).

---

## 3. Repo layout

| Path | Purpose |
|---|---|
| `PLAN.md`, `STATE.md`, `README.md` | this plan; current state and resume instructions; reader-facing front page (Neon) |
| `go.mod` | module (**OQ-1**), `go 1.26`, no `require` lines |
| `internal/env/` | the four interfaces protocol code sees: `Clock`, `Rand`, `Transport`, `Disk`; shared sentinels (addition to the spec's layout — reason in 7.1) |
| `internal/sim/` | discrete-event simulator: PRNG streams, virtual clock, event loop, fault-injecting network and disk, nemesis, event log, digests |
| `internal/sim/simtest/` | test helpers: `-seed`/`DST_SEED`, seed sweeps, `RequireDeterministic`, replay-command printing, cluster builders |
| `internal/prop/` | stdlib-only property-testing helper: tape generators, shrinking, replay (addition; 2.2) |
| `internal/gossip/` | SWIM membership: probe, indirect probe, suspicion state machine, piggybacked dissemination |
| `internal/erasure/` | GF(2^8) arithmetic and the Reed-Solomon encoder/decoder |
| `internal/merkle/` | Merkle tree builder, diff protocol, anti-entropy repair worker |
| `internal/crdt/` | state-based CRDTs: PN-Counter, OR-Set, LWW-Register, LWW-Element-Set; hybrid logical clock; replication of CRDT state |
| `internal/saga/` | durable Saga orchestrator, log, recovery; the bucket-migration workflow definition |
| `internal/storage/` | local chunk store, shard placement, object put/get paths, HTTP object API |
| `internal/node/` | wiring of all primitives into one node; the real runtime (UDP/TCP transports, file disk, wall clock, node loop) (addition; 7.1) |
| `internal/testutil/` | goroutine leak check and other test-only helpers |
| `cmd/objnode/` | CLI entry point for a storage and sync peer |
| `cmd/objsim/` | run a named simulation scenario from a seed outside `go test` (addition; 2.1) |
| `test/e2e/` | end-to-end suite: tier S scenarios, tier R process harness, `FuzzClusterNemesis` |
| `docs/project-source.md` | the spec, as given |
| `docs/architecture/<slug>.md` | Neon's architecture write-up per paradigm; published |
| `docs/index.md`, `docs/_config.yml` | Pages site front page and configuration |
| `docs/knowledge/` | project knowledge entries (markdown, in git), one or more per landed chunk; indexed by the vendored kb |
| `kb/` | the kb tool vendored whole (a nested Go module with its own dependencies; outside the root module, so the zero-dependency check is unaffected) |
| `.kb/` | location file for the in-repo knowledge store |
| `.github/workflows/pages.yml` | builds `docs/` and deploys to GitHub Pages on every push to main |
| `.github/workflows/ci.yml` | `go test` on Linux and macOS runners (7.6) |
| `agent_work/INDEX.md` | index of every script: name, purpose, chunk, author seat, what it proved / last result |
| `agent_work/orch/` | orchestration scripts: launch, wait, seat probes, Neon/Astra runners, land, knowledge sync |
| `agent_work/gates/` | gate scripts (`gate.sh`, `fuzz-short.sh`, `detlint/`, prose and self-containment checks) and their canaries |
| `agent_work/probes/` | feasibility probes (Phase 1 probes are already here) |
| `agent_work/adversarial/<chunk>/` | Astra's harnesses and scripts with `RESULTS.md`; permanent, indexed in their own section; each harness is its own nested Go module or a bash script |
| `agent_work/<chunk>/` | per-chunk work notes: brief copy, build notes, reviews, gate results, seeds, route used for Neon |
| `agent_work/kb-sync.sh`, `kb-vendor.sha` | vendor the kb tool, rebuild the in-repo index, report drift |

---

## 4. Chunk plan

### 4.1 Coverage of the spec — nothing dropped silently

| Spec item | Chunk | Note |
|---|---|---|
| Guideline: zero external engine dependencies | all; gate G0 | checked mechanically at every landing |
| Guideline: deterministic chaos testing | 0, then every chunk | |
| Guideline: `-race` and property tests | 0 (tooling), every chunk (gates G2, G4) | |
| Structure: `internal/sim` | 0 | |
| Structure: `cmd/objnode` | 6 | |
| Structure: `internal/gossip` | 1 | |
| Structure: `internal/erasure` | 2 | |
| Structure: `internal/merkle` | 3 | |
| Structure: `internal/crdt` (LWW-Element-Set, OR-Set, PN-Counter, LWW-Register) | 4 | LWW-Element-Set is in the structure list but not in Commit 4's scope; **kept** — all four types are built |
| Structure: `internal/saga` | 5 | |
| Structure: `internal/storage` (chunk store + HTTP/RPC object API) | 2 (chunk store), 6 (object API) | split because the chunk store is needed by Chunks 2 and 3 and the API needs everything |
| Commit 0 + its replay meta-test | 0 | |
| Commit 1 + 20-node convergence tests | 1 | |
| Commit 2 + round-trip tests | 2; distribution across gossip-discovered peers finishes in 6 | Chunk 2 places shards on a membership list behind an interface; the real gossip view is wired in 6 (reason: lets 1 and 2 build concurrently) |
| Commit 3 + fault-injection tests | 3; applied to erasure-coded objects in 6 | same reason |
| Commit 4 + random-order property tests | 4; membership-driven peer choice wired in 6 | same reason |
| Commit 5 + failure-injection tests | 5 (engine + workflow against a model of the cluster); 6 (the same failure matrix against the real components) | the spec's terminal-state claim is only fully proven when Chunk 6 lands; Chunk 5's write-up says so and is updated in 6 |
| DST core: single-threaded simulation driven by `-seed` | 0 | |
| DST core: continuous nemesis and healing cycles | 0 (mechanism), 7 (whole-cluster cycles) | |
| DST core: mathematical law fuzzing | 2, 4 | |
| SWIM suite: convergence property; asymmetry resilience | 1 | |
| RS suite: `FuzzErasureCoding` round-trip; failure boundary | 2 | |
| Merkle suite: `FuzzMerkleDiff` completeness and efficiency; self-healing DST | 3 (replicated chunks); 6–7 (erasure-coded objects under traffic) | |
| CRDT suite: `FuzzCRDTLaws`; multi-region convergence DST | 4 | |
| Saga suite: exhaustive failure points; terminal-state invariant | 5, re-run on real components in 6 | |
| Mission: E2E as final build phase | 7 | |
| Mission: write-up per paradigm, Pages incremental | every chunk; scaffold in 0; finish in 8 | |
| Mission: kb vendoring and both stores | scaffold in 0; entry per chunk | |

Cut, with reasons: S3 wire compatibility and request signing (not in the spec; the API is
S3-like, path-style, unauthenticated — **OQ-6**); SWIM Lifeguard extensions (not in the
spec; the suspicion mechanism the spec names is built); consensus of any kind (the spec is
availability-first); delta-state CRDTs (state-based is what the spec names; full-state
exchange guarded by a digest is enough at 100 nodes).

### 4.2 Chunks

Sizes are rough non-comment Go lines, code + tests. Every chunk's landing message names the
paradigm and what its tests prove (4.3).

#### Chunk 0 — Foundation and the DST simulator (spec Commit 0)

Lands as two commits so the tour reads cleanly: **0a** foundation (tooling only), then
**0b** the simulator. They are built at the same time by different seats.

**0a deliverable (orchestrator scripts; Neon for the site's front page).** `go.mod`; gate
scripts and their canaries (`agent_work/gates/`); `detlint` (stdlib `go/ast` + `go/types`);
orchestration scripts (`agent_work/orch/`); kb vendored into `kb/` with `kb-sync.sh` and an
empty in-repo store; Pages scaffold (`docs/index.md`, `_config.yml`, `pages.yml`) deployed
and verified serving; `ci.yml`.
- Tests first: one canary per gate — a planted unformatted file, banned `time.Now`, a
  `require` line, an unlisted panic, a comment in pre-prose Go, a `/root/` path in a doc —
  each must turn its gate red.
- Adversarial (Astra): make a gate pass on a tree that violates it; get a comment or a
  dependency past the checks; find an entropy source `detlint` misses (aliased imports,
  method values, `maphash`, pointer formatting with `%p`, `select` order, `os.Getpid`).

**0b deliverable (Opus).** `internal/env`, `internal/sim`, `internal/sim/simtest`,
`internal/prop`, `cmd/objsim` skeleton, and a toy protocol used only by the simulator's own
tests (a max-register flood, the smallest thing that converges).
- Tests written first:
  - meta: `TestReplaySameProcess`, `TestReplayAcrossProcesses`, `TestGoldenDigests`,
    `TestDifferentSeedsDiffer`, at 20, 50 and 100 nodes under a full chaos schedule;
    canaries `TestCanaryUnseededDraw`, `TestCanaryMapOrder`;
  - unit (table-driven): event ordering and FIFO tie-break; timer set/cancel/reset; clock
    skew and drift; each network fault alone (drop, duplicate, reorder, asymmetric
    partition, region partition, clog and release, size limit, lost request vs lost
    response); each disk fault alone (bit-rot is silent, wipe, crash drops unsynced,
    torn tail, read error); crash/restart/pause; stall detection;
  - property: `TestPropHealedNetworkDelivers`, `TestPropTimeMonotonic`,
    `TestPropStreamsIndependent` (extra draws on one stream do not change another),
    `TestPropNemesisRespectsBudget`, disk against a reference model; `prop`'s own
    `TestShrinkFindsMinimal` and `TestTapeReplay`;
  - fuzz: `FuzzSimSchedule`;
  - errors: every `env` sentinel produced by the simulated transport and disk.
- Adversarial (Astra): break replay (find any API use, schedule or node count where two
  runs differ, including across processes and with `GOMAXPROCS`/`GOGC` varied); find a
  fault the network claims to inject but does not (statistical check of drop/duplicate
  rates against configuration); show an asymmetric partition leaking in the blocked
  direction; show bit-rot that is not silent or a wipe that leaves data; break stream
  independence; make the loop hang instead of reporting a stall.
- Write-ups: `deterministic-simulation` (the DST/chaos harness).
- Size: 0a about 900 lines of scripts; 0b about 2,400 code + 2,600 tests.
- **Interface freeze:** when 0b lands, `internal/env` and the `simtest` API are frozen
  (5.1). This is what lets Chunks 1–5 build concurrently.

#### Chunk 1 — SWIM gossip (spec Commit 1)

**Deliverable.** `internal/gossip`: join through seed nodes; per-period randomized probe of
one member in shuffled round-robin order; indirect probe through `k` others on timeout;
alive/suspect/dead state machine with suspicion timeout scaled by `log N` and incarnation
numbers for refutation; piggybacked dissemination with a retransmit limit of `λ·log N`;
clean leave; periodic full-state sync with one random peer, including previously dead and
seed addresses, so two sides of a healed partition find each other again (7.7). Exposes a
`Membership` view and change events.
- Tests first:
  - unit: `TestStateTransitions` (the full alive/suspect/dead × message table),
    `TestIncarnationRules`, `TestPiggybackSelectionAndLimit`, `TestProbeTargetRotation`,
    `TestWireRoundTrip`, malformed-packet table;
  - property: `TestPropViewMergeIsCommutative` (applying the same updates in any order
    gives the same view), `TestPropIncarnationMonotonic`;
  - DST: `TestSWIMConvergenceDST` (20, 50, 100 nodes; 5–10% loss; joins, clean leaves,
    crashes; identical views among live nodes within `c·log₂N` periods after faults stop,
    with `c` fixed in the test and periods-to-converge reported for each N);
    `TestSWIMAsymmetricLinkDST` (one-way drop on one link: zero evictions over a long run);
    `TestSWIMCrashDetectionDST` (dead within a bound, at all nodes);
    `TestSWIMPartitionHealDST` (region partition, then heal: views re-merge);
    `TestSWIMRefutationDST` (a falsely suspected node refutes and stays);
    canary `TestCanaryNoIndirectProbe` must fail the asymmetry test;
  - fuzz: `FuzzGossipWire`;
  - errors: join with no reachable seed, operations after leave, oversize packet.
- Adversarial (Astra): find a seed with a false-positive eviction of a healthy, reachable
  node inside the fault budget; find loss/partition patterns where views never converge
  after heal; forge or replay stale incarnations to resurrect a dead node or kill a live
  one; check whether convergence really scales as `log N` (fit across N up to 200) or the
  constant hides linear growth; flapping node; packet at the size limit; a node restarted
  with lost state and the same ID.
- Write-up: `swim-gossip`. Size: about 1,300 code + 2,000 tests.

#### Chunk 2 — Reed-Solomon erasure coding and the chunk store (spec Commit 2)

**Deliverable.** `internal/erasure`: GF(2^8) with exp/log tables, matrix inversion, a
systematic Cauchy coding matrix (7.3), `Encode`, `Reconstruct`, `Verify`. In
`internal/storage`: the shard file format with per-shard SHA-256, the local chunk store
over `env.Disk` (atomic write, verified read, list, delete), object striping (fixed-size
stripes so multi-MB objects stream), the object manifest, and region-aware placement by
rendezvous hashing over a `Membership` interface (7.2).
- Tests first:
  - unit: GF golden vectors; `TestMatrixInvert`; `TestEncodeKnownVectors`; config table
    (`k=0`, `m=0`, `k+m=256`, `k+m=257`); shard header table; chunk-store table (missing,
    exists, corrupt, wiped); placement table (no region holds more than `m` of a stripe
    when regions allow);
  - property: `TestPropFieldAxioms`, `TestPropMDS`, `TestPropRoundTrip` (sizes 0, 1,
    primes, boundaries, multi-MB; any ≤ m erasures, data and parity alike),
    `TestPropFailureBoundary`, `TestPropCorruptionBecomesErasure`,
    `TestPropPlacementStable` (a membership change moves only the shards it must),
    chunk store against a reference model;
  - fuzz: `FuzzGF256`, `FuzzErasureCoding`, `FuzzShardDecode`;
  - DST: `TestStripedPutGetDST` (put on a 20-node simulated cluster with a static
    membership list, wipe/bit-rot up to `m` shards per stripe, get returns exact bytes;
    more than `m` returns `ErrUnavailable`);
  - errors: every sentinel in 2.6's erasure and storage rows.
- Adversarial (Astra): differential test against an independent reference implementation
  in the harness's own module (**OQ-5**); hunt for any `(k, m, loss set)` that decodes to
  wrong bytes rather than an error; corrupt shards so the header and checksum still agree;
  swap shards between stripes or objects; truncated and oversized shards; `k+m` at the
  field limit; multi-MB memory behaviour; placement skew and region concentration.
- Write-up: `reed-solomon-erasure-coding`. Size: about 1,500 code + 2,200 tests.

#### Chunk 3 — Merkle anti-entropy (spec Commit 3)

**Deliverable.** `internal/merkle`: a SHA-256 hash tree over a `key → hash` map, shaped as
a fixed-fanout trie on the hash of the key (7.4), incremental update, a diff protocol that
exchanges roots and descends only mismatched branches, one level per round trip, and an
anti-entropy worker that picks a peer each period, diffs, and repairs through a `Repairer`
interface. Chunk 3 proves it on a simple replicated chunk store (every replica should hold
the same chunks); Chunk 6 points the same code at erasure-coded objects.
- Tests first:
  - unit: `TestRootKnownVectors`, `TestInsertDeleteUpdate`, `TestEmptyAndSingle`,
    `TestDiffProtocolSteps`, wire table;
  - property: `TestPropDiffComplete`, `TestPropDiffCost` (hash count ≤ `c·d·log N`, round
    trips ≤ depth), `TestPropRootIffEqual`, `TestPropIncrementalEqualsRebuild`,
    `TestPropOrderIndependent`;
  - fuzz: `FuzzMerkleDiff`, `FuzzMerkleWire`;
  - DST: `TestBitRotRepairDST` (flip random bytes on replica disks during traffic; all
    corrupted chunks detected and repaired within a bound; bytes transferred proportional
    to damage); `TestMissedWriteRepairDST` (writes dropped during a partition are repaired
    after heal); `TestRepairUnderLossDST` (diff rounds lose messages and still finish);
    canary `TestCanarySkippedBranch`;
  - errors: peer vanishes mid-diff, malformed node, depth overflow.
- Adversarial (Astra): find two different maps with the same root short of a SHA-256
  collision (structure attacks: key/value boundary ambiguity, empty vs absent, second
  preimage through leaf/inner confusion); find a divergence the diff misses; find `d` and
  `N` where cost exceeds the bound; mutate the tree during a diff; a peer that lies in the
  protocol (must not corrupt the honest side or loop it); rot in the stored tree itself.
- Write-up: `merkle-anti-entropy`. Size: about 1,100 code + 1,800 tests.

#### Chunk 4 — CRDT metadata layer (spec Commit 4)

**Deliverable.** `internal/crdt`: PN-Counter (storage quotas), OR-Set (bucket tags, ACL
memberships), LWW-Register (object metadata, routing pointers), LWW-Element-Set; a hybrid
logical clock for LWW timestamps with node-ID tie-break; canonical encoding so equal states
are equal bytes; a typed metadata document composed of these; and a replicator that
exchanges state with random peers from a `Membership` interface (digest first, state on
mismatch) (7.5).
- Tests first:
  - unit: per-type operation tables; `TestHLCRules`; `TestCanonicalEncoding`;
    `TestLWWTieBreak`; decode malformed table;
  - property: `TestPNCounterLaws`, `TestORSetLaws`, `TestLWWRegisterLaws`,
    `TestLWWElementSetLaws`, `TestDocumentLaws` (the three laws each), `TestPropInflation`,
    `TestPropSEC` (random, duplicated, reordered delivery → identical bytes),
    `TestPropPNCounterNoDoubleCount`, `TestPropORSetAddWins`, `TestPropHLCMonotonic`;
  - fuzz: `FuzzCRDTLaws`, `FuzzCRDTDecode`;
  - DST: `TestMultiRegionConvergenceDST` (3 regions, concurrent mutations, partitions,
    reordering, duplication, clock skew; bit-identical metadata on all replicas after
    heal, and every acknowledged update reflected); `TestOfflineRegionDST` (a region
    edits alone for a long time, then syncs); canary `TestCanaryMergeTakesLeft`;
  - errors: decode failures only; a test asserts `Merge` cannot fail on any decoded state.
- Adversarial (Astra): hunt for states (including hostile decoded ones) that break a law;
  clock skew and HLC overflow making LWW pick a surprising winner; equal timestamps from
  different nodes; a node ID reused after a wipe (OR-Set tag reuse, counter reset);
  remove-then-re-add interleavings; unbounded tombstone growth measured; states that
  encode differently yet compare equal.
- Write-up: `crdt-metadata`. Size: about 1,300 code + 2,100 tests.

#### Chunk 5 — Saga engine and the bucket-migration workflow (spec Commit 5)

**Deliverable.** `internal/saga`: a durable orchestrator — an append-only, checksummed log
on `env.Disk`; intent written and synced before a step acts; recovery that resumes or
compensates from any log prefix; bounded retry with clock-driven backoff; compensations in
reverse order, retried until they succeed; idempotency keys (`saga ID + step`). The
migration workflow — allocate target → copy shards → verify Merkle root → flip routing
pointer (the pivot) → delete source — defined against small step interfaces. Chunk 5
proves it against a model of the cluster inside the simulator; Chunk 6 binds the steps to
the real components and re-runs the same matrix (7.8).
- Tests first:
  - unit: `TestLogAppendRecover`, `TestStateMachineTable`, `TestRetryBackoffSchedule`,
    `TestCompensationOrder`, `TestIdempotencyKey`, `TestPivotSemantics` (after the pivot
    the saga only goes forward);
  - property: `TestPropTerminalState` (random step outcomes and crash points),
    `TestPropRecoveryIdempotent`, `TestPropNoStepAfterTerminal`, engine against a
    reference model;
  - fuzz: `FuzzSagaLogRecover`;
  - DST: `TestMigrationFailurePointsDST` — the exhaustive matrix: for every step, fail or
    crash the orchestrator **before the action**, **after the action but before the
    durable log write**, and **mid-compensation**, with and without a torn log tail; each
    cell must end Completed with a verified root and flipped pointer, or Compensated with
    zero orphan shards and the pointer unchanged; `TestMigrationUnderNemesisDST`;
    `TestDuelingMigrationsDST` (two coordinators migrate one bucket across a partition:
    after heal exactly one location holds it, the pointer agrees everywhere, the loser
    cleaned up); canary `TestCanarySkippedCompensation`;
  - errors: `*StepError` fields, `ErrPermanent` stops retry, `ErrLogCorrupt` on a damaged
    middle record.
- Adversarial (Astra): find a crash point or double-crash sequence leaving an orphan or a
  half-flipped pointer; non-idempotent step execution after a lost response; a
  compensation that fails forever (what is promised then?); log corruption in the middle;
  a crash during recovery itself; writes arriving at the source during the copy; two
  sagas on one bucket.
- Write-up: `sagas`. Size: about 1,400 code + 2,200 tests.

#### Chunk 6 — Integration: the object store and sync engine

**Deliverable.** `internal/node` wires gossip, placement, the chunk store, erasure coding,
anti-entropy, CRDT metadata and the Saga engine into one node, and supplies the real
runtime: UDP transport, TCP request/response transport, file-backed disk, wall clock, the
node loop. `internal/storage` gains the HTTP object API (put/get/head/delete/list, bucket
create, tags and ACL, quota, metadata, migration start/status, membership and health) and
the metadata sync endpoint (**OQ-7**). `cmd/objnode` runs a node from flags or a config
file. The cross-links deferred from Chunks 2–5 are made here: shards placed on the gossip
view; Merkle trees over manifests and over each node's shard inventory (7.4); CRDT
replication to gossip-chosen peers; migration steps on real components.
- Tests first:
  - unit: HTTP status table, config validation, wire-code ↔ sentinel round-trip, frame
    codec, flag parsing;
  - property: object-level round-trip through the full path; idempotent remote
    operations (duplicate request, lost response);
  - fuzz: `FuzzFrame`, `FuzzObjectAPI`;
  - DST (full nodes on the simulator): `TestClusterPutGetDST`, `TestClusterBitRotDST`,
    `TestClusterPartitionHealDST`, `TestClusterMigrationFailurePointsDST` (Chunk 5's
    matrix on real components), `TestClusterCrashRestartDST`;
  - concurrency: everything in 2.5 — synctest lifecycle tests, cancellation tests,
    shutdown tests, leak checks, `-race -count=3`; runtime conformance (same scenario on
    simulator and loopback);
  - errors: the whole 2.6 API table through HTTP.
- Adversarial (Astra): concurrent PUTs to one key from two regions (which wins, and are
  the loser's shards collected?); GET during repair and during migration; malformed and
  slow HTTP clients; shutdown races; goroutine leaks under churn; cross-feature
  interleavings (migration during partition during bit-rot); quota overshoot size under
  partition; real-socket behaviour the simulator does not model (short reads, connection
  resets).
- Write-up: `object-store-and-sync-engine` (the integrated system); Chunk 5's write-up
  updated with the real-component evidence. Size: about 2,800 code + 3,000 tests.

#### Chunk 7 — E2E suite, nemesis cycles, fuzz hardening

**Deliverable.** `test/e2e`: scenarios E1–E9 from 2.7 on both tiers, the tier R process
harness, the long nemesis/healing cycle runner, `FuzzClusterNemesis`, the hardening fuzz
runs at the 2.3 budgets with results recorded per target, and a large seed sweep. Product
fixes found here go through the normal pipeline as part of this chunk.
- Tests first: the scenarios are the tests; each is written with its pass criteria before
  any fix it forces.
- Adversarial (Astra): its own nemesis schedules aimed at the seams between primitives; a
  long soak looking for slow leaks (memory, goroutines, tombstones, orphan shards); tier R
  chaos (kill, restart, corrupt files, fill disk); any claim in any earlier write-up
  retested on the whole system.
- Write-up: `end-to-end-and-hardening` (what the whole-system tests show and what they do
  not). Size: about 600 code + 3,200 tests and harness.

#### Chunk 8 — Publishing finish

**Deliverable (Neon prose; orchestrator checks).** Site complete: front page, a reading
order, all eight write-ups cross-linked and consistent with the final code; final README
with the live site URL, how to run the tests, how to replay a seed, and how to run a local
cluster; self-containment sweep over every reader-facing file; prose-provenance sweep over
the whole tree; kb stores final sync; link check on the deployed site.
- Tests first: `site-lint`, link check, self-containment and provenance checks, every
  command quoted in the README executed by a script (`agent_work/gates/readme-commands.sh`).
- Adversarial (Astra): follow the README on a clean clone (Linux, and the macOS CI job)
  and report every step that fails or is unclear; find any claim in the prose the evidence
  does not support.
- Size: prose only.

### 4.3 Commit history on main

One merge commit per chunk, in chunk order, each with a message of the form:

```
feat(cluster): SWIM gossip membership and failure detection

Tests prove: 20/50/100-node clusters converge to identical views within
c·log2(N) periods under 10% loss; a one-way link drop causes no eviction; ...
Adversarial: 9 claims attacked, 8 held, 1 narrowed (see docs/architecture/swim-gossip.md).
```

The spec's own commit subjects are used for Chunks 0b–5. Inside a merge, the build,
repair and prose commits stay visible on the side branch.

---

## 5. Concurrency map

### 5.1 Dependency graph

```
   0a foundation ─┐
                  ├─► env + simtest API frozen
   0b simulator ──┘            │
      ┌───────────┬────────────┼────────────┬───────────┐
      ▼           ▼            ▼            ▼           ▼
  1 gossip    2 erasure    3 merkle      4 crdt      5 saga     (independent packages)
      └───────────┴────────────┼────────────┴───────────┘
                               ▼
                        6 integration
                               ▼
                        7 E2E + hardening
                               ▼
                        8 publishing finish
```

- **Wave A:** 0a (orchestrator) and 0b (Opus) at the same time. 0b's review, adversarial
  pass and landing complete before Wave B starts, because everything depends on the
  interfaces being right.
- **Wave B:** Chunks 1–5 are independent: each touches only its own package (Chunk 2 also
  owns the chunk-store files in `internal/storage`) and sees the others only through small
  interfaces it declares itself (`Membership`, `Repairer`, saga step interfaces). All five
  can be built concurrently; the recommended cap is three Opus builders at once
  (**OQ-10**), started in the order 1, 2, 4, then 3 and 5 as slots free up. Neon reviews
  and Astra adversarial runs draw on other pools and overlap with builds freely.
- **Waves C–E:** Chunks 6, 7, 8 in sequence. Within Chunk 7 the scenario groups (E1–E3,
  E4–E5, E6–E9) can be built by separate agents on disjoint files.

**The freeze.** After 0b lands, `internal/env` and the exported API of `internal/sim` and
`simtest` change only through a *simulator change request*: the builder writes the need in
its work notes and stops on that point; the orchestrator dispatches a small separate fix
(own worktree, full Chunk 0 gates, replay goldens updated on purpose), lands it as its own
commit, and the waiting chunks rebase. Builders never edit those packages in their own
branches; a gate check rejects a chunk diff that does.

### 5.2 Worktrees and branches

- One branch per chunk, `chunk/NN-<slug>`, in its own worktree under
  `/root/Software/decentralized-object-store-work/NN-<slug>`, created from main at
  dispatch. Every stage of the chunk (build, repair, adversarial harness, prose) is a fresh
  agent working in that worktree; agents commit there and never touch the main checkout.
- Launch verification 15 seconds in: log file exists, process alive, `/proc/<pid>/cwd` is
  the worktree. A run found in the main checkout is killed and main verified clean.
- Only the orchestrator edits `STATE.md`, `agent_work/INDEX.md`, `PLAN.md`, `docs/index.md`
  navigation and the kb stores, and only on main at landing — so parallel chunks never
  conflict on shared files.
- Worktrees are removed after landing; branches are kept until the project is finished.

### 5.3 Landing order when chunks finish out of order

Main reads as a tour, so chunks land in numeric order. A chunk that is fully green before
its predecessors waits in a *ready* state. When its turn comes it is rebased onto main (a
clean rebase is expected, since packages are disjoint), gates G0–G6 are re-run on the
rebased tree, and it is merged with `--no-ff`. A ready chunk never blocks other work: its
builder slot is already free. If a chunk is stuck in repair or arbitration for more than
one working session while later chunks are ready, the orchestrator records the choice in
STATE.md and lands the ready ones out of order rather than idle the pipeline; the Pages
reading order and README keep the logical order regardless.

---

## 6. Per-chunk pipeline

Every stage is a fresh headless agent with a brief file (`/root/briefs/objstore-<chunk>-
<stage>.md`, copied into `agent_work/<chunk>/`), a log, a pid file and an exit file under
`/root/runs/`. The orchestrator waits for both the exit file and the expected outputs, with
a deadline, and fails closed. A run that dies is diagnosed from its log and re-dispatched
once with the failure appended to the brief; a second failure stops that chunk's chain and
is reported in STATE.md.

| # | Stage | Seat | Output |
|---|---|---|---|
| 1 | Tests-first brief: claims to prove, named tests by kind, error contract, interfaces, files owned, gates, what not to touch | Fable | brief file |
| 2 | Build in the worktree: tests first, then code; no comments; build notes; runs G0–G6 itself | Opus (fresh) | commits on the chunk branch, `build-notes.md` |
| 3 | Orchestrator runs G0–G6 independently | Fable | gate results |
| 4 | Review of the diff against the brief, tests and gate results; route probe first | Neon (fresh) | `REVIEW-1.md`: PASS / NITS / FAIL with numbered findings |
| 5 | Repair, one round, if NITS or FAIL | Opus (fresh) | commits |
| 6 | Re-review if stage 5 ran | Neon (fresh) | `REVIEW-2.md` |
| 7 | Adversarial: Astra reads the code and the claims and writes harnesses that try to break each claim | Astra (fresh) | `agent_work/adversarial/<chunk>/` scripts + claimed-attack list |
| 8 | Orchestrator runs every adversarial script and records held/broken per claim | Fable | `RESULTS.md`, index rows |
| 9 | If anything broke: one Opus repair round (fix, or propose narrowing the claim), Neon re-review of that repair, adversarial scripts re-run | Opus, Neon, Fable | commits, `REVIEW-3.md`, updated `RESULTS.md` |
| 10 | Full gate suite on the result | Fable | gate results |
| 11 | Prose, once, late: all code comments and the architecture write-up, from the final code, build notes, reviews, gate results, adversarial results and every narrowed claim | Neon (fresh) | prose commits with the `Prose-By` trailer; `docs/architecture/<slug>.md` |
| 12 | G9 checks and G0–G6 again on the final tree | Fable | gate results |
| 13 | Acceptance against the brief | Fable | accept, or one more targeted fix through the same seats |
| 14 | Land: rebase if needed, merge `--no-ff` with the tour message, push | Fable | commit on main |
| 15 | Verify the Pages deploy: workflow run succeeded and the write-up URL returns 200 with the write-up's title | Fable | recorded in work notes |
| 16 | Bookkeeping: STATE.md, `docs/knowledge/<entry>.md` + in-repo index, vm-1 kb entry, `agent_work/INDEX.md`, progress file | Fable | commit on main, pushed |

Rules that go with the table:
- Adversarial comes after the review cycle on purpose: Astra attacks code that has already
  passed review, so its findings are about claims, not typos.
- A second FAIL from Neon, or a builder/reviewer standoff, goes to arbitration under the
  standing protocol. Which seat arbitrates in this project is **OQ-11**.
- The Neon prose brief contains only verified evidence. Any claim that was narrowed is
  given to Neon in its narrowed form, with the reason.
- The knowledge entry written at stage 16 is operational text by the orchestrator; it is
  not published on the site and is excluded from the Neon rule (**OQ-8**).
- Pool check before each dispatch: Claude weekly, Codex weekly, and the Neon route probe
  (`agent_work/orch/seat-probe.sh`). A 429 on one Neon route switches routes and continues.

---

## 7. Key technical decisions (researched, with recommendations)

### 7.1 Simulated vs real transport: one protocol core, two environments

Protocol packages are sans-IO state machines. Each exposes handlers — `OnMessage(from,
bytes)`, `OnTimer(id)`, `OnRequest(from, bytes) → bytes` — and acts only through
`internal/env`:

```
Clock      Now() ; AfterFunc(d, fn) → Timer(Stop/Reset)
Rand       *rand.Rand (math/rand/v2), one stream per node
Transport  Send(to, datagram) ; Request(to, bytes, timeout, callback(reply, err))
Disk       Read / Write / Append / Sync / Delete / List on named blobs
```

- **Simulated:** `internal/sim` implements all four; one event loop calls every node.
- **Real:** `internal/node` implements them with UDP (datagrams), TCP with length-prefixed
  frames (request/response), files with write-temp + fsync + rename, and the wall clock.
  Each node runs one loop goroutine that owns all protocol state; socket readers and timers
  post events into its bounded inbound channel. The loop calls the same handlers the
  simulator calls. `Request` is callback-style so the single-threaded core never blocks.

Consequences: the simulator proves the protocol logic exactly; the runtime layer is thin
and is the only place where races can exist, which is where `-race`, synctest and the
leak checks are aimed; the conformance tests show both worlds run the same code. What the
simulator does not model — short reads, kernel buffers, connection resets — is covered by
tier R and named in the write-ups as a limit of the simulation evidence. Client-facing
HTTP is outside the simulated network; in tier S, clients call the API layer in process.

`internal/env` and `internal/node` are additions to the spec's suggested layout. Reason:
the interfaces need a home that neither the simulator nor the protocols own, and the real
runtime needs one that is not `internal/storage`.

### 7.2 Object and chunk-store layout

- An object is cut into stripes of 1 MiB of data; each stripe is encoded into `k` data and
  `m` parity shards (default `k=4, m=2`, set per bucket). Multi-MB objects stream stripe
  by stripe.
- A shard blob is a 64-byte header (magic, version, `k`, `m`, shard index, stripe index,
  payload length, object version ID, SHA-256 of the payload) plus the payload. Every read
  verifies the checksum.
- On disk (real runtime): `<data>/shards/<bucket>/<hh>/<object-version-hex>/<stripe>.<idx>`,
  written as temp file → fsync → rename → directory fsync. In the simulator the same blob
  names live in the in-memory disk.
- The **manifest** of an object version (size, content SHA-256, `k`, `m`, stripe count,
  per-shard checksums, and the node chosen for each shard at write time) is metadata: it
  is the value of that key's LWW-Register and replicates to every node as CRDT state.
- **Placement:** rendezvous (highest-random-weight) hashing over the live membership view,
  constrained so no region gets more than `m` shards of a stripe when the region count
  allows. No ring to maintain, and a membership change moves only the shards it must.
- **Write:** a PUT succeeds when at least `k+1` shards (configurable, between `k` and
  `k+m`) are durably acknowledged and the manifest is stored locally; missing shards are
  filled by repair. **Read:** any `k` verified shards. **Delete:** a tombstone manifest.
  Shards no manifest refers to are collected after a grace period.
- Trade-offs accepted, to be stated in the write-up: every node holds all metadata (simple
  and fine at 100 nodes, not at 10,000); concurrent PUTs to one key resolve by LWW, so one
  writer's acknowledged object is superseded; no read-your-writes across regions.

### 7.3 Reed-Solomon construction and what "corrupted" means

- Field GF(2^8) with polynomial `x⁸+x⁴+x³+x²+1` (0x11D), generator 2, exp/log tables.
- Coding matrix: systematic `[I | C]` with `C` a Cauchy matrix. Every square submatrix of a
  Cauchy matrix is invertible, which gives the MDS property directly and lets the test
  check it exhaustively for small sizes. The spec allows "Vandermonde/Cauchy"; a
  Vandermonde matrix made systematic the naive way is not guaranteed MDS, and the correct
  way needs an extra elimination step, so Cauchy is the recommended default. Limit:
  `k + m ≤ 256`.
- Reed-Solomon corrects `m` *erasures* (known positions) but only `m/2` *errors* at unknown
  positions. The spec asks for recovery from up to `m` missing **or corrupted** shards.
  That holds because corruption is located by the per-shard SHA-256: a shard that fails its
  checksum is treated as missing. The write-up must say this plainly, and
  `TestPropCorruptionBecomesErasure` is the proof.

### 7.4 Merkle tree shape and what the trees cover

- Shape: a trie on `SHA-256(key)`, fanout 16, fixed depth chosen from the expected key
  count, leaves holding sorted `(key, hash)` lists. Two replicas with different key sets
  still have comparable structure, an insert never shifts other leaves, and updates are
  `O(depth)`. Leaf and inner hashes are domain-separated (different prefix bytes), and
  keys are length-prefixed, to close second-preimage and boundary-ambiguity attacks.
- Diff: exchange roots; if they differ, request the children of each mismatched node, one
  level per round trip, all mismatched nodes of a level in one message. Cost: at most
  `16·d·depth` hashes for `d` differing keys, depth round trips.
- In the integrated system (Chunk 6) the same library is used twice. (1) **Manifest
  tree** per bucket, identical on every node when in sync: diffing it between peers finds
  missed metadata writes. (2) **Shard inventory tree** per bucket per node, over the
  shards that node actually holds and their actual content hashes (kept honest by a
  background scrub that re-reads shards): a peer compares it with the tree it *expects*
  that node to have, computed from the manifests. Mismatches are rotted or missing shards;
  the holder rebuilds them from any `k` good shards. Different nodes hold different shards
  of an object, so a plain replica-to-replica comparison of chunk hashes (the spec's
  wording) would not work with erasure coding; this is the adaptation, and Chunk 3 proves
  the library on plain replicas first.

### 7.5 CRDT choices

- State-based (convergent) types as the spec says, merged by join. OR-Set in its classic
  form — unique add-tags plus a tombstone set — because its laws are easy to state and
  prove; tombstones grow without bound and that is the trade-off recorded (**OQ-12** offers
  the compact variant).
- LWW timestamps are hybrid logical clocks `(wall, counter, node ID)`: totally ordered,
  monotonic per node, close to real time when clocks are sane. "Last" means highest HLC,
  not last in real time; with skewed clocks a later write can lose. The simulator's clock
  skew makes this visible and the write-up states it.
- Replication: each period a node sends a digest of its metadata state to a random live
  peer and exchanges full state on mismatch (push-pull). SWIM's piggyback channel is too
  small for CRDT state, so "disseminated over gossip" is implemented as this epidemic
  exchange using gossip's membership for peer choice.
- PN-Counter quotas are eventually enforced: regions can overshoot a quota while
  partitioned. Stated as a trade-off, measured by an adversarial test.

### 7.6 Property-testing approach, fuzz budget, macOS, Go version

- **Property testing:** the in-repo, stdlib-only `internal/prop` described in 2.2
  (recommended). The alternative is `pgregory.net/rapid` as a test-only dependency: more
  mature shrinking and state-machine support, at the cost of the repo's first external
  module. The spec's zero-dependency rule is about the engine, so either is allowed;
  staying on the standard library keeps `go.mod` empty and makes the properties double as
  fuzz targets. **OQ-4.**
- **Fuzz budget:** 2.3 — 30 s per touched target per chunk; 15/60 minutes in hardening.
- **macOS:** gate every chunk on `GOOS=darwin GOARCH=arm64 go build ./... && go vet ./...`
  locally (seconds; the probe confirms cross-building works here), and add a CI job on a
  GitHub macOS runner that runs `go test ./...` after each push (free for a public repo).
  The cross-build is a landing gate; the macOS test job is checked after landing, and a
  failure there becomes a fix-forward item before the next chunk lands. Platform-specific
  code is confined to `internal/node` (directory fsync, socket options).
- **Go version:** `go 1.26` with `toolchain go1.26.8` in `go.mod`; CI reads the version
  from `go.mod`. Needs 1.25+ for `testing/synctest` and `WaitGroup.Go`.

### 7.7 Gossip after a partition heals

Plain SWIM forgets dead members, so two halves of a healed partition would never meet
again. Each node therefore keeps dead members for a retention period and, each sync
period, also tries one address from the dead list or the seed list with a full-state
exchange. Incarnation numbers decide who is right when views disagree. This is the
mechanism behind the spec's "converge … after partitions heal".

### 7.8 Saga design points

- The coordinator is the node that started the saga; its log is on its own disk and it
  resumes after restart. A coordinator that never comes back leaves its saga unfinished —
  a stated limit of a leaderless design without consensus (**OQ-13**).
- Step order: allocate, copy, verify (all compensable) → flip pointer (pivot) → catch-up
  diff and delete source (forward-only, retried until done). Writes that reach the source
  during the copy are caught by a second Merkle comparison after the flip, before the
  source is deleted.
- Without consensus there is no mutual exclusion, so two coordinators can migrate one
  bucket at once across a partition. The routing pointer is an LWW-Register; after the
  flip a saga re-reads the merged pointer, and the one that lost cleans up its target.
  `TestDuelingMigrationsDST` is the proof, and this is the claim most likely to be
  narrowed by the adversarial pass.
- "Terminal state" is promised once faults heal: a compensation whose target is
  unreachable keeps retrying.

### 7.9 Pages build tooling

- Recommended: the stock Actions pipeline — `actions/checkout`,
  `actions/configure-pages`, `actions/jekyll-build-pages` with `source: ./docs`,
  `actions/upload-pages-artifact`, `actions/deploy-pages` — on every push to main. No
  Node, no site generator in the repo, nothing to maintain; the write-ups stay plain
  markdown that also reads well on GitHub. `docs/_config.yml` sets title, theme and
  excludes `knowledge/`.
- Known hazard: Jekyll runs Liquid over markdown, so `{{` or `{%` in a code sample breaks
  or alters a page. `site-lint` rejects them in `docs/` outside raw blocks, and the prose
  brief tells Neon.
- Fallback if the Jekyll output disappoints: a pandoc step in the same workflow.
- State today: Pages is already enabled on the repo with source "GitHub Actions"
  (checked through the API); no workflow exists yet, so the URL serves nothing until
  Chunk 0a lands. Workflow files are pushed over SSH, which the machine's key allows.
  Action versions are pinned and checked at Chunk 0a.
- Verification after each landing: the workflow run concluded successfully and
  `curl` of the write-up URL returns 200 and contains the write-up's title. A 404 is not
  published.

### 7.10 Knowledge stores

- `kb/` is vendored whole with `git archive` of the kb repo at a recorded SHA (about
  4.5 MB of tracked files), following the pattern used in the sqlite-internals repo;
  `agent_work/kb-sync.sh vendor|index|status`. It is a nested module with its own
  dependencies; the root module does not import it.
- Project knowledge goes to `docs/knowledge/*.md`, indexed by the vendored tool through
  `.kb/`. At each landing the orchestrator writes the chunk's entry (findings, seeds that
  mattered, narrowed claims, pitfalls) there and adds the matching entry to the vm-1 store
  with the installed `kb` CLI. `docs/knowledge/` is excluded from the published site.

---

## 8. Risks

| Risk | Handling |
|---|---|
| The VM's Claude sign-in ends between 2026-11-25 and 2026-11-28, and both the orchestrator and the builders are Claude seats | Wave B concurrency; STATE.md resumable at every checkpoint; land in order so main is always a coherent prefix of the tour; **OQ-14** asks what should happen at the cutoff |
| Claude weekly cap with several Opus builders at once | cap of three concurrent builders; usage checked before each dispatch; stop new dispatches when limited and let in-flight work finish |
| Neon direct route credits run out; OpenRouter depleted until 2026-12-01 | route probe before each Neon stage; a quota error is a pause on that stage, not a pipeline failure; other chunks continue |
| Simulator interface turns out wrong after the freeze | change-request path in 5.1; Chunk 0b gets the heaviest adversarial pass for this reason |
| The `O(log N)` convergence and `O(d log N)` cost claims are empirical | tests fix the constant and report measurements; write-ups say "measured for N up to …", not "proven" |
| Integration (Chunk 6) is the largest and least parallel chunk | interfaces declared in Chunks 2–5 are written with 6 in mind; 6's brief may split into 6a (node + runtime) and 6b (API + CLI) if its size estimate holds |
| Neon comments could change code by accident | comment-only-diff check compares the Go AST before and after the prose commit |

Rough schedule if signed off promptly, assuming no arbitration: Chunk 0 about one day;
Wave B about one and a half to two days; Chunk 6 one day; Chunk 7 one day; Chunk 8 half a
day. That is tight against the sign-in window.

---

## 9. Open questions for Nick

No default is applied to any of these until you rule. Each has a recommendation.

1. **OQ-1 Module path and repo name.** Recommend
   `github.com/nickstrad/decentralized-object-store` (matches the repo). Confirm the repo
   name stays.
2. **OQ-2 Chunk 0 as two commits.** Recommend 0a (foundation tooling, vendored kb, Pages
   scaffold, CI) then 0b (the simulator, the spec's Commit 0), so the 4.5 MB vendoring and
   scripts do not bury the first primitive in the history. Alternative: one commit.
3. **OQ-3 PRNG type.** The spec says `*math/rand.Rand`. Recommend `math/rand/v2`
   (`*rand.Rand` over PCG, named streams). Alternative: `math/rand` v1 exactly as written.
4. **OQ-4 Property-testing helper.** Recommend the in-repo, stdlib-only `internal/prop`
   (no dependency; properties double as fuzz targets). Alternative: adopt
   `pgregory.net/rapid` as a test-only dependency.
5. **OQ-5 Reference implementations in adversarial harnesses.** Recommend allowing Astra's
   harnesses — which live in their own nested modules under `agent_work/adversarial/` and
   are never imported by the product — to use an outside library (for example
   klauspost/reedsolomon) purely as a differential oracle. The root module stays
   dependency-free either way. Alternative: forbid it everywhere in the repo.
6. **OQ-6 Object API surface.** Recommend an S3-like path-style HTTP API without S3 wire
   compatibility, signatures or authentication. Say so if you want real S3 compatibility
   or any auth — both change scope.
7. **OQ-7 What "local-first sync" includes.** Recommend: regions as replicas that work
   offline, plus a minimal client replica (a `/v1/sync` endpoint and a small client-side
   CRDT document that edits offline and syncs), exercised in E8. Alternative: node-level
   only, no client replica.
8. **OQ-8 Where the Neon prose rule stops.** Recommend: Neon writes README, `docs/`
   (architecture and site), every code comment, and CLI help text; Opus writes
   identifiers, error strings, log messages and test names; the orchestrator writes
   operational files (PLAN, STATE, briefs, `agent_work/`, `docs/knowledge/` entries,
   commit messages). Confirm, in particular for `docs/knowledge/` and commit messages.
9. **OQ-9 Hardening fuzz budget.** Recommend 15 minutes per target and 60 minutes for the
   four headline targets, in parallel (roughly 2–3 hours of wall time). Say if you want
   more (overnight) or less.
10. **OQ-10 Builder concurrency.** Recommend at most three Opus builders at once in Wave B.
    All five at once is possible and faster but heavier on the Claude weekly cap.
11. **OQ-11 Arbitration seat.** The standing protocol sends a second FAIL to the third
    family's top seat. Here the builder is Claude (Opus) and the reviewer is Google (Neon).
    Recommend Astra (OpenAI) as arbiter, since Fable is the orchestrator and the same
    family as the builder. Alternatives: Fable, or Muse Wonder Full.
12. **OQ-12 OR-Set variant.** Recommend the classic tag-and-tombstone OR-Set (simple laws;
    unbounded tombstones recorded as the trade-off). Alternative: the compact
    dot/version-vector variant — no tombstones, harder proofs, more code.
13. **OQ-13 Saga coordinator loss.** Recommend accepting that a permanently lost
    coordinator leaves its saga unfinished, and saying so. Alternative: replicate saga
    logs so another node can take over — a real feature with its own correctness burden.
14. **OQ-14 The sign-in cutoff (2026-11-25 to 28).** If the build is not finished by then,
    should work stop at the last landed chunk with STATE.md ready for a later resume
    (recommended), or should the remaining chunks move to other seats?
15. **OQ-15 Default erasure parameters and write threshold.** Recommend `k=4, m=2` in 3
    regions (two shards per region; any one region can be lost) and PUT acknowledged at
    `k+1` durable shards. Confirm or give your own.
16. **OQ-16 macOS.** Recommend the local darwin cross-build as a landing gate plus a macOS
    CI test job checked after landing. Say if you want the macOS test job to block
    landings instead.

---

## Appendix A — Research and probe evidence (Phase 1)

- **Prior art on this machine.** The knowledge store had no entries on these primitives
  (searches recorded with feedback). The useful sources were the DST canvas of 21
  simulators (its technique ladder and taxonomy shaped 2.1: named PRNG streams, the
  `(time, seq)` queue, run-twice and cross-process replay checks, golden digests, planted
  bugs, the fault budget, heal-then-require-progress) and the sqlite-internals repo's
  pipeline scripts (launch/wait/land, the Neon runner, kb vendoring), whose patterns
  section 6 and 7.10 reuse. No product code is copied from either.
- **`agent_work/probes/phase1/run.sh`** (2026-11-21, go1.26.8 linux/amd64): a 100-line
  event loop with PCG streams, 10% drop, 5% duplication, random delay and an asymmetric
  partition gives the same digest in separate processes for the same seed at 50 and at 100
  nodes (6 of 6 runs), different digests for different seeds, and runs 200,000 events in
  0.29 s. The same loop with one map iteration added gives 6 different digests in 6 runs.
  Native fuzzing, `-race`, a `testing/synctest` lifecycle test and a darwin/arm64
  cross-build and vet all pass.
- **`agent_work/orch/seat-probe.sh`** (2026-11-21): Neon direct route live; Neon
  OpenRouter route down (out of credits, as expected until 2026-12-01); Astra
  (`gpt-6-astra`) live. Both CLIs must be run with stdin closed.
- **GitHub:** repo public; Pages enabled with build type "workflow"; no workflow yet.
