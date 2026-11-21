# Brief — Chunk 3 BUILD: Merkle-tree anti-entropy (spec Commit 3)

Seat: Opus builder (fresh). Worktree: /root/Software/decentralized-object-store-work/03-merkle
(branch `chunk/03-merkle`). Work notes: `agent_work/03-merkle/`.

FIRST read `agent_work/03-merkle/objstore-p2-builder-rules.md` (binding), then PLAN.md sections
2.1–2.6, 2.9, 4.2 "Chunk 3", 5.1, 7.1, 7.4. The plan item "Chunk 3 — Merkle anti-entropy" in section 4.2 is your specification: its Deliverable
paragraph, its "Tests first" list (the test names are part of the contract — every named
test must exist and test what its name says) and its error rows in 2.6. This brief adds
detail; where it differs from PLAN.md wording, this brief wins (it carries later rulings).

## What this chunk is
"Implement Merkle-tree anti-entropy for data repair." A hash tree over `key → hash`, a diff protocol that descends only mismatched branches, and a worker that finds and repairs divergence between replicas.

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
`internal/merkle/` only (including the simple replicated chunk store used by its own DST tests, kept in test files or a clearly named test-support sub-package).
Plus `agent_work/03-merkle/build-notes.md`, appended lines in
`agent_work/gates/panic-allowlist.txt`, and — only if the determinism lint needs an
exemption you can justify — a request in your notes (do not edit `detlint-allow.txt`).
Other builders are working at the same time in other packages; touch nothing outside your
files. Declare the small interfaces you need from other chunks yourself (the plan says
which); do not import another chunk's package.

## Requirements beyond the plan item
- Tree shape per PLAN 7.4: fixed-fanout trie on the hash of the key, SHA-256, with domain separation between leaf and inner nodes and unambiguous key/value framing (the adversarial pass will try leaf/inner confusion and boundary ambiguity).
- The diff protocol runs over `env.Transport` request/response, one tree level per round trip, and must finish (or fail cleanly with an error) when messages are lost or the peer vanishes or lies; an honest side is never corrupted or looped by a lying peer (bounded work per diff).
- Repair goes through a `Repairer` interface you declare (fetch/put a key's bytes); peers come from a `Membership` interface you declare. Chunk 6 points both at the real store.
- Do NOT import `internal/storage` or `internal/erasure`: this chunk is independent; use your own minimal replicated store over `env.Disk` in the tests.
- Bit-rot in the simulator is silent: the worker must detect it by re-hashing stored bytes, not by trusting the stored tree.
- Errors per PLAN 2.6: sentinels and typed errors, `%w` wrapping, every sentinel produced
  and checked with `errors.Is` in a test. Errors cross the network only as status codes
  through the `env` code table (register your application rows the way the contract shows).
- Canary: `TestCanarySkippedBranch` (a diff that skips a mismatched branch behind a test-only switch must make a completeness test fail).
- DST scenarios run through `simtest` at the default seed count in `go test ./...`, and must
  hold at `DST_SEEDS=500` (the gate runs that). Every assertion bound (time to converge,
  time to detect, cost) is a named constant in the test with the measured value in the notes.

## Measurements to put in the build notes
Hashes compared and round trips for d differing keys out of N (table for N=1e3, 1e4, 1e5 and d=1, 10, 100) against the bound; bytes transferred per repaired byte; time-to-repair in the DST scenarios; wall time of default tests and the 500-seed sweep.

## The gate you must pass before you stop
Commit, then from the worktree root on the host run `agent_work/gates/gate.sh 03-merkle`. It
runs, in the dev container: gofmt, vet, build, the determinism lint (its rules are stricter
than they look — read `agent_work/gates/detlint/README-tools.txt` BEFORE writing code: a
fixed import allowlist for protocol packages, no floats, no goroutines/channels/mutexes, no
value that can print an address passed to `fmt`, a map range only in the collect-then-sort
shape), the panic allowlist, the dependency check, the no-comments check, unit tests with an
85% coverage floor on your package(s), rapid properties at 2,000 checks, every fuzz target
for 30 s, `-race`, and the 500-seed sweep. It must end `overall=pass`. Record its summary
in the build notes. Leave the result files under `agent_work/03-merkle/gates/` uncommitted.

## Out of scope
Anything in another chunk's package; the real runtime (sockets, files, goroutines — Chunk
6); HTTP; reader-facing prose, comments and CLI help (another seat); `cmd/` binaries unless
the plan item names one.
