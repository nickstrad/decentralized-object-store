# Chunk 0b — the deterministic simulator: findings, narrowed claims, pitfalls

The contract protocol builders rely on is in `agent_work/0b-simulator/build-notes.md`
("Semantics Wave B builders must know"); the frozen exported API is
`agent_work/0b-simulator/frozen-api.txt`. This entry holds what is not obvious from those.

## Findings

- Replay holds across processes and with `GOMAXPROCS`/`GOGC` varied, for the toy protocol
  and for an outside harness's own protocol at 1–100 nodes (`TestReplayAcrossProcesses`;
  `agent_work/adversarial/0b-simulator/`).
- About 230,000 events per second at 100 nodes; the 500-seed gate sweep of the package
  takes about 100 s; rapid at 2,000 checks under a minute.
- Virtual time has a stated horizon, `sim.Horizon` (2^62 ns). A timer or timeout past it
  never fires; skew is bounded by `sim.MaxSkew`, drift by `sim.MaxDriftPPM`; out-of-range
  configuration is `env.ErrInvalidArgument` at `AddNode`/`New`/`StartNemesis`.
- Requests and responses between one ordered pair of nodes stay in send order across
  pause, clog, resume, unclog and heal (`TestPropStreamOrderPerConnection` found the
  original bug in seven cases).
- A blob's damage mark follows the DURABLE copy: an unsynced rewrite does not clear it.
- A torn crash can tear the first append of a new blob (a new log may be absent or a
  strict prefix after a crash).
- The event log hashes payload bytes as they crossed the wire, before any handler runs.

## Narrowed claims

- The nemesis's schedule — when it acts, on which node, with which kind of fault — depends
  only on the seed and the nemesis configuration. Which blob a disk fault hits is chosen
  from the blobs on that node at that instant, so it depends on what the protocol has
  written (`TestPropNemesisTimingIgnoresInventory`).
- Under a group budget (`Budget.GroupOf` set) the nemesis's read-error fault names one
  existing blob; without a group budget it is whole-node, as before.
- The simulator models datagram and request/response faults and a two-layer disk. It says
  nothing about real sockets, real file systems or the Go scheduler.

## Pitfalls

- `go test ./internal/sim/... -rapid.checks=2000` fails in `simtest`, which does not import
  rapid (the flag is undefined there). The gate runs rapid per importing package.
- The determinism lint allows a fixed list of standard packages; the simulator's one
  exemption (`runtime/debug` for the panic stack in failure reports) is a line in
  `agent_work/gates/detlint-allow.txt`.
- Running Astra's harness rewrites its tracked evidence files; commit or restore them
  before a gate run, or the tree is dirty and the result does not count.
- One attack (`clock-overflow`) still prints BROKEN after its repair, because its driver
  treats the new rejection of an absurd skew as a failure. See the chunk's `RESULTS.md`.
