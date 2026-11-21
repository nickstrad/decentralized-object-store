# Brief — Chunk 1 BUILD: SWIM gossip membership and failure detection (spec Commit 1)

Seat: Opus builder (fresh). Worktree: /root/Software/decentralized-object-store-work/01-gossip
(branch `chunk/01-gossip`). Work notes: `agent_work/01-gossip/`.

FIRST read `agent_work/01-gossip/objstore-p2-builder-rules.md` (binding), then PLAN.md sections
2.1–2.6, 2.9, 4.2 "Chunk 1", 5.1, 7.1, 7.7. The plan item "Chunk 1 — SWIM gossip" in section 4.2 is your specification: its Deliverable
paragraph, its "Tests first" list (the test names are part of the contract — every named
test must exist and test what its name says) and its error rows in 2.6. This brief adds
detail; where it differs from PLAN.md wording, this brief wins (it carries later rulings).

## What this chunk is
"Implement SWIM-like gossip protocol for cluster membership." Failure detection and membership dissemination that the rest of the store relies on to know who is alive and where.

## The simulator you build on (FROZEN — read before designing)
- `agent_work/0b-simulator/frozen-api.txt`: the exported API of `internal/env`,
  `internal/sim` and `internal/sim/simtest`.
- `agent_work/0b-simulator/build-notes.md`, section "Semantics Wave B builders must know
  (the contract)" and the later round sections: delivery classes, copy semantics, disk
  layers, fault budget, stall reporting, how a DST test is written with
  `simtest.RequireDeterministic`, seeds, the REPLAY line.
- `docs/knowledge/` entries on the simulator and the gate tooling.
- The toy protocol under `internal/sim/` shows the smallest complete process and its tests.
You may NOT edit `internal/env`, `internal/sim` or `simtest` (a gate rejects it). If you
need something they lack, write a "simulator change request" in your build notes, stop on
that point, and finish everything else. Your protocol code is a single-threaded state
machine that acts only through `env`; it never blocks.

## Files you own
`internal/gossip/` only.
Plus `agent_work/01-gossip/build-notes.md`, appended lines in
`agent_work/gates/panic-allowlist.txt`, and — only if the determinism lint needs an
exemption you can justify — a request in your notes (do not edit `detlint-allow.txt`).
Other builders are working at the same time in other packages; touch nothing outside your
files. Declare the small interfaces you need from other chunks yourself (the plan says
which); do not import another chunk's package.

## Requirements beyond the plan item
- Exposes a small `Membership` view (live members with ID, region and incarnation, in a deterministic order) and change events (joined, suspected, died, left). Other chunks declare their own `Membership` interfaces; keep yours easy to satisfy them with (Chunk 6 wires it).
- All protocol traffic is datagrams over `env.Transport.Send` except the periodic full-state sync, which may use request/response. Respect the transport's datagram size limit: piggyback only what fits; an oversize packet is an error path with a test.
- All timing through `env.Clock`, all randomness through the node's `env.Rand`. Protocol periods, suspicion multiplier, indirect-probe fan-out `k`, retransmit factor `λ` are configuration with stated defaults.
- Wire format: your own compact binary encoding, versioned, decoded defensively (`FuzzGossipWire`): malformed input is an error, never a panic.
- A node restarted with lost state and the same ID must be able to rejoin (incarnation handling) — say in the notes exactly what is guaranteed.
- Errors per PLAN 2.6: sentinels and typed errors, `%w` wrapping, every sentinel produced
  and checked with `errors.Is` in a test. Errors cross the network only as status codes
  through the `env` code table (register your application rows the way the contract shows).
- Canary: `TestCanaryNoIndirectProbe` (indirect probing switched off behind a test-only switch must make the asymmetric-link test fail).
- DST scenarios run through `simtest` at the default seed count in `go test ./...`, and must
  hold at `DST_SEEDS=500` (the gate runs that). Every assertion bound (time to converge,
  time to detect, cost) is a named constant in the test with the measured value in the notes.

## Measurements to put in the build notes
Periods-to-converge at N=20, 50, 100 (and 200 if it fits the time budget) with the fitted constant `c`; detection time for a crash; messages per node per period; wall time of the default tests and of the 500-seed sweep.

## The gate you must pass before you stop
Commit, then from the worktree root on the host run `agent_work/gates/gate.sh 01-gossip`. It
runs, in the dev container: gofmt, vet, build, the determinism lint (its rules are stricter
than they look — read `agent_work/gates/detlint/README-tools.txt` BEFORE writing code: a
fixed import allowlist for protocol packages, no floats, no goroutines/channels/mutexes, no
value that can print an address passed to `fmt`, a map range only in the collect-then-sort
shape), the panic allowlist, the dependency check, the no-comments check, unit tests with an
85% coverage floor on your package(s), rapid properties at 2,000 checks, every fuzz target
for 30 s, `-race`, and the 500-seed sweep. It must end `overall=pass`. Record its summary
in the build notes. Leave the result files under `agent_work/01-gossip/gates/` uncommitted.

## Out of scope
Anything in another chunk's package; the real runtime (sockets, files, goroutines — Chunk
6); HTTP; reader-facing prose, comments and CLI help (another seat); `cmd/` binaries unless
the plan item names one.
