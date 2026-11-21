# Brief — Chunk 0b BUILD: the deterministic simulator (spec Commit 0)

Seat: Opus builder (fresh). Worktree: /root/Software/decentralized-object-store-work/0b-simulator
(branch `chunk/0b-simulator`). Work notes: `agent_work/0b-simulator/`.

FIRST read /root/briefs/objstore-p2-builder-rules.md (binding), then PLAN.md sections 2.1–2.6,
2.9, 4.2 "Chunk 0", 5.1 and 7.1. This brief adds detail; where it differs from PLAN.md wording,
this brief wins (it carries later rulings).

## What this chunk is
"Initialize deterministic simulation harness with virtual clock and network." The discrete-event
simulator that every later primitive is proven on, plus the four environment interfaces that
protocol code is written against. After this chunk lands its exported API is FROZEN and five
other builders (gossip, erasure/chunk store, Merkle anti-entropy, OCC metadata, Sagas) build
against it at the same time — so the interface quality matters more than anything else here.

## Files you own
- `internal/env/` — `Clock`, `Rand`, `Transport`, `Disk`, the `Env` bundle, node identity
  types, shared sentinels (`ErrTimeout`, `ErrUnreachable`, `ErrClosed`, `ErrTooLarge`,
  `ErrRemote`, plus the disk sentinels you need, e.g. `ErrNotFound`, `ErrIO`), and the wire
  status-code table for request/response errors (sentinel ↔ uint8 code, unknown → `ErrRemote`).
- `internal/sim/` — PRNG streams, virtual clock, event loop, network, disk, process faults,
  nemesis and fault budget, invariants, event log, digests.
- `internal/sim/simtest/` — test helpers (seed flag/env handling, sweeps,
  `RequireDeterministic`, replay-command printing, cluster builders, failing-seed artifacts).
- `cmd/objsim/` — runs a named scenario from a seed outside `go test`, prints the two digests.
  A scenario registry that later chunks can add to. Help text via embedded `help.txt` (rules).
- A toy protocol used only by the simulator's own tests (max-register flood: each node holds
  an integer, floods its max, converges) — in a test-support package of your choice under
  `internal/sim/` (it must not be importable as product behaviour by accident; name it clearly).
- You may delete `internal/buildcheck/` (a placeholder) once real packages exist.
- NOT built: `internal/prop` (dropped; rapid is used instead).

## Interface requirements (design latitude is yours inside these)
1. Protocol code is a single-threaded state machine. It never blocks and is only ever called
   by one caller at a time (the simulator loop, or later the real node loop). It acts only
   through `env`.
2. `Clock`: `Now()` in virtual nanoseconds (an integer type; using `time.Duration`/a named
   int64 is fine), `AfterFunc(d, fn) Timer` with `Stop`/`Reset`. Per-node skew and drift.
3. `Rand`: a `*rand.Rand` from `math/rand/v2` over PCG, one independent stream per node.
   Root-seed stream derivation exactly as PLAN 2.1: `NewPCG(uint64(seed), fnv64a(label))`,
   labels `net`, `disk`, `nemesis`, `workload`, `node/<id>`.
4. `Transport`: two classes. Datagram `Send(to, bytes)` (drop, duplicate, delay/reorder, size
   limit). Request/response `Request(to, bytes, timeout, callback(reply, err))` (delay, lost
   request, lost response — indistinguishable to the caller — refused, timeout; never
   duplicated or reordered within a connection). The SERVING side must be able to answer a
   request asynchronously (e.g. the handler receives a reply function it may call later, after
   its own outbound requests complete) — a later chunk's PUT handler fans out to peers before
   replying. Errors never cross as Go values: a reply carries a status code and a short
   message, mapped back to sentinels by the table in `env`.
   Messages cross the simulated network as copied bytes, never shared memory.
5. `Disk`: named blobs with `Read / Write / Append / Sync / Delete / List`, synchronous calls;
   written vs durable layers; faults: silent bit-rot, wipe, crash dropping unsynced data with
   optional torn tail of the last unsynced append, read error.
6. A node in the simulator is created by a factory `func(env.Env) <Process>` so that a restart
   builds fresh volatile state over the same durable disk; a wipe-and-rejoin gets an empty
   disk. The process interface covers: start, inbound datagram, inbound request, and a
   canonical state encoding for the state digest. Nodes carry an ID and a region label.
7. One node must be able to host several protocols later (Chunk 6 multiplexes them over one
   Transport). Do not build the multiplexer, but do not make it impossible: keep handler
   registration and the `Env` bundle composable (e.g. an `Env` can be wrapped/derived).
8. Topology faults for both classes: symmetric partition, asymmetric partition, per-link loss
   (integer parts-per-million), region partition, isolate-one-node, clog/release, heal-all.
   Process faults: crash, restart, pause/resume, wipe-and-rejoin.
9. Nemesis: a fault schedule drawn from the `nemesis` stream, alternating chaos and heal
   phases, recorded in the event log, working inside a configurable fault budget (minimum
   live nodes, maximum simultaneously damaged disks/blobs per group — make the budget
   expressible by later chunks, e.g. "no more than m shards of one stripe"); budget can be
   lifted on purpose. After the last chaos phase everything heals and tests assert convergence
   within a stated bound of virtual time.
10. Workloads: a way for a test to schedule client actions against nodes as events drawn from
    the `workload` stream, and invariants as functions over the whole cluster run after every
    event or every N events.
11. Loop API: `Step`, `RunUntil(t)`, `RunUntilQuiet`, step budget; "queue empty and not
    converged" and "budget exhausted" are reported as stall failures, never hangs.
12. Event log: canonical binary record per executed event (time, seq, kind, node, peer,
    payload hash). Fingerprint = SHA-256(event log) + SHA-256(canonical state and disk of
    every node). A panic inside a node is never swallowed: it fails the test with the seed.
13. simtest: `-seed` flag registered in importing test packages AND `DST_SEED`; `DST_SEEDS`
    (count, small fixed default), `DST_SEED_BASE`; failing test prints seed, both digests,
    event-log tail and `REPLAY: go test ./<pkg> -run '^<Test>$' -seed=<n> -count=1`;
    `DST_ARTIFACTS=<dir>` writes the full event log of a failing seed; failed seeds are
    regression-pinned in `testdata/seeds.txt` (read by the sweep helper; "append, never
    delete"). Keep helper signatures small and obvious — five other builders will read them
    cold.

## Tests to write first (names are part of the contract)
- Meta: `TestReplaySameProcess`, `TestReplayAcrossProcesses` (re-executes the test binary
  twice and compares digests), `TestGoldenDigests` (pinned seeds in `testdata/`),
  `TestDifferentSeedsDiffer` — at 20, 50 and 100 nodes under a full chaos schedule; canaries
  `TestCanaryUnseededDraw`, `TestCanaryMapOrder`, `TestCanaryDroppedInvariant` (each plants
  the bug behind a test-only switch and proves the harness catches it).
- Unit (table-driven): event ordering and FIFO tie-break; timer set/cancel/reset; clock skew
  and drift; each network fault alone (drop, duplicate, reorder, asymmetric partition, region
  partition, clog and release, size limit, lost request vs lost response, refused, timeout);
  each disk fault alone (bit-rot is silent, wipe, crash drops unsynced, torn tail, read
  error); crash/restart/pause/wipe-and-rejoin; stall detection (both kinds); async reply.
- Property (rapid): `TestPropHealedNetworkDelivers`, `TestPropTimeMonotonic`,
  `TestPropStreamsIndependent`, `TestPropNemesisRespectsBudget`, `TestPropDiskModel` (disk
  against a reference model as a rapid state machine, including crash and sync).
- Fuzz: `FuzzSimSchedule` (bytes → fault schedule; invariants and replay hold).
- Errors: every `env` sentinel produced by the simulated transport and disk and checked with
  `errors.Is`; the status-code table round-trips every row.
- `cmd/objsim`: a test that runs the toy scenario twice by seed and gets equal digests.

## Measurements to put in the build notes
Events per second at 100 nodes; wall time of the default `go test ./...`; wall time of
`DST_SEEDS=500` for the meta scenarios; the exported API of `internal/env`, `internal/sim` and
`simtest` as a `go doc -all`-style listing (file `agent_work/0b-simulator/frozen-api.txt`) —
this listing is what the Wave B builders are briefed from.

## Out of scope
Any real socket, file or goroutine (the real runtime is Chunk 6); gossip or any protocol other
than the toy; gate scripts and detlint (another seat owns them — follow the determinism rules
in the builder rules by hand; the static check will be run on your code).
