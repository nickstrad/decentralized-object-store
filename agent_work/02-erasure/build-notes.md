# Chunk 2 build notes — Reed-Solomon erasure coding and the chunk store

Builder: Opus (fresh). Branch `chunk/02-erasure`. Spec: PLAN.md 4.2 "Chunk 2", 2.6, 7.2, 7.3;
brief `objstore-p2-02-erasure-build.md`.

## What was built

`internal/erasure` (pure computation: no `env`, no I/O, no floats, no maps in product code):

- `gf256.go` — GF(2^8) over `x^8+x^4+x^3+x^2+1` (0x11D), generator 2. `expTable` (doubled to
  510 entries so `exp[log a + log b]` needs no modulo), `logTable`, and a full 64 KiB
  `mulTable[a][b]` built at package init from exp/log. `Add`, `Mul`, `Div`, `Inv`, `Exp`,
  `Log`, `MulAddSlice` (dst ^= c·src over the shorter length, with 0 and 1 fast paths).
  `Inv`, `Div`, `Log` return `(value, ok)`: zero has no inverse/log, and that is not an error.
- `matrix.go` — opaque row-major `Matrix` (no exported indexing, so no out-of-range panic
  through the API): `NewMatrix` (copies, rejects empty/ragged), `Identity`, `Dims`, `Rows`
  (copy), `Equal`, `Mul`, `SelectRows`, `Invert` (Gauss-Jordan with row swaps, works on a
  copy, `ErrSingular` on a zero pivot column, `ErrInvalidConfig` if not square),
  `CodingMatrix(k, m)`, `MaxShards = 256`.
- `codec.go` — `Codec` from `New(k, m)`: `K/M/N`, `ShardSize`, `Split` (one allocation,
  zero-padded, parity slots allocated), `Encode` (reuses parity buffers of the right
  length), `Verify`, `Reconstruct` (data and parity), `ReconstructData` (data only), `Join`.
  A nil slot is a missing shard; a present zero-length shard is `[]byte{}`.
- `errors.go` — `ErrInvalidConfig`, `ErrTooFewShards`, `ErrShardSize` (PLAN 2.6) plus
  `ErrShardCount` (wrong number of slots), `ErrSingular`, `ErrParityMismatch` (Verify), and
  `*ShardError{Index, Err}`.

`internal/storage` (my files; Chunk 6 adds the HTTP API in other files):

- `errors.go` — `ErrNotFound`, `ErrCorrupt`, `ErrExists` (PLAN 2.6) plus `ErrUnavailable`
  (the object-API row) and `ErrInvalid`; `*ObjectError{Op, Bucket, Key, Err}`; wire codes
  `CodeNotFound..CodeInvalid` = `CodeAppMin+32 .. +36` (96–100), `CodeEntries()`, `Codes()`
  (= `env.NewCodeTable(CodeEntries()...)`, passed as `sim.Config.Codes`).
- `shard.go` — shard blob format, blob names, `VersionID` (uint64, printed as 16 lowercase
  hex), `ShardID{Bucket, Version, Stripe, Index}`, `ValidBucket`, `ParseBlobName`,
  `StripeGroup` (the `Budget.GroupOf` function), `EncodeShard`, `DecodeShard`,
  `BlobChecksum`.
- `chunkstore.go` — `ChunkStore` over `env.Disk`: `Put`, `Get`, `GetBlob`, `Delete`, `List`,
  `ListVersion`.
- `stripe.go` — `Layout{K, M, StripeSize}` (`DefaultLayout` = 4+2, 1 MiB; `MaxStripeSize`
  64 MiB), `Stripes`, `StripeLen`, `Codec`, `EncodeStripe`, `DecodeStripe`.
- `manifest.go` — `Manifest{Bucket, Key, Version, Size, ContentSHA, Layout, Stripes[]
  {Shards[]{Node, Checksum}}}`, `Validate`, `Encode`, `DecodeManifest`, `ShardID`.
- `placement.go` — `Member{ID, Region}`, the `Membership` interface (declared here, Chunk 1
  satisfies it later), `StaticMembership`, `StripeKey`, `RegionCap`, `Place`, `Replace`.
- `shardproto.go` — request wire format: `Op` (`OpPutShard/OpGetShard/OpDeleteShard`),
  `EncodeShardRequest`, `DecodeShardRequest`.
- `node.go` — `Node` (an `env.Process`): serves shard requests from its chunk store, and as
  a coordinator runs `Put(bucket, key, data, cb)` and `Get(manifest, cb)`; `Config`
  (`Layout`, `Members`, `AckQuorum`, `RequestTimeout`, `Window`), `NodeStats`, `Store()`.

## Design choices and why

**Cauchy matrix.** `C[i][j] = 1 / (x_i + y_j)` with `x_i = k+i` (i < m) and `y_j = j`
(j < k). The x's and y's are distinct field elements as long as `k+m ≤ 256`, so every square
submatrix of C is invertible and `[I; C]` is MDS. Exact limit of this construction:
**k ≥ 1, m ≥ 0, k+m ≤ 256** (k = 256, m = 0 is valid; k+m = 257 is not). Both sides are in
`TestNewConfigTable`, `TestCodingMatrixIsSystematicCauchy`, `TestLayoutTable` and
`TestEncodeShardRejectsBadHeaders`. m = 0 is a valid (no-parity) code.

**Erasures, not errors.** The code corrects up to m erasures (known positions). It does not
locate errors. A corrupted shard is found by the storage layer (SHA-256, header checks,
manifest checksum) and handed to the decoder as missing (`nil`). `TestPropCorruptionBecomesErasure`
is the proof; `TestDecodeStripeRejectsForgedShards` shows what the decoder would do with a
self-consistent forged shard if the manifest checksum were not compared (it decodes wrong
bytes) — the manifest checksum is the defence against forged or stale shards.

**Decode** picks the first k present shards, inverts that k×k submatrix (O(k³), done per
call; no inverse cache) and multiplies. Rebuilt parity is re-encoded from the data.

**Shard blob (64-byte header + payload).**

| bytes | field |
|---|---|
| 0–3 | magic `OSSH` |
| 4 | format version 1 |
| 5 | reserved, must be 0 |
| 6–7 | k (uint16 BE) |
| 8–9 | m |
| 10–11 | shard index |
| 12–15 | stripe index (uint32) |
| 16–19 | payload length (uint32) |
| 20–27 | object version ID (uint64) |
| 28–31 | reserved, must be 0 |
| 32–63 | SHA-256(bytes 0–31 ‖ payload) |

The checksum covers the header fields, not only the payload, so a flipped header bit (say
the index) is caught even though the payload is intact. Decoding rejects (as `ErrCorrupt`):
short, wrong magic/version, non-zero reserved bytes, invalid k/m/index, payload length that
disagrees with the blob length (truncated or oversized), checksum mismatch. A blob is
accepted only if it re-encodes byte for byte (`FuzzShardDecode` checks that).
Blob name: `shards/<bucket>/<hh>/<version-hex16>/<stripe>.<index>`, `hh` = first byte of
SHA-256(`bucket/version-hex`) in hex. `ParseBlobName` accepts only the canonical form
(re-derives the name and compares). Bucket names: 1–63 bytes of `[a-z0-9.-]`, first byte
alphanumeric.

**Chunk store.** `Put` verifies the blob decodes and names the shard it is stored under
(else `ErrCorrupt`, nothing written); if a valid shard with other bytes is stored, `ErrExists`
(the first write wins); identical bytes are a no-op (idempotent, so a duplicated request is
safe); a stored blob that fails verification is overwritten (repair). Write then `Sync`
before returning, so a returned `Put` is durable (`TestChunkStoreWritesAreDurable` checks the
durable layer). `Get`/`GetBlob` verify every read; a missing blob is `ErrNotFound` joined with
`env.ErrNotFound`; an injected I/O error is passed through (`env.ErrIO`, not `ErrCorrupt`).
`Delete` syncs the removal. `List(bucket)`/`ListVersion` return parsed IDs in disk order
(sorted by name) and skip names that are not canonical shard names.

**Striping and streaming.** Fixed stripes of `StripeSize` bytes; the last one is short. A
stripe of L bytes becomes k data shards of `ceil(L/k)` bytes (zero-padded) plus m parity
shards. The coordinator works stripe by stripe with at most `Window` (default 4) stripes in
flight, both for PUT and GET. The GET API returns the whole object as one `[]byte`
(see Requests).

**Manifest encoding.** `OMAN`, version 1, uvarint-length strings, BE uint64 version, uvarint
size/k/m/stripe size/stripe count, then per shard a node ID and its blob checksum.
`DecodeManifest` validates and then re-encodes and compares, so only the canonical encoding
is accepted (a non-minimal varint is `ErrCorrupt`). The stripe table size is checked against
the remaining input before allocating.

**Placement.** Rendezvous: score = first 8 bytes of SHA-256(stripe key ‖ 0x00 ‖ node ID),
highest first, ties by ID. Members are sorted by ID first, so the input order does not
matter, and duplicates/empty IDs are `ErrInvalid`. Region cap = `max(m, c*)`, where `c*` is
the smallest per-region cap for which the regions can hold n shards; greedy selection in
score order skips a member whose region is at the cap. So no region holds more than m shards
when regions allow, and otherwise the shards spread as evenly as the regions allow
(2 regions → 3+3; regions of 1, 1, 10 → 1, 1, 4). The cap is `max(m, c*)` rather than `c*`
so it does not move when a region grows or shrinks a little (stability). `Place` returns
nodes in selection order; shard i goes to element i.
`Replace(members, key, current, m)` is the repair-time function: it keeps every shard whose
node is still a member (and within the cap), and fills only the emptied slots from the
ranking. Stateless `Place` cannot keep shard *indices* stable when a holder leaves (the
replacement can rank anywhere); `Replace`, which uses the placement recorded in the manifest,
moves exactly the shards it must.

**Node protocol.** Request = op byte, uvarint-length blob name, body (blob for put; must be
empty otherwise). Responses: put/delete empty, get = the verified blob. Errors cross as codes
through the table. PUT: version ID from `env.Rand`; per stripe encode, place, send n put
requests; a stripe is acknowledged at `AckQuorum` (default `k+1`, configurable within
`[k, k+m]`) successful replies; the next stripe launches when the current one has quorum or
all replies; the PUT succeeds when every stripe has quorum (early, before slower replies) and
fails with `ErrUnavailable` (all per-shard errors joined) as soon as a stripe has all replies
and fewer than quorum. GET: requests the k data shards; each reply is verified (decode,
k/m, index, stripe, version, payload length, manifest checksum); a rejected shard triggers a
request for the next untried shard (parity); when no untried shard is left, shards that
failed with a transient error (`env.ErrIO`, `ErrTimeout`, `ErrUnreachable`) are retried
after `RetryBackoff` (50 ms, from `env.Clock`) up to `MaxShardAttempts` (4) per shard;
corrupt and missing shards are never retried. With k verified shards the stripe decodes.
The whole object's SHA-256 is compared with `ContentSHA` at the end (`ErrCorrupt` on
mismatch). Every callback fires exactly once; an empty object or an invalid argument calls
back through `AfterFunc(0)`, never inside the `Put`/`Get` call.

**Why the retry exists (found by the DST).** The fault budget bounds damage *at an instant*.
A GET spans time, and read errors are consumed by reads: seed 987691 injected read errors on
three shards of one stripe one after another, never more than two at once, and a GET that
saw all three failed. Retrying transient failures closes that. The claim is now stated
precisely (below). The seed is pinned in `internal/storage/testdata/seeds.txt`.

## Claims and the tests that prove them

| Claim | Test(s) |
|---|---|
| GF tables agree with bitwise multiplication for all 65,536 pairs | `TestGFTablesMatchBitwiseExhaustive` |
| Known products, inverses and powers of 2 for 0x11D (from an independent Python script) | `TestGFGoldenProducts`, `TestGFGoldenInverses`, `TestGFGoldenExp`, `TestGFLogExpAgree` |
| Field axioms (commutativity, associativity, distributivity, identities, inverses, `a·a⁻¹=1`, division) | `TestPropFieldAxioms`, `TestFieldAxiomsExhaustivePairs`, `FuzzGF256` |
| Matrix inversion is correct, both sides; singular and non-square are errors; input untouched | `TestMatrixInvert`, `TestMatrixInvertLeavesInputUnchanged` |
| Coding matrix is `[I | Cauchy]` with the stated entries | `TestCodingMatrixIsSystematicCauchy` |
| MDS: every k×k submatrix invertible — exhaustive for k ≤ 6, m ≤ 6; sampled to k,m ≤ 64; two at k = m = 128 | `TestMDSExhaustiveSmall`, `TestPropMDS`, `TestMDSAtTheFieldLimit` |
| Parity bytes match independently computed vectors (k=1/m=1, 2/1, 4/2, 3/3) | `TestEncodeKnownVectors` |
| Config: k=0, negative, k+m=256 valid, k+m=257 invalid, m=0 valid | `TestNewConfigTable` |
| Round trip: any payload (0, 1, primes, k±1, 2k±1, 4096±1, 65536±1, up to 8 KiB, multi-MB 1–3 MiB), any valid (k, m) up to 256, any ≤ m erasures data or parity, exact bytes; systematic | `TestPropRoundTrip`, `TestRoundTripMultiMB`, `TestRoundTripEveryErasureSetSmall` (exhaustive k ≤ 5, m ≤ 4) |
| More than m losses → `ErrTooFewShards`, shards untouched, no panic | `TestPropFailureBoundary`, `TestPropErasureCodingAnyLossSet`, `FuzzErasureCoding` |
| Decoding is total: hostile shard slices give a package sentinel, never a panic | `TestDecodingIsTotalOnHostileShards`, `TestShardErrors`, both fuzz targets |
| Up to m corrupted shards (bit flips anywhere, truncation, extension, empty, missing, swapped index, swapped stripe, swapped object version) are detected and treated as erasures; exact bytes; every damaged present shard reported; more than m → `ErrUnavailable`, never bytes | `TestPropCorruptionBecomesErasure` |
| A forged shard with a self-consistent checksum, a shard from another code, a shard of another length are rejected via header/manifest checks | `TestDecodeStripeRejectsForgedShards` |
| Shard decode never panics, never accepts a blob whose checksum or length disagrees, accepted blobs re-encode to themselves | `FuzzShardDecode`, `TestDecodeShardMalformedTable`, `TestShardHeaderGolden` (pins the header bytes) |
| Chunk store: missing → `ErrNotFound`; same bytes idempotent; other bytes `ErrExists`; rot/torn/misnamed → `ErrCorrupt`; corrupt is repaired by Put; wiped → `ErrNotFound`; I/O error passes through; durable on return | `TestChunkStoreTable`, `TestChunkStoreWipedDisk`, `TestChunkStoreWritesAreDurable`, `TestChunkStoreIOErrors`, `TestChunkStoreClosedDiskAndBadBuckets` |
| Chunk store against a reference model (put/delete/rot sequences, every key checked after every step) | `TestPropChunkStoreModel` |
| Placement: distinct nodes, ≤ m per region when regions allow, even spread otherwise, deterministic, member order irrelevant, errors | `TestPlacementTable`, `TestRegionCap`, `TestPlacementIgnoresMemberOrder` |
| Placement load spread: 3,000 stripes on 7/7/6 nodes, every node within [700, 1100] shards (expected 857 / 1,000) | `TestPlacementSpreadsLoad` |
| A membership change moves only the shards it must (when the region cap is unchanged): removing/adding a member that holds no shard changes nothing; removing a holder swaps exactly that node out and one in; `Replace` moves exactly 1 shard on removal and 0 on addition | `TestPropPlacementStable`, `TestReplaceKeepsLiveShards`, `TestReplaceWhenTheRegionCapShrinks` |
| Manifest encode/decode round trip, canonical only, malformed → `ErrCorrupt`, invalid → `ErrInvalid` | `TestManifestRoundTrip`, `TestManifestValidate`, `TestDecodeManifestMalformedTable` |
| Node: put/get of 0..4099 bytes from every node; shards durable where the manifest says; ≤ m per region; a clean GET fetches exactly k shards per stripe | `TestNodePutGet` |
| GET falls back to parity on rotted shards; > m rotted → `ErrUnavailable` in an `ObjectError` with per-shard `ErrCorrupt` causes that crossed the wire (`env.ErrRemote`) | `TestNodeGetFallsBackToParity` |
| PUT with fewer than k+1 reachable holders → `ErrUnavailable` (`env.ErrUnreachable` causes); with k+1 it succeeds; it acks at quorum without waiting for a paused node | `TestNodePutUnavailable`, `TestNodePutAcksAtQuorum`, `TestNodePutWithTooFewMembers` |
| Transient read errors are retried; persistent ones give `ErrUnavailable` with `env.ErrIO`; request count bounded | `TestNodeGetRetriesTransientErrors` |
| End-to-end content hash catches a wrong manifest | `TestNodeDetectsContentMismatch` |
| Every storage sentinel survives the wire code table; ObjectError message/unwrap | `TestCodeTableRoundTrip`, `TestObjectErrorMessage` |
| **DST, within budget**: 20 nodes, 3 regions, static membership, 11 objects (0 B … 6 stripes + 4,093 B, 16 KiB stripes); all shards durable before faults; nemesis of bit-rot, disk wipe and per-blob read errors under `MaxDamagedPerGroup = m` with `GroupOf = StripeGroup`; 60 GETs during chaos plus a final sweep from every region. Every GET returns exact bytes, or (only when a transient I/O error is among its causes) `ErrUnavailable` — never wrong bytes, never a hang; observed: 0 unavailable in 46,500 GETs over 500 seeds | `TestStripedPutGetDST/within-budget` |
| **DST, budget lifted**: same, plus m+1 shards of one stripe rotted on purpose: that object is always `ErrUnavailable`; every GET is exact bytes or `ErrUnavailable` | `TestStripedPutGetDST/budget-lifted` |
| The suites fail with checksum verification planted off | `TestCanaryChecksumSkipBreaksCorruptionProperty` (runs the rapid property on a recording TB, with and without the plant), `TestCanaryChecksumSkipFailsStripedDST` (the DST fails with `ErrCorrupt` from the end-to-end content hash) |

Narrowed claim (state it this way): *within the fault budget, permanent damage (bit-rot,
wipe) never makes a GET unavailable; a transient read error can, if read errors keep arriving
on more than m shards of a stripe over one GET's lifetime faster than four attempts per
shard can outlast them — the DST requires `env.ErrIO` among the causes in that case and
observed none in 500 seeds.* "Within budget" means the simulator's instantaneous budget.

Canary: `PlantSkipChecksum` (in `export_test.go`) sets a package variable that makes
`DecodeShard` skip the SHA-256 comparison. With it on, `TestPropCorruptionBecomesErasure`
fails (wrong bytes or an undetected damaged shard) and the striped DST fails (the end-to-end
content hash turns the wrong bytes into `ErrCorrupt`). Note: the DST alone would not show
wrong bytes for this canary because of that content-hash check; the per-shard property is the
direct proof.

DST assertion bounds (named constants in `dst_test.go`) and measured values:

| Constant | Bound | Measured max (500 seeds, base 987654, both modes) |
|---|---|---|
| `dstPutBound` (PUT ack latency) | 2 s | 298 ms |
| `dstGetBound` (GET completion) | 3 s | 643 ms within budget, 854 ms lifted |
| `dstMaxRequests` (shard requests per stripe) | (k+m)·4 = 24 | mean per stripe at most 9 within budget, 15 lifted |
| `dstInvariantEvery` (budget check period) | every 20 events | — |

Sweep totals (500 seeds, base 987654): within budget 46,500 GETs, all exact; 33,534 needed a
fallback; nemesis applied 22,122 faults and the budget rejected 23,006. Lifted: 46,500 GETs,
13,026 exact, 33,474 `ErrUnavailable`, 0 wrong bytes.

## Measurements (inside the `dev` container; 4-core droplet shared with other builders)

- Encode, k=4 m=2, 1 MiB stripe: **483 MB/s** (2.17 ms/stripe, 0 allocs).
- Reconstruct two missing data shards (`ReconstructData`), 1 MiB: **297 MB/s**.
- Reconstruct one data + one parity (`Reconstruct`), 1 MiB: **280 MB/s**.
  (`go test -run '^$' -bench . -benchmem ./internal/erasure`; MB/s is of stripe data.)
- Memory, PUT then GET through the simulator on 9 nodes, default layout (1 MiB stripes,
  window 4), live heap sampled after a forced GC every 10 events
  (`BenchmarkStripedPutGet8MiB/32MiB`, `-benchtime 1x`): 8 MiB object → peak live heap
  36.9 MiB above baseline with 12.0 MiB durable on the simulated disks; 32 MiB → 135.2 MiB
  with 48.0 MiB on disk. The disks hold written and durable layers (≈ 2 × disk), and the GET
  output is one object; what is left — the coordinator and the in-flight messages — is about
  5 MiB at 8 MiB and 7 MiB at 32 MiB, i.e. roughly constant (window × 1.5 MiB plus message
  copies). That split is my inference from the totals; I did not instrument the disks'
  internal sharing.
- Wall time, default `go test` (fixed seeds): `internal/erasure` ≈ 1.0 s,
  `internal/storage` ≈ 1.5–1.9 s.
- rapid at `-rapid.checks=2000`: erasure 9.1 s, storage 5.3 s.
- `-race`: erasure 33 s, storage 15 s.
- 500-seed sweep `DST_SEEDS=500 go test -run TestStripedPutGetDST ./internal/storage`:
  167 s test time (2 min 49 s wall with compile). Before the budget invariant was moved to
  every 20 events it was 410 s.
- Fuzz, 30 s each (`-fuzzminimizetime 5s`): `FuzzGF256` 267,607 execs; `FuzzErasureCoding`
  113,615 execs; `FuzzShardDecode` 424,725 execs. No crashers.

## Gate commands run (through Docker, before the gate)

- `gofmt -l internal` → empty. `go vet ./internal/...` → clean.
- detlint over `internal/erasure internal/storage` (`go run . -root /src ...`) → clean
  ("checked 12 files in 2 packages"). The gate itself lints only erasure (storage is not in
  its root list); I ran it on storage by hand.
- nocomments over both packages → clean. paniccheck over `internal` → clean.
- `go test -count=1 -cover`: erasure 98.8 %, storage 97.9 %.
- Gate result: see "Gate result" below.

## Uncovered branches (erasure 98.8 %, storage 97.9 %)

Unreachable by construction (defensive error paths kept so no error is dropped):
- `erasure/codec.go` `reconstruct`: error return from `rebuildData`; `rebuildData`: errors from
  `SelectRows` (rows come from present indices < n) and `Invert` (every k×k submatrix is
  invertible — that is the MDS property `TestPropMDS` proves).
- `storage/node.go` `NewNode`: `Layout.Codec` error after `Validate` passed; `putOp.launch`:
  the `EncodeStripe` error (stripe data ≤ `StripeSize` ≤ `MaxStripeSize`, valid code; the
  `Place` error path is tested by `TestNodePutWithTooFewMembers`); `putOp.finish`/`getOp.finish`: the already-finished guard (every caller checks
  first); `getOp.reply`: `decodeVerified` error after k verified shards of the right length.
- `storage/stripe.go` `EncodeStripe`: `Encode` and `EncodeShard` errors (shape already valid);
  `decodeVerified`: `Join` error (lengths verified).
- `storage/placement.go`: the score tie-break by ID (needs a 64-bit SHA-256 prefix collision);
  `Replace`: "no member left" (unreachable: the cap is always feasible for n ≤ members).
Not reachable with the simulator's disk:
- `storage/chunkstore.go` `Put`: `Write` and `Sync` errors, `Delete`: `Sync` error — the sim
  disk fails writes/syncs only when closed, and a closed disk already fails the read that
  precedes them.
- `storage/shard.go` `EncodeShard`: payload over `MaxStripeSize` (would need a 64 MiB
  allocation in a test; `EncodeStripe`'s identical check is tested).

## Errors intentionally ignored (`_ =`)

- `storage/shard.go` `checksum`, `storage/placement.go` `score`: `_, _ = h.Write(...)` on a
  `hash.Hash` (documented never to return an error).
- `erasure/matrix.go` `Invert` and `CodingMatrix`: `scale, _ := Inv(...)` / `row[j], _ = Inv(...)`
  drop a `bool`, not an error: the pivot / `x ^ y` is non-zero by construction.
- Test only: `_ = flag.Set("rapid.nofailfile", old)` in the canary cleanup.

## Panics added

None in product code. `panic-allowlist.txt` unchanged. (The test helper `nodeFactory`
panics on a bad config inside a simulator factory; paniccheck does not cover test files,
and the simulator reports it as a `PanicError`.)

## Things I did not do, and open points

- No inverse-matrix cache: inversion is O(k³) per stripe decode (64 multiply-adds at k=4,
  ~2M at k=128). Fine at the defaults.
- GET returns the whole object as one buffer; the wire and codec work stripe by stripe.
- No shard garbage collection, tombstones or background repair (later chunks). `Replace` is
  the placement half of repair.
- `VersionID` is a uint64 drawn from `env.Rand` by the PUT coordinator; Chunk 4 owns real
  version IDs and may replace this type.
- The cost assertion in the DST checks total shard requests ≤ stripes × 24; the logged
  "max mean requests per stripe" is a per-GET mean, not a per-stripe maximum.

## Requests to the orchestrator

1. Wire codes: storage registers `CodeAppMin+32 .. +36` (96–100). Other chunks should pick
   disjoint ranges; Chunk 6 merges the tables.
2. The detlint root list in `gate.sh` does not include `internal/storage`, so the gate does
   not lint my storage files (I ran detlint on them by hand: clean). Chunk 6 will add HTTP
   there, which detlint's import list forbids; if the protocol half should stay linted, it
   may need its own package (e.g. `internal/chunkstore`) — a layout decision for you.
3. Chunk 6 may want a streaming GET (a per-stripe callback) for large HTTP responses; the
   current `Get` buffers the object.
4. A knowledge entry candidate: the fault budget is instantaneous, and any protocol whose
   operations span time needs retries for transient faults or a narrower claim (seed 987691).

## Gate result

`agent_work/gates/gate.sh 02-erasure` from the host on commit
`a29de5342508a6149560be70a351d8ce3219bc3f` (clean tree; result file left uncommitted under
`agent_work/02-erasure/gates/`): **overall=pass, counts_for_landing=true**, image
`sha256:db6b715f…44f35`, go1.26.11 linux/amd64, comment baseline `ceb5438`.

| Gate | Check | Result | Time |
|---|---|---|---|
| G0 | tools, gofmt, vet, build, detlint, panics, deps, comments, selfcontained | pass | ≈ 11 s |
| G1 | unit + coverage floor | pass | 8 s |
| G2 | rapid, 2,000 checks, seed 178799038965000, 3 packages | pass | 82 s |
| G3 | fuzz, 4 targets × 30 s (incl. the simulator's) | pass | 131 s |
| G4 | race (+ `-count=3` on storage) | pass | 102 s |
| G5 | 500 seeds from base 178799038965000, whole module | pass | 273 s |
| G6 | docker | pass | 0 s |

The only commit after the gated one adds this file (operational notes, no Go).
