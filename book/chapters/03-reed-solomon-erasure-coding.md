# Reed-Solomon Erasure Coding

Three-way replication survives two failures at 200% storage overhead. A `(k, m)` Maximum Distance Separable (MDS) erasure code splits a stripe into `k` equal-sized data shards and `m` parity shards (`n = k + m`). Any `k` shards reconstruct the stripe, tolerating `m` losses at `m/k` overhead — 50% at this repository's default `4+2` layout instead of 200%.

Reed-Solomon coding uses the finite field GF(2^8), mapping every byte onto a field element with no overflow. Addition is XOR (`a ^ b`); multiplication is polynomial multiplication modulo `x^8 + x^4 + x^3 + x^2 + 1` (`0x11D`), generator `2`. Precomputed log, exponential, and multiplication tables turn field math into byte lookups.

Encoding multiplies a `(k + m) × k` matrix `G` by the `k` data shards byte by byte. Stacking the `k × k` identity `I` over an `m × k` *Cauchy* matrix `C` (`G = [I; C]`, `C[i][j] = 1 / (x_i ^ y_j)` with distinct `x_i = k + i` and `y_j = j`) makes the code *systematic* — clean reads skip decoding — and ensures every square submatrix of `C` is invertible for `k + m <= 256`. To recover losses, the decoder inverts the `k` rows of `G` matching any `k` surviving shards via Gauss-Jordan elimination and multiplies to restore the data (re-encoding rebuilds parity). Crucially, this decoder corrects *erasures* (known missing positions), not silent *errors*; checksums and manifests must turn bad or forged shards into `nil` slots first.

## How erasure coding fits this repository

Two packages implement this layer. `internal/erasure` is pure computation (no `internal/env`, no I/O, no floats, no maps). `internal/storage` adds shard framing, a local chunk store, striping, manifests, placement, and a striped PUT/GET node running over the environment boundary from [Deterministic Simulation Testing](01-deterministic-simulation.md).

### Finite-field math and codec (`internal/erasure`)

- **GF(2^8) tables (`gf256.go`).** Init builds `expTable` (510 entries, skipping modulo), `logTable`, and a 64 KiB `mulTable[256][256]byte`. `Inv`, `Div`, and `Log` return `(value, ok)` because zero has no inverse or log. `MulAddSlice` computes `dst[i] ^= c * src[i]` with `0` and `1` fast paths.
- **Opaque matrices (`matrix.go`).** `Matrix` hides its slice, preventing out-of-range panics. `CodingMatrix(k, m)` builds `[I; C]` for `k >= 1`, `m >= 0`, `k + m <= 256` (`MaxShards = 256`; `m = 0` valid), checking `k > MaxShards || m > MaxShards-k` before adding so hostile integers return `ErrInvalidConfig`. `Invert` runs Gauss-Jordan on a copy (`ErrSingular` on zero pivot).
- **Codec (`codec.go`, `errors.go`).** `Split` allocates one buffer for all `k + m` shards, zero-padding to `ShardSize = (dataLen-1)/k + 1`. `Encode` writes parity with zero allocations when pre-sized; `Verify` checks parity (`ErrParityMismatch`). `ReconstructData` and `Reconstruct` rebuild data or all missing shards (`nil` missing; `[]byte{}` empty); `Join` strips padding.

### Shards, chunk store, and manifests (`internal/storage`)

Every shard blob carries a 64-byte `OSSH` v1 header plus payload (`shard.go`): magic `OSSH` (`0–3`), version `1` (`4`), reserved `0` (`5`), big-endian `uint16` `k`, `m`, index (`6–11`), `uint32` stripe (`12–15`) and payload length (`16–19`, max `MaxStripeSize = 64 MiB`), `uint64` `VersionID` (`20–27`), four zero bytes (`28–31`), and `SHA-256(bytes 0–31 || payload)` (`32–63`). Hashing bytes `0–31` catches flipped header fields such as shard or stripe index. `DecodeShard` rejects short, malformed, oversized, non-zero-reserved, or checksum-failing blobs as `ErrCorrupt`. Blob names follow `shards/<bucket>/<hh>/<version-hex16>/<stripe>.<index>` (`hh` = first byte of `SHA-256(bucket/version-hex)`).

`ChunkStore` (`chunkstore.go`) persists shards on `env.Disk`. `Put` verifies header identity and checksum before writing: identical bytes re-`Sync` and return `nil` (idempotent and durable even after an unsynced write); different valid bytes return `ErrExists`; corrupt stored bytes are overwritten and synced. `Delete` syncs on both found and `ErrNotFound` paths so a crash cannot resurrect an unsynced removal. `Get`/`GetBlob` verify every read (`ErrCorrupt` on damage, `ErrNotFound` joined with `env.ErrNotFound` when absent, `env.ErrIO` passed through).

`Manifest` (`manifest.go`) stores `Bucket`, `Key`, `Version`, `Size`, `ContentSHA`, `Layout`, and per-stripe `ShardRef{Node, Checksum}` in canonical `OMAN` v1 format (bounds-checked before allocating; re-encoded on decode). Comparing shard hashes against the manifest blocks forged or stale shards.

### Placement and the striped node (`placement.go`, `stripe.go`, `shardproto.go`, `node.go`)

Rendezvous (Highest Random Weight) hashing assigns each stripe's `n` shards to distinct nodes (`placement.go`), scoring members by the top 8 bytes of `SHA-256(stripeKey || 0x00 || nodeID)` (ties by `NodeID`) over a cloned, ID-sorted copy of `Membership.Members()`. To survive a region outage when topology allows, `regionCap` caps shards per region at `max(m, c*)`, where `c*` is the smallest feasible cap (`4+2` gives max 2 per region across three regions, `3+3` across two, `1/1/4` across `1/1/10`); `max(m, c*)` avoids churn on small region changes. For repair, `Replace` keeps live holders at their manifest indices within the cap and fills only emptied slots.

`Node` implements `env.Process`, serving `OpPutShard`, `OpGetShard`, and `OpDeleteShard` from its `ChunkStore` and mapping storage sentinels across the wire via codes `CodeAppMin+32..+36` (96–100, registered in `storage.Codes()`).

```mermaid
graph TD
  OBJ[Object bytes] --> RS[Split stripe: k data + m parity]
  RS --> FRAME[64B OSSH header + SHA-256]
  FRAME --> PLACE[Rendezvous + region cap]
  PLACE --> RPC[Send n OpPutShard RPCs]
  RPC --> QUORUM[k+1 durable replies?]
  QUORUM -->|yes| MAN[Advance window / return Manifest]
  QUORUM -->|no| FAIL[ErrUnavailable]
```

<!-- draw-visual: diagrams/03-write-path.mmd -->
```text
┌────────────────────────────────┐
│          Object bytes          │
└────────────────┬───────────────┘
                 ▼
┌────────────────────────────────┐
│Split stripe: k data + m parity │
└────────────────┬───────────────┘
                 ▼
┌────────────────────────────────┐
│   64B OSSH header + SHA-256    │
└────────────────┬───────────────┘
                 ▼
┌────────────────────────────────┐
│    Rendezvous + region cap     │
└────────────────┬───────────────┘
                 ▼
┌────────────────────────────────┐
│     Send n OpPutShard RPCs     │
└────────────────┬───────────────┘
                 ▼
┌────────────────────────────────┐
│      k+1 durable replies?      │
└────────────────┬───────────────┘
                 │
                 ├────────────────────────────┐
                 │                            │
                yes                          no
                 │                            │
                 ▼                            ▼
┌────────────────────────────────┐    ┌──────────────┐
│Advance window / return Manifest│    │ErrUnavailable│
└────────────────────────────────┘    └──────────────┘
```

On `Put`, the coordinator draws a `VersionID` from `env.Rand`, splits data into fixed stripes (default 1 MiB, last stripe short), and keeps up to `Window` (default 4) stripes awaiting quorum at once. Each stripe advances at `AckQuorum` (default `k + 1`, within `[k, k + m]`) durable replies and `Put` clears `op.data = nil` on finish so late reply closures do not pin the object buffer. Below quorum after all `n` replies, `Put` fails with `ErrUnavailable` joining all shard errors.

```mermaid
graph TD
  FETCH[Fetch k data shards] --> VERIFY[Verify header + manifest SHA]
  VERIFY -->|valid| DECODE[At k valid: invert + Join]
  VERIFY -->|rejected| MORE[Untried parity left?]
  MORE -->|yes| PARITY[Fetch next parity shard]
  MORE -->|no| RETRY[Retry transient errors]
  RETRY -->|none left| FAIL[ErrUnavailable]
```

<!-- draw-visual: diagrams/03-read-fallback.mmd -->
```text
┌────────────────────────────┐
│    Fetch k data shards     │
└──────────────┬─────────────┘
               ▼
┌────────────────────────────┐
│Verify header + manifest SHA│
└──────────────┬─────────────┘
               │
               ├──────────────────────────────────────┐
               │                                      │
             valid                                rejected
               │                                      │
               ▼                                      ▼
┌────────────────────────────┐            ┌──────────────────────┐
│ At k valid: invert + Join  │            │ Untried parity left? │
└────────────────────────────┘            └───────────┬──────────┘
                                                      │
               ┌──────────────────────────────────────┤
               │                                      │
              yes                                    no
               │                                      │
               ▼                                      ▼
┌────────────────────────────┐            ┌──────────────────────┐
│  Fetch next parity shard   │            │Retry transient errors│
└────────────────────────────┘            └───────────┬──────────┘
                                                      │
┌────────────────────────────┐                        │
│       ErrUnavailable       │◄──────none─left────────┘
└────────────────────────────┘
```

On `Get`, the coordinator validates the manifest, builds `man.Layout.Codec()` when `(k, m)` differs from the node's default, and fetches `k` data shards per active stripe. Each reply is checked against header, length, and manifest checksum; rejections fetch the next parity shard. Once all `n` shards are tried, transient errors (`env.ErrIO`, `env.ErrTimeout`, `env.ErrUnreachable`) retry after `RetryBackoff` (50 ms) up to `MaxShardAttempts` (4); `ErrCorrupt` and `ErrNotFound` never retry. At `k` valid shards the stripe decodes, and completion verifies `ContentSHA`. Callbacks always fire once asynchronously (`Clock.AfterFunc(0)` on early paths).

### Design choices and what each costs

- **Cauchy over Vandermonde.** Directly systematic and MDS for `k + m <= 256`; caps total shards at 256 in GF(2^8).
- **Per-call O(k^3) inversion, no cache.** Stateless, map-free, deterministic; ~64 ops at `k = 4`, ~2M ops per decode at `k = 128`.
- **Erasures only, guarded by hashes.** Simple decoder that blocks forgery; every read hashes the shard against a manifest.
- **Early quorum ack.** Advancing at `k + 1` avoids blocking on a paused holder, leaving up to `N - AckQuorum` requests per stripe in flight for up to `RequestTimeout` (1 s).
- **Buffered `Get` and placeholders.** Wire and codec stream per stripe, while `Node.Get` currently buffers the object. `StaticMembership`, random `VersionID`, and single-layout `Put` stand in until SWIM gossip, OCC metadata, per-bucket PUT layouts, streaming HTTP reads, anti-entropy, and shard GC arrive in coming chapters.

### Testing on the simulator

`TestStripedPutGetDST` runs 20 nodes across 3 regions (`4+2`, 16 KiB stripes, 11 objects from 0 B to 6 stripes + 4,093 B) on `internal/sim`. Each PUT callback verifies `>= k + 1` durable matching shards via `PeekDurable`. Once all shards are durable, a nemesis injects bit-rot, disk wipes, and per-blob read errors under `Budget{MaxDamagedPerGroup: m, GroupOf: storage.StripeGroup}` during 60 chaos GETs plus a final sweep from every region; a second mode lifts the budget and rots `m + 1` shards of one stripe. Seed `987691` caught three sequential read errors on one stripe staying within `m = 2` at every instant yet failing an unretried GET, motivating transient retries. Two canaries toggle `PlantSkipChecksum`: `TestCanaryChecksumSkipBreaksCorruptionProperty` catches bad bytes directly, and `TestCanaryChecksumSkipFailsStripedDST` fails on `ContentSHA` (`ErrCorrupt`).

## Key pieces in code

The GF(2^8) inner loop in [`internal/erasure/gf256.go`](../../internal/erasure/gf256.go) uses a 256-byte row lookup with zero and identity fast paths:

```go
func MulAddSlice(c byte, src, dst []byte) {
	n := min(len(src), len(dst))
	src, dst = src[:n], dst[:n]
	switch c {
	case 0:
		return
	case 1:
		for i, s := range src {
			dst[i] ^= s
		}
		return
	}
	t := &mulTable[c]
	for i, s := range src {
		dst[i] ^= t[s]
	}
}
```

Systematic Cauchy construction in [`internal/erasure/matrix.go`](../../internal/erasure/matrix.go):

```go
func CodingMatrix(k, m int) (Matrix, error) {
	if err := validConfig(k, m); err != nil {
		return Matrix{}, err
	}
	c := newMatrix(k+m, k)
	for i := 0; i < k; i++ {
		c.data[i*k+i] = 1
	}
	for i := 0; i < m; i++ {
		x := byte(k + i)
		row := c.row(k + i)
		for j := 0; j < k; j++ {
			// x >= k > j, so x^byte(j) is always non-zero and Inv succeeds.
			row[j], _ = Inv(x ^ byte(j))
		}
	}
	return c, nil
}
```

`shardExpect.verify` in [`internal/storage/stripe.go`](../../internal/storage/stripe.go) turns corruption, header mismatches, and forged payloads into `ErrCorrupt` erasures:

```go
func (e shardExpect) verify(index int, blob []byte, want *[32]byte) ([]byte, error) {
	h, payload, err := DecodeShard(blob)
	if err != nil {
		return nil, err
	}
	switch {
	case h.K != e.k || h.M != e.m:
		return nil, fmt.Errorf("storage: shard %d: code k=%d m=%d, want k=%d m=%d: %w", index, h.K, h.M, e.k, e.m, ErrCorrupt)
	case h.Index != index || h.Stripe != e.stripe || h.Version != e.version:
		return nil, fmt.Errorf("storage: shard %d: belongs to version %s stripe %d index %d: %w", index, h.Version, h.Stripe, h.Index, ErrCorrupt)
	case h.PayloadLen != e.shardSize:
		return nil, fmt.Errorf("storage: shard %d: payload %d bytes, want %d: %w", index, h.PayloadLen, e.shardSize, ErrCorrupt)
	case want != nil && h.Checksum != *want:
		return nil, fmt.Errorf("storage: shard %d: checksum differs from the manifest: %w", index, ErrCorrupt)
	}
	return payload, nil
}
```

`regionCap` in [`internal/storage/placement.go`](../../internal/storage/placement.go) computes `max(c*, m)` for region isolation:

```go
func regionCap(sizes []int, n, m int) int {
	total := 0
	for _, s := range sizes {
		total += s
	}
	if n < 1 || total < n {
		return 0
	}
	for c := 1; ; c++ {
		sum := 0
		for _, s := range sizes {
			sum += min(s, c)
		}
		if sum >= n {
			return max(c, m)
		}
	}
}
```

## What the tests show, and what they do not

All measurements were recorded in the 4-core Docker container (shared CPU; MB/s varies with load):

- **Throughput (`4+2`, 1 MiB stripe, pure Go).** `BenchmarkEncode1MiBStripe`: 481 MB/s, 0 allocs (483 MB/s, 2.17 ms/stripe earlier). `BenchmarkReconstructTwoDataShards1MiBStripe` (2 data missing): 394 MB/s (297 MB/s earlier). `BenchmarkReconstructAllMissing1MiBStripe` (1 data + 1 parity): 390 MB/s (211 MB/s under load).
- **Memory (`BenchmarkStripedPutGet8MiB`/`32MiB`, `-benchtime 1x`).** On 9 simulated nodes (1 MiB stripes, `Window = 4`, heap sampled after GC every 10 events): 8 MiB peaked at 36.9 MiB above baseline (12.0 MiB durable); 32 MiB peaked at 135.2 MiB (48.0 MiB durable). Subtracting two disk layers (~2× durable) and the buffered GET leaves ~5 MiB and ~7 MiB for coordinator state and in-flight messages (inferred).
- **Algebra, properties, fuzzing, race, coverage.** GF(2^8) tables matched bitwise multiplication for all 65,536 pairs; MDS passed exhaustively (`k <= 6, m <= 6`), sampled (`k, m <= 64`), and at `k = m = 128`. All `rapid` properties passed at 2,000 checks (`TestPropChunkStoreModel` also passed at 20,000 checks over two disk layers, unsynced ops, rot, and crashes). Three 30 s fuzz runs found 0 crashers and 0 skipped seeds across all 11 + 11 + 7 seeds (`FuzzGF256`: 245,137 execs; `FuzzErasureCoding`: 18,227 execs up to `k = 256`, 3 MiB; `FuzzShardDecode`: 230,868 execs). `go test -race` passed (`-count=3` on `internal/storage`). Coverage: 98.4% (`internal/erasure`), 97.8% (`internal/storage`); `detlint`, `go vet`, `gofmt`, `paniccheck`, and stdlib-only imports passed on all 12 product files.
- **DST 500-seed sweep (`TestStripedPutGetDST`, base 987654 + seed 987691 = 501 runs, 46,593 GETs/mode).** Within budget: 46,593 exact, 0 `ErrUnavailable`, 33,426 fallbacks, 22,201 faults applied, 22,912 rejected; max PUT 294 ms (bound 2 s), max GET 643 ms (bound 3 s), max mean requests/stripe 8 (bound 24), fewest durable shards at PUT ack 5 (`k + 1`). Lifted (plus `m + 1` shards rotted): 12,959 exact, 33,634 `ErrUnavailable`, 0 wrong bytes, max GET 872 ms, max mean requests/stripe 15.
- **Adversarial and regression tests.** An independent harness (separate polynomial/Cauchy/elimination oracle) ran 42 checks: 41 held; 1 (`window-pending-stripes`) stays broken against the old wording because `Window` was narrowed below. Regression tests pin four fixed defects: syncing `ChunkStore.Put`/`Delete` on identical-byte and absent paths, non-re-entrant `Node.Put` callbacks on placement failure, overflow-safe `k + m`/`ShardSize`/`Identity` bounds, and non-skipping fuzz seeds.

### Narrowed claims

1. **Availability within the fault budget.** Under the simulator's instantaneous budget (`MaxDamagedPerGroup = m`), permanent damage (bit-rot, wipe) never makes a GET unavailable; transient read errors can, if they keep hitting >`m` shards of one stripe during one GET faster than 4 attempts/shard outlast them (requiring `env.ErrIO` among causes; 0 observed in 46,593 GETs). *Why:* the budget bounds instantaneous damage, while a GET spans time and read errors are consumed on read (seed `987691`).
2. **What `Window` bounds.** `Window` bounds stripes awaiting quorum (PUT) or decode (GET), not individual requests in flight to slow holders. After quorum, up to `N - AckQuorum` requests/stripe (1 at `4+2`, quorum 5) remain outstanding until reply or `RequestTimeout` (1 s) — at most `stripes × (N - AckQuorum)` requests and `(N - AckQuorum)/k` of object size (25% at defaults; `1 + 1 + len(blobName) + 64 + ceil(L/k)` bytes each, or 256 KiB + 98 B for a 1 MiB stripe in bucket `"b"`). `Put` clears `op.data` at ack (`TestNodePutReleasesTheObjectAtTheAck`). *Why:* capping slow-holder requests by `Window` would throttle PUTs to `Window` stripes per `RequestTimeout` when one holder pauses.

### What the tests do not prove

Behavior is proven only on simulated `env.Disk` and `env.Transport`, not real OS filesystems, sockets, disk-full errors, sector torn writes, or concurrent goroutines in the integrated node. `TestStripedPutGetDST` does not crash nodes during PUT (crash durability of `ChunkStore` is proven by `TestPropChunkStoreModel`; DST checks `>= k + 1` durable shards at ack). Dynamic SWIM membership, OCC versions, per-bucket PUT layouts, `Get` size caps, streaming HTTP reads, anti-entropy, and shard GC wait for coming chapters. The DST request check bounds total requests at `stripes × 24` (logged value is a per-GET mean), and benchmarks use pure-Go tables without SIMD.

## Go deeper in the repo

- **Erasure package:** [`internal/erasure/gf256.go`](../../internal/erasure/gf256.go), [`internal/erasure/matrix.go`](../../internal/erasure/matrix.go), [`internal/erasure/codec.go`](../../internal/erasure/codec.go), [`internal/erasure/errors.go`](../../internal/erasure/errors.go).
- **Storage package:** [`internal/storage/shard.go`](../../internal/storage/shard.go), [`internal/storage/chunkstore.go`](../../internal/storage/chunkstore.go), [`internal/storage/stripe.go`](../../internal/storage/stripe.go), [`internal/storage/manifest.go`](../../internal/storage/manifest.go), [`internal/storage/placement.go`](../../internal/storage/placement.go), [`internal/storage/shardproto.go`](../../internal/storage/shardproto.go), [`internal/storage/node.go`](../../internal/storage/node.go), [`internal/storage/errors.go`](../../internal/storage/errors.go).
- **Named tests and canaries:** `TestGFTablesMatchBitwiseExhaustive`, `TestPropFieldAxioms` ([`internal/erasure/gf256_test.go`](../../internal/erasure/gf256_test.go)); `TestMDSExhaustiveSmall`, `TestPropMDS`, `TestMDSAtTheFieldLimit` ([`internal/erasure/matrix_test.go`](../../internal/erasure/matrix_test.go)); `TestPropRoundTrip`, `TestRoundTripEveryErasureSetSmall`, `TestPropFailureBoundary`, `TestDecodingIsTotalOnHostileShards`, `TestFuzzSeedsRunToCompletion` ([`internal/erasure/codec_test.go`](../../internal/erasure/codec_test.go), [`internal/erasure/fuzzseeds_test.go`](../../internal/erasure/fuzzseeds_test.go)); `TestPropCorruptionBecomesErasure`, `TestDecodeStripeRejectsForgedShards`, `TestCanaryChecksumSkipBreaksCorruptionProperty` ([`internal/storage/stripe_test.go`](../../internal/storage/stripe_test.go)); `TestPropChunkStoreModel` ([`internal/storage/chunkstore_test.go`](../../internal/storage/chunkstore_test.go)); `TestPropPlacementStable`, `TestReplaceKeepsLiveShards` ([`internal/storage/placement_test.go`](../../internal/storage/placement_test.go)); `TestNodePutAcksAtQuorum`, `TestNodeGetFallsBackToParity`, `TestNodeGetRetriesTransientErrors`, `TestNodePutWindowBoundsStripesAwaitingQuorum`, `TestNodePutReleasesTheObjectAtTheAck`, `TestNodeGetUsesTheManifestLayout` ([`internal/storage/node_test.go`](../../internal/storage/node_test.go), [`internal/storage/node_repair_test.go`](../../internal/storage/node_repair_test.go)); `TestStripedPutGetDST`, `TestCanaryChecksumSkipFailsStripedDST` ([`internal/storage/dst_test.go`](../../internal/storage/dst_test.go)).
- **Fuzz targets and benchmarks:** `FuzzGF256` ([`internal/erasure/gf256_test.go`](../../internal/erasure/gf256_test.go)), `FuzzErasureCoding` ([`internal/erasure/codec_test.go`](../../internal/erasure/codec_test.go)), `FuzzShardDecode` ([`internal/storage/shard_test.go`](../../internal/storage/shard_test.go)); [`internal/erasure/bench_test.go`](../../internal/erasure/bench_test.go), [`internal/storage/bench_test.go`](../../internal/storage/bench_test.go).
- **Adversarial harness and results:** `agent_work/adversarial/02-erasure/` (`ATTACKS.md`, `RESULTS.md`, and `run-all.sh`).

Replay the pinned transient-fault seed in the striped DST or run the 1 MiB codec benchmarks:

```sh
docker compose run --rm dev go test ./internal/storage -run '^TestStripedPutGetDST$' -seed=987691 -v -count=1
docker compose run --rm dev go test ./internal/erasure -run '^$' -bench . -benchmem
```

## Sources and further reading

- **Irving S. Reed and Gustave Solomon, "Polynomial Codes over Certain Finite Fields" (SIAM, 1960).** The finite-field polynomial construction behind MDS erasure codes.
- **James S. Plank, "A Tutorial on Reed-Solomon Coding for Fault-Tolerance in RAID-like Systems" (1997) and James S. Plank and Ying Ding, "Note: Correction to the 1997 Tutorial on Reed-Solomon Coding" (2005).** The byte-wise GF(2^8) implementation guide, plus why naive Vandermonde matrices can fail the MDS property.
- **Johannes Blömer, Malik Kalfane, Richard Karp, Marek Karpinski, Michael Luby, and David Zuckerman, "An XOR-Based Erasure-Resilient Coding Scheme" (ICSI TR-95-048, 1995).** Cauchy Reed-Solomon matrices whose every square submatrix is invertible over GF(2^w).
- **Backblaze engineering write-ups on Vaults and open-source Reed-Solomon (`JavaReedSolomon`).** Production tradeoffs in parity ratios, stripe sizing, table lookups, and shard repair.
- **Ceph documentation on Erasure-Coded Pools.** Mapping `(k, m)` erasure profiles onto failure domains so host or rack outages stay within `m` lost shards.
- **David G. Thaler and Chinya V. Ravishankar, "Using Name-Based Mappings to Increase Hit Rates" (1998).** Highest Random Weight (rendezvous) hashing for deterministic, minimal-movement placement without a central directory.
