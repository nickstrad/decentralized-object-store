# Chunk 1 build notes — SWIM gossip membership (`internal/gossip`)

Builder: Opus (fresh seat), branch `chunk/01-gossip`. Spec: PLAN.md 4.2 "Chunk 1", 2.1–2.6,
7.1, 7.7, plus the brief `objstore-p2-01-gossip-build.md`.

## What was built

`internal/gossip` (product code: `config.go`, `errors.go`, `member.go`, `wire.go`,
`node.go`; no other package touched). One `*gossip.Node` per process, implementing
`env.Process` (`Start`, `OnDatagram`, `OnRequest`, `EncodeState`). It is a single-threaded
state machine that acts only through `env.Clock`, `env.Rand` and `env.Transport`
(it does not use `env.Disk`). It never blocks and never starts a goroutine.

### Exported surface

- `New(env.Env, Config) (*Node, error)`: validates config and identity (ID 1–255 bytes,
  region ≤ 255 bytes, non-nil Clock/Net/Rand) → `env.ErrInvalidArgument`.
- `Membership` interface (`Members() []Member`, `Lookup(id) (Member, error)`,
  `Subscribe(func(Event))`), satisfied by `*Node`. Chunk 6 can declare a smaller interface of
  its own; every method here returns plain values.
  - `Members()`: live members (alive or suspect), self included, sorted by ID.
  - `All()`: every retained entry, dead and left ones included, sorted by ID.
  - `Lookup(id)`: any retained entry; unknown or purged → `ErrNotMember`.
  - `Tombstones()`: the purged verdicts this node still remembers (see "Lifetime of a dead
    entry"), sorted by ID. Added in the repair round.
  - `Self()`, `Stats()`, `LastError()`, `Closed()`.
  - `Subscribe(fn)`: `fn` is called synchronously, inside the handler that caused the change,
    with `Event{Kind, Member, At}`. Kinds: `EventJoined`, `EventSuspected`, `EventRecovered`
    (suspect → alive by refutation), `EventDied`, `EventLeft`. `EventRecovered` is a fifth
    kind beyond the brief's four, so a subscriber can tell a refutation from a rejoin.
- `Join(seeds, done func(error)) error`, `Leave() error`.
- `Member{ID, Region, Incarnation, State}`, `State` (`StateAlive`, `StateSuspect`,
  `StateDead`, `StateLeft`), `Supersedes(a, b)`, and `View` (the pure merge lattice:
  `NewView`, `Apply`, `Get`, `Remove`, `All`, `Live`, `IDs`, `Len`, `LiveCount`).
- Wire: `Message{Kind, Seq, Target, Updates}`, `Encode`, `Decode`, `Size`, `UpdateSize`,
  `Kind*` constants, `Version`, `MaxIDLen`, `MaxUpdates`, `MinPacket`.
- Errors: `ErrMalformed`, `ErrNotMember`, `ErrClosed`, `ErrJoinFailed`, `ErrViewFull`
  (repair round); `Codes()` and `CodeTable()` (wire rows `CodeAppMin+16..+20` = 80..84, inside
  the gossip block 80–95).
- Bounds (repair round): `MaxTombstones` = 4096, `MaxRelays` = 1024, `MaxSeeds` = 256.
- `Config` with `WithDefaults`, `Validate`, `SuspicionTimeout(n)`, `RetransmitLimit(n)`.

### Configuration and defaults (zero means default)

| Field | Default | Meaning |
|---|---|---|
| `ProbeInterval` | 1 s | protocol period: one probe per period |
| `ProbeTimeout` | 400 ms | direct-ack wait before ping-req; must be < interval |
| `IndirectProbes` (k) | 3 | ping-req fan-out |
| `SuspicionMult` | 4 | suspicion timeout = mult × ⌈log₂ N⌉ × interval (N = live members incl. self, log ≥ 1) |
| `RetransmitMult` (λ) | 4 | each update is piggybacked λ × ⌈log₂ N⌉ times |
| `SyncInterval` | 15 s | push-pull period (first one at a random offset in [interval/2, 3·interval/2)) |
| `SyncTimeout` | 2 s | request timeout for sync and join |
| `DeadRetention` | 5 min | dead/left entries kept (for partition heal, 7.7), then purged |
| `MaxPacket` | 1400 | datagram size budget; min `MinPacket` = 781 (one max header + one max update) |
| `Seeds` | none | joined at `Start`, and contacted by the heal sync while not live; at most `MaxSeeds` |
| `MaxMembers` | `MaxUpdates` (65,535) | view size cap, self included; range 1..`MaxUpdates` (repair round) |

Indirect probing cannot be turned off through `Config` (0 means default 3). The canary
switch is `WithoutIndirectProbes` in `export_test.go`, so it exists only in test builds.

## Design and why

**Merge order (the lattice).** Each member entry is ordered by `(incarnation, state,
region)` with states ranked alive < suspect < dead < left; an update replaces the entry
iff it is strictly greater (`Supersedes`). This is a max-lattice, so applying any set of
updates in any order, with any duplication, yields the same view
(`TestPropViewMergeIsCommutative`). It gives exactly the SWIM rules: suspect(i) beats
alive(i); alive(i+1) refutes suspect(i) and overturns dead(i); dead/left at i beat alive and
suspect at i; nothing below the current incarnation changes anything. Region is the last
tie-break only so that two different entries can never be "equal but different"; honest
nodes never produce it. The full 16-pair × lower/equal/higher table is
`TestStateTransitions`, checked against both `View.Apply` and a live `Node` (events too).

**Self entry.** Updates about the node itself never go through the lattice. Any claim at an
incarnation ≥ own that is not "alive at exactly own incarnation" (i.e. suspect/dead/left at ≥
own, or alive above own) is refuted by setting own incarnation to claim+1 and gossiping
alive. A node therefore never declares itself suspect or dead and its incarnation never
decreases (`TestIncarnationRules`, `TestPropIncarnationMonotonic`, and a DST invariant run
every 64 events in every scenario). At `MaxUint32` a claim cannot be refuted: the node sets
its incarnation to `MaxUint32`, counts `Stats.RefuteOverflows`, never wraps, and stays dead
in others' views (stated limitation; SWIM has no authentication, so a forged
dead(MaxUint32) evicts a node permanently — see "Limits").

**Probing.** Shuffled round-robin: the live members (minus self) are shuffled with the node's
`env.Rand` at the start of each round; a member that becomes live mid-round is inserted at
a random position in the rest of the round; dead/left members are skipped
(`TestProbeTargetRotation`). Each tick: if the previous probe got no ack (direct or
relayed), its target becomes suspect; then a ping goes to the next target and a
`ProbeTimeout` timer arms the indirect stage, which sends ping-req to k random other live
members. A relay pings the target with its own sequence number and forwards the ack with
the requester's number; relays expire after one interval. Sequence numbers are one
per-node counter shared by probes and relays.

**Suspicion.** Entering suspect (from a failed probe or from gossip) starts a timer of
`SuspicionTimeout(live count)`; if the member is still suspect at that incarnation when it
fires, it is declared dead (`EventDied`) and gossiped. Leaving suspect stops the timer.

**Dissemination.** One queue entry per member (a newer update replaces the queued older
one). Every outgoing datagram piggybacks entries in order: (1) explicit leads (the leave
notice), (2) the destination's own entry if we hold it as suspect/dead/left — a node hears
about its own suspicion from the first packet we send it, even after the queue is spent —
then (3) the queue sorted by fewest transmissions, newest first. Only entries that fit in
`MaxPacket` minus the header are taken; each taken entry counts one transmission and is
dropped at λ⌈log₂N⌉ (`TestPiggybackSelectionAndLimit`).

**Join, sync and heal (7.7).** Join and periodic sync are the same push-pull request: the
caller sends its whole view (`KindSyncReq`), the server merges it and replies with its
whole merged view (`KindSyncResp`), the caller merges that. `Join` contacts every seed at
once; `done(nil)` on the first success, else, after all answered,
`ErrJoinFailed` joined (`errors.Join`) with each seed's cause. Seeds given to `Join` are
remembered with `Config.Seeds` (repair round). Every `SyncInterval` a node syncs with one
random live member and one random entry from {dead members in the view} ∪ {dead tombstones}
∪ {remembered seeds that are not live}; after a partition this is how the halves find each other: the contacted node sees
itself dead in the request and refutes, the caller sees itself dead in the reply and
refutes, and refutations spread. While a node knows no live member other than itself and
has seeds, it re-joins its seeds every probe period, one join at a time (added after DST seed
33, below).

**Leave.** `Leave` marks self left (same incarnation; left beats alive), sends a gossip
datagram led by that entry to every live member, stops all timers, and closes the node:
`Join`/`Leave` return `ErrClosed`, datagrams are dropped and counted
(`Stats.DroppedClosed`), requests are answered with `ErrClosed`, which crosses the wire as
code 82 (`TestOperationsAfterLeave`).

**Wire format** (`Version` 1, big-endian): `version u8 | kind u8 | seq u32 | [target: len
u8, bytes — ping-req only] | count u16 | count × (state u8 | incarnation u32 | id: len u8,
bytes | region: len u8, bytes)`. Decoding is strict and canonical: unknown version, kind or
state, empty id or target, truncation, a count larger than the bytes can hold (checked
before allocating), and trailing bytes are all `ErrMalformed`; so `Encode(Decode(b)) == b`
for every accepted `b` (`FuzzGossipWire`). Datagram kinds are ping/ack/ping-req/gossip;
sync kinds are accepted only on the request path and vice versa. Golden encodings are
pinned in `testdata/wire-golden.txt` (rewrite only on purpose with
`GOSSIP_UPDATE_GOLDEN=1`).

**Size limit.** Outbound packets never exceed `MaxPacket`. An inbound datagram longer than
`MaxPacket` is dropped and counted as malformed. If the transport still refuses a packet
(`env.ErrTooLarge`, e.g. a transport limit below `MaxPacket`), the send is counted in
`Stats.SendErrors` and `LastError()` wraps the cause (`TestErrors`).

**Errors.** Sentinels as above, wrapped with `%w` and the operation and peer. No error is
returned across the network except as a code from `Codes()`; a remote `ErrClosed`/
`ErrMalformed` arrives as `*env.RemoteError` matching both the sentinel and
`env.ErrRemote` (`TestErrors/every sentinel survives the wire code table`). Datagram-path
failures are never fatal: they are counted in `Stats` and kept in `LastError()`.

## Lifetime of a dead entry (repair round)

One rule serves three needs that pull against each other: forget dead members (bounded
state, no re-gossip storms), never let old news overturn a verdict or lower an incarnation,
and still find the other side after a partition longer than the retention.

1. **Verdict.** A dead or left entry enters a node's view when it supersedes what the node
   holds for that ID (view entry or tombstone). The node's retention clock for it starts
   then. If the node held the member, the change is queued for gossip as before. If the node
   never held the ID (a second-hand verdict about a stranger, e.g. learned from a sync), the
   entry is kept but **not queued**: only sync replies carry it, so a verdict cannot be
   relayed onward by nodes that never knew the member.
2. **Retention.** For `DeadRetention` the entry is in the view: `Lookup` returns it, syncs
   carry it, a packet to that member leads with it, and dead (not left) entries are heal
   targets.
3. **Burial.** When the retention expires the entry leaves the view (`Lookup` →
   `ErrNotMember`, it is never sent again) and becomes a **tombstone**: the exact `Member`
   value (ID, region, incarnation, state), nothing else. A node holds at most
   `MaxTombstones` = 4096 tombstones; burying one more evicts the oldest-buried
   (`Stats.TombstonesEvicted`). A tombstone has no time limit of its own.
4. **Old news is answered, not accepted.** An update about a tombstoned ID that does not
   supersede the tombstone:
   - if it is dead or left (the same or an older verdict, e.g. from a peer whose retention
     has not expired yet) it is dropped. This is what stops the ping-pong the review found:
     a node that has buried an entry can never take it back from a slower peer;
   - if it claims the member alive or suspect (a stale replay, or a wiped node rejoining at
     a low incarnation), the tombstone's verdict goes back into the view for one more
     `DeadRetention` (`Stats.Reinstated`; no event, not queued). The node now answers with the
     verdict: the claimant's sync reply carries it and packets to the member lead with it, so
     a live owner refutes to verdict + 1. Then it is buried again.
5. **New life.** An update that supersedes the tombstone (in practice alive at a higher
   incarnation, i.e. a refutation) removes the tombstone and is applied as a newly joined
   member (`EventJoined`).
6. **Heal.** Every `SyncInterval` the heal sync picks one target from {dead entries in the
   view} ∪ {dead tombstones} ∪ {remembered seeds not live}. Left members are never contacted.
   Seeds given to `Join` are remembered exactly like `Config.Seeds` (self and duplicates
   dropped, at most `MaxSeeds` = 256).

**What holds (per node, while the ID's tombstone is held, i.e. fewer than 4096 later burials
on that node):** the node's record for an ID (view entry, else tombstone) never moves down
the `Supersedes` order, so its incarnation never decreases and a dead or left verdict is
overturned only by a higher incarnation. Purge is monotone: once buried, the ID is absent from
`Lookup` unless a contradicting live claim arrives (then the verdict, not the claim, comes
back) or a higher incarnation arrives. A dead entry is purged on every survivor and stays
absent with no periodic re-gossip (`TestDeadEntriesStayPurgedDST`: 5 nodes, crash and leave,
`DeadRetention` 10 s, sync every 2 s, watched for 3 × `DeadRetention` after the last purge;
every survivor purges the victim exactly once and reinstates nothing).

**What does not hold:** after a node evicts an ID's tombstone, that node accepts the ID as
new at any incarnation (a replayed alive(x, 0) would be believed by that node). Each node
also keeps a verdict about a stranger it learned from a sync for one full retention from when
it learned it, so a verdict can outlive its first burial by up to one `DeadRetention` per
node that had never heard of the member; it cannot cycle while fewer than `MaxTombstones`
(4,096) other burials occur before peers purge, because every node that buries it then holds a
tombstone. Past that bound a node that evicted the tombstone can re-admit the dead entry from a
peer that still holds it, for one more `DeadRetention` (not re-gossiped); a finite burst of K
deaths settles within ceil(K/4096) retention periods, sustained churn above 4,096 burials per
`DeadRetention` keeps the window open.

**Partition re-merge (PLAN 7.7, as worded here):** two sides of a partition of any length
re-merge after the heal if at least one node on either side holds, on the other side, a
reachable remembered seed (`Config.Seeds` or a seed it gave to `Join`) or a dead tombstone /
dead view entry of a node that is up. A cluster formed only through `Config.Seeds` and
`Join` always has a seed edge crossing any cut, so it re-merges while the seed at the end of
that edge is up; if those seeds are down, the tombstones still connect the sides until each
side has buried more than 4096 other members since. Without any of these (no live seed across
the cut and the other side's tombstones evicted) the sides stay apart: SWIM has no other
address source. Latency: the heal sync picks one candidate uniformly per node per
`SyncInterval`, so with h heal candidates of which u are up on the other side, a side of m
nodes makes its first useful contact after about `SyncInterval` × h / (u × m); many long-dead
tombstones slow the re-merge but do not prevent it. Proved by `TestSWIMLongPartitionHealDST` (partition held until each side has
buried the other, so longer than `DeadRetention` = 20 s; one variant with the seed across the
cut, one where the only seed crashed before the cut so only tombstones point across) and
`TestSWIMPartitionHealDST` (within the retention).

## Restart with lost state and the same ID — what is guaranteed

A restarted node starts at incarnation 0 with an empty view and joins its seeds. If any
member still holds an entry for its ID at incarnation i:
- if that entry is suspect/dead/left at i, or alive at i > 0, the node learns it from the
  join reply (the seed's whole view) or from the first piggybacked/destination-led update,
  and moves to incarnation i+1 (or the claim's +1), whose alive entry supersedes the old one
  everywhere by normal dissemination;
- if that entry is alive at exactly 0 (it crashed and restarted before anyone noticed and
  before it ever refuted anything), nothing needs to change: the new life is indistinguishable
  from the old one and stays at 0.
If the old entry was purged but its tombstone is still held (repair round), the seed answers
the join's alive(0) by bringing the recorded verdict back into its view and replying with it,
so the node refutes to the verdict's incarnation + 1 exactly as in the first case. Only if no
node holds the entry or a tombstone for the ID (each node evicts its oldest tombstone after
`MaxTombstones` later purges) is alive(0) accepted as new; nothing remembers an older life then.
Guarantee as tested (`TestSWIMRestartSameIDDST`, both cases, 500 seeds): after a wipe-and-
rejoin, every node holds the ID as alive within `rejoinBound` (30 s) with an incarnation ≥
the old entry's, and strictly greater when the old entry was not alive. Not guaranteed:
rejoin over an entry at `MaxUint32` (see Limits).

## Claims and the tests that prove them

| Claim | Test |
|---|---|
| Transition table alive/suspect/dead/left × message × lower/equal/higher incarnation, plus unknown members, events per transition | `TestStateTransitions` |
| Refutation rules, never-decreasing incarnation, no self-declared death, overflow handling, ignore after leave, death overturned only by higher incarnation | `TestIncarnationRules` |
| Merge is order-independent and duplicate-insensitive; merging partial views equals applying all updates; each entry is the `Supersedes`-maximum | `TestPropViewMergeIsCommutative`, `FuzzViewMergeLaws` |
| Incarnations in a view and a node's own incarnation never decrease; self always alive; view sorted | `TestPropIncarnationMonotonic` |
| Piggyback: exactly λ⌈log₂N⌉ transmissions; packets ≤ MaxPacket; freshest first; destination's suspicion leads; newer replaces older; stale not re-gossiped | `TestPiggybackSelectionAndLimit` |
| Shuffled round-robin; dead never probed; new member probed; lonely node silent | `TestProbeTargetRotation` |
| Ping/ack; ping-req after timeout through k distinct others; indirect ack saves; suspicion then death at the timeout; refutation cancels; relay forwards once with the requester's seq; relay expiry; timeout and limit scale with ⌈log₂N⌉ | `TestProbeAndIndirectProbe` |
| Join: all-seeds-fail → `ErrJoinFailed` + each cause; any-seed success; malformed reply; bootstrap; Start joins seeds; lonely retry; one join at a time; restarted node rejoins above its old entry | `TestJoin` |
| Sync merge+reply; malformed request; periodic sync hits live and dead/seed; purge after retention | `TestSyncRequests` |
| Operations after leave (errors, drops, timers stopped, `ErrClosed` across the wire) | `TestOperationsAfterLeave` |
| Oversize in/out, malformed datagrams, own-id datagrams, `ErrNotMember`, code table rows, config and identity validation | `TestErrors` |
| Encoding round trip, golden bytes, malformed table, encoder refusals | `TestWireRoundTrip`, `TestDecodeMalformed`, `TestEncodeRejectsInvalidMessages` |
| Decode never panics, decode∘encode identity, any inbound datagram/request keeps node invariants | `FuzzGossipWire` |
| `EncodeState` canonical | `TestEncodeStateIsCanonical` |
| Convergence within `convergeC`·⌈log₂N⌉ periods after faults stop, N=20/50/100, 5–10% loss, joins/leaves/crashes; cost bound | `TestSWIMConvergenceDST` |
| Zero suspicions and zero evictions under one one-way link (300 s, 12 nodes) | `TestSWIMAsymmetricLinkDST` |
| Without indirect probes the same scenario produces suspicions on every seed | `TestCanaryNoIndirectProbe` |
| Crash detected as dead at every live node within `detectBound`; no healthy node ever declared dead (5% loss) | `TestSWIMCrashDetectionDST` |
| Region partition: both sides declare each other dead; after heal views re-merge within `healBound` | `TestSWIMPartitionHealDST` |
| A paused (falsely suspected) node refutes, is never declared dead, raises its incarnation, all views agree | `TestSWIMRefutationDST` |
| Restart with lost state and same ID | `TestSWIMRestartSameIDDST` |
| Join with no reachable seed through the simulator (crashed and unknown seeds) | `TestJoinNoReachableSeedDST` |
| A dead verdict is never overturned without a higher incarnation (any observer, any scenario, across purges: the recorder keeps every verdict); self never suspect/dead; own incarnation monotonic per life | recorder check + invariant in every DST scenario |
| Dead/left entries are purged on every survivor exactly once and stay absent for 3 × `DeadRetention` (crash and leave, 5 nodes, sync every 2 s), each survivor keeps a tombstone (repair) | `TestDeadEntriesStayPurgedDST` |
| Nodes purging at different times never take a buried verdict back from a slower peer (repair) | `TestDeadEntryLifetime/two nodes…` |
| Old news about a buried member: equal/older verdicts dropped; live claims at or below it bring the verdict back (no event, not gossiped, buried again later); a higher incarnation is a new life; a wiped node at 0 is answered with its verdict and refutes above it; tombstones capped at 4096, oldest evicted, an evicted ID is new again (repair) | `TestDeadEntryLifetime` |
| Verdicts about members never held are kept but not gossiped (repair) | `TestDeadEntryLifetime/a death learned about an unknown member…` |
| Partition longer than `DeadRetention` re-merges, via a Join seed across the cut or via tombstones alone (repair) | `TestSWIMLongPartitionHealDST` |
| Join seeds retried while alone and used by the heal sync; self-only seeds never retried; seed cap (repair) | `TestJoinSeedsAreRemembered` |
| Relays bounded and O(1) under a ping-req flood (repair) | `TestRelayBounds` |
| View bounded by `MaxMembers` ≤ `MaxUpdates`; a full view still encodes; packets ≤ `MaxUpdates` entries (repair) | `TestViewBounds` |
| No callback runs inside the call that registered it; closed-node paths report `ErrClosed` (repair) | `TestCallbacksNeverRunInsideTheRegisteringCall` |
| Region-mismatch refutation, single self entry in leave packets, codes in 80–95 (repair) | `TestRepairNits` |
| Every `FuzzViewMergeLaws` seed runs under plain `go test` (repair) | `TestFuzzSeedsRunWithoutSkipping` |

Every DST scenario goes through `simtest.Sweep` + `simtest.RequireDeterministic` (two runs,
digests compared first), runs its seeds sequentially (see "Why DST seeds run sequentially";
the top-level tests run in parallel with each other), and uses the gossip code table in
`sim.Config.Codes`.

## Measurements

All inside the `dev` container on the shared 4-core droplet (load average 5–10 from other
builders during these runs). Periods = `ProbeInterval` (1 s). Convergence is measured from
the instant faults stop (`HealNet` at 30 s) to the first 500 ms poll at which every live
node holds the identical member list (same IDs, all alive, same incarnations), rounded up to
whole periods. Scenario: N nodes in 3 regions, link loss drawn per seed in 5–10% on every
link (datagrams and requests), N/10 late joiners, N/20 crashes and N/20 clean leaves at
random times in [1 s, 27 s].

500 seeds (`DST_SEEDS=500`, base 1, plus pinned seeds), `TestSWIMConvergenceDST`:

| N | ⌈log₂N⌉ | periods to converge: min / median / p99 / max | max ÷ ⌈log₂N⌉ | bound (`convergeC`=8) |
|---|---|---|---|---|
| 20 | 5 | 0 / 8 / 21 / 22 | 4.4 | 40 |
| 50 | 6 | 0 / 18 / 27 / 32 | 5.3 | 48 |
| 100 | 7 | 10 / 26 / 32 / 33 | 4.7 | 56 |
| 200 (33 seeds, one-off, not in the suite) | 8 | 21 / 33 / 36 / 36 | 4.5 | 64 |

Fitted constant: the worst case stays between 4.4 and 5.3 × ⌈log₂N⌉ from N=20 to N=200,
so `c` = 8 in the test leaves margin. What dominates: a node crashed shortly before the
heal must first be probed, then wait out the suspicion timeout (4 × ⌈log₂N⌉ periods), then
the death must spread; so the constant is mostly `SuspicionMult` + dissemination. The
median grows faster than log N between N=20 and 50 (8 → 18) because at N=20 many seeds
have no crash late in the window; read the max column for scaling. Astra's check
("log N or hidden linear growth") should fit across N with the crash time controlled.

Datagrams sent per node per period (pings, acks, ping-reqs, relays, leave notices; sync
requests are not datagrams and are not counted), measured over the nodes alive for the
whole run: N=20 median 3.25 / max 4.14; N=50 median 3.02 / max 3.86; N=100 median 2.91 /
max 3.52; N=200 median 2.79 / max 3.15. Bound `maxMilliMsgsPerNodePeriod` = 5.000 (the
first bound of 4.000 failed at N=20 with 4.058 and 4.235 on three seeds of 500 — loss
drives indirect probes and relays). Plus two sync requests per node per `SyncInterval`.

False deaths (`EventDied` about a node that neither crashed nor left, at any observer):
0 in every one of the 1,500 convergence runs and 500 crash-detection runs.

Crash detection, `TestSWIMCrashDetectionDST` (N=20, 5% loss on every link, victim crashed
at 10 s after everyone knows it), time until every live node holds it dead, 500 seeds:
min 21.5 s, median 23.0 s, p99 27.0 s, max 28.0 s. Bound `detectBound` = 40 s. Expected:
first failed probe within ~1–2 periods, suspicion timeout 4 × 5 = 20 s, dissemination.

Partition heal, `TestSWIMPartitionHealDST` (N=12, region r0 cut off at 10 s until each side
holds the other dead, then `HealNet`), time until all views are identical, 500 seeds:
min 4.0 s, median 6.5 s, p99 11.0 s, max 12.0 s. Bound `healBound` = 60 s (`SyncInterval`
is 15 s; the first heal sync from any of 12 nodes comes much sooner).

Asymmetric link, `TestSWIMAsymmetricLinkDST` (N=12, one random ordered pair one-way blocked
from 10 s, 300 s run): 0 suspicions and 0 deaths in all 500 seeds.
Canary `TestCanaryNoIndirectProbe`, same scenario with indirect probes off: 416–537
suspicions per seed (median 492), so the asymmetric test fails on every seed; 0 evictions,
because the suspected node always refutes in time. The asymmetric test therefore asserts
zero suspicions as well as zero evictions: "zero evictions" alone would not see this bug.

Wall time (inside the container):
- `go test ./internal/gossip` (default 3 seeds + 3 pinned): 6–10 s; with `-cover` 8.5 s.
- `DST_SEEDS=500` of the whole package: 641 s with load average ≈ 9 (482 s in an earlier
  run at load ≈ 5–9 before the last fix). The N=100 convergence sweep alone is the critical
  path (seeds run sequentially, see below); peak memory 82 MB.

### Why DST seeds run sequentially

With `t.Parallel()` inside the `simtest.Sweep` callback, every finished simulation stayed
alive until the parent test ended (`RequireDeterministic`'s cleanup holds the `*sim.Sim`,
and parallel subtests' cleanups run with the parent): 5.1 GB RSS at 500 seeds, killed by
the kernel OOM killer. Seeds now run sequentially; the eight top-level DST tests and the
three N sizes of the convergence test run in parallel instead. Peak 82–132 MB.

## Seeds that failed (pinned in `internal/gossip/testdata/seeds.txt`)

- 33, `TestSWIMCrashDetectionDST`: the victim's join failed on all three seeds (5% loss on
  request and reply) and it crashed at 10 s before its first periodic seed sync, so no node
  had ever heard of it and nothing could detect it. Fix: a node that knows no live member
  other than itself re-joins its seeds every probe period, one join at a time
  (`Stats.Rejoins`). The test now also checks its precondition (victim known as live by
  everyone at the crash) and fails with a `setup:` message if not.
- 78 and 469, `TestSWIMCrashDetectionDST`: found while trying an optimisation that stopped
  re-gossiping entries a node first learned through sync. Most nodes then learned a joiner
  only through sync and nobody re-gossiped it; one node did not hear of it for over 20 s.
  The optimisation was reverted: every view change is re-gossiped.

## Gate commands run (through `docker compose run --rm dev`)

Final gate: `agent_work/gates/gate.sh 01-gossip` from the host on commit `8199b0e`
(image `sha256:d445694d2ac5…`, go1.26.11), comment baseline `ceb5438`:

```
G0  tools/gofmt/vet/build/detlint/panics/deps/comments/selfcontained  pass
G1  unit           pass   12s   COVERAGE internal/gossip: 97.5%, internal/sim: 95.4%
G2  property       pass   93s   rapid 2000 checks, seed 142497097537330 (gossip package 8.9 s)
G3  fuzz           pass  102s   FuzzGossipWire, FuzzViewMergeLaws, FuzzSimSchedule, 30 s each
G4  race           pass   65s   (gossip package 59.4 s)
G5  dst            pass  740s   500 seeds from base 142497097537330 (gossip package 738.6 s)
G6  docker         pass
gate: overall=pass counts_for_landing=true
```

The gossip package's 500-seed sweep took 738.6 s in the gate, above the ~10-minute target in
the builder rules, on the shared droplet (see Requests). The result files stay uncommitted
under `agent_work/01-gossip/gates/`.

Individual commands while building:
- `gofmt -l internal/gossip` → empty; `go vet ./internal/gossip` → clean.
- detlint `-root /src -allow agent_work/gates/detlint-allow.txt internal/env internal/sim
  internal/gossip` → 0 findings (19 files, 3 packages); `nocomments` → clean;
  `paniccheck` → clean.
- `go test -cover ./internal/gossip` → ok, 97.5%.
- `DST_SEEDS=500` package run → PASS (numbers above).

## Uncovered branches (97.5% before the repair; repair-round list below)

All are defensive and unreachable through the public API, or reachable only in ways the
simulator does not produce:
- `Start` early return on a second `Start` — now covered; `refute` while closed: a closed
  node drops datagrams and sync callbacks before decoding, so nothing reaches `refute`.
- Suspicion-timer callback when closed, or when the member changed since: `Leave` and every
  state change stop the timer first, so the stale-fire branch is reached only if the sim
  delivers a stopped timer (it does not).
- `onTick`/`onSync` while closed: timers are stopped by `Leave`.
- `nextTarget` final `return "", false` after the two-pass loop: the second pass either
  returns a target or returns on an empty rotation.
- `piggyback` when a lead entry does not fit: `MaxPacket ≥ MinPacket` always fits one
  maximum header and one maximum update.
- The queue comparator's `return 0`: queue orders are unique.
- `sendWith` encode error, `syncWith` request encode error, `OnRequest` reply encode
  error: only for a view of more than 65,535 members (the u16 count); messages built by the
  node are otherwise always valid.

Repair round, 97.4% (gate G1): the uncovered statements are `refute` while closed,
`apply` with an empty ID or invalid state (`Decode` rejects both), the suspicion timer firing
closed or stale, `nextTarget`'s final return, `onTick`/`onSync` while closed, `piggyback`'s
lead-does-not-fit and comparator `return 0`, the `sendWith` encode error, the `syncWith`
encode error (now reported through `AfterFunc(0)`; unreachable since `MaxMembers` ≤
`MaxUpdates`) and the `OnRequest` reply encode error (same reason). All defensive, reasons as
above.

## Errors intentionally ignored (`_ =`)

None in product code. (`Timer.Stop`'s bool result is not an error.)

## Panics added

None. No line added to `agent_work/gates/panic-allowlist.txt`.

## Limits and things not done

- No authentication: anyone who can send a datagram can claim any state at any
  incarnation. A forged `dead(id, MaxUint32)` evicts that ID permanently (it cannot refute
  past `MaxUint32`); a forged alive at a high incarnation is refuted by the real node only
  while it can still go higher. This is plain SWIM's trust model.
- Incarnation is volatile (not on `env.Disk`). A restart relies on learning its old entry
  (see the restart section); a restart after the old entry was purged everywhere starts at 0,
  which is correct because nothing remembers it.
- Suspicion has no Lifeguard extensions (no confirmation-based shortening, no local health
  multiplier). The timeout is `SuspicionMult × ⌈log₂N⌉ × ProbeInterval`.
- A node that is suspected but cannot hear anyone (fully isolated) is declared dead after
  the timeout, as in SWIM; when isolation ends it learns its death from the first packet led
  by its own entry or from a heal sync, and refutes.
- Sync messages carry the whole view; at most 65,535 members (the u16 count).
- The heal list is {dead members} ∪ {dead tombstones} ∪ {remembered seeds not live}, sorted
  and deduplicated; left members are not contacted (they left on purpose). Repair round: see
  "Lifetime of a dead entry" for what is and is not guaranteed after a long partition.
- `Config.MaxPacket` is assumed equal across the cluster: an inbound packet larger than
  this node's own limit is dropped as malformed.

## Requests to the orchestrator

- Code table rows: gossip registers `CodeAppMin+16..+20` (80–84), inside its block 80–95
  (pinned by `TestRepairNits`).
- The 500-seed sweep of this package now takes about 6–8 minutes on the shared droplet
  (352 s at load 6–8, 484 s in the gate; it was 8–12 minutes). It is CPU-bound: the N=100
  convergence seeds run in four parallel shards. If G5 gets tight when all Wave B packages
  land, the N=100 case is still the one to move to a separate nightly sweep.
- No simulator change request. No detlint exemption needed.

## Repair round (review REVIEW-1 FAIL + adversarial run, one repair)

Brief: `objstore-p2-01-gossip-repair1.md`. Every fix has a regression test written first and
committed failing (`852ef2b`, on top of the API-only stub commit `1943c70`); the
implementation is `875ce69`, with follow-ups `f28be3f`, `516807a`, `91b4b61`.

### Findings, tests, changes

| Item | Test that now proves it (failed before the fix) | What changed |
|---|---|---|
| R1 dead/left entries resurrect after `DeadRetention` | `TestDeadEntryLifetime/two nodes that purge a dead member at different times never resurrect it in each other` (the reviewer's two-harness repro, 50 more sync rounds), `TestDeadEntriesStayPurgedDST` (crash and leave) | tombstone floor; old verdicts dropped; strangers' verdicts not queued; see "Lifetime of a dead entry" |
| R2 relay state under a ping-req flood | `TestRelayBounds` (10,000 ping-reqs → 1,024 relays held, 8,976 counted in `Stats.RelaysDropped`; pruning on arrival without a tick; no relay when the ping send fails; expiry list ≤ 2 × cap under ack-at-once) | relays in a map keyed by relay sequence (O(1) ack lookup) plus an expiry FIFO compacted at 2 × `MaxRelays`; pruned at every ping-req and tick; `MaxRelays` = 1024; relay kept only if `sendWith` reports the ping sent |
| R3 view past `MaxUpdates`, piggyback bound, callbacks inside the registering call | `TestViewBounds` (cap 10: 11 refusals, `ErrViewFull`, known members still change, purge frees room; a full 65,535-member view still encodes as a sync and its packets decode; `MaxMembers` range), `TestCallbacksNeverRunInsideTheRegisteringCall` | `Config.MaxMembers` (default and maximum `MaxUpdates`), new IDs and reinstatements refused when full; `take` stops at `MaxUpdates` entries; `syncWith` reports an encode failure through `AfterFunc(0)`; the empty-targets join reports `ErrClosed` if the node left first |
| R3 hostile-input cost (found while writing the 65,535-member test: 93 s before) | same test, 0.69 s after | queue indexed by ID (enqueue O(1)); rotation insert O(1) (append + swap into a random remaining slot, membership in a set); `View` appends and sorts lazily on the next ordered read, `Remove` is O(1) |
| C4 `retention-monotonicity`, C23 `stale-replay-after-purge` | `TestDeadEntryLifetime/a stale alive below a purged death brings back the death, never the stale entry` (4 cases incl. both seed-901 repros), `.../an equal or older verdict…dropped`, `.../a higher incarnation after the purge is a new life`, `.../a wiped node rejoining at incarnation 0 is told its purged death and refutes above it`, `.../the tombstones are capped…`, `.../one member buried and reinstated many times…` | the fix, not a narrowed claim: bounded floor of `MaxTombstones` = 4096 per node, oldest evicted; exact guarantee in "Lifetime of a dead entry" |
| C19 `partition-heal-retained-false`, `default-retention-long-partition` | `TestJoinSeedsAreRemembered`, `TestDeadEntryLifetime/the heal sync contacts a purged dead member but never a purged left one`, `TestSWIMLongPartitionHealDST` (two variants) | seeds given to `Join` remembered like `Config.Seeds`; dead tombstones are heal targets; claim stated in "Lifetime of a dead entry" |
| R4 refute ignored a region mismatch | `TestRepairNits/a peer holding this node alive at its incarnation in another region is refuted` | `refute` returns early only for alive at own incarnation **and** own region |
| R5 leave packets carried alive and left self | `TestRepairNits/leave packets carry this node's entry once, as left` | `Leave` enqueues the left self entry (replacing the queued alive one) before sending |
| R6 fuzz seeds skipped | `TestFuzzSeedsRunWithoutSkipping` runs every `FuzzViewMergeLaws` seed through `rapid.MakeFuzz` and fails if one skips | seeds replaced by five 64–1024-byte patterns that rapid accepts (random-looking bytes are rejected by rapid's draws; zero-heavy and small-valued patterns run) |
| R7 self-only seeds, join `AfterFunc` after leave, loose assertion | `TestJoinSeedsAreRemembered/a node whose only seed is itself never retries a join`, `TestCallbacksNeverRunInsideTheRegisteringCall/a Join pending when the node leaves reports ErrClosed once`, `TestJoin/no second join…` now asserts exactly 1 | self and duplicates filtered from the seed list at `New`/`Join`; closed check in the deferred callback |
| R8 notes | this section; "runs its seeds in parallel subtests" corrected; restart section rewritten | — |
| Wire codes in the gossip block | `TestRepairNits/every gossip wire code lies in the package's block 80 to 95` | `ErrViewFull` → `CodeAppMin+20` = 84 |
| Also: a join reply arriving after `Leave` | `TestCallbacksNeverRunInsideTheRegisteringCall/a seed reply arriving after Leave reports ErrClosed and merges nothing` | (behaviour unchanged; was uncovered) |

### Numbers re-measured (repair round)

500 seeds (`DST_SEEDS=500`, base 1, plus pinned 33/78/469), inside the dev container, commit
`93edee0`'s product code (the MEASURE lines were byte-identical between the runs before and
after the allocation cut, so the numbers below hold for both):

| Measure | Before repair | After repair |
|---|---|---|
| Converge N=20, periods min/median/p99/max | 0/8/21/22 | 0/8/21/21 |
| Converge N=50 | 0/18/27/32 | 1/18/26/29 |
| Converge N=100 | 10/26/32/33 | 10/26/32/34 (bound 56) |
| Datagrams per node per period, median/max, N=20/50/100 | 3.25/4.14, 3.02/3.86, 2.91/3.52 | 3.233/4.011, 3.027/3.778, 2.905/3.554 (bound 5.000) |
| False deaths (1,500 convergence + 500 detection runs) | 0 | 0 |
| Crash detection N=20, 5% loss, min/median/p99/max | 21.5/23.0/27.0/28.0 s | 21.5/23.0/27.0/28.0 s (bound 40 s) |
| Partition heal within retention, N=12 | 4.0/6.5/11.0/12.0 s | 4.0/6.5/10.5/11.5 s (bound 60 s) |
| Long partition heal, seed across the cut (new) | never (C19) | 2.5/7.0/14.5/16.0 s (bound 90 s) |
| Long partition heal, tombstones only (new) | never | 2.5/12.0/29.0/35.0 s (bound 90 s) |
| Asymmetric link suspicions / canary suspicions | 0 / 416–537 | 0 / 416–543 (median 492) |

Max ÷ ⌈log₂N⌉ after: 4.2 (N=20), 4.8 (N=50), 4.9 (N=100); `convergeC` = 8 unchanged. The
small shifts come from the changed random draws of the O(1) rotation insert and from
verdicts about strangers no longer being re-gossiped.

Wall time and what was done about it. The first 500-seed run after the fixes hit `go test`'s
default 10-minute timeout with the N=100 convergence sweep at seed 439 of 503 (load ≈ 11):
every other DST had finished within about 2.5 minutes and one sequential N=100 loop ran alone.
Two changes, neither touching coverage (same seeds, same assertions):
1. `TestSWIMConvergenceDST` runs its seeds in 4 shards in parallel (seed mod 4; each shard
   sequential, each seed's simulation released when its subtest ends, so memory stays at
   four simulations; a REPLAY line names the shard, and `-seed=N` runs only that shard).
   Wall time 591 s at load ≈ 12.
2. Per-seed cost: per-node scratch slices for outgoing piggyback lists and incoming decoded
   updates, string reuse for known IDs/regions on decode, an early return in `apply`, and
   no view copies in the sync/indirect-probe scans: about 10% less CPU per N=100 seed
   (old vs new test binary, same seeds, 3 runs each: user CPU ≈ 23.1 s → 20.7 s).
   Wall time 352 s at load 6–8.
Default-seed `go test ./internal/gossip`: 14–15 s (it was 6–10 s; the 65,535-member view test
and the 8,200-cycle tombstone-backlog test add about 2 s, the new DSTs the rest).

### Gate and adversarial run (repair round)

`agent_work/gates/gate.sh 01-gossip` from the host on `02be72f` (image
`sha256:6b0fb0bbeb63…`, go1.26.11, rapid/DST seed 3016995830548):

```
G0  tools/gofmt/vet/build/detlint/panics/deps/comments/selfcontained  pass
G1  unit           pass   13s   COVERAGE internal/gossip: 97.4%, internal/sim: 95.4%
G2  property       pass   95s   rapid 2000 checks
G3  fuzz           pass  101s   FuzzGossipWire, FuzzViewMergeLaws, FuzzSimSchedule, 30 s each
G4  race           pass   75s
G5  dst            pass  486s   500 seeds (gossip package 484.1 s, was 738.6 s)
G6  docker         pass
gate: overall=pass counts_for_landing=true
```

A first gate run on the same commit also passed every stage (G5 428 s) but reported
`counts_for_landing=false` (`dirty_tree`): a `gossip.test` binary left at the repo root by a
`go test -cpuprofile` profiling run. Removed; the run above is the counting one. Commits after
`02be72f` change only this notes file.

`agent_work/adversarial/01-gossip/run-all.sh` (harness not edited), on `02be72f`:

`SUMMARY held=72 broken=1 setup_errors=0 elapsed_seconds=42` (was held=69 broken=4).

- Now HELD: C4 `retention-monotonicity` ("incarnation stayed monotonic across purge"), C23
  `stale-replay-after-purge` ("dead verdict survives stale replay after purge"), C19
  `partition-heal-retained-false` (4 seeds re-merged within 120 s), C19
  `default-retention-long-partition` ("default-retention partition healed"); the seed control
  heals in 2.75 s (was 1.75 s); C21 wipe-after-5m30s still holds through the tombstone path.
- Still BROKEN: C7 `relay-once-original-sequence` — **repaired, the driver expects the old
  behaviour.** The driver relays a ping-req to `Target: "target"`, an ID that is not a
  simulator node, so `env.Transport.Send` fails synchronously with `env.ErrUnreachable`; it
  then expects the ack for that never-sent ping to be forwarded. Review finding 2 asked for
  exactly the opposite ("no relay kept for a ping that was not sent"), pinned by
  `TestRelayBounds/a relay whose ping could not be sent is not kept`. The forwarding rule the
  attack meant to check (forward once, with the requester's sequence number) still holds and is
  pinned by `TestProbeAndIndirectProbe/a ping-req is relayed and the target's ack forwarded…`
  (which relays to a reachable member).
- The run rewrites `agent_work/adversarial/01-gossip/evidence/`; those files were restored to
  the adversary's committed record, and this section is the record of the repair-round run.

### API changes

Added: `Config.MaxMembers`; `MaxTombstones`, `MaxRelays`, `MaxSeeds`; `Node.Tombstones()`;
`ErrViewFull`, `CodeViewFull` (84); `Stats.RelaysDropped`, `Stats.ViewFull`,
`Stats.TombstonesEvicted`, `Stats.Reinstated`. `Config.Validate` refuses more than
`MaxSeeds` seeds and `MaxMembers` outside 1..`MaxUpdates`. Behaviour changes a caller can
see: `Join` seeds are remembered; a verdict about a member this node never held is not
re-gossiped; a stale live claim about a buried member brings the verdict back into the view;
`EncodeState` now also covers tombstones, the relay count, the seed list and the new counters.
Test-only (`export_test.go`): `RelayCount`, `RelayBacklog`, `TombBacklog`, `SeedCount`.

### Chosen not to do

- No byte bound on the view: `MaxMembers` bounds the entry count so a sync always fits the
  wire's u16 count, but 65,535 entries with long IDs and regions encode to more than the
  simulator's 16 MiB request limit; such a sync fails with `env.ErrTooLarge` and is counted in
  `Stats.SyncErrors` (nothing bricks; the view stays usable). A deployment with long IDs sets
  `MaxMembers` lower.
- No deduplication of ping-reqs by (requester, sequence): the cap and the O(1) ack path bound
  the work; a duplicated ping-req costs one extra relay slot until it expires.
- The adversarial harness was not edited (as briefed).

## REVIEW-2 nits

- Finding 1: `FuzzViewMergeLaws` seeds 3 and 4 are rapid buffers from
  `internal/gossip/testdata/merge-seeds.txt` (hex uint64 words, blank line between seeds) that
  draw 12 and 36 updates covering all four IDs and states; seeds 0–2 keep the old empty-draw
  patterns. `TestFuzzSeedsDrawNonEmptyViews` fails unless at least two seeds draw five or more
  updates; `TestFuzzSeedsRunWithoutSkipping` still covers every seed. The draw is factored into
  `drawMergeUpdates` and the checks into `checkViewMerge`; the property is unchanged.
- Finding 2: the "cannot cycle" sentence above now carries the `MaxTombstones` bound.
- Finding 3: `View.Apply` runs the ordered compaction when the ID list exceeds twice the member
  count, so `len(ids) <= 2*Len()` after any Apply and `<= 2*Len()+2` after any Remove, with no
  ordered read needed. `TestViewApplyRemoveWithoutReadStaysBounded` (1,000 Apply/Remove rounds,
  checked through the `ViewBacklog` export_test hook) failed before (7 IDs for 2 members after
  5 rounds) and passes after. Merge laws untouched.
- Checks (Docker): `gofmt -l .` empty; `go vet ./...` clean;
  `go test ./internal/gossip/...` ok (11.6 s); `-race` ok (52.5 s); `-run 'Prop|Laws|FuzzSeeds'
  -rapid.checks=2000` ok; coverage 97.4%. Full gate not run (orchestrator).
