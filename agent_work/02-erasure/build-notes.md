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
disagrees with the blob length (truncated or oversized), a payload over `MaxStripeSize`
(repair round), checksum mismatch. A blob is
accepted only if it re-encodes byte for byte (`FuzzShardDecode` checks that).
Blob name: `shards/<bucket>/<hh>/<version-hex16>/<stripe>.<index>`, `hh` = first byte of
SHA-256(`bucket/version-hex`) in hex. `ParseBlobName` accepts only the canonical form
(re-derives the name and compares). Bucket names: 1–63 bytes of `[a-z0-9.-]`, first byte
alphanumeric.

**Chunk store.** `Put` verifies the blob decodes and names the shard it is stored under
(else `ErrCorrupt`, nothing written); if a valid shard with other bytes is stored, `ErrExists`
(the first write wins); identical bytes are re-synced and then acknowledged (idempotent, so a
duplicated request is safe; repair round, finding 1); a stored blob that fails verification is overwritten (repair). Write then `Sync`
before returning, so a returned `Put` is durable (`TestChunkStoreWritesAreDurable` checks the
durable layer). `Get`/`GetBlob` verify every read; a missing blob is `ErrNotFound` joined with
`env.ErrNotFound`; an injected I/O error is passed through (`env.ErrIO`, not `ErrCorrupt`).
`Delete` syncs the removal, also when it reports `ErrNotFound` (repair round). `List(bucket)`/`ListVersion` return parsed IDs in disk order
(sorted by name) and skip names that are not canonical shard names.

**Striping and streaming.** Fixed stripes of `StripeSize` bytes; the last one is short. A
stripe of L bytes becomes k data shards of `ceil(L/k)` bytes (zero-padded) plus m parity
shards. The coordinator works stripe by stripe with at most `Window` (default 4) stripes
awaiting their quorum (PUT) or their decode (GET) at once. Requests to slow holders can stay
outstanding after that; see "Repair round", C30, for what `Window` does not bound. The GET API returns the whole object as one `[]byte`
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
mismatch). Every callback fires exactly once; an empty object, an invalid argument or a
placement/encode error calls back through `AfterFunc(0)`, never inside the `Put`/`Get` call
(the placement case was fixed in the repair round, finding 2).

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
- Reconstruct one data + one parity (`Reconstruct`), 1 MiB: superseded, see "Repair round"
  (the benchmark erased two data shards before the repair round, finding 8).
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
- `storage/shard.go` `EncodeShard`: payload over `MaxStripeSize` — covered since the repair
  round by `TestDecodeShardRejectsPayloadOverMaxStripeSize`.

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

## Repair round (review 1: FAIL, four MAJOR; adversarial C30 BROKEN)

Fresh Opus builder. Tests first: commit `4b7ac61` adds the regression tests and fails on the
reviewed code (each failure listed below was run and seen); `30c9689` fixes; `aad5cc0` adds
the object-buffer release; `096560a` fixes a bug in the new model test (not the store).

### Finding 1 (MAJOR) — `Put` acknowledged bytes that were only in the unsynced layer
- Tests (fail on the old code): `TestChunkStorePutOfUnsyncedIdenticalBytesIsDurable` (the
  reviewer's reproduction: unsynced write, `Put` of the same bytes, crash, restart, `Get`
  → was `ErrNotFound`), `TestChunkStoreSecondPutAfterAFailedSyncIsDurable` (two Puts after
  the write, crash, `PeekDurable`), `TestChunkStoreDeleteOfAnUnsyncedRemovalIsDurable`.
- `TestPropChunkStoreModel` now models the two disk layers: actions `put`, `delete`, `rot`,
  `unsynced write` and `unsynced delete` (raw `env.Disk` calls standing in for an earlier
  call whose `Sync` failed), and `crash` (crash + restart, the store rebuilt on the new
  disk handle; the written layer reverts to the durable one). After every acknowledged
  `Put` it checks `PeekDurable` holds exactly those bytes; after every `Delete` (either
  result) that nothing durable remains; after every step every key and `List` against the
  written layer. On the old code it fails in 2–11 checks. At 20,000 checks it passes.
- Same audit on `Delete`: a `Delete` that found the name already removed in the written
  layer returned `ErrNotFound` without syncing, so after a crash the shard came back.
  Fixed the same way: `Delete` syncs before reporting `ErrNotFound`. A `Sync` that itself
  reports `env.ErrNotFound` (nothing ever reached the durable layer) is not an error.
- Change: `ChunkStore.Put` calls `Sync` in the identical-bytes branch; `ChunkStore.Delete`
  syncs on both paths. "`Put`/`Delete` returned" now means durable on every path.
- End to end: `TestStripedPutGetDST` now checks, inside every PUT callback, that every
  stripe has at least k+1 shards whose durable bytes carry the manifest checksum
  (`PeekDurable` + `BlobChecksum`). Measured minimum over 500 seeds (base 987654, both modes): 5, i.e. some stripe was acknowledged with exactly k+1 durable shards, so the check is exercised at its edge.
  Limit: the DST has no crash during the PUT phase (its "all shards durable before the
  faults" precondition and the instantaneous budget would both need rework); the crash
  side of the claim is proven by the chunk-store model, the DST proves the count at ack.

### Finding 2 (MAJOR) — a callback ran inside `Put`
- Tests: the shared `put`/`get` helpers now fail if the callback runs before the call
  returns, so every node test checks it; `TestNodePutWithTooFewMembers` failed on the old
  code through that check. `TestNodePutPlacementFailureNeverCallsBackInsideTheCall`
  (a membership that shrinks after 0, 1 or 2 placements, windows 1 and 4: `ErrUnavailable`,
  callback outside the call, and no stripe placed after the failing one).
  `TestNodeCallbacksNeverRunInsideTheCall` audits every early path of both methods: bad
  bucket, empty key, 0-byte object, 1-byte object, nil manifest, invalid manifest, hostile
  layout, valid empty manifest (passes on the old code too; it pins the audit).
- Audit of every callback-taking method in `internal/storage`: `Node.Put` (invalid object,
  empty object, placement or encode error on any stripe, quorum failure — the last runs in
  a reply event), `Node.Get` (nil, invalid, layout, empty, any reply-path error — reply
  events only). `OnRequest`'s `env.Reply` is the server side and is answered inside the
  call by design (the simulator delivers the response as a later event). The chunk store
  takes no callbacks.
- Change: `putOp.fill` on a launch error sets `stopped`, schedules `finish(err)` through
  `AfterFunc(0)` and launches nothing more; replies arriving meanwhile are counted and
  ignored.

### Finding 3 (MAJOR) — `k+m` overflow
- Tests: `TestNewConfigTable` (MaxInt+1, 1+MaxInt, 2^62+2^62, MaxInt+MaxInt, 257/−1),
  `TestLayoutTable` (three hostile rows), `TestHostileCodeShapesAreErrorsNotPanics`
  (`Layout.Validate`, `Layout.Codec`, `Config.Validate`, `Manifest.Validate`,
  `EncodeShard` with MinInt/MaxInt combinations; for `EncodeShard` it also checks the error
  comes from the code check, not from a wrapped `k+m` in the index check),
  `TestGetOfAManifestWithAHostileLayoutCallsBackWithErrInvalid`. All panicked on the old
  code (`makeslice: len out of range`).
- Change: `erasure.validConfig` and `storage.validShape` test `k > MaxShards || m >
  MaxShards-k` before any addition.
- Audit of size arithmetic fed by caller or decoded numbers:
  - `Codec.ShardSize` is `(n-1)/k + 1` (finding 6); `TestShardSizeAtTheIntLimit`.
  - `Identity(n)` returns the empty matrix for `n <= 0` or when `n*n` overflows (finding 6);
    `TestIdentityOfNonPositiveOrHugeSizeIsEmpty` (0, −1, −2, MinInt, MaxInt, 2^32).
  - `Matrix.Mul` refuses a product whose `rows*cols` overflows (`ErrInvalidConfig`). Not
    testable: the two inputs would need over 8 GiB. Listed as uncovered.
  - `DecodeShard` caps the decoded payload length at `MaxStripeSize` (finding 7).
  - Checked and left: `NewMatrix`/`SelectRows` multiply lengths of slices that exist;
    `Join`'s `shardSize*k` (a real slice length times k ≤ 256); `Layout.Stripes` and
    `StripeLen` already divide first; `DecodeManifest` bounds every uvarint and checks the
    stripe table against the remaining input before allocating, then `Validate` (now
    overflow-safe) rejects the code; `Manifest.Validate`'s `K+M` runs after the layout is
    valid; `regionCap` sums member counts.
  - Not changed: `Node.Get` allocates `man.Size` bytes for a valid manifest. A caller can
    build a valid in-memory manifest with a huge size (it must also supply the matching
    number of stripe entries, so 1 TiB needs 16,384 stripes at 64 MiB). This is a resource
    limit, not an overflow; the object-size limit belongs to the API chunk (Chunk 6).

### Finding 4 (MAJOR) — fuzz seeds all skipped
- Test: `TestFuzzSeedsRunToCompletion` runs every `f.Add` seed of `FuzzGF256` and
  `FuzzErasureCoding` through `rapid.MakeFuzz` in a subtest and fails if any subtest
  skipped. I substituted five of the old seeds (two of `FuzzGF256`'s: 0 and 24 bytes; three of
  `FuzzErasureCoding`'s: 0, 128 and 128 bytes); the test failed on all five with "rapid ran
  out of input before the property finished".
- Change: both targets take 11 seeds of 4,096 bytes from `fuzzseeds_test.go`: all zero
  (every draw minimal: k=1, the smallest payload), `01 00…` words, `ff…7f` words, and eight
  SHA-256 counter streams. `go test -v -run '^Fuzz' ./internal/erasure`: 11/11 PASS per
  target, none SKIP.

### Findings 5–8 (NIT)
- 5: `Place`/`Replace` return `ErrInvalid` for a nil `Membership` and sort a clone of
  `Members()`. Tests: `TestPlacementWithNilMembershipIsErrInvalid` (panicked),
  `TestPlacementDoesNotReorderTheMembershipSlice` (a membership returning its cached slice;
  was reordered). A typed-nil pointer inside the interface still panics in its own
  `Members` method; that is the implementation's bug, not detectable without reflection.
- 6: see finding 3 (`Identity`, `ShardSize`).
- 7: `DecodeShard` rejects `PayloadLen > MaxStripeSize` as `ErrCorrupt`.
  `TestDecodeShardRejectsPayloadOverMaxStripeSize` builds a checksum-valid 64 MiB + 1 blob
  (accepted before) and checks a blob of exactly `MaxStripeSize` still decodes; it also
  covers `EncodeShard`'s over-size branch, which was listed as uncovered.
- 8: `BenchmarkReconstructAllMissing1MiBStripe` now erases shards 1 (data) and 4 (parity);
  re-measured below.

### Finding 9 (NOTE) — per-bucket `(k, m)`
The obstacle the reviewer shows is real for GET: the manifest records the layout but
`Node.Get` refused any manifest whose k/m differed from the node's. Smallest change:
`Node.Get` builds a codec from `man.Layout` (already validated by `Manifest.Validate`) when
it differs and uses it for the whole operation (`getOp.codec`). Test:
`TestNodeGetUsesTheManifestLayout` — a 4+2 node and a 2+1 node in one cluster each read the
other's object exactly (failed before). `TestNodeRejectsBadInput`'s "manifest written with
another code" case changed accordingly: it now expects `ErrUnavailable` with `ErrNotFound`
causes, because the shards were never written. PUT still uses the node's `Config.Layout`
(and `AckQuorum` is validated against it): choosing a layout per bucket needs bucket
configuration, which does not exist yet, so I left `Put`'s signature alone. Chunk 6 can add
a layout argument (with a per-layout quorum) without touching the GET path.

### C30 (adversarial, BROKEN) — what `Window` bounds
Decision: keep the behaviour (a PUT acknowledges when every stripe has its quorum and does
not wait for slow holders) and narrow the claim. Bounding stripes with any outstanding
request by `Window` would make one paused or dead holder cap a PUT at `Window` stripes per
`RequestTimeout` (4 MiB/s at the defaults) and would defeat C23's early ack.

What `Window` bounds: stripes launched and not yet at quorum (PUT) / not yet decoded (GET).
What it does not bound, stated plainly:
- After a stripe reaches quorum, its other requests (at most `N − AckQuorum` per stripe,
  1 at the defaults) stay outstanding until they get a reply or hit `RequestTimeout`
  (default 1 s) after they were sent. They are still outstanding when the PUT is
  acknowledged. With a holder that never answers, every stripe of the object can have one
  outstanding request at the ack; the number outstanding at any instant is bounded by the
  stripes launched in the last `RequestTimeout`, and over a whole PUT by
  `stripes × (N − AckQuorum)`.
- Memory per outstanding request: the request message, `1 + 1 + len(blob name) + 64 +
  ceil(L/k)` bytes (≈ 256 KiB + 70 B at the default 1 MiB stripe), held by the transport,
  plus a closure of a few words in the coordinator. As a fraction of the object: at most
  `(N − AckQuorum)/k` of its size (25 % at 4+2, quorum 5), held for at most
  `RequestTimeout` per request.
- The coordinator no longer pins the caller's object while late requests wait: `finish`
  drops `op.data` (before the repair, the late reply closures kept the whole object
  reachable for up to `RequestTimeout` after the ack).
- GET, by reading (not measured): the k initial requests and each fallback keep
  `verified + pending ≤ k` until a retry batch is sent, so a decoded stripe can leave at
  most `N − k` retried requests outstanding; their replies are dropped.
- Counters added to `NodeStats` (and to `EncodeState`): `PutAwaitingPeak` (largest number
  of one PUT's stripes awaiting quorum at once), `PutOutstanding` and `PutOutstandingBytes`
  (put requests sent and not yet answered or timed out, and their encoded size).
- Tests: `TestNodePutWindowBoundsStripesAwaitingQuorum` — Astra's setup (seed 220211, 6
  nodes, 4+2, stripe 4, `Window=1`, 1 µs latency, n005 paused) for 2, 8 and 64 stripes:
  the PUT acks before half a timeout; `PutAwaitingPeak == 1`; `PutOutstanding` equals the
  stripe count (one request per stripe to n005, the behaviour Astra measured, now pinned);
  `PutOutstandingBytes` equals the exact encoded size of those requests; one
  `RequestTimeout` after the ack both counters are 0.
  `TestNodePutReleasesTheObjectAtTheAck` — 16 stripes, one paused holder: after the ack,
  with 16 requests outstanding, a weak pointer to the object buffer is cleared by
  `runtime.GC()` (fails without the `op.data = nil` change).
- The harness under `agent_work/adversarial/02-erasure/` is unchanged; its
  `window-pending-stripes` attack measures outstanding requests and will still report
  2/8/64. That attack stays scored BROKEN against the old wording; the claim is narrowed
  here instead.

### Determinism lint on `internal/storage` (by hand; the gate does not cover it)
`docker compose run --rm -T dev bash -c 'cd agent_work/gates/detlint && go build -o /tmp/dl . && cd /src && /tmp/dl -root /src internal/storage internal/erasure'`
→ `detlint: checked 12 files in 2 packages`, no findings (run on `30c9689`; the later
commits add no product code except `op.data = nil`).

### API changes
- `storage.NodeStats`: new fields `PutAwaitingPeak`, `PutOutstanding`,
  `PutOutstandingBytes`; `Node.EncodeState` appends them.
- `Node.Get` accepts manifests coded with any valid layout.
- `ChunkStore.Delete` of an absent shard still returns `ErrNotFound`, now after a sync.
- `erasure.Identity(n)` for `n <= 0` (or an overflowing `n*n`) returns the empty 0×0
  matrix; `Matrix.Mul` can return `ErrInvalidConfig` for an overflowing product.
- No signature changed.

### Uncovered branches added
- `erasure/matrix.go` `Mul`: the overflow guard (needs > 8 GiB of input).
- `storage/chunkstore.go` `Put`: `Sync` error in the identical-bytes branch; `Delete`: the
  `Sync` error after a not-found delete and after a delete — the sim disk fails `Sync` only
  when closed, and a closed disk already fails the read/delete before it.
- `putOp.launch`'s placement-error path is now covered (it was listed before).

### Numbers that moved
All through Docker, `dev` container, same 4-core droplet (shared; MB/s varies with load).
- `go test -run '^$' -bench . -benchmem ./internal/erasure`:
  Encode 1 MiB **481 MB/s** (was 483; 0 allocs); `ReconstructData`, two data shards missing,
  **394 MB/s** (was 297 — no code change on this path, so this is load, not a speed-up);
  `Reconstruct`, one data + one parity missing (the benchmark now does what its name says),
  **390 MB/s**. The old 280 MB/s figure measured two data shards and is withdrawn.
- `BenchmarkStripedPutGet8MiB/32MiB -benchtime 1x`: peak live heap 36.9 MiB / 135.2 MiB
  above baseline, 12.0 / 48.0 MiB durable — unchanged. (No paused holder in this benchmark,
  so `op.data = nil` does not show here; `TestNodePutReleasesTheObjectAtTheAck` covers it.)
- DST, `DST_SEEDS=500 go test -count=1 -v -run TestStripedPutGetDST ./internal/storage`:
  177 s test time (was 167 s). Within budget: put max 294 ms, get max 643 ms, max mean
  requests per stripe 8, 46,593 GETs all exact, 33,426 fell back, 22,201 faults applied,
  22,912 rejected by the budget, 0 unavailable. Lifted: get max 872 ms, 15 requests per
  stripe, 46,593 GETs, 12,959 exact, 33,634 `ErrUnavailable`, 0 wrong bytes. Fewest durable
  shards at a PUT ack: 5. (46,593 = 501 runs × 93 GETs: the 500 swept seeds plus the pinned seed 987691 from
  `testdata/seeds.txt`; the first round reported 500 × 93 = 46,500.)
- Fuzz, 30 s each, `-fuzzminimizetime 5s`: `FuzzGF256` 245,137 execs; `FuzzErasureCoding`
  18,227 execs (was 113,615: the old count was mostly inputs that skipped before encoding;
  with draws that complete, many inputs now encode k up to 256 and payloads up to 3 MiB);
  `FuzzShardDecode` 230,868 execs. No crashers. `go test -v -run '^Fuzz'` over both
  packages: 0 SKIP.
- `-rapid.checks=2000`: erasure 11.1 s, storage 8.3 s (passing run after the model fix; the
  first 2,000-check run found the model bug recorded above).
- `-race`: whole module passed (erasure 47.8 s, storage 12.8 s); again on erasure+storage
  after the model fix: erasure 47.9 s, storage 16.3 s.
- Coverage (`go test -count=1 -cover`): erasure 98.4 %, storage 97.8 % (before the last
  two test commits; final figures in the gate result).

### Not done, and why
- No crash during the DST's PUT phase (see finding 1).
- `Node.Put` has no per-call layout (finding 9).
- No object-size cap in `Node.Get` (finding 3, last bullet).
- I did not edit the adversarial harness or the gate.

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
