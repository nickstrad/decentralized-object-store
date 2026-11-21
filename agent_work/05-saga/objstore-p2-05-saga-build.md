# Brief — Chunk 5 BUILD: Saga engine and the bucket-migration workflow (spec Commit 5)

Seat: Opus builder (fresh). Worktree: /root/Software/decentralized-object-store-work/05-saga
(branch `chunk/05-saga`). Work notes: `agent_work/05-saga/`.

FIRST read `agent_work/05-saga/objstore-p2-builder-rules.md` (binding), then PLAN.md sections
2.1–2.6, 2.9, 4.2 "Chunk 5", 5.1, 7.1, 7.8, and ruling OQ-13 in 9.1. The plan item "Chunk 5 — Saga engine and the bucket-migration workflow" in section 4.2 is your specification: its Deliverable
paragraph, its "Tests first" list (the test names are part of the contract — every named
test must exist and test what its name says) and its error rows in 2.6. This brief adds
detail; where it differs from PLAN.md wording, this brief wins (it carries later rulings).

## What this chunk is
"Implement Saga orchestration for multi-step bucket migration." A durable saga orchestrator and the five-step migration workflow (allocate target → copy shards → verify Merkle root → flip routing pointer, the pivot → delete source), proven against a model of the cluster inside the simulator.

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
`internal/saga/` only (including the model of the cluster its DST tests run against, in test files or a clearly named test-support sub-package).
Plus `agent_work/05-saga/build-notes.md`, appended lines in
`agent_work/gates/panic-allowlist.txt`, and — only if the determinism lint needs an
exemption you can justify — a request in your notes (do not edit `detlint-allow.txt`).
Other builders are working at the same time in other packages; touch nothing outside your
files. Declare the small interfaces you need from other chunks yourself (the plan says
which); do not import another chunk's package.

## Requirements beyond the plan item
- The log is append-only and checksummed on `env.Disk`: intent is written AND synced before a step acts; recovery resumes or compensates from ANY log prefix, including a torn last record (the simulator can tear the first append of a new blob — see the 0b build notes); a damaged middle record is `ErrLogCorrupt`.
- Steps are small interfaces you declare (do/compensate with an idempotency key `saga ID + step`); the migration steps are written against those interfaces and a model cluster. Do NOT import `internal/storage`, `internal/merkle`, `internal/occ` or `internal/gossip`: Chunk 6 binds the real components and re-runs your matrix, so export the matrix runner in a form Chunk 6 can call with other step implementations.
- Retry is bounded with clock-driven backoff (`env.Clock`, integer arithmetic); `ErrPermanent` stops retry; compensations run in reverse order and are retried until they succeed; after the pivot the saga only goes forward.
- Stated limitation (OQ-13): a permanently lost coordinator leaves its saga unfinished; no log replication, no takeover. Say exactly what an operator would find on disk.
- Errors per PLAN 2.6: sentinels and typed errors, `%w` wrapping, every sentinel produced
  and checked with `errors.Is` in a test. Errors cross the network only as status codes
  through the `env` code table (register your application rows the way the contract shows).
- Canary: `TestCanarySkippedCompensation` (skipping one compensation behind a test-only switch must make a matrix cell fail).
- DST scenarios run through `simtest` at the default seed count in `go test ./...`, and must
  hold at `DST_SEEDS=500` (the gate runs that). Every assertion bound (time to converge,
  time to detect, cost) is a named constant in the test with the measured value in the notes.

## Measurements to put in the build notes
The failure-point matrix as a table (step × failure kind × torn/untorn → terminal state) with cell counts; retries per step under the nemesis; wall time of default tests and the 500-seed sweep.

## The gate you must pass before you stop
Commit, then from the worktree root on the host run `agent_work/gates/gate.sh 05-saga`. It
runs, in the dev container: gofmt, vet, build, the determinism lint (its rules are stricter
than they look — read `agent_work/gates/detlint/README-tools.txt` BEFORE writing code: a
fixed import allowlist for protocol packages, no floats, no goroutines/channels/mutexes, no
value that can print an address passed to `fmt`, a map range only in the collect-then-sort
shape), the panic allowlist, the dependency check, the no-comments check, unit tests with an
85% coverage floor on your package(s), rapid properties at 2,000 checks, every fuzz target
for 30 s, `-race`, and the 500-seed sweep. It must end `overall=pass`. Record its summary
in the build notes. Leave the result files under `agent_work/05-saga/gates/` uncommitted.

## Out of scope
Anything in another chunk's package; the real runtime (sockets, files, goroutines — Chunk
6); HTTP; reader-facing prose, comments and CLI help (another seat); `cmd/` binaries unless
the plan item names one.
