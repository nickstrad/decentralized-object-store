#!/usr/bin/env python3
# Generates the Wave B build briefs (chunks 1-5) from one template.
chunks = {
 "01-gossip": dict(n="1", title="SWIM gossip membership and failure detection (spec Commit 1)",
   plan="Chunk 1 — SWIM gossip", sections="2.1–2.6, 2.9, 4.2 \"Chunk 1\", 5.1, 7.1, 7.7",
   owns="`internal/gossip/` only.",
   what="\"Implement SWIM-like gossip protocol for cluster membership.\" Failure detection and membership dissemination that the rest of the store relies on to know who is alive and where.",
   extra="""- Exposes a small `Membership` view (live members with ID, region and incarnation, in a deterministic order) and change events (joined, suspected, died, left). Other chunks declare their own `Membership` interfaces; keep yours easy to satisfy them with (Chunk 6 wires it).
- All protocol traffic is datagrams over `env.Transport.Send` except the periodic full-state sync, which may use request/response. Respect the transport's datagram size limit: piggyback only what fits; an oversize packet is an error path with a test.
- All timing through `env.Clock`, all randomness through the node's `env.Rand`. Protocol periods, suspicion multiplier, indirect-probe fan-out `k`, retransmit factor `λ` are configuration with stated defaults.
- Wire format: your own compact binary encoding, versioned, decoded defensively (`FuzzGossipWire`): malformed input is an error, never a panic.
- A node restarted with lost state and the same ID must be able to rejoin (incarnation handling) — say in the notes exactly what is guaranteed.""",
   measure="Periods-to-converge at N=20, 50, 100 (and 200 if it fits the time budget) with the fitted constant `c`; detection time for a crash; messages per node per period; wall time of the default tests and of the 500-seed sweep.",
   canary="`TestCanaryNoIndirectProbe` (indirect probing switched off behind a test-only switch must make the asymmetric-link test fail)."),
 "02-erasure": dict(n="2", title="Reed-Solomon erasure coding and the chunk store (spec Commit 2)",
   plan="Chunk 2 — Reed-Solomon erasure coding and the chunk store", sections="2.1–2.6, 2.9, 4.2 \"Chunk 2\", 5.1, 7.1, 7.2, 7.3",
   owns="`internal/erasure/` and the chunk-store, shard-format, striping, manifest and placement files in `internal/storage/` (Chunk 6 later adds the HTTP object API to that package in other files — keep yours cohesive and name them for what they hold).",
   what="\"Implement Reed-Solomon erasure coding for object data.\" GF(2^8) arithmetic and a systematic Cauchy Reed-Solomon code, then the local chunk store, striping, the object manifest and region-aware placement built on it.",
   extra="""- Defaults (ruling OQ-15): `k=4, m=2` across 3 regions, two shards per region, so any one region can be lost; a PUT is acknowledged at `k+1` durable shards. `k` and `m` are configuration; `k+m ≤ 255` (or the exact limit your construction has — test both sides of it).
- `internal/erasure` is pure computation: no `env`, no I/O. A corrupted shard is detected by its SHA-256 in the storage layer and handed to the decoder as an erasure (7.3) — the code corrects erasures, not unknown errors; say so.
- The chunk store sits on `env.Disk` only (written vs durable layers: write, then `Sync`, means durable; read verifies the checksum; a torn or rotted shard is `ErrCorrupt`, never wrong bytes).
- Placement: rendezvous hashing over a `Membership` interface YOU declare (a list of members with ID and region is enough); region-aware so no region holds more than `m` shards of a stripe when regions allow. Deterministic: no map-order dependence.
- `TestStripedPutGetDST` uses a static membership list and the simulator's fault budget with `GroupOf` mapping a shard blob to its stripe ("no more than m damaged shards per stripe"); lifting the budget must give `ErrUnavailable`, never wrong bytes.
- Integer arithmetic only (no floats) — the lint enforces it in these packages.""",
   measure="Encode and reconstruct throughput (MB/s) for k=4,m=2 at 1 MiB stripes; memory high-water for a multi-MB object; wall time of the default tests, the fuzz targets (execs in 30 s) and the 500-seed sweep.",
   canary="Plant one of your choice and name it `TestCanary...` (for example a decoder that skips checksum verification must make `TestPropCorruptionBecomesErasure` fail)."),
 "03-merkle": dict(n="3", title="Merkle-tree anti-entropy (spec Commit 3)",
   plan="Chunk 3 — Merkle anti-entropy", sections="2.1–2.6, 2.9, 4.2 \"Chunk 3\", 5.1, 7.1, 7.4",
   owns="`internal/merkle/` only (including the simple replicated chunk store used by its own DST tests, kept in test files or a clearly named test-support sub-package).",
   what="\"Implement Merkle-tree anti-entropy for data repair.\" A hash tree over `key → hash`, a diff protocol that descends only mismatched branches, and a worker that finds and repairs divergence between replicas.",
   extra="""- Tree shape per PLAN 7.4: fixed-fanout trie on the hash of the key, SHA-256, with domain separation between leaf and inner nodes and unambiguous key/value framing (the adversarial pass will try leaf/inner confusion and boundary ambiguity).
- The diff protocol runs over `env.Transport` request/response, one tree level per round trip, and must finish (or fail cleanly with an error) when messages are lost or the peer vanishes or lies; an honest side is never corrupted or looped by a lying peer (bounded work per diff).
- Repair goes through a `Repairer` interface you declare (fetch/put a key's bytes); peers come from a `Membership` interface you declare. Chunk 6 points both at the real store.
- Do NOT import `internal/storage` or `internal/erasure`: this chunk is independent; use your own minimal replicated store over `env.Disk` in the tests.
- Bit-rot in the simulator is silent: the worker must detect it by re-hashing stored bytes, not by trusting the stored tree.""",
   measure="Hashes compared and round trips for d differing keys out of N (table for N=1e3, 1e4, 1e5 and d=1, 10, 100) against the bound; bytes transferred per repaired byte; time-to-repair in the DST scenarios; wall time of default tests and the 500-seed sweep.",
   canary="`TestCanarySkippedBranch` (a diff that skips a mismatched branch behind a test-only switch must make a completeness test fail)."),
 "04-occ": dict(n="4", title="Optimistic concurrency control for metadata (replaces spec Commit 4)",
   plan="Chunk 4 — Optimistic concurrency control for metadata", sections="2.1–2.6, 2.9, 4.2 \"Chunk 4\", 5.1, 7.1, 7.5, and ruling OQ-7 and the OCC amendment in 9.1",
   owns="`internal/occ/` only.",
   what="Nick's OCC amendment: metadata is protected by optimistic concurrency control instead of CRDTs. The rule this chunk proves: NO WRITE IS EVER LOST SILENTLY. A writer states what it believes the current version is; if wrong, the write is refused; if two writers could not see each other, both versions are kept as siblings until someone resolves them. Last-writer-wins applies to scalar metadata fields only.",
   extra="""- Build exactly the list under "Deliverable" in the plan item: version vectors and HLC; versioned records with siblings and a deterministic ETag; check-and-set `Put`/`Delete` with `If-Match` / `If-None-Match: *` / none; replica merge (commutative, associative, idempotent, total — `Merge` cannot fail on any decoded state); resolving writes; LWW scalar fields; OCC-checked quota counters with a recompute hook; canonical encoding; the typed metadata document; the replicator over a `Membership` interface you declare (digest first, state on mismatch) on `env.Transport`.
- Actor identity must survive the classic hazard: an actor ID reused after a wipe with its counter reset must not make new writes look dominated. Use the identity/epoch facilities the simulator contract gives a wiped-and-rejoined node (see the contract section of the 0b build notes) and state the guarantee.
- No CRDT types (LWW-Element-Set, OR-Set, PN-Counter) are built. No client-side replica (OQ-7): node-level sync only.
- HLC uses `env.Clock` only; state what happens at skew and at counter overflow.
- Sibling count must be bounded by concurrent writers, not by writes — measure it.""",
   measure="Sibling counts under the partitioned workload (max and distribution); merge cost for a document of 1e3 and 1e4 records; convergence time after heal in the DST scenarios; quota overshoot under partition; wall time of default tests, fuzz execs in 30 s, and the 500-seed sweep.",
   canary="`TestCanaryMergeDropsSibling` (a merge that drops a concurrent sibling behind a test-only switch must make `TestPropNoSilentLoss` or the convergence DST fail)."),
 "05-saga": dict(n="5", title="Saga engine and the bucket-migration workflow (spec Commit 5)",
   plan="Chunk 5 — Saga engine and the bucket-migration workflow", sections="2.1–2.6, 2.9, 4.2 \"Chunk 5\", 5.1, 7.1, 7.8, and ruling OQ-13 in 9.1",
   owns="`internal/saga/` only (including the model of the cluster its DST tests run against, in test files or a clearly named test-support sub-package).",
   what="\"Implement Saga orchestration for multi-step bucket migration.\" A durable saga orchestrator and the five-step migration workflow (allocate target → copy shards → verify Merkle root → flip routing pointer, the pivot → delete source), proven against a model of the cluster inside the simulator.",
   extra="""- The log is append-only and checksummed on `env.Disk`: intent is written AND synced before a step acts; recovery resumes or compensates from ANY log prefix, including a torn last record (the simulator can tear the first append of a new blob — see the 0b build notes); a damaged middle record is `ErrLogCorrupt`.
- Steps are small interfaces you declare (do/compensate with an idempotency key `saga ID + step`); the migration steps are written against those interfaces and a model cluster. Do NOT import `internal/storage`, `internal/merkle`, `internal/occ` or `internal/gossip`: Chunk 6 binds the real components and re-runs your matrix, so export the matrix runner in a form Chunk 6 can call with other step implementations.
- Retry is bounded with clock-driven backoff (`env.Clock`, integer arithmetic); `ErrPermanent` stops retry; compensations run in reverse order and are retried until they succeed; after the pivot the saga only goes forward.
- Stated limitation (OQ-13): a permanently lost coordinator leaves its saga unfinished; no log replication, no takeover. Say exactly what an operator would find on disk.""",
   measure="The failure-point matrix as a table (step × failure kind × torn/untorn → terminal state) with cell counts; retries per step under the nemesis; wall time of default tests and the 500-seed sweep.",
   canary="`TestCanarySkippedCompensation` (skipping one compensation behind a test-only switch must make a matrix cell fail)."),
}
T = '''# Brief — Chunk {n} BUILD: {title}

Seat: Opus builder (fresh). Worktree: /root/Software/object-store-work/{slug}
(branch `chunk/{slug}`). Work notes: `agent_work/{slug}/`.

FIRST read `agent_work/{slug}/objstore-p2-builder-rules.md` (binding), then PLAN.md sections
{sections}. The plan item "{plan}" in section 4.2 is your specification: its Deliverable
paragraph, its "Tests first" list (the test names are part of the contract — every named
test must exist and test what its name says) and its error rows in 2.6. This brief adds
detail; where it differs from PLAN.md wording, this brief wins (it carries later rulings).

## What this chunk is
{what}

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
{owns}
Plus `agent_work/{slug}/build-notes.md`, appended lines in
`agent_work/gates/panic-allowlist.txt`, and — only if the determinism lint needs an
exemption you can justify — a request in your notes (do not edit `detlint-allow.txt`).
Other builders are working at the same time in other packages; touch nothing outside your
files. Declare the small interfaces you need from other chunks yourself (the plan says
which); do not import another chunk's package.

## Requirements beyond the plan item
{extra}
- Errors per PLAN 2.6: sentinels and typed errors, `%w` wrapping, every sentinel produced
  and checked with `errors.Is` in a test. Errors cross the network only as status codes
  through the `env` code table (register your application rows the way the contract shows).
- Canary: {canary}
- DST scenarios run through `simtest` at the default seed count in `go test ./...`, and must
  hold at `DST_SEEDS=500` (the gate runs that). Every assertion bound (time to converge,
  time to detect, cost) is a named constant in the test with the measured value in the notes.

## Measurements to put in the build notes
{measure}

## The gate you must pass before you stop
Commit, then from the worktree root on the host run `agent_work/gates/gate.sh {slug}`. It
runs, in the dev container: gofmt, vet, build, the determinism lint (its rules are stricter
than they look — read `agent_work/gates/detlint/README-tools.txt` BEFORE writing code: a
fixed import allowlist for protocol packages, no floats, no goroutines/channels/mutexes, no
value that can print an address passed to `fmt`, a map range only in the collect-then-sort
shape), the panic allowlist, the dependency check, the no-comments check, unit tests with an
85% coverage floor on your package(s), rapid properties at 2,000 checks, every fuzz target
for 30 s, `-race`, and the 500-seed sweep. It must end `overall=pass`. Record its summary
in the build notes. Leave the result files under `agent_work/{slug}/gates/` uncommitted.

## Out of scope
Anything in another chunk's package; the real runtime (sockets, files, goroutines — Chunk
6); HTTP; reader-facing prose, comments and CLI help (another seat); `cmd/` binaries unless
the plan item names one.
'''
import sys
out = sys.argv[1]
for slug, c in chunks.items():
    open(f"{out}/objstore-p2-{slug}-build.md", "w").write(T.format(slug=slug, **c))
    print("wrote", slug)
