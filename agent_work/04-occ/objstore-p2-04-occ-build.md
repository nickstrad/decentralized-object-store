# Brief — Chunk 4 BUILD: Optimistic concurrency control for metadata (replaces spec Commit 4)

Seat: Opus builder (fresh). Worktree: /root/Software/decentralized-object-store-work/04-occ
(branch `chunk/04-occ`). Work notes: `agent_work/04-occ/`.

FIRST read `agent_work/04-occ/objstore-p2-builder-rules.md` (binding), then PLAN.md sections
2.1–2.6, 2.9, 4.2 "Chunk 4", 5.1, 7.1, 7.5, and ruling OQ-7 and the OCC amendment in 9.1. The plan item "Chunk 4 — Optimistic concurrency control for metadata" in section 4.2 is your specification: its Deliverable
paragraph, its "Tests first" list (the test names are part of the contract — every named
test must exist and test what its name says) and its error rows in 2.6. This brief adds
detail; where it differs from PLAN.md wording, this brief wins (it carries later rulings).

## What this chunk is
Nick's OCC amendment: metadata is protected by optimistic concurrency control instead of CRDTs. The rule this chunk proves: NO WRITE IS EVER LOST SILENTLY. A writer states what it believes the current version is; if wrong, the write is refused; if two writers could not see each other, both versions are kept as siblings until someone resolves them. Last-writer-wins applies to scalar metadata fields only.

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
`internal/occ/` only.
Plus `agent_work/04-occ/build-notes.md`, appended lines in
`agent_work/gates/panic-allowlist.txt`, and — only if the determinism lint needs an
exemption you can justify — a request in your notes (do not edit `detlint-allow.txt`).
Other builders are working at the same time in other packages; touch nothing outside your
files. Declare the small interfaces you need from other chunks yourself (the plan says
which); do not import another chunk's package.

## Requirements beyond the plan item
- Build exactly the list under "Deliverable" in the plan item: version vectors and HLC; versioned records with siblings and a deterministic ETag; check-and-set `Put`/`Delete` with `If-Match` / `If-None-Match: *` / none; replica merge (commutative, associative, idempotent, total — `Merge` cannot fail on any decoded state); resolving writes; LWW scalar fields; OCC-checked quota counters with a recompute hook; canonical encoding; the typed metadata document; the replicator over a `Membership` interface you declare (digest first, state on mismatch) on `env.Transport`.
- Actor identity must survive the classic hazard: an actor ID reused after a wipe with its counter reset must not make new writes look dominated. Use the identity/epoch facilities the simulator contract gives a wiped-and-rejoined node (see the contract section of the 0b build notes) and state the guarantee.
- No CRDT types (LWW-Element-Set, OR-Set, PN-Counter) are built. No client-side replica (OQ-7): node-level sync only.
- HLC uses `env.Clock` only; state what happens at skew and at counter overflow.
- Sibling count must be bounded by concurrent writers, not by writes — measure it.
- Errors per PLAN 2.6: sentinels and typed errors, `%w` wrapping, every sentinel produced
  and checked with `errors.Is` in a test. Errors cross the network only as status codes
  through the `env` code table (register your application rows the way the contract shows).
- Canary: `TestCanaryMergeDropsSibling` (a merge that drops a concurrent sibling behind a test-only switch must make `TestPropNoSilentLoss` or the convergence DST fail).
- DST scenarios run through `simtest` at the default seed count in `go test ./...`, and must
  hold at `DST_SEEDS=500` (the gate runs that). Every assertion bound (time to converge,
  time to detect, cost) is a named constant in the test with the measured value in the notes.

## Measurements to put in the build notes
Sibling counts under the partitioned workload (max and distribution); merge cost for a document of 1e3 and 1e4 records; convergence time after heal in the DST scenarios; quota overshoot under partition; wall time of default tests, fuzz execs in 30 s, and the 500-seed sweep.

## The gate you must pass before you stop
Commit, then from the worktree root on the host run `agent_work/gates/gate.sh 04-occ`. It
runs, in the dev container: gofmt, vet, build, the determinism lint (its rules are stricter
than they look — read `agent_work/gates/detlint/README-tools.txt` BEFORE writing code: a
fixed import allowlist for protocol packages, no floats, no goroutines/channels/mutexes, no
value that can print an address passed to `fmt`, a map range only in the collect-then-sort
shape), the panic allowlist, the dependency check, the no-comments check, unit tests with an
85% coverage floor on your package(s), rapid properties at 2,000 checks, every fuzz target
for 30 s, `-race`, and the 500-seed sweep. It must end `overall=pass`. Record its summary
in the build notes. Leave the result files under `agent_work/04-occ/gates/` uncommitted.

## Out of scope
Anything in another chunk's package; the real runtime (sockets, files, goroutines — Chunk
6); HTTP; reader-facing prose, comments and CLI help (another seat); `cmd/` binaries unless
the plan item names one.
