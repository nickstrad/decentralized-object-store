VERDICT: FAIL

1. **MAJOR** — `internal/gossip/node.go:312-353`, `469-494`, `613`, `695`: Dead and left entries resurrect after `DeadRetention` and never stay purged in a multi-node cluster.
   - **What is wrong:** `build-notes.md` claims dead/left entries are kept for `DeadRetention` and then purged (and that after purge, `alive(0)` is accepted as new), backed only by the single-node unit test `TestSyncRequests/dead_members_are_purged_after_the_retention_period`. In any cluster with `N >= 2` nodes, dead and left entries ping-pong back into every node's view forever:
     1. Nodes record a death/leave at slightly different times (normal gossip propagation spread of 1–6 s, staggered `onTick` jitter up to `ProbeInterval`, or clock drift).
     2. `syncWith` (`node.go:613`) and `OnRequest` (`node.go:695`) always serialize `n.view.All()`, including dead and left entries right up to their local purge time.
     3. When node `A` purges dead/left member `C` (`node.go:488-493`) and then exchanges a sync with peer `B` that has not purged `C` yet, `A.apply(C)` sees `had == false`, calls `n.view.Apply(C)` (returning `true`), resets `A.local[C].changed = now` (a fresh full `DeadRetention` window), **and calls `n.enqueue(C)` (`node.go:351`)**, which re-gossips `C` `λ·⌈log₂N⌉` times across the cluster.
     4. When `B` purges `C` moments later, `A`'s piggybacked gossip or next sync re-inserts `C` into `B` with a fresh `DeadRetention` clock and re-enqueues it again.
     5. Consequence: dead and left members never leave cluster views, trigger periodic re-gossip storms every `DeadRetention`, keep long-gone dead nodes in `onSync`'s `heal` list indefinitely, and under churn grow `View`/`local` without bound until `View.Len() > MaxUpdates` (65,535), at which point all syncs and joins fail permanently.
   - **Reproduction:**
     ```go
     // In internal/gossip/dst_test.go with DeadRetention: 10*time.Second, SyncInterval: 2*time.Second:
     // Start 3 nodes (n000, n001, n002), wait 3s, have n002 call Leave() (or s.Crash(n002)).
     // Advance virtual time to 60s (6 * DeadRetention).
     // Assert Lookup(n002) returns ErrNotMember on both n000 and n001 and stays ErrNotMember at 70s.
     // Fails: n000 and n001 repeatedly purge n002 (Stats.Purged climbs) and re-infect each other via sync + enqueue.
     ```
     Or deterministically on two `harness` nodes `A` and `B` (`DeadRetention: 10s`): deliver `dead(C, 0)` to `A` at `t=0` and to `B` at `t=3s`; advance both to `t=11s` (`A` purges `C`); run one sync between `A` and `B` (`A` re-inserts `C` with `changed=11s` and enqueues `C`); advance to `t=14s` (`B` purges `C`); deliver one ping from `A` to `B` (`B` re-inserts `C` with `changed=14s` and enqueues `C`).
   - **Smallest fix:** Use a two-phase retention rule for dead/left entries without changing the wire format:
     - When building `KindSyncReq` to `peer`, include all live members plus any dead/left entry that is either `peer` itself (so a healed peer refutes for the full `DeadRetention`) or younger than `DeadRetention/2`.
     - When building `KindSyncResp` to `from`, include all live members plus any dead/left entry that is either `from` itself, listed as live in `req.Updates` (so a healed partition side learns we hold its members dead), or younger than `DeadRetention/2`.
     - Do not call `n.enqueue(u)` when applying an unknown (`!had`) dead/left entry learned from sync.
     - Add a multi-node DST test asserting dead and left entries are purged on every survivor and remain absent past `3 * DeadRetention`.

2. **MAJOR** — `internal/gossip/node.go:243-271`: Unbounded `n.relays` growth and `O(R²)` ack scan on `KindPingReq` floods.
   - **What is wrong:** `onPingReq` (`node.go:243-251`) unconditionally appends to `n.relays` and sends a `KindPing` to `m.Target` on every received `KindPingReq` datagram:
     - There is no cap on `len(n.relays)` and no deduplication on `(from, m.Seq)`.
     - Expired relays are pruned only in `onTick` (`node.go:417`), never in `onPingReq` — so before `Start()` is called, or with a large `ProbeInterval` (up to `MaxInterval = 1h`), expired relays are never evicted by incoming traffic.
     - If `n.send(m.Target, ...)` fails synchronously (e.g. `env.ErrUnreachable` for an unknown target), the relay entry is still appended and retained.
     - `onAck` (`node.go:261-270`) linearly scans `n.relays` for every non-matching `KindAck`, causing `O(R²)` CPU work on a burst of `R` ping-reqs followed by `R` acks.
     This violates PLAN 2.6 / Review Q4 resource bounds on hostile input.
   - **Reproduction:**
     ```go
     h := newHarness(t, "self", gossip.Config{})
     for i := range 10_000 {
         h.deliver("p01", gossip.Message{Kind: gossip.KindPingReq, Seq: uint32(i), Target: "victim"})
     }
     // n.relays now holds 10,000 entries and 10,000 pings were emitted;
     // delivering 10,000 unmatched KindAcks scans 10^8 relay entries.
     ```
   - **Smallest fix:** Prune expired entries (`now > r.expires`) at the start of `onPingReq`, enforce a hard cap (e.g. `MaxRelays = 1024`, dropping and counting excess requests), and only append the relay if sending the `KindPing` succeeds (`sendWith` returning `bool`).

3. **MAJOR** — `internal/gossip/node.go:502-548`, `612-618`, `678-701`: A single valid `KindSyncReq` (or large `MaxPacket`) can exceed `MaxUpdates` (65,535), permanently bricking sync/join and violating async callback semantics.
   - **What is wrong:** Three related bound mismatches between `Node` and `wire.go`'s `uint16` count (`MaxUpdates = 65535`):
     1. `OnRequest` (`node.go:692-695`) inserts every update from `KindSyncReq` into `n.view` with no member-count bound, then encodes `n.view.All()`. A single valid ~524 KiB `KindSyncReq` (well inside `sim.DefaultMaxMessage` = 16 MiB) containing 65,535 distinct `StateAlive` member IDs raises `n.view.Len()` to 65,536 (> `MaxUpdates`). Every subsequent `OnRequest` reply (`node.go:695`) and `syncWith` call (`node.go:613`) permanently fails `Encode()` with `env.ErrInvalidArgument` (and if those 65,535 entries are `StateSuspect`, `OnRequest` arms 65,535 timers in one event).
     2. When `Encode()` fails in `syncWith` (`node.go:614-617`), `cb(n.lastErr)` is invoked **synchronously** inside `Join()`, violating the non-reentrant callback contract followed by `env.Transport.Request` and the empty-seeds path of `Join`.
     3. `Config` permits `MaxPacket` up to `MaxPacketLimit = 1 MiB` (`config.go:25`), which fits > 65,535 minimal 8-byte updates, but `piggyback` (`node.go:502-548`) never checks `len(out) < MaxUpdates`, allowing `sendWith` to produce an un-encodable packet after already incrementing `q.transmits`.
   - **Reproduction:** Send one valid `KindSyncReq` with 65,535 distinct `StateAlive` IDs (`"m00000"`..`"m65534"`) to `n.OnRequest`; `OnRequest` returns `env.ErrInvalidArgument`, `n.All()` has 65,536 entries, and every subsequent `n.OnRequest` or `n.Join` fails `Encode()` forever (with `Join` invoking `done` synchronously before `Join` returns).
   - **Smallest fix:** Enforce `n.view.Len() < MaxUpdates` (or a configurable `MaxMembers <= MaxUpdates`) before inserting new unknown IDs in `apply` (recording an error/counter when full and refusing to grow past `MaxUpdates`), add `len(out) >= MaxUpdates` to `take` in `piggyback`, and schedule `cb(err)` via `n.e.Clock.AfterFunc(0, ...)` if `syncWith` fails to encode.

4. **NIT** — `internal/gossip/node.go:285`: `refute` ignores a region mismatch at equal incarnation, allowing permanent view divergence.
   - **What is wrong:** `Supersedes` (`member.go:44-52`) breaks ties on equal `(Incarnation, State)` by `a.Region > b.Region`, but `refute` (`node.go:285`) returns early whenever `u.Incarnation == n.self.Incarnation && u.State == StateAlive`, without checking `u.Region == n.self.Region`. If a peer holds `{ID: "self", Region: "r1", Incarnation: 0, State: StateAlive}` while `"self"` has `Region: "r0"` at incarnation 0 (for example after restarting in another region, or after a forged datagram), `"self"` does not refute and the peer rejects `"self"`'s `"r0"` entry (`Supersedes("r0", "r1") == false`), so their views never converge.
   - **Reproduction:** Deliver `Member{ID: "self", Region: "r1", Incarnation: 0, State: StateAlive}` to peer `p` and sync `self` (`Region: "r0"`) with `p`; `self.Self().Region` remains `"r0"` while `p.Lookup("self")` remains `"r1"`.
   - **Smallest fix:** Change `node.go:285` to `if u.Incarnation < n.self.Incarnation || (u.Incarnation == n.self.Incarnation && u.State == StateAlive && u.Region == n.self.Region)`.

5. **NIT** — `internal/gossip/node.go:703-712`, `538`: `Leave()` leaves stale `StateAlive` self-entry in `n.queue`, sending contradictory self entries in leave packets.
   - **What is wrong:** `Start()` enqueues `alive(self, 0)` (`node.go:156`). `Leave()` sets `n.self.State = StateLeft` and updates `n.view`, then sends `KindGossip` with `lead = []Member{n.self}`, without updating `n.queue`. Because `piggyback` deduplicates `first` by full struct equality (`slices.Contains(first, q.m)` at `node.go:538`), `StateLeft != StateAlive`, so leave packets sent while `alive(self)` is still queued carry both `left(self, inc)` and `alive(self, inc)` in the same datagram.
   - **Reproduction:** Start a node with peers `p01..p03` and call `Leave()` before `alive(self)` finishes its `λ·⌈log₂N⌉` transmissions; decoding the emitted `KindGossip` datagrams shows `m.Updates` contains two entries for `"self"` (`StateLeft` followed by `StateAlive`).
   - **Smallest fix:** Call `n.enqueue(n.self)` in `Leave()` before sending (or deduplicate `first` by `ID` in `piggyback`).

6. **NIT** — `internal/gossip/prop_test.go:82-87`: Every seed corpus entry of `FuzzViewMergeLaws` skips during regular `go test`.
   - **What is wrong:** `f.Add([]byte{})`, `f.Add([]byte{0, 0, 0, 0})`, and `f.Add([]byte{255, ..., 255})` (8 bytes) are all too short for `rapid.MakeFuzz(propViewMergeCommutative)` to draw `updates`, `perm`, and `split`, so `go test -v ./internal/gossip -run '^FuzzViewMergeLaws$'` prints `SKIP` for all 3 seeds (zero non-skipped iterations outside `-fuzz`).
   - **Reproduction:** `docker compose run --rm -T dev go test -v ./internal/gossip -run '^FuzzViewMergeLaws$'` reports `SKIP` on `seed#0`, `seed#1`, `seed#2`.
   - **Smallest fix:** Add one or two longer seed slices (e.g. 64 and 128 bytes) to `f.Add` so the seed corpus executes `propViewMergeCommutative` to completion in normal `go test`.

7. **NIT** — `internal/gossip/node.go:419-422`, `587`; `internal/gossip/node_test.go:552`: Minor join/rejoin edge cases and a loose unit assertion.
   - **What is wrong:**
     - `node.go:419-422`: If `Config.Seeds` contains only the node's own ID (common in single-node bootstrap or shared seed lists), `onTick` increments `Stats.Rejoins` and schedules a no-op `AfterFunc(0, ...)` every probe period while alone.
     - `node.go:587`: The empty-targets `AfterFunc(0, func() { done(nil) })` timer is not stopped by `Leave()` and does not check `n.closed`, so calling `Leave()` before that timer fires still delivers `done(nil)` on a closed node.
     - `node_test.go:552`: `TestJoin/no_second_join_starts_while_one_is_in_flight` advances `5s` (`< SyncInterval/2 = 7.5s`) and asserts `if n > 2` instead of `if n != 1`, which would pass even if one duplicate join were erroneously started.
   - **Reproduction:** Construct a node with `ID: "self"`, `Seeds: []env.NodeID{"self"}`, call `Start()`, advance 5 probe intervals -> `Stats().Rejoins == 5`.
   - **Smallest fix:** Check for a non-self seed in `onTick` (or filter self from `n.cfg.Seeds` in `New`), guard the `AfterFunc(0)` callback in `join` with `if n.closed { done(fmt.Errorf("gossip: join: %w", ErrClosed)); return }`, and tighten `node_test.go:552` to `if n != 1`.

8. **NOTE** — `agent_work/01-gossip/build-notes.md:187` vs `255` and Review Questions 5–6 check.
   - `build-notes.md:187` still states "runs its seeds in parallel subtests", contradicting line 255 ("Seeds now run sequentially") after commit `cf4d5c0`; update line 187 when refreshing the notes.
   - **Integration & real runtime (Q5):** `Membership`, `Member`, `Event`, `Codes()` (`CodeAppMin+16..+19`), and `env.Process` are clean value-based interfaces that Chunks 2–4 and Chunk 6 can wire on both the simulator and the real UDP/TCP runtime. `Decode` copies strings (does not alias inbound socket buffers), `Encode` allocates fresh slices per send/request, `OnRequest` calls `reply` exactly once on every path, and all periodic/suspicion timers are stopped in `Leave()` and guarded by `n.closed` (save the minor `join` `AfterFunc(0)` in Finding 7).
   - **Rules & determinism (Q6):** Verified clean. Standard library only in product code; `pgregory.net/rapid` imported only from `prop_test.go`; zero comments, panics, floats, goroutines, channels, mutexes, wall-clock reads, global `rand`, or map-order iterations in `internal/gossip` product code; `internal/env`, `internal/sim`, `internal/sim/simtest`, `cmd/`, and `agent_work/gates/` are untouched.

## Verification

- **Verified by running (inside Docker `dev` container):**
  - `docker compose run --rm -T dev sh -c 'gofmt -l internal/gossip && go vet ./internal/gossip/...'` — clean (empty output, exit 0).
  - `docker compose run --rm -T dev go test -v -cover ./internal/gossip/...` — PASS, 97.5% statement coverage, 10.6 s (confirmed all named tests from PLAN.md 4.2 exist and run, canary `TestCanaryNoIndirectProbe` produces 458–519 suspicions per seed, and `FuzzViewMergeLaws` skips all 3 seeds in regular `go test`).
  - `docker compose run --rm -T dev go test -race ./internal/gossip/...` — PASS, 46.1 s.
  - `docker compose run --rm -T dev go test -v ./internal/gossip -run 'Laws|Prop' -rapid.checks=2000 -count=1` — PASS, 0.56 s (2,000 checks each on `TestPropViewMergeIsCommutative` and both `TestPropIncarnationMonotonic` checks).
  - `docker compose run --rm -T -e DST_SEEDS=50 dev go test -count=1 ./internal/gossip/...` — PASS, 68.0 s.
  - `docker compose run --rm -T dev go test ./internal/gossip -run '^$' -fuzz '^FuzzViewMergeLaws$' -fuzztime 3s -fuzzminimizetime 2s` — PASS, 2,656 execs.
- **Verified by reading:**
  - `git log main..HEAD --stat`, `git diff main..HEAD --stat -- internal cmd agent_work/gates`, `go.mod`, `agent_work/gates/panic-allowlist.txt`, `agent_work/01-gossip/gates/result.json`.
  - `PLAN.md` (sections 2, 4.2 Chunk 1, 5.1, 7.1, 7.7, 9.1), `agent_work/01-gossip/objstore-p2-01-gossip-build.md`, `agent_work/01-gossip/objstore-p2-builder-rules.md`, `agent_work/01-gossip/build-notes.md`, `agent_work/0b-simulator/build-notes.md`, `agent_work/0b-simulator/frozen-api.txt`, `internal/env/errors.go`.
  - Every file under `internal/gossip/` (`config.go`, `errors.go`, `member.go`, `wire.go`, `node.go`, `export_test.go`, `helpers_test.go`, `member_test.go`, `wire_test.go`, `node_test.go`, `prop_test.go`, `fuzz_test.go`, `dst_test.go`, `testdata/seeds.txt`, `testdata/wire-golden.txt`).
