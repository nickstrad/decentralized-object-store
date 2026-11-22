# Chunk 3 — Merkle anti-entropy: findings, limits, pitfalls

What was built, the wire format, the claims-to-tests table and every measurement are in
`agent_work/03-merkle/build-notes.md` (the section "Repair round" holds the final bounds);
the outside harness is in `agent_work/adversarial/03-merkle/RESULTS.md`. This entry holds
what a later builder must know and would not guess.

## Using the package (integration chunk)

- `NewWorker(env, Config, Membership, Repairer)`. `Membership` is `Peers() []env.NodeID`;
  the worker sorts the list, drops duplicates and its own ID, and picks one peer at random
  per round. `Repairer` is `Keys`, `Read`, `Valid(key, hash)`, `Fetch(peer, key, done)` and
  `Put`. Only test code implements either interface so far.
- `Valid` is where the truth lives. The worker fetches a key only if `Valid` accepts the
  remote hash, and stores bytes only if `Valid` accepts their SHA-256, so a lying peer can
  cause fetches and never a bad write (`ErrRejected`). In the tests `Valid` is content
  addressing; with real shards it must be "the hash the manifest expects".
- The worker uses `env.Clock`, `env.Rand` and `env.Transport` only. It never calls
  `env.Disk`: stored bytes are reached through the `Repairer`. `Session` and `Tree.Answer`
  do no I/O; the worker sends each request and feeds the reply back.
- The tree is in memory only, rebuilt at every `Start` by hashing what the `Repairer` reads.
  The store reports writes through `Record` and `Forget`; the host routes inbound diff
  requests to `OnRequest`. Replicas must use one depth (`ErrDepth` otherwise).
- Repair is pull-only: a node fixes itself, at most `MaxRepairs` (256) fetches per round.
- Wire codes: merkle owns block 112–127 and uses 112 (`ErrMalformed`) and 113 (`ErrDepth`),
  exported by `CodeEntries()`. Project blocks: gossip 80–95, storage 96–111, merkle
  112–127, occ 128–143, saga 144–159. One `env.CodeTable` must register all of them.

## Cost bounds, as restated

- Round trips with an honest peer: 1 when the roots are equal, otherwise one per tree
  level, `Depth + 1` with the leaf level, plus one for each extra leaf reply. A second
  leaf reply is needed only when the mismatched leaves' entries together pass the 1 MiB
  reply limit: exactly `Depth + L`, with `L ≤ ⌈S / 1,047,510⌉` for `S` bytes of leaf data.
- Round trips with any peer, lying or answering one leaf per reply: at most
  `Depth + 5·Frontier`. The round deadline (`DeadlinePeriods` = 10 periods) also ends it.
- Hashes, for `d` differing keys: at most `1 + 16·d·Depth` for the root and the inner
  levels, plus the entries of the mismatched remote leaves (`d·LeafLoad`). The first
  wording, `16·d·(Depth+1)`, holds only while a leaf has at most 15 entries: 46 keys in
  one leaf with one changed hash cost 49 hashes against 48 (`RESULTS.md`, N1 and N2).

## One reply-size bound

- Every reply `Tree.Answer` emits, of every kind, is at most `MaxReplyBytes` = 1,048,576
  bytes. A leaf that does not fit is cut and resumed after its last key. The largest
  request is 17,351 bytes. Size the transport frame limit from `MaxReplyBytes` plus the
  host's own framing; there is no larger figure.
- Frontier truncation: mismatched nodes beyond `Limits.Frontier` (default 1,024, at most
  `MaxRequestNodes` = 2,040) are dropped for this round and `Result.Truncated` is set. The
  worker repairs what it found, does not count the round as in sync, and later rounds
  finish the job (300 keys with frontier 4 reach equal roots).

## Stated limits

- No tombstones. `Forget` only removes the key locally; a later round with a peer that
  still holds it pulls it back. Deletes need tombstones or manifests before they are safe.
- `Worker.Stop` fails the round in flight with `env.ErrClosed`, keeps its cost in `Stats`
  and ignores late replies. `Start` after `Stop` rescans every key and resumes. `OnRequest`,
  `Record` and `Forget` still work on a stopped worker: the host decides whether it answers.
- Open (`REVIEW-2.md`, note 1): if a `Repairer` call stops the worker inside `Start` and
  `Start` runs again before the first timer fires, a second timer survives a later `Stop`.

## Measurements

Virtual time, maximum over 500 seeds; 48 chunks, period 500 ms, 16 keys scrubbed per period.

- A rotted blob is clean again within 4.98 s of the rot (bound 20 s); a write missed
  behind a partition is repaired 2.70 s after the heal; a wiped disk is refilled in 2.80 s.
- Under 20% loss on every link the replicas converge within 41.97 s (bound 60 s).
- Bytes fetched per rotted byte: 0.99 in total, 1.12 on the worst seed. With no damage,
  zero chunk bytes move once the initial writes have settled.
- An in-sync round costs 1 round trip, 1 hash and 36 bytes. One differing key among
  100,000 (depth 5): 6 round trips, 65 hashes, about 2.2 KB; 100 keys: 3,017 hashes, 99 KB.

## Pitfalls

- The simulator's `Disk.Damaged()` over-reports: the mark is cleared only by a `Sync`, so
  a blob whose bytes are right again stays marked. Seed 66014753249261 (two flips of one
  bit 310 ms apart) is pinned in `internal/merkle/testdata/seeds.txt`. Judge rot by whether
  the bytes hash to the key; treat `Damaged()` as a hint.
