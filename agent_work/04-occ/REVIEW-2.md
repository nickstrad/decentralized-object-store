VERDICT: FAIL

## Repair brief item table

| Item | Status | Evidence (code, tests, and old-code `8ad382e1` check) |
|---|---|---|
| A1 — One version per key | FIXED | `Record` (`record.go:24-27`) holds `(v Version, ok bool)`, `Doc.records` (`doc.go:17`) is `map[string]Version`, `ConflictError`/`ErrConflict`/`CodeConflict`/`IsConflict` and `maximal` are deleted, slot 131 stays unassigned (`errors.go:25-26`), `Decode` rejects repeated keys (`doc.go:189-191`) and `OCC\x01` (`doc.go:10, 179`); proven by `TestDecodeMalformedTable`, `TestErrorCodesRoundTrip`, `TestConditionalPutTable`, `TestConditionalDeleteTable` (old `8ad382e1` returned `*ConflictError` and kept sibling slices). |
| A2 — Pure LWW total order | FIXED | `Version.Compare` (`version.go:48-53`) orders by `Stamp.Compare(Wall, Logical, Node)` then `VersionID.Compare`; `Record.Merge` (`record.go:48-58`) takes the max; scalars share records (`SetScalar` = unconditional `Put`); proven by `TestLWWWinnerTable`, `TestPropMergeLaws`, `FuzzOCCMergeLaws`, `TestPropConvergence`, `TestMergeIsTotalOnDecodedStates`, `TestCanaryMergePrefersLocal`. |
| A3 — Causality agrees with clock & far-future stamps | FIXED | `Store.successor` (`store.go:243-256`) picks `max(HLC.Now(), held.stamp.successor(node))` without advancing `s.hlc`, returning `ErrClockExhausted` (with full rollback) only at `(MaxInt64, MaxUint32)`; proven by `TestPropCausalWriteAlwaysWins`, `TestFarFutureStampPinsOnlyItsKey`, `TestStoreClockEdges`. |
| A4 — Version vectors for detection only | PARTLY FIXED | `Version.Supersedes` classifies causal overwrites vs discards (including equal event sets and forged causally-below winners as discards: `TestLWWWinnerTable`, `TestCausalOverwriteIsNotADiscard`, `TestPropNoDiscardWithoutConcurrency`), **but** `Version.Clock()` (`version.go:46`) inflates over a discarded dot hole so a second write after a discard causes another replica holding the lost version to misclassify the concurrent replacement as causal (Finding 1). |
| A5 — Every LWW discard counted and logged | PARTLY FIXED | Saturating persisted `LWWDiscards`, deterministic `Discard.String()`, 64-entry `DiscardTail`, `OnDiscard` hook, and persist-failure rollback are implemented and tested (`TestLWWDiscardCountedAndLogged`, `TestPropEveryLossRecorded`, `TestFailedPersistLeavesNoTrace`, `TestCanaryDiscardNotRecorded`), **but** incoming concurrent losers are dropped silently (allowing total invisible loss if the writer crashes/wipes: Finding 2), dot-gap inflation suppresses recording on other replicas holding the loser (Finding 1), and `publish` runs after `Disk.Sync` (Finding 3). |
| A6 — Preconditions & deterministic ETag | FIXED | `Precondition.Holds` (`record.go:91-99`) checks single version/tombstone ETag (`v.ID()`); unconditional write sets `ctx = held.v.Clock()` (causal overwrite); proven by `TestConditionalPutTable`, `TestConditionalDeleteTable`, `TestPropCheckAndSetModel`, `TestETagDeterministic`, `TestConditionalWriteRaceDST`. |
| A7 — Deletes compete under A2 | FIXED | Tombstones are `Version`s (`tomb: true`) competing identically in `Version.Compare`/`Record.Merge`/`Store.classify`; proven by `TestLWWWinnerTable` and `TestLWWDiscardCountedAndLogged` (both directions). |
| A8 — Quota counters & recount | FIXED | `UpdateQuota` stays CAS with bounded retry; `RecountQuota` (`quota.go:91-115`) skips writing when `rerr == nil && cur == want`; proven by `TestQuotaCheckAndSet`, `TestQuotaRecount`, `TestPropQuotaNoStaleApply`, `TestQuotaOvershootUnderPartitionDST` (driven by quota-key `OnDiscard`, settles in 2 rounds). |
| A9 — Wiped-node epoch guarantee | FIXED | `Node.open` draws a fresh epoch on wipe/corrupt (`node.go:159-162, 170-188`); `TestWipedNodeTakesAFreshEpoch` (`node_test.go:145-198`) writes 3 times post-wipe so `hazardReuseEpoch=true` misclassifies `v2` as causal and fails `oracle.check`. |
| B1 — Bounded work on hostile input | FIXED | `decoder.vector`/`decoder.version` (`encoding.go:174-216`) stop before building on error; `Version.descends` (`version.go:74-99`) is a linear two-pointer scan; multi-version records rejected at key repeat (`doc.go:189-191`); `checkLaws` (`prop_test.go:34-49`) deduplicates keys with `O(1)` per key. `TestHostileMessagesBoundedWork` (16 MiB, linear ops/byte) and `TestReviewerHostileInputIsRejectedEarly` (seed `testdata/fuzz/FuzzOCCDecode/reviewer-28k-multi-version`) pass; all fail on `8ad382e1` (where `encoding.go:180-209` built after error, `version.go:53-62` binary-searched per entry, and `maximal` + `checkLaws` blew up quadratically). |
| B2 — HLC observation | FIXED | `Store.observe` (`store.go:129-135`) observes every stamp in `Doc.Stamps()` (sorted key order), counting each `ErrClockAhead` without hiding other keys; empty doc observes nothing; `Doc.MaxStamp` (`doc.go:135-144`) returns `(Stamp, bool)` and handles negative walls. Both reviewer reproductions pass in `TestHLCObservesEveryStamp` (`store_test.go:495-546`); both fail on `8ad382e1` (`store.go:45, 143` observed only global `MaxStamp()`, which returned `Stamp{Wall: 0}` on empty/negative docs). |
| B3 — Failed persist leaves no trace | FIXED | Snapshot-and-restore transaction (`begin`/`touch`/`end`/`rollback`, `store.go:137-171`) restores doc entries, actor, counter, rotations, ahead, discards, and HLC, publishing discards only after `persist` succeeds. `TestFailedPersistLeavesNoTrace` and `TestNodeFailedPersistLeavesNoTrace` (both `Write` and `Sync` failures across put/delete/scalar/quota/inbound push) pass; all fail on `8ad382e1` (`node.go:291-296, 359-413` mutated `n.store` before `persist` with no rollback). |
| B4 — Fuzz seeds run without SKIP | FIXED | `fuzzSeeds` (`prop_test.go:195-205`) uses four 32 KiB seeds; `go test -v ./internal/occ/...` confirms all 4 `FuzzOCCConditionalWrite`, 4 `FuzzOCCMergeLaws`, and 11 `FuzzOCCDecode` seeds execute and PASS with zero SKIPs (on `8ad382e1:prop_test.go:328-335, 519-525` all 9 seeds skipped). |
| B5 — Typed write helpers on `*Node`; remove `Node.Store()` | FIXED | `Node.Store()` removed; `Node.Reader()` (`node.go:99-104`, `store.go:330-353`) is read-only; `PutTag`, `PutACL`, `PutObjectMeta`, `PutManifest`, `SetRoute`, `SetContentType` on `*Node` (`node.go:423-445`) go through `Node.Put` (persist + broadcast); `TestNodeTypedWritesGoThroughCommit` (`meta_test.go:182-228`) verifies durability and replication (absent on `8ad382e1`). |
| B6 — `Node.peers()` sorts; quota DST asserts contest | FIXED | `Node.peers()` (`node.go:212-224`) sorts and compacts peers (`TestPeerChoiceIgnoresMembershipOrder`, fails on `8ad382e1:node.go:186-197`); `quotaRun` (`dst_test.go:581-587, 618-621`) fails if `contested == 0` (absent on `8ad382e1:dst_test.go:543-594`). |
| Also — Wire codes 128–143, 120 s fuzz, build notes | PARTLY FIXED | Codes moved to 128–143 with 131 unassigned (`errors.go:19-30`, `TestErrorCodesRoundTrip`), goldens updated, 120 s `FuzzOCCDecode` recorded (`build-notes.md:310-312, 447`), and `build-notes.md` rewritten for LWW — **but** `build-notes.md:94-98` overstates the discard guarantee in ways contradicted by Findings 1 and 2. |

---

## Owner-ruling design checks (a)–(e)

- **(a) Winner independent of arrival order and locality:** Confirmed. `Version.Compare` (`version.go:48-53`) compares `Stamp{Wall, Logical, Node}` then `VersionID` (SHA-256 over canonical length-prefixed encoding of all fields), which is a strict total order on all distinct decoded versions (honest or forged). `Record.Merge` and `Doc.Merge` compute the per-key maximum under this order, and `Doc.Encode` serializes keys in sorted order, so equal version sets are always byte-identical.
- **(b) Is EVERY discard recorded?** **No** — two MAJOR defects and one real-runtime NIT found (see Findings 1, 2, 3 below).
  - *Failed persist:* Handled cleanly — `Store.end` rolls back all state and does not publish discards; a failed `Merge` leaves the loser held until a later successful sync.
  - *Incoming loser rejected + writer crashes/wipes:* **Fails** (Finding 2).
  - *Dot-gap inflation on honest 3-node run:* **Fails** on the non-writer replica holding the loser (Finding 1).
  - *Crash between `Disk.Sync` and `publish`:* Unreachable in `internal/sim`, reachable in Chunk 6 (Finding 3).
- **(c) Independent oracle in `TestPropEveryLossRecorded`:** Confirmed. `oracle` (`helpers_test.go:117-186`) tracks the transitive `seen` graph purely from `(v.ID(), held.ID())` observed at each local write, without calling `VersionVector`, `Descends`, `Supersedes`, `Dot`, or `Stamp`, and verifies every acknowledged version is current, transitively seen by a later acknowledged write, or reported via `OnDiscard`, plus `sum(LWWDiscards) == or.logged`.
- **(d) Far-future-stamp decision:** Confirmed sound. An acknowledged local write can never lose to the version it replaced: `Store.successor` (`store.go:244-256`) takes `max(HLC.Now(), held.stamp.successor(node))` (`Stamp.After` compares `(Wall, Logical)` strictly, so the new stamp beats `held` regardless of `Node` or `VersionID`), and at `(MaxInt64, MaxUint32)` refuses the write with `ErrClockExhausted` and rolls back. Because `s.hlc` is not moved, one forged far-future stamp pins only its own key (concurrent writes from other nodes to that key lose and are logged until wall time catches up, while other keys keep normal stamps); one forged stamp at `(MaxInt64, MaxUint32)` permanently bricks only that single key.
- **(e) No reader-facing sibling/conflict terminology:** Confirmed. Grep over all non-test files in `internal/occ/*.go` found zero occurrences of `sibling` or `conflict` (the only two test-file occurrences explicitly assert rejection of old `OCC\x01` magic and unassigned code 131).

---

## New findings

### 1. MAJOR — `Version.Clock()` inflates over discarded dot gaps, causing another replica holding a concurrent lost version to misclassify its replacement as causal (zero discards recorded on that replica)
- **Where:** `internal/occ/version.go:46` (`Version.Clock`), `internal/occ/store.go:190-200` (`Store.classify`), `internal/occ/store.go:248-265` (`Store.successor`), `internal/occ/store_test.go:378-402` (`TestDotsAreNeverReused`).
- **What is wrong:** When node `a` writes `lost` with dot `(a@1, 2)`, discards `lost` on `a` for concurrent `winner` `(b@1, 1)`, and then writes `next1` with dot `(a@1, 3)` and context `{a@1: 1, b@1: 1}`, a *second* write `next2` on top of `next1` (by `a` or by `b` after syncing `next1`) computes `ctx = next1.Clock() = next1.ctx.Join({a@1: 3}) = {a@1: 3, b@1: 1}`. Because `VersionVector` represents contiguous downsets `1..N`, `{a@1: 3, b@1: 1}` falsely claims that `next2` saw `(a@1, 2)` (`lost`). When a third replica `c` still holding `lost` merges `next2`, `next2.Supersedes(lost)` returns `true` (`2 <= 3`), so `c` drops `lost` as a causal overwrite with `c.LWWDiscards() == 0`, zero log lines, and zero `OnDiscard` callbacks (including missed quota recount triggers on `c`). This directly violates A4/A5, contradicts `build-notes.md:97-98` ("One lost version held by several replicas is counted once on each of them"), and falsifies the assertion in `TestDotsAreNeverReused` (`store_test.go:400`), which only passed because it stopped at `next1` (one write) instead of `next2` (two writes).
- **Reproduction:**
  ```go
  // Extend TestDotsAreNeverReused (store_test.go:378-402) by one write before merging into c:
  a, ca, la := newLoggedStore(t, "a", 1, 1)
  b, cb, _ := newLoggedStore(t, "b", 1, 2)
  c, _, lc := newLoggedStore(t, "c", 1, 3)
  mustPut(t, a, "k", "one", occ.IfNoneMatch())                           // dot (a@1, 1)
  mustMerge(t, b, a.Doc())
  ca.now += 10
  cb.now += 20
  lost := mustPut(t, a, "k", "lost", occ.IfMatch(a.Record("k").ETag()))  // dot (a@1, 2), ctx {a@1:1}
  mustMerge(t, c, a.Doc())                                               // c holds lost
  mustPut(t, b, "k", "winner", occ.IfMatch(b.Record("k").ETag()))        // dot (b@1, 1), ctx {a@1:1}
  mustMerge(t, a, b.Doc())                                               // a discards lost (len(la.discards)==1)
  ca.now += 100
  mustPut(t, a, "k", "next1", occ.IfMatch(a.Record("k").ETag()))         // dot (a@1, 3), ctx {a@1:1, b@1:1}
  ca.now += 100
  mustPut(t, a, "k", "next2", occ.IfMatch(a.Record("k").ETag()))         // dot (a@1, 4), ctx {a@1:3, b@1:1}
  mustMerge(t, c, a.Doc())
  // FAILS: next2.Supersedes(lost) == true, so c.LWWDiscards() == 0 and len(lc.discards) == 0,
  // even though neither next1 nor next2 ever saw lost.
  ```
- **Smallest fix:** In `Store.classify` (`store.go:190-200`), when a discard is recorded and `loser.Clock().Get(s.actor) > winner.Clock().Get(s.actor)`, rotate `s.actor.Epoch = s.freshEpoch()`, reset `s.counter = 0`, and increment `s.rotations` (all three fields are already saved/restored by `undo`/`rollback`). Because `s.actor` retires before issuing any higher counter after a gap, no future `Version.Clock()` can ever absorb the discarded dot into its downset. Add the `next2` step to `TestDotsAreNeverReused`.

### 2. MAJOR — Silently dropping incoming concurrent losers lets an acknowledged write that reached a peer and lost an LWW merge disappear from every node with zero discard records anywhere
- **Where:** `internal/occ/doc.go:76-90` (`Doc.mergeVersion`), `internal/occ/store.go:267-280` (`Store.Merge`), `internal/occ/node.go:159-160` (`Node.load`).
- **What is wrong:** `Doc.mergeVersion` returns early at line 82 (`if next.Equal(cur) { return false }`) without invoking `replace` whenever an incoming version loses to the locally held version. Consequently, when `n2` receives a concurrent version `v1` from `n1` and rejects it in favor of `n2`'s higher-stamped `v2`, `n2` records nothing (`LWWDiscards() == 0`, no log line, no `OnDiscard`). The design assumes `n1` will always stay up with its disk intact until it pulls `v2` and records the discard itself (`build-notes.md:94-97`). If `n1` instead crashes permanently (`s.Crash("n1")`) or loses its state blob (`s.WipeRejoin("n1")` or `s.BitRot("n1", occ.StateBlob)`) after delivering `v1` to `n2` and before merging `v2`, `v1` vanishes with no causal successor and **zero discard records on any node anywhere** — even though `n2` explicitly received `v1` and discarded it via LWW. On `WipeRejoin` or `BitRot`, both nodes end up live, healthy, and converged on `v2` (`n1` loads an empty doc, pulls `v2` onto an absent key, so `held.ok == false` and `n1` records 0 discards too), leaving `LWWDiscards() == 0` everywhere.
- **Reproduction:**
  ```go
  // Two nodes n1, n2 in sim with Discard hooks/logs on both:
  // 1. Partition {n1} vs {n2}.
  // 2. n1.Put("k", []byte("v1"), occ.IfNoneMatch()) at t=10s -> acknowledged & synced to n1's disk.
  // 3. n2.Put("k", []byte("v2"), occ.IfNoneMatch()) at t=20s -> acknowledged & synced to n2's disk.
  // 4. Deliver n1's doc (or delta for "k") to n2 (e.g. one-way partition n1->n2 or direct msgPush/msgDelta):
  //    n2.store.Merge sees v2 > v1, returns changed=false, records 0 discards.
  // 5. Before n1 receives v2, trigger s.WipeRejoin("n1") (or s.Crash("n1")+s.BitRot("n1", occ.StateBlob)+s.Restart("n1"),
  //    or simply leave n1 permanently crashed via s.Crash("n1")).
  // 6. HealNet() and run until converged.
  // Result: all nodes hold v2, v1 is gone from every node's memory and disk, v1 has no causal successor,
  // and n1.LWWDiscards() == 0 && n2.LWWDiscards() == 0 with zero discard log lines anywhere.
  ```
- **Smallest fix:** In `Doc.mergeVersion` / `Store.Merge`, when `cur.ok && next.Equal(cur) && v.id != cur.v.id`, also classify `(key, winner=cur.v, loser=v)` if `!cur.v.Supersedes(v)` and `v.id` has not already been recorded in a bounded per-store seen-loser set/ring (e.g. keyed by `VersionID` alongside `tail`, so repeated gossip deliveries of the same losing version are not recounted; set `u.changed = true` when a new discard increments `s.discards` so the counter persists). Update `TestLWWDiscardCountedAndLogged` and add a `sim` regression test for the `v1`-sent-then-writer-wiped/crashed schedule.

### 3. NIT — Crash window between `Disk.Sync` and `Store.publish` can drop the discard log line and `OnDiscard` callback in Chunk 6 (and `DiscardTail` is volatile across restarts)
- **Where:** `internal/occ/store.go:153-163` (`Store.end`), `internal/occ/store.go:284-297` (`encodeState`).
- **What is wrong:** `Store.end` persists the new `Doc` (loser removed) and incremented `s.discards` counter via `s.persist(s.EncodeState())` (`Disk.Write` + `Disk.Sync`), and only afterward runs `s.publish(u.pending)` to append to `s.tail`, call `s.log`, and invoke `s.onDiscard`. In single-threaded `internal/sim`, `end` is atomic so a crash cannot intervene; in Chunk 6's real process, a crash (`SIGKILL`/power loss) after `Disk.Sync` and before `s.publish` completes leaves the node restarting with the winner on disk and `LWWDiscards() == 1`, but with no log line emitted, empty `DiscardTail()`, and no `OnDiscard` callback (potentially missing a quota recount). Additionally, `DiscardTail()` is not persisted in `OCCS`, so even a crash after `publish` clears `DiscardTail()`.
- **Reproduction:** In `Store.end` (`store.go:154-162`), terminate the host process immediately after `s.persist` returns `nil` and before line 161 executes; reload the state blob from disk and observe the winner present, `LWWDiscards() == 1`, `DiscardTail() == nil`, and zero `Log`/`OnDiscard` invocations.
- **Smallest fix:** Either persist a bounded discard-tail / pending-log outbox inside `OCCS` (draining `Log`/`OnDiscard` on `open`) or explicitly document this crash-after-sync window in `build-notes.md` as a Chunk 6 operational trade-off (e.g. Chunk 6 also reconciles quotas periodically or on restart).

---

## Verification summary

### Verified by running (in Docker `dev` container)
- `docker compose run --rm -T dev go test -v -count=1 -cover ./internal/occ/...` — PASS, 30.55 s, 98.4% statement coverage; verified all unit, property, DST, canary, and fuzz seed tests pass with **zero `SKIP`s** across all seeds of `FuzzOCCDecode`, `FuzzOCCConditionalWrite`, and `FuzzOCCMergeLaws`.
- `docker compose run --rm -T dev go test -race -count=1 ./internal/occ/...` — PASS, 17.76 s.
- `docker compose run --rm -T dev sh -c 'DST_SEEDS=50 go test -count=1 -run "DST|Canary" ./internal/occ/...'` — PASS, 15.43 s.
- `docker compose run --rm -T dev go test -count=1 ./internal/occ/... -rapid.checks=2000` — PASS, 45.80 s.
- `docker compose run --rm -T dev go list -f '{{.Imports}}' ./internal/occ` and `go list -deps ./internal/occ` — stdlib + `internal/env` only (no external deps in product code).
- `git diff --name-only main..HEAD` and `git log --oneline 8ad382e1f84766701edc7a19f0262a1818a01a9b..HEAD` — confirmed only `internal/occ/` and `agent_work/04-occ/` were modified; frozen simulator packages (`internal/env`, `internal/sim`, `internal/sim/simtest`) and `agent_work/gates/panic-allowlist.txt` are untouched.
- Grep across `internal/occ/*.go` — confirmed zero comments, zero panics, zero goroutines/channels/mutexes/wall-clock/global-rand/`maphash`/floats in product code, single map range (`doc.go:40`) collected into a local slice and sorted, and zero sibling/conflict terms in product code.

### Verified by reading
- Read `agent_work/04-occ/objstore-p2-04-occ-review2.md`, `agent_work/04-occ/objstore-p2-04-occ-repair1.md`, `agent_work/04-occ/REVIEW-1.md`, `agent_work/04-occ/build-notes.md`, `agent_work/04-occ/gates/result.json`, `PLAN.md` (sections 4.2 Chunk 4, 7.5, 9.1), `internal/env/env.go`, `internal/sim/disk.go`, every `.go` file in `internal/occ/`, and old `8ad382e1` versions of `store.go`, `doc.go`, `node.go`, `version.go`, `errors.go`, `encoding.go`, `prop_test.go`, and `dst_test.go` via `git show`.
- Identified Findings 1, 2, and 3 by code inspection and trace analysis against `version.go`, `doc.go`, `store.go`, `node.go`, and `store_test.go`.
