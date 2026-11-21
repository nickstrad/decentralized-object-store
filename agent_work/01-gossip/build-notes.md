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
- Errors: `ErrMalformed`, `ErrNotMember`, `ErrClosed`, `ErrJoinFailed`; `Codes()` and
  `CodeTable()` (wire rows `CodeAppMin+16..+19` = 80..83).
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
| `Seeds` | none | joined at `Start`, and contacted by the heal sync while not live |

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
`ErrJoinFailed` joined (`errors.Join`) with each seed's cause. Every `SyncInterval` a node
syncs with one random live member and one random entry from {dead members} ∪ {seeds that are
not live}; after a partition this is how the halves find each other: the contacted node sees
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
If every entry was purged (after `DeadRetention`), alive(0) is accepted as new.
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
| A dead verdict is never overturned without a higher incarnation (any observer, any scenario); self never suspect/dead; own incarnation monotonic per life | recorder check + invariant in every DST scenario |

Every DST scenario goes through `simtest.Sweep` + `simtest.RequireDeterministic` (two runs,
digests compared first), runs its seeds in parallel subtests, and uses the gossip code table
in `sim.Config.Codes`.

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

(see the section "Gate result" below for the gate run; individual commands while building:)
- `gofmt -l internal/gossip` → empty; `go vet ./internal/gossip` → clean.
- detlint `-root /src -allow agent_work/gates/detlint-allow.txt internal/env internal/sim
  internal/gossip` → 0 findings (19 files, 3 packages); `nocomments` → clean;
  `paniccheck` → clean.
- `go test -cover ./internal/gossip` → ok, 97.5%.
- `DST_SEEDS=500` package run → PASS (numbers above).

## Uncovered branches (97.5%)

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
- The node does not deduplicate seeds that are also live members in the heal list (a live
  seed is excluded; a dead one may be picked twice as often if it is both dead and a seed —
  harmless).
- `Config.MaxPacket` is assumed equal across the cluster: an inbound packet larger than
  this node's own limit is dropped as malformed.

## Requests to the orchestrator

- Code table rows: gossip registers `CodeAppMin+16..+19` (80–83) to leave 64–79 free.
  Chunk 6 builds the node's table from every chunk's `Codes()`; please reserve 80–95 for
  gossip, and tell the other Wave B builders to pick disjoint blocks (`env.NewCodeTable`
  rejects duplicates).
- The 500-seed sweep of this package takes about 8–11 minutes on the shared droplet; the
  N=100 convergence sweep is the critical path. If the gate's G5 budget gets tight when all
  Wave B packages land, the N=100 case is the one to move to a separate nightly sweep.
- No simulator change request. No detlint exemption needed.
