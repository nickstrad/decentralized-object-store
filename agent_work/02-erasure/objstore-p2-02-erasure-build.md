# Brief — Chunk 2 BUILD: Reed-Solomon erasure coding and the chunk store (spec Commit 2)

Seat: Opus builder (fresh). Worktree: /root/Software/decentralized-object-store-work/02-erasure
(branch `chunk/02-erasure`). Work notes: `agent_work/02-erasure/`.

FIRST read `agent_work/02-erasure/objstore-p2-builder-rules.md` (binding), then PLAN.md sections
2.1–2.6, 2.9, 4.2 "Chunk 2", 5.1, 7.1, 7.2, 7.3. The plan item "Chunk 2 — Reed-Solomon erasure coding and the chunk store" in section 4.2 is your specification: its Deliverable
paragraph, its "Tests first" list (the test names are part of the contract — every named
test must exist and test what its name says) and its error rows in 2.6. This brief adds
detail; where it differs from PLAN.md wording, this brief wins (it carries later rulings).

## What this chunk is
"Implement Reed-Solomon erasure coding for object data." GF(2^8) arithmetic and a systematic Cauchy Reed-Solomon code, then the local chunk store, striping, the object manifest and region-aware placement built on it.

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
`internal/erasure/` and the chunk-store, shard-format, striping, manifest and placement files in `internal/storage/` (Chunk 6 later adds the HTTP object API to that package in other files — keep yours cohesive and name them for what they hold).
Plus `agent_work/02-erasure/build-notes.md`, appended lines in
`agent_work/gates/panic-allowlist.txt`, and — only if the determinism lint needs an
exemption you can justify — a request in your notes (do not edit `detlint-allow.txt`).
Other builders are working at the same time in other packages; touch nothing outside your
files. Declare the small interfaces you need from other chunks yourself (the plan says
which); do not import another chunk's package.

## Requirements beyond the plan item
- Defaults (ruling OQ-15): `k=4, m=2` across 3 regions, two shards per region, so any one region can be lost; a PUT is acknowledged at `k+1` durable shards. `k` and `m` are configuration; `k+m ≤ 255` (or the exact limit your construction has — test both sides of it).
- `internal/erasure` is pure computation: no `env`, no I/O. A corrupted shard is detected by its SHA-256 in the storage layer and handed to the decoder as an erasure (7.3) — the code corrects erasures, not unknown errors; say so.
- The chunk store sits on `env.Disk` only (written vs durable layers: write, then `Sync`, means durable; read verifies the checksum; a torn or rotted shard is `ErrCorrupt`, never wrong bytes).
- Placement: rendezvous hashing over a `Membership` interface YOU declare (a list of members with ID and region is enough); region-aware so no region holds more than `m` shards of a stripe when regions allow. Deterministic: no map-order dependence.
- `TestStripedPutGetDST` uses a static membership list and the simulator's fault budget with `GroupOf` mapping a shard blob to its stripe ("no more than m damaged shards per stripe"); lifting the budget must give `ErrUnavailable`, never wrong bytes.
- Integer arithmetic only (no floats) — the lint enforces it in these packages.
- Errors per PLAN 2.6: sentinels and typed errors, `%w` wrapping, every sentinel produced
  and checked with `errors.Is` in a test. Errors cross the network only as status codes
  through the `env` code table (register your application rows the way the contract shows).
- Canary: Plant one of your choice and name it `TestCanary...` (for example a decoder that skips checksum verification must make `TestPropCorruptionBecomesErasure` fail).
- DST scenarios run through `simtest` at the default seed count in `go test ./...`, and must
  hold at `DST_SEEDS=500` (the gate runs that). Every assertion bound (time to converge,
  time to detect, cost) is a named constant in the test with the measured value in the notes.

## Measurements to put in the build notes
Encode and reconstruct throughput (MB/s) for k=4,m=2 at 1 MiB stripes; memory high-water for a multi-MB object; wall time of the default tests, the fuzz targets (execs in 30 s) and the 500-seed sweep.

## The gate you must pass before you stop
Commit, then from the worktree root on the host run `agent_work/gates/gate.sh 02-erasure`. It
runs, in the dev container: gofmt, vet, build, the determinism lint (its rules are stricter
than they look — read `agent_work/gates/detlint/README-tools.txt` BEFORE writing code: a
fixed import allowlist for protocol packages, no floats, no goroutines/channels/mutexes, no
value that can print an address passed to `fmt`, a map range only in the collect-then-sort
shape), the panic allowlist, the dependency check, the no-comments check, unit tests with an
85% coverage floor on your package(s), rapid properties at 2,000 checks, every fuzz target
for 30 s, `-race`, and the 500-seed sweep. It must end `overall=pass`. Record its summary
in the build notes. Leave the result files under `agent_work/02-erasure/gates/` uncommitted.

## Out of scope
Anything in another chunk's package; the real runtime (sockets, files, goroutines — Chunk
6); HTTP; reader-facing prose, comments and CLI help (another seat); `cmd/` binaries unless
the plan item names one.
