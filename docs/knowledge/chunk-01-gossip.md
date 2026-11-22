# Chunk 1 — SWIM gossip membership: findings, limits, pitfalls

What was built, the configuration table, the claims-to-tests table and every measurement are
in `agent_work/01-gossip/build-notes.md`; the outside harness is in
`agent_work/adversarial/01-gossip/RESULTS.md`. This entry holds what a later builder must know
and would not guess.

## Using the package (integration chunk)

- Consumers read `gossip.Membership`: `Members()` (alive and suspect, self included, sorted
  by ID), `Lookup(id)` (any retained entry, dead and left ones too; `ErrNotMember` once the
  entry is purged) and `Subscribe(fn)`. Every method returns plain values, so a consumer can
  declare a smaller interface of its own. Nothing outside the package reads it yet.
- `Subscribe` callbacks run synchronously, inside the handler that changed the view, and
  must not block. `EventRecovered` (suspect back to alive) tells a refutation from a rejoin.
- A callback never runs inside the call that registered it: `Join`'s `done` always fires
  later, also for an empty seed list. A join still pending at `Leave` reports `ErrClosed`.
- A `Node` is single-threaded and uses `env.Clock`, `env.Rand` and `env.Transport` only.
  Nothing is on `env.Disk`: the incarnation is volatile, and a restarted node starts at 0.
- Wire codes: gossip owns block 80–95 and uses 80–84. Project blocks: gossip 80–95,
  storage 96–111, merkle 112–127, occ 128–143, saga 144–159.

## The lifetime of a dead entry

- A dead or left entry stays in the view for `DeadRetention` (default 5 min) from when this
  node learned it, then becomes a tombstone: the exact `Member` value, with no time limit.
  A node keeps at most `MaxTombstones` = 4,096 and evicts the oldest-buried.
- While a node holds an ID's tombstone, its record for that ID never moves down the merge
  order. An equal or older verdict is dropped. A stale alive or suspect claim brings the
  verdict back into the view for one more retention, with no event and no gossip, so a
  wiped node rejoining at incarnation 0 hears its old verdict and refutes above it.
- Eviction reopens two things on that node: it accepts the ID as new at any incarnation (a
  replayed `alive(x, 0)` is believed), and it can take the dead entry back from a peer that
  still holds it, for one more retention. A burst of K deaths settles within ⌈K/4096⌉
  retention periods; churn above 4,096 burials per retention keeps the window open.
- A verdict about a member this node never held is kept but not gossiped. Without these
  rules, dead entries bounced between nodes that purged at different times and never left
  any view (`TestDeadEntriesStayPurgedDST`, `TestDeadEntryLifetime`).

## Re-merge after a partition

- Every `SyncInterval` a node syncs with one random live member and one random heal target
  from: dead entries in its view, dead tombstones, and remembered seeds that are not live.
  Left members are never contacted. Many old tombstones slow a re-merge, not prevent it.
- Seeds given to `Join` are remembered like `Config.Seeds` (at most `MaxSeeds` = 256).
  Without that, halves formed through `Join(seeds)` never re-merged after a long partition.
- Guaranteed: two sides re-merge if some node holds, on the other side, a reachable seed or
  a dead entry or dead tombstone of a node that is up. Not guaranteed: no live seed across
  the cut and the other side's tombstones evicted; SWIM has no other source of addresses.

## Bounds and stated limits

- `MaxRelays` = 1,024 ping-req relays in flight; more are dropped and counted. No relay is
  kept for a ping that could not be sent.
- `MaxMembers` (default and maximum `MaxUpdates` = 65,535) caps the view: a new ID is
  refused with `ErrViewFull`, known members still change. There is no byte bound; a full
  view with long IDs can exceed the transport's request limit (`env.ErrTooLarge`).
- No authentication. A forged `dead` at incarnation `MaxUint32` evicts a node for good: it
  cannot refute past the maximum.

## Measurements

In the `dev` container, virtual time, 500 seeds, `ProbeInterval` 1 s, defaults otherwise.

- Convergence after faults stop (5–10% loss, joins, crashes, leaves), in periods,
  min/median/p99/max: N=20 0/8/21/21, N=50 1/18/26/29, N=100 10/26/32/34. The worst case is
  4.2 to 4.9 × ⌈log₂ N⌉ (test bound 8); the suspicion timeout alone is 4 × ⌈log₂ N⌉.
- Crash detection, N=20, 5% loss, until every live node holds the victim dead:
  min/median/max 21.5/23.0/28.0 s (the suspicion timeout is 20 s).
- Partition heal, N=12, min/median/max: within the retention 4.0/6.5/11.5 s; longer, with
  a seed across the cut, 2.5/7.0/16.0 s; through tombstones alone 2.5/12.0/35.0 s.
- About 3 datagrams per node per period (maximum 4.0), plus two sync requests per
  `SyncInterval`. No false death in 2,000 runs.
- One one-way link, N=12, 300 s: 0 suspicions with indirect probes; 416–543 per seed
  without them, and still 0 evictions. Assert zero suspicions, not zero evictions.

## Pitfalls

- Sweep cost: the 500-seed sweep of this package takes 6 to 8 minutes and is CPU-bound in
  the N=100 convergence case, which runs its seeds in four shards (seed mod 4), each shard
  sequential. `-seed=N` runs only that seed's shard. Running every seed as a parallel
  subtest kept each finished simulation alive until the parent ended: 5.1 GB, killed.
- Seed 33: a node whose join failed on every seed stayed unknown until its first periodic
  sync; a node that knows no live member but itself now re-joins its seeds every period.
  Seeds 78 and 469: not re-gossiping entries first learned by sync left one node ignorant
  of a joiner for over 20 s. All three are pinned in `internal/gossip/testdata/seeds.txt`.
