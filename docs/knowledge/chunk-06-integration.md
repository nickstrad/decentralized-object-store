# Chunk 6 — integration, the object store and sync engine: findings, limits, pitfalls

What was built file by file, the claims-to-tests table and every measurement are in
`agent_work/06-integration/build-notes.md` (section 10 is the repair round, 13 the second
review's fixes, 14 the loss declaration); the reviews are `REVIEW-1.md`, `REVIEW-2.md`,
`ARBITRATION-1.md` and `ARBITRATION-2.md` beside it; the outside harness is in
`agent_work/adversarial/06-integration/RESULTS.md`. This entry holds what the next builder
must know and would not guess.

## How the node is put together

- No protocol package calls another. Gossip, storage, Merkle, OCC and saga import only
  `internal/env` (storage also `internal/erasure`). `internal/node` builds all five into one
  `Node` and implements the interfaces they declare: `storage.Membership`,
  `merkle.Membership` and `occ.Membership` from the gossip view (`gossipView`, `regionView`
  in `mux.go`), `merkle.Repairer` over the metadata keys (`metaRepairer`), and
  `saga.MigrationOps` (`migrator`).
- Three packages, by what may run where. `internal/node` is the single-threaded core that the
  simulator executes; it is a determinism-lint root, so it has no goroutines, locks, sockets
  or wall clock. `internal/noderuntime` is the real environment: UDP and TCP, the file disk,
  the wall clock, the loop goroutine, and `Runtime`. `internal/storage/httpapi` is the HTTP
  handler and server; it calls one interface, `httpapi.Service`, which `Runtime` implements
  by posting every call to the loop. The request and response types stay in
  `internal/storage/api.go`.
- One transport, five channels: the first byte of every message is 1 gossip, 2 shard
  requests, 3 OCC, 4 metadata Merkle diffs, 5 node requests (metadata fetch, pointer view,
  purge).
- One object is one OCC key. The record under `occ.ManifestKey(bucket, key)` holds the
  storage manifest and the content type, so a concurrent pair of PUTs cannot leave one
  write's metadata over the other's bytes. The object's ETag is that record's ETag. The typed
  `PutManifest`, `PutObjectMeta` and `SetContentType` of `internal/occ` are not used.
- Metadata anti-entropy is one Merkle tree per node over every OCC key, tombstones and
  system keys included, not a tree per bucket: repair is pull-only, and a per-bucket worker
  cannot discover a bucket it has never heard of. OCC's own push-pull still runs beside it.
- Shards are not repaired through the Merkle worker. Per bucket, each node compares the
  tree of shards it holds with the tree its manifests expect, on the node itself, and
  rebuilds a missing or rotted shard from the other holders. Peers exchange no shard trees.
- New writes are placed on the current gossip view. An existing manifest keeps its
  placement when a member dies.
- Wire codes: the object API uses 160–163, `internal/node` 176–185. `node.CodeTable()`
  joins every package's codes with no collision: 37 application codes and 8 from `env`.

## Shard collection: evidence, not absence

A shard blob is deleted only on one of three paths.

- Dead version: the version has a replicated `~gc/<version>` marker, or this holder
  recorded it as a last-writer-wins loser, and the holder has seen that evidence for
  `GCGrace` (10 minutes by default).
- Orphan: no manifest expects it and there is no evidence either way for `OrphanGrace`
  (1 hour by default). The holder first writes one `~orphan/...` record per shard.
- Migration purge: an explicit list of versions, each with a `~gc/` marker. A holder
  refuses a version its own metadata calls live.

A node refuses a shard delete that arrives over the wire. "Unreferenced for a grace period"
is unsafe as a rule on its own: with a 10 s grace, a region cut off for 10 s collected live
shards whose manifest had not reached it.

## Declaring a version lost

A PUT is acknowledged at k+1 durable shards, so one holder may never have stored its shard.
If the writer is then cut off for longer than `OrphanGrace` before its manifest spreads,
other holders collect theirs as orphans, and a stripe can fall below k shards for good. The
rule that answers this is built on absence records, not on orphan records and not on a
timer.

- **Absence record.** The placement holder H of shard (version v, stripe s, index i) writes
  the replicated record `~absent/<v>/<s>.<i>/<H>` only when, in one step of its loop: v's
  manifest is live in H's own metadata, v is not purged and has no death evidence, and H
  holds no shard of (v, s, i) that matches the manifest's checksum; H's rebuild of that
  shard failed after the holder of every other index of the stripe answered with a shard,
  `not found` or `corrupt`; and fewer than k of the shards that rebuild gathered verify. A
  timeout, an unreachable or not-ready node, or any other error is not an answer, and then
  no record is written. The same holds for H's own disk: only `not found`, `corrupt` or a
  blob with the wrong checksum counts as "holds none"; a read error writes nothing.
- **The record is binding.** After writing it, H never rebuilds that shard and refuses it
  over the wire with `ErrFenced`. A rebuild that was already waiting for replies reads the
  metadata again when it finishes and stores nothing if H's record arrived meanwhile. The check reads H's own metadata, so it holds after a
  restart. Only H writes the key and every write of it is identical, so no merge can erase
  it.
- **Declaration.** A version is declared lost only when more than m indices of one stripe
  have an absence record written by that index's holder in the manifest. More than m
  binding records leave at most k-1 indices that can ever hold a shard, so nothing can
  rebuild the stripe, then or later. The decision needs no timer and no agreement on the
  member view. The node with the lowest ID in its own view makes it on a maintenance tick:
  it writes `~lost/<v>` on the object, then tombstones the manifest key under `IfMatch` of
  that version's ETag, marks the version dead and decrements usage. A GET then answers not
  found (404), and health counts `lost_objects`, `absent_records` and `absent_refused`.
- **An unreachable holder blocks the decision.** It cannot write its own record, and every
  other holder's rebuild times out on it, so they write none. While any holder of the
  stripe cannot be heard from, a declaration can rest only on records written earlier,
  while every holder answered.
- **Three safeguards, added late.** An absence record is read back for any holder ID, also
  one that contains `/`; before, such a holder's records were ignored and its stripes could
  never be declared. A node refuses shard writes while its metadata is not loaded; before,
  the absence check was skipped in that state. The loss record is written before the
  tombstone, so a crash between the two leaves a live key with a loss record, which the
  next tick finishes, and never a deleted key with no record.
- **A newer write of the same key** made where the declarer has not heard of it competes
  with the tombstone under last-writer-wins, and the loser is counted and logged like any
  discard.

Residuals, stated as the code behaves:

- The guarantee is "every key listed live is readable, or declared lost on record" once
  every holder of every stripe below k is up, holds the manifest and can reach the others.
  Until then the key is live and unreadable: GET fails with `ErrUnavailable` (503).
- **A holder that never returns keeps the key live and unreadable forever.** The code cannot
  tell a node removed for good from a partitioned one. A holder whose own disk keeps failing
  reads of a shard blocks the decision in the same way.
- **Wiped holder plus a late shard.** A holder whose disk is wiped loses its own copy of its
  absence record, and with it the refusal, until metadata sync brings the record back. A
  writer's shard request that reaches the new incarnation in that window is stored. The
  simulator delivers such a request, because it addresses a node and not an incarnation. On
  the real runtime an established TCP connection cannot survive the receiver's restart, but
  a request carries no destination incarnation and dials only after it is scheduled, so a
  request not yet dialed can reach the restarted holder before its deadline. That is read
  from the code and was not reproduced on real sockets. On either transport the refusal
  assumes the holder keeps its absence metadata. Two cases follow.
  - (a) The version is already declared lost. The stored shard belongs to a version that is
    dead on record; the holder gets the death marker by sync and collects the shard after
    `GCGrace`. The key stays lost, on record. Tested.
  - (b) The version is not yet declared. The record is false while that shard exists. If
    more than m records were already written, the version is declared lost although k
    shards exist at that moment. It takes a stripe already below k, a disk loss on a holder
    that wrote a record, and a shard write held in the network across the wipe. Not closed
    and not fixed; `TestResidualCaseBWipedHolderBeforeDeclaration` reproduces it and shows the
    test oracle catching it. A closure was built
    and withdrawn: refusing shard writes until the first metadata sync also refused the
    first writes of every new cluster.
- A crash right after the tombstone skips that declaration's death marker and usage
  decrement. The shards are then collected as orphans, and the quota audit corrects the
  usage.
- The orphan rule itself is unchanged: a write whose writer stays away longer than
  `OrphanGrace` can lose its shards and then be declared lost.

## Migration on real components

- Only a bucket created with `?region=` is routed and can migrate; others get 400. A bucket
  is not writable between the flip and the promote (`ErrBusy`).
- The pointer check asks every live peer for its route, flip owner and a digest of the
  bucket's manifest records, and proceeds only when all equal the coordinator's. Checking
  the route alone lost a write acknowledged just before the flip: its manifest had not
  reached the coordinator, and the purge deleted its shards.
- A purge names its versions. The copy records an intent (`~cv/<saga>/<version>`) before it
  writes a shard, so retire and discard know every version the saga wrote.
- Registrations are one key per saga (`~reg/<saga>`), so two concurrent migrations
  commute; the flip records its owner in `~flip`. Two migrations of one bucket end with
  exactly one `moved`.
- A peer that was away during a migration keeps its pre-flip write. A new key stays
  readable in the old region until the next migration moves it.
- Migration status is known only on the coordinating node (404 elsewhere).

## Quota

- A discard on a quota key or on a manifest key schedules a recount `RecountSettle` (3 s)
  later. That alone leaves a drift: a recount written during a partition by a node with
  incomplete manifests is causally newer than every quota version it saw, so nothing ever
  conflicts with it. The quota audit closes it: when usage differs from a recount and
  neither has moved for `RecountSettle`, the lowest-ID live node writes the recount. Seeds
  7 and 383 of the crash scenario's 500-seed sweep are the two that drifted; a test replays
  them.

## Real runtime

- Node ID is the advertised `host:port`; UDP and TCP share the port. One TCP connection per
  request. A failure after the request is written is `ErrTimeout` (outcome unknown); a dial
  failure is `ErrUnreachable`.
- `env.Disk` is synchronous, so file I/O and fsync run on the node loop; a stalled disk
  stalls the node. The OCC state blob is the whole document, rewritten and fsynced on every
  metadata write. That dominates PUT latency and half the CPU of the full-node tests.
- File disk: write a temp file, fsync it, rename; `Sync` fsyncs the file, its directory and
  every new ancestor. Tested on a POSIX crash model; real power loss is not covered.
- A call canceled before the loop takes it never applies. One canceled after is applied
  once and returns `ErrOutcomeUnknown` joined with the context error.
- A panic on the loop closes the sockets, ends every goroutine and closes `Runtime.Done()`;
  `objnode` then exits 1.
- A 3-node cluster needs `-k 2 -m 1`: with 4+2 it cannot place a stripe.

## To reuse in Chunk 7

- `node.NewFactory(cfg)` gives the `env.Process` for `sim.AddCluster`. Hooks on
  `node.Config`: `CommitSink` (every client commit: the record the writer held and the
  version written), `DiscardSink`, `WrapMigration`, `ConfigureSaga`.
- The full-node harness and its oracles are in test files of package `node_test`
  (`internal/node/helpers_test.go`, `oracle_test.go`), so another package cannot import
  them: copy them, or move them into a support package first. `newHarness(seed, n, cfg,
  net)` builds the cluster in three regions with the code table, the sinks and a watched
  disk on every node. `dstConfig()` is the 4+2 configuration the cluster tests use.
  - `ackOracle`: every acknowledged write is current, a migration's copy, superseded by a
    later acknowledged write that held it, or named as a loser in a discard. Ancestry is per
    key.
  - `gcOracle` with `watchDisk`: sees every shard delete as it happens and fails if the
    version is, or can still become, the winner on any node, read from all nodes' metadata
    and independent of the product's bookkeeping.
  - The converse loss oracle (`onCommitLoss`): when any node commits `~lost/<v>`, it reads
    every holder's disk and fails unless some stripe has fewer than k verifiable shards.
  - `checkLiveReadableOrLost`: at the end of a run, every key listed live is read through
    a random node; a failed read fails the run unless a loss record exists.
- `sagasim.Harness` on real nodes is `nodeHarness` in `dst_migration_test.go`.
- `internal/noderuntime/conformance_test.go` runs the same four scenarios on the simulator
  and on loopback sockets; it is the pattern for a test that must hold on both.
- `internal/testutil/leakcheck`: `Main(m, settle)` as a `TestMain` goroutine-leak check.
- `agent_work/06-integration/cluster-smoke.sh` is gate G7. It starts the compose `cluster`
  profile (three peers in regions a, b, c), waits for three alive members on every node,
  puts on one node, gets on another and checks the conditional-write refusals. It runs under
  its own compose project name and reads back ephemeral loopback ports, so two runs can
  overlap. It needs Docker on the host and cannot run inside the `dev` container.
- `agent_work/adversarial/06-integration/` is a nested Go module with a `replace` to the
  repository root; its module path lies under the product's, so it may import
  `internal/...`. `run-all.sh` runs six drivers (simulator, loopback, existing checks, CLI,
  extended, repair checks): 662 checks held, 0 broken, in about 7 minutes.

## Measurements

Virtual time, 500 seeds, 21 nodes in 3 regions unless noted, in the `dev` container.

- Convergence after the heal: put/get p50 3.27 s, max 10.96 s; partition p50 7.09 s, max
  12.01 s; crash p50 2.04 s, max 4.38 s.
- Partition: the oracle explained the acknowledged writes of a run as current (p50 16),
  superseded (p50 9) or discarded on record (p50 7); none was unexplained. Usage equals a
  recount on every node p50 8.62 s, max 12.75 s after the heal; quota overshoot p50 500 B,
  max 700 B over an 800 B limit.
- A restarted node holds all its shards again p50 6.04 s, max 8.38 s after the restart; a
  wiped node is refilled p50 15.0 s, max 17.4 s after the wipe.
- Bit-rot of up to m shards per stripe: repaired p50 153 ms, max 159 ms; rebuilt bytes per
  rotted byte 1.000 on every seed.
- Migration failure matrix on real nodes: 577 runs, every one in its expected state; begin
  to terminal p50 11.4 s, max 18.9 s against a bound of 20 s. Two migrations of one bucket
  plus a third elsewhere: all terminal p50 14.0 s, max 19.9 s.
- Real runtime, 3 nodes on loopback, 2+1, 1 MiB stripes: 1 MiB PUT p50 174 ms, GET p50
  16 ms; 32 MiB PUT p50 2.70 s, GET p50 522 ms; 11 goroutines at idle.
- Wall time: the 500-seed sweep of `internal/node` alone takes 22.4 minutes, and
  `-race -count=3` on it 671 s. Coverage: `internal/node` 86.2%, `internal/noderuntime`
  87.7%, `internal/storage/httpapi` 87.8%.

## Stated limits

- No authentication anywhere: gossip accepts any datagram, request frames carry a
  self-declared sender, and the HTTP API is open.
- The shards of a node that never returns are not re-placed; its stripes stay one shard
  short.
- GET and PUT hold the whole object in memory, at most 32 MiB by default.
- Tombstones, death markers, copy intents, orphan, absence and loss records are kept
  forever, and every metadata write re-encodes the whole document.
- A permanently lost coordinator leaves its saga unfinished, and its registration blocks
  other migrations of that bucket.
- Pending recounts and the set of recorded losers are in memory; the quota audit and the
  durable `~gc/` marker cover a crash.
- The layout is per node, not per bucket.
- The failure matrix runs 78 cells with the sweep's seeds spread over them (577 runs at 500
  seeds), not 78 × 500: that was measured at about 2 hours and is left to the hardening
  work.

## Pitfalls

- A clean small sample is not evidence. The first outside harness run held 284 of 284
  checks, and the code review then found a blocker and eight majors. Its migration
  scenarios, like the builder's, waited for the cluster to settle before they began.
- An oracle built on version-vector coverage across keys hides losses: a node's dot counter
  is shared by all its keys, so its next quota write "covers" its lost manifest write.
  Track ancestry per key.
- Do not key content matching on the payload alone. Equal payloads on two keys made a lost
  write look superseded.
- A node that has just restarted has an empty member view. It agreed on the pointer with
  zero peers until the view-settle rule (`ViewSettle`, 5 s without a join, and at least one
  peer) was added.
- A record that several maintenance ticks write must not be one key overwritten each tick.
  The per-node orphan record lost earlier entries that way; it is now one immutable key per
  shard.
- `If-Match` handling: a present header must never become an unconditional write. An empty
  or whitespace-only field, and a list of weak tags, match nothing (412). An entity-tag may
  contain a comma, so the list is scanned left to right, not split on commas.
