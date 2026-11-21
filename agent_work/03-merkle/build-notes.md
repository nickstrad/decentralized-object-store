# Chunk 3 build notes — Merkle anti-entropy (`internal/merkle`)

Builder: Opus 5.5 (fresh seat), branch `chunk/03-merkle`, 2026-11-21.
Commits: `982e486` (tests first; they did not compile without the package), `03ed2f5`
(implementation), `d8ecd92` (ErrRejected checked through `errors.Is`), `19f3a93` (these
notes), then the fixes from the first gate runs (below).

## What was built

One package, `internal/merkle`, no other package touched, no simulator change needed.

| File | What it holds |
|---|---|
| `tree.go` | `Hash`, `Sum`, `Entry`, `Tree` (persistent fanout-16 trie), `New`, `Build`, `DepthFor`, `Set`, `Delete`, `Get`, `Snapshot`, `Keys`, `Entries`, `LeafLoad`, `Root` |
| `wire.go` | the diff-protocol messages, encode/decode, limits `MaxRequestNodes` (16,384), `MaxReplyBytes` (1 MiB) |
| `diff.go` | `Session` (the initiator's state machine), `Tree.Answer` (the responder, stateless), `Compare` (in-memory driver), `Limits`, `Difference`, `Cost`, `Result` |
| `worker.go` | `Worker` (anti-entropy), `Membership`, `Repairer`, `Config`, `Stats` |
| `errors.go` | sentinels and the wire code rows |

The replicated chunk store used by the DST tests lives in `store_test.go` (package
`merkle_test`): content-addressed chunks (`key = hex(SHA-256(bytes))`) in blobs
`chunk/<key>` on `env.Disk`, replicated by the writer with one request per peer, fetched
with a `G` request. It multiplexes three request kinds by a leading tag byte; the worker gets
an `env.Env` whose `Net` is wrapped (`env.WithNet`) to add the `M` tag. A liar process sits
beside it.

## Design choices

**Tree shape (PLAN 7.4).** Path of a key = the first 64 bits of `SHA-256(key)`, read as 16
nibbles; so `MaxDepth = 16`, `MinDepth = 1`. Leaves sit at level `Depth`, hold the entries
whose path shares their prefix, sorted by key. `DepthFor(n)` = smallest depth with
`16^depth >= n` (1e3→3, 1e4→4, 1e5→5).
- Leaf hash: `SHA-256(0x00 ‖ u32be count ‖ for each entry: u32be len(key) ‖ key ‖ 32-byte hash)`.
- Inner hash at level `l`: `SHA-256(0x01 ‖ u8 l ‖ 16 × 32-byte child hash)`, an empty child
  contributes 32 zero bytes.
- An empty subtree (leaf or inner) is the all-zero hash and is not stored (nil node). So the
  empty tree's root is zero; a non-empty node's hash is a SHA-256 output, and a collision
  with zero is a SHA-256 preimage.
- Why this closes the structure attacks: the tag byte separates leaf from inner (a leaf
  encoding can never be read as an inner one); the level byte separates inner nodes of
  different levels; keys are length-prefixed and the count is explicit, so no two entry
  lists share an encoding; a present key with the zero value hash is in the leaf encoding,
  so "empty value" and "absent" differ (`TestRootKnownVectors` row "an empty key with a zero
  hash is present, not absent"). Trees of different depths are never compared (`ErrDepth`).
- Nodes are immutable: `Set`/`Delete` copy the path (O(depth) nodes, `depth` SHA-256 of 514
  bytes plus one leaf hash). `Snapshot` is O(1). A diff runs against a snapshot, so local
  writes during a diff do not move the initiator's side.
- `Build` is a separate bottom-up construction from a sorted list; it is the "rebuild from
  scratch" that `TestPropIncrementalEqualsRebuild` compares against.
- Limits: `MaxKeyLen` 1,024 bytes (`ErrKey`), `MaxLeafEntries` 4,096 per leaf (`ErrLeafFull`).
  The leaf cap bounds a reply; an honest leaf only reaches it if keys are ground against
  SHA-256 prefixes (`TestLeafFull` grinds 4,097 keys into one leaf at depth 1 to reach it).

**Diff protocol.** One level per round trip, all mismatched nodes of a level in one request.
- Round trip 1: the initiator sends its depth and root hash. The responder answers `equal`
  (1 byte) or the 16 child hashes of its root.
- Round trip k (2..Depth): the initiator sends the paths of the level-(k−1) nodes whose
  hashes differed; the responder answers 16 child hashes per node.
- Round trip Depth+1: the initiator sends the mismatched leaf paths; the responder answers
  each leaf's entries. The initiator merges them with its own leaf and emits a
  `Difference{Key, Local, Remote, HasLocal, HasRemote}` per key that differs or exists on
  one side. Result sorted by key.
- So **round trips = Depth+1 = the number of levels in the tree** (root through leaves).
  PLAN's "round trips ≤ depth" counts levels; with `Depth` as the leaf level the property
  test asserts `≤ Depth+1`. This is the one wording point a reviewer should check.
- Wire format (all big-endian, uvarints must be minimal):
  request `01 ‖ depth ‖ level ‖ (level 0: root[32] | uvarint n ≥ 1, n × ceil(level/2) path bytes, strictly increasing, odd level low nibble zero)`;
  inner reply `02 ‖ level ‖ uvarint n ‖ n × (u16 bitmap, bit 15−i = child i ‖ one 32-byte hash per set bit, never zero)`;
  leaf reply `03 ‖ level ‖ uvarint n ‖ n × (uvarint m ≤ 4096 ‖ m × (uvarint keylen ≤ 1024 ‖ key ‖ hash), keys strictly increasing)`;
  equal `04`. Every accepted message re-encodes to the same bytes (checked by `FuzzMerkleWire`).
- Bounded work against a lying or changing peer:
  - every reply is validated before use: kind, echoed level, node count equal to the request
    (a leaf reply may answer a non-empty prefix), each leaf key's path must lie under the
    leaf that was asked for;
  - the frontier (mismatched nodes carried to the next level) is capped by
    `Limits.Frontier` (default 1,024, at most `MaxRequestNodes`); extra mismatches are
    dropped and `Result.Truncated` is set. The worker repairs what it found; the next round
    continues (`TestDiffFrontierTruncates` repairs 300 keys with frontier 4 to equal roots);
  - the responder stops adding leaves once the reply would pass `MaxReplyBytes`; unanswered
    leaves are asked again. A reply must answer at least one leaf, so every round trip makes
    progress;
  - hence any session ends within `Depth + Frontier` round trips whatever the peer sends
    (`TestPropHostileRepliesEndTheSession`), and the worker also has a round deadline.
- The responder holds no state between requests: a peer that vanishes leaves nothing behind.

**Worker.** `NewWorker(env, Config, Membership, Repairer)`; `Start` scans every key the
Repairer lists and hashes the bytes it reads (the tree is never persisted, so there is no
stored tree to rot; it is rebuilt from data at every start). Each tick (period `P`, jittered
uniformly in `[P/2, 3P/2)` from `env.Rand`):
1. **Scrub**: re-read and re-hash the next `ScrubBatch` keys (cursor over the sorted union of
   `Repairer.Keys()` and the tree's keys). Changed bytes → the tree takes the actual hash
   (`Changed`); unreadable or missing → the key leaves the tree (`Dropped`, `ReadErrors` for
   non-`ErrNotFound`). This is how silent bit-rot is found: the stored bytes are re-hashed,
   the tree is not trusted. The tree is the node's honest inventory (key → hash of the bytes
   it actually holds), which is what 7.4's shard inventory tree needs.
2. **Round** (if none in flight): pick a peer uniformly from `Membership.Peers()` minus self
   (sorted, deduplicated), diff against a snapshot. For each difference where the remote has
   a hash that `Repairer.Valid(key, hash)` accepts and the live local tree does not already
   hold it, fetch through `Repairer.Fetch`; accept only if `Valid(key, Sum(bytes))`, then
   `Repairer.Put` and update the tree. Pull-only: a node fixes itself; a peer missing our
   data fixes itself when it picks us. At most `MaxRepairs` fetches per round (`Deferred`
   counts the rest).
3. A round ends `Completed` or `Failed` (transport error, malformed reply, any fetch/put
   failure joined with `errors.Join`, or the round deadline `DeadlinePeriods × P` with
   `env.ErrTimeout`). A failed round is abandoned whole; the next tick is the retry. A
   generation counter makes late callbacks of an abandoned round no-ops.
- `Record(key, hash)` / `Forget(key)` are the store's write hooks (incremental update).
- `OnRequest` answers diff requests from the live tree.
- `Valid` is where the truth lives: content addressing in the test store; in Chunk 6 it is
  "matches the hash the manifest expects for this shard". A lying peer can only make us
  fetch, never store bad bytes.
- Deletes are out of scope: the chunk store is add-only. Pull-only anti-entropy over a tree
  without tombstones would resurrect a deleted key; Chunk 6 needs tombstones or manifests
  for that.

**Errors (PLAN 2.6).** Sentinels `ErrMalformed`, `ErrDepth` (2.6's rows) plus `ErrKey`,
`ErrLeafFull`, `ErrRejected` (fetched bytes failed `Valid`), `ErrSession` (Feed when no
request is outstanding). Peer failures surface as the transport's own errors wrapped with
`%w`. Wire codes: `CodeMalformed = CodeAppMin+48`, `CodeDepth = CodeAppMin+49`, exported as
`CodeEntries()` for `env.NewCodeTable`. Chosen to sit beside gossip (+16..+19) and storage
(+32..+35) on the other Wave B branches; Chunk 6 must register all three sets in one table.

**Canary.** `skipLastBranch` (package variable, set only through `SetSkipBranch` in
`export_test.go`) makes the session ignore a mismatch in child 15 of every node.

## Claims and the tests that prove them

| Claim | Test |
|---|---|
| Roots match pinned vectors and an independent reference implementation of the hash rules | `TestRootKnownVectors` |
| Insert, update (incl. same hash), delete, delete-missing keep Len/Get/Root right; deleting all returns the empty root | `TestInsertDeleteUpdate` |
| Empty tree has the zero root at every depth 1..16; a single key diffs in Depth+1 round trips; self-diff is 1 round trip, 1 hash | `TestEmptyAndSingle` |
| Depth bounds and `DepthFor` table | `TestNewAndDepthFor` |
| Key length and duplicate-key limits (`ErrKey`), leaf cap (`ErrLeafFull`) in `Set` and `Build` | `TestKeyLimits`, `TestLeafFull` |
| A snapshot is unaffected by later writes | `TestSnapshotIsolation`, also checked inside `TestPropIncrementalEqualsRebuild` |
| The protocol runs level by level, one node per request when one key differs, root request 35 bytes, equal → 1-byte reply | `TestDiffProtocolSteps` |
| Leaf replies over 1 MiB are split and still complete | `TestDiffLeafRepliesAreBatched` |
| A frontier cap truncates, never reports a spurious difference, and repeated rounds converge | `TestDiffFrontierTruncates` |
| Wire golden bytes; decode∘encode identity; malformed table (16 request rows, 20 reply rows) | `TestWireTable`, `TestWireMalformed` |
| Completeness: exactly the differing/one-sided keys, with both hashes, zero missed, zero spurious | `TestPropDiffComplete`, `FuzzMerkleDiff` |
| Cost: hashes ≤ 1 + 16·d·Depth + d·LeafLoad(remote); round trips ≤ Depth+1; equal trees 1 round trip | `TestPropDiffCost` (also asserted in `TestPropDiffComplete`) |
| Root equal ⇔ maps equal | `TestPropRootIffEqual` |
| Incremental Set/Delete equals `Build` from the model, for root, Len and LeafLoad | `TestPropIncrementalEqualsRebuild` (rapid state machine) |
| Insertion order does not change the root | `TestPropOrderIndependent` |
| Hostile replies (random bytes, or answers from a different random tree at every step = a lying or mutating peer) end the session within Depth+Frontier round trips with nil, `ErrMalformed` or `ErrDepth` | `TestPropHostileRepliesEndTheSession`, `FuzzMerkleWire` |
| Arbitrary bytes as request or reply never panic; errors are `ErrMalformed`/`ErrDepth`; `Answer` output always decodes | `FuzzMerkleWire` |
| Rot injected during traffic is detected by re-hashing and every damaged blob is repaired within `BitRotRepairBound`; fetched bytes ≤ `RepairCopiesPerRot` × rotted bytes; with no damage zero chunk bytes move | `TestBitRotRepairDST` |
| Writes dropped while a node is isolated are repaired after heal within `MissedWriteBound` | `TestMissedWriteRepairDST` |
| Under 20% loss on every link, rounds fail and are retried, every round ends (`Rounds = Completed + Failed + in flight`), replicas converge within `LossConvergeBound` | `TestRepairUnderLossDST` |
| A lying peer (garbage, `ErrDepth`, well-formed fabricated trees, garbage chunk bytes) never puts invalid bytes on an honest disk (invariant every 50 events / every event), never stretches a round past Depth+1 round trips, and honest replicas still converge | `TestLyingPeerDST` |
| A wiped disk is noticed by the scrub and refilled; injected read errors are counted | `TestScrubRefillsAWipedDiskDST` |
| Peer vanishes mid-diff: the round fails with the transport error (`ErrUnreachable`/`ErrTimeout`), nothing is repaired; after the peer restarts the next round repairs all 20 chunks | `TestErrorPeerVanishesMidDiff` |
| Malformed node: garbage reply, zero-hash child, remote `ErrMalformed` → round fails with `ErrMalformed`; a malformed request is answered with `CodeMalformed` and arrives as `ErrMalformed`+`ErrRemote` | `TestErrorMalformedNode`, `TestDiffErrors` |
| Depth overflow: request level > depth, depth > 16, `New`/`Build` out of range, peers of different depth (`ErrDepth` across the wire; only `ErrRemote` without the registered codes) | `TestErrorDepthOverflow`, `TestDiffErrors`, `TestNewAndDepthFor`, `TestWireMalformed` |
| Every code row round-trips through `env.CodeTable` | `TestCodeEntries` |
| Worker config validation; `Record`/`Forget` | `TestNewWorkerValidation` |
| Canary: skipping one mismatched branch fails the completeness check, and the replication scenario then never converges (seeds 1, 2, 3) | `TestCanarySkippedBranch` |

Every sentinel is produced and checked with `errors.Is`: `ErrMalformed`, `ErrDepth`
(several), `ErrKey` (`TestKeyLimits`, `TestNewWorkerValidation`), `ErrLeafFull`
(`TestLeafFull`), `ErrSession` (`TestDiffProtocolSteps`, `TestDiffErrors`), `ErrRejected`
(`TestLyingPeerDST/a lone liar cannot plant bytes`).

## Measurements (dev container, 4-core droplet shared with other builders)

**Diff cost** (`MERKLE_MEASURE=1 go test -v ./internal/merkle -run TestMeasureDiffCost`;
keys `object-%07d`, d differences split evenly between changed, deleted and inserted keys;
"exact bound" = 1 + 16·d·Depth + d·LeafLoad, the property's bound; "16d(Depth+1)" = PLAN's
`16·d·depth` with depth counted in levels):

| N | Depth | d | found | round trips | hashes | exact bound | 16·d·(Depth+1) | leaf load | bytes sent+received |
|---|---|---|---|---|---|---|---|---|---|
| 1,000 | 3 | 1 | 1 | 4 | 38 | 53 | 64 | 4 | 51+1,218 |
| 1,000 | 3 | 10 | 10 | 4 | 212 | 521 | 640 | 4 | 86+6,967 |
| 1,000 | 3 | 100 | 100 | 4 | 713 | 5,201 | 6,400 | 4 | 345+24,255 |
| 10,000 | 4 | 1 | 1 | 5 | 51 | 68 | 80 | 3 | 57+1,639 |
| 10,000 | 4 | 10 | 10 | 5 | 323 | 671 | 800 | 3 | 108+10,478 |
| 10,000 | 4 | 100 | 100 | 5 | 1,851 | 6,701 | 8,000 | 3 | 548+60,704 |
| 100,000 | 5 | 1 | 1 | 6 | 65 | 85 | 96 | 4 | 64+2,092 |
| 100,000 | 5 | 10 | 10 | 6 | 452 | 841 | 960 | 4 | 142+14,629 |
| 100,000 | 5 | 100 | 100 | 6 | 3,017 | 8,401 | 9,600 | 4 | 842+98,130 |

Hashes grow with d·log₁₆N and stay well under the bound (differences share upper nodes).
An in-sync round costs 1 round trip, 1 hash, 36 bytes.

**DST bounds** (named constants in `dst_test.go` / `worker_test.go`; measured as the
maximum over 500 seeds from base 987654321, `-v` logs):

| Constant | Value | Measured max |
|---|---|---|
| `BitRotRepairBound` (rot first seen → blob clean) | 20 s | 4.98 s |
| `BitRotConvergeBound` (after the last rot) | 25 s | 2.81 s |
| `MissedWriteBound` (after heal / last write) | 20 s | 2.70 s |
| `LossConvergeBound` (after the last write, 20% loss stays on) | 60 s | 41.97 s |
| `LyingConvergeBound` | 30 s | passes at 500 seeds (not logged) |
| `refillBound` (wiped disk) | 30 s | 2.80 s |
| `RepairCopiesPerRot` (fetched ÷ rotted bytes) | 4 | total ratio 0.99, worst seed 1.12 |

Time to detect is inside the repair time: a full scrub pass is ⌈48/16⌉ = 3 ticks
(≤ 2.25 s at P = 500 ms); the rest is waiting for a round with a peer whose copy is good.
Bytes per repaired byte: chunk bytes fetched ÷ chunk bytes rotted = 0.99 over 500 seeds
(below 1 because two rot events on one blob before its repair count twice); diff traffic
on top is 36 bytes per in-sync round and a few hundred bytes per round that finds a rotted
key (table above). The loss bound is the tightest (70% used); it is a geometric tail of
round success at 20% per-message loss.

**Wall time** (dev container): default `go test ./internal/merkle` 11–12 s;
`DST_SEEDS=500 go test ./internal/merkle` 400 s; rapid at 2,000 checks 38 s; `-race` 44 s.

## Commands run (through `docker compose run --rm dev`)

- `gofmt -l internal/merkle` → empty; `go vet ./internal/merkle` → clean.
- detlint `-root /src internal/env internal/sim internal/merkle` → 0 findings;
  `nocomments -root /src internal/merkle` → 0; `paniccheck ... internal` → 0.
- `go test -count=1 -cover ./internal/merkle` → ok, **96.8%** statements.
- `go test -count=1 ./internal/merkle -rapid.checks=2000 -rapid.seed=12345` → ok, 37 s.
- `go test -count=1 -race ./internal/merkle` → ok, 39 s.
- `DST_SEEDS=500 DST_SEED_BASE=987654321 go test -count=1 ./internal/merkle` → ok, 400 s.
- The full gate: see "Gate result" below.

## Uncovered branches (96.8%; from `go test -coverprofile`, blocks with count 0)

- `diff.go` `Compare`: the `Feed` error break. `Compare` answers with a real `Tree.Answer`,
  whose replies always decode; the Feed error path itself is covered through `Session`.
- `wire.go` `decodeResponse`: the `r.bad` check after reading a leaf entry. The earlier
  check `m <= len(rest)/33` already rejects every truncated entry list the malformed table
  and the fuzzer produced; kept as a guard.
- `worker.go`: `Start` called a second time; `scrub` failing in `tick` and `Repairer.Keys()`
  returning an error (the test store lists from `env.Disk`, whose `List` only fails on a
  closed disk); `tree.Set` failing in the scrub or after a repair (needs a >1,024-byte key on
  disk or a ground leaf); an empty peer list; the round deadline firing (per-request
  timeouts always end a round first in the scenarios: deadline 5 s vs timeouts 250–300 ms);
  stale callbacks of an abandoned round (only after a deadline); `Result.Truncated` in the
  worker (a diff with more than 1,024 mismatched nodes per level); the `MaxRepairs` cap (256
  per round, scenarios have 48 chunks); the "already fixed while the fetch was in flight"
  shortcut; `Repairer.Put` failing.

## Findings from the first gate runs (fixed)

1. **Decoder panic (product bug), found by `FuzzMerkleWire` in G3.** `decodeResponse` checked
   `len(rest) >= 2·n` once, before the loop, so an inner reply whose second bitmap was cut
   short (after a full first node) reached `binary.BigEndian.Uint16(nil)`. Fix: check the
   reader after taking each bitmap. Regression: corpus file
   `testdata/fuzz/FuzzMerkleWire/2104aef022c948e1` and the malformed-table row "inner bitmap
   truncated after a full node".
2. **Fuzz oracle too narrow (test bug).** `Feed` returns `ErrDepth` for a reply level above
   16 (documented); the fuzz oracle accepted only `ErrMalformed`. Corpus file
   `110121ffaa6941a6` kept.
3. **Seed 66014753249261 (test oracle, not protocol).** Two bit-rot events 310 ms apart hit
   the same blob on n001 before the scrub reached it, and the second flip undid the first.
   The bytes were correct in both disk layers and the tree was right, but the simulator's
   damage mark is only cleared by a `Sync`, so `Disk.Damaged()` reported the blob forever
   and the repair-time invariant failed at 20.5 s. The tests now count a blob as damaged
   only when it is marked AND its written or durable bytes fail to hash to the key
   (`world.damaged`). Seed pinned in `testdata/seeds.txt`. Note for later chunks (and the
   simulator docs): `Damaged()` is an over-approximation; use it as a hint, not an oracle.

## Errors intentionally ignored

- `diff.go` `Session.Fail`: `_ = s.fail(err)` — `fail` returns its argument for chaining
  inside `Feed`; `Fail` records it and has no result.
- Test only: `pairRun.run` `_ = s.RunFor(...)` — the sticky error is in `s.Err()` and is
  checked by `RequireDeterministic`. `canary_test.go` deliberately inspects `CheckDeterministic`'s error.

## Panics added

None in product code. (`codes()` in `dst_test.go` panics on a malformed code table; test
files are outside the panic gate.)

## Things not done, limits

- Round trips are bounded by `Depth+1` levels, not `Depth` (see above), and only while the
  mismatched leaves' entries fit in one 1 MiB reply and the mismatched nodes per level fit
  the frontier (default 1,024). Beyond that: `Depth + Frontier` round trips, or a truncated
  diff finished by later rounds.
- The cost bound holds as `c·d·log N` only while leaf loads are O(1); keys ground against
  SHA-256 prefixes raise `LeafLoad` (bounded by 4,096) and the bound includes it.
- Under concurrent writes on the responder a round may miss or report extra keys; repair is
  validated and the next round catches up. Not a snapshot-consistent diff on the remote side.
- No deletes/tombstones (see Worker).
- Scrub lists and sorts all keys every tick (O(N log N)); fine for the DST sizes, Chunk 6
  may want an incremental listing.

## Requests to the orchestrator

- Chunk 6 builds one `env.CodeTable` from gossip, storage, merkle (and occ/saga) rows; the
  merkle rows are +48/+49. A shared registry of code ranges per package would prevent a clash.
- No detlint exemption and no simulator change request.
