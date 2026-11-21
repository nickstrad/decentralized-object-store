# Simulator kernel — extraction note (what is separable today and what is tangled)

A later project will lift a small simulation kernel out of `internal/sim` into its own
repository: seeded named PRNG streams, virtual clock and timers, event queue and loop,
event log and digest, seed and replay plumbing. Decision of 2026-11-21: NOT now; this
package stays whole and its API is frozen. This note records, from two reading checks
(builder and reviewer) of the code as landed, what an extraction will have to untangle.

## Already clean

- `internal/sim/stream.go`: stream derivation (`NewStream`, FNV-64a of the label). Imports
  `hash/fnv` and `math/rand/v2` only; no domain names.
- `internal/sim/simtest/seeds.go`: `-seed`, `DST_SEED*`, pinned seeds, `Seeds`, `Sweep`.
  Standard library only; no `sim` or `env` reference. Apart from the cluster builders
  (`cluster.go`).
- `simtest/determinism.go`: needs nine methods of `*sim.Sim` and the panic stack; a small
  interface would make it kernel-generic. It references `SeedsFile` from `seeds.go`.
- The heap in `queue.go` orders by `at`, `seq`, `hi` only.

## Tangles

- **Clock and timers (`clock.go`).** `local`/`toGlobal` are methods on the domain `node`
  (skew, drift); `timer` holds a `*handle`, the per-incarnation object that is also the
  Transport and Disk; `handle.closed` reads `node.inc`/`node.up`; `timer.arm` builds an
  event with the node index and incarnation and a sentinel peer `-1`; `timer.Stop` calls
  `Sim.cancel`, which adjusts hold counters on links and nodes. `MaxDriftPPM`, `MaxSkew`,
  skew validation and `Sim.LocalTime` sit in `sim.go`; `handle.Request` calls `toGlobal`.
- **Event and queue (`queue.go`).** `event` carries domain fields (incarnations, payload,
  call id, status code, fault, nemesis, timer, release bookkeeping) and the file defines
  the domain `evKind` and `disposition` enums. `evKind.network()`/`stream()` are in `sim.go`.
- **Loop (`sim.go`).** The loop does NOT treat kind/node/peer as opaque integers: `Step`
  asks `e.kind.network()` to pre-hash payloads; `exec` switches on every kind, indexes
  nodes and links, and owns the clog/pause hold-and-release accounting (the FIFO-at-release
  rule is network logic inside the loop). `Sim` mixes loop state with nodes, links, the
  code table, network config, the four named PRNGs and the log. `Config`/`New` couple loop
  construction to network defaults and the code table. `PanicError.Node` is `env.NodeID`;
  `guard` reads nodes to name it. Invariants, stall errors, `At` and `Workload` are
  kernel-shaped but live next to node management.
- **Event log and digest (`log.go`).** The log cannot be switched off or replaced: no
  interface, SHA-256 fixed, `LogCap <= 0` clamps to the default. `record` interprets
  `evKind` and encodes faults; node and peer are packed as `uint16` (under 65,535 actors,
  `-1` as `0xffff`); `Digests` combines a kernel digest (`Events`) with a domain one
  (`State`: node flags, incarnation, process state, disk layers) and calls `guard`. Log
  accessors are methods on `*Sim`, not on a log type.
- **Shared helpers.** `fault.go` owns `appendUvarint`/`appendBytes` (used by `disk.go` and
  `log.go`); `disk.go` owns `sortedKeys` (used by `nemesis.go`). `Disk` keeps a `*Sim`
  back-pointer only for its PRNG. `rand.go` holds the domain stream labels and
  `NodeStream(env.NodeID)`.
- **Nemesis.** Chaos phases are fault events carrying a `*Nemesis`, dispatched by `exec`.

## What an extraction would do first

Give the loop an event interface with an opaque kind and two integer actors; move
hold-and-release out of `exec` into the network; put the log behind an interface with a
no-op implementation; split `Digests` into kernel and domain halves; move clock math to a
type that takes skew and drift as values.
