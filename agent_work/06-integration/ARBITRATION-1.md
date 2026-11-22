VERDICT: FAIL

The chunk may not land. A MAJOR remains in the readable-or-declared-lost guarantee: an acknowledged stripe can lose fewer than `m+1` shards to orphan collection and still become permanently unreadable because another shard was never stored. The fixed detector counts only orphan records. It leaves the manifest live with no loss declaration after healing. This is not a concurrent-write loss permitted by the LWW ruling.

Reviewed HEAD `fc6c2a2c1f4448b4cfd0110af9a1addf0a424a04`; old-code comparison `94c1a8468806277af07014eda566a79a1a4e0896`.

| REVIEW-2 new finding | Verdict | Evidence |
|---|---|---|
| 1. Orphan records overwritten across maintenance ticks | PARTLY FIXED | `inventory.go:267–301` now writes an immutable value under each version/stripe/index/holder key; `TestOrphanRecordsAccumulateAcrossMaintenanceTicks` fails on the old tree and passes on HEAD. The stronger residual guarantee still fails in the new deterministic reproduction below. |
| 2. Duplicate `realNet.Request` callbacks | FIXED | `runtime.go:563–634` rejects invalid timeouts and oversized bodies before registering a request/timer; later paths share the pending-ID completion guard. Four callback-count rows fail on the old tree and pass on HEAD, including under runtime race tests. Oversized peer requests are rejected before encoding. |
| 3. Flip retry after its two writes diverge | FIXED | `migrate.go:537–545` lets the owner repair its route. `TestFlipAfterACrashBetweenItsWritesLeavesTheBucketWritable` fails on the old tree with a compensated saga and passes on HEAD, checking every node's route/owner, new writes through three nodes, and the old object's readability. |
| 4. Cross-key content matching in the acknowledgement oracle | FIXED | `oracle_test.go:63,78–79` prefixes content with the metadata key. `TestAckOracleKeepsIdenticalPayloadsOnTwoKeysApart` fails with the old oracle explaining k1 as superseded and passes with the current oracle. |
| 5. Retried ancestor sync skips unfinished ancestors | FIXED | `realenv.go:175–190` walks the full ancestor chain and skips only individual completed entries. `TestSyncRetriedAfterAMidChainFailureSyncsEveryNewAncestor` fails on the old tree with a nondurable `shards` ancestor and passes on HEAD. |
| 6. Connection tracking, config error, fuzz signature, stale notes | FIXED | Both post-panic/post-cancellation tracking tests and the negative OCC-fanout message assertion fail on the old tree and pass on HEAD. The two stale notes are corrected. Commit `94c1a846` narrows the fuzz retry; eight synthetic script cases confirm the intended retry/refusal behavior. |

**Remaining MAJOR: orphan collection combined with an unwritten shard.**

Locations: `internal/node/inventory.go:339–354` (`lostByOrphans`), called at `:371`; `internal/storage/node.go:65` sets the default acknowledgement quorum to `min(k+1,k+m)`, and `:198–201` explicitly permits outstanding shard requests after acknowledgement. The claim in the final “Residual limit, restated” paragraph of `agent_work/06-integration/build-notes.md` is consequently stronger than the implementation.

Reproduction on HEAD, seed 1, no corruption, disk loss, concurrent write, or migration:

1. Four nodes, `k=2,m=2`, one 512-byte stripe. Writer n003 cannot send to n002. Shards reach n000, n001 and n003, satisfying the default quorum of three.
2. Suppress metadata delivery from the writer after the two remote shards land but before their replies commit the manifest. The successful PUT callback isolates the writer.
3. Wait beyond the 30-second orphan grace. n000 and n001 durably record and collect their shards; n002 never had a shard to collect or record.
4. Heal all links and run 120 simulated seconds. Membership and metadata converge. All four nodes still list the manifest live; all four have `LostDeclared == 0`; none has `~lost/<version>`.
5. GET fails: only one of four shards verifies, while two are required. `lostByOrphans` sees exactly two recorded removals, so `c > m` remains false forever. Rebuild cannot recreate a stripe from one shard.

Observed version `c7bd04bd6c737491`; orphan removals are n000/index 3 and n001/index 0; n002/index 1 was never stored. The independent test fails with `storage: get g/k: stripe 0: 1 of 4 shards verified, need 2: storage: unavailable`. This is a structural steady-state failure, not an insufficient healing timeout. The same arithmetic applies to the production `k=4,m=2` quorum: five durable shards, two collected, three remaining, one never stored.

To close: make loss handling cover orphan collection combined with shards that never became durable. Establish enough durable evidence to decide whether at least k recoverable shards remain, or change collection/acknowledgement semantics so it cannot create this state. Do not simply lower the orphan-count threshold: that would discard recoverable objects when all shards did land. Add this regression and the corresponding readable control; retain the multi-tick and 1/2/>window size cases. Correct the residual statement to match the resulting guarantee. Re-run the post-heal oracle and relevant package checks.

Self-contained regression follows. Put it only in a temporary export of HEAD as `internal/node/arbitration_test.go`, alongside the existing test helpers. Run through Docker using a temporary mount and `go test -count=1 ./internal/node -run TestArbitrationOrphansPlusAnUnwrittenShard -v`. It intentionally fails on this HEAD.

```go
package node_test

import (
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
	"testing"
	"time"
)

func TestArbitrationOrphansPlusAnUnwrittenShard(t *testing.T) {
	cfg := smallConfig()
	cfg.Layout = storage.Layout{K: 2, M: 2, StripeSize: 1024}
	cfg.OrphanGrace = 30 * time.Second
	fixed := sim.Range{Min: 2 * time.Millisecond, Max: 2 * time.Millisecond}
	net := sim.DefaultNetConfig()
	net.Latency, net.InterRegionLatency = fixed, fixed
	h, err := newHarness(1, 4, cfg, net)
	if err != nil {
		t.Fatal(err)
	}
	h.gc.off = true
	if err = h.s.RunFor(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	w := h.ids[3]
	missing := h.ids[2]
	if err = h.node(w).CreateBucket(storage.BucketSpec{Name: "g"}); err != nil {
		t.Fatal(err)
	}
	if err = h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	if err = h.s.SetLinkLoss(w, missing, sim.PPM); err != nil {
		t.Fatal(err)
	}
	done := false
	var oi storage.ObjectInfo
	h.node(w).PutObject(storage.PutRequest{Bucket: "g", Key: "k", Data: make([]byte, 512)}, func(o storage.ObjectInfo, e error) {
		done = true
		oi = o
		if e != nil {
			t.Errorf("put: %v", e)
		}
		if e = h.s.Isolate(w); e != nil {
			t.Error(e)
		}
	})
	landed := func() bool {
		return len(shardsByStripe(h, h.ids[0], "g")) == 1 && len(shardsByStripe(h, h.ids[1], "g")) == 1
	}
	if err = h.s.RunUntilConverged(h.s.Now().Add(10*time.Second), landed); err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("put completed before metadata isolation")
	}
	for _, id := range h.ids[:3] {
		if err = h.s.SetLinkLoss(w, id, sim.PPM); err != nil {
			t.Fatal(err)
		}
	}
	if err = h.s.RunUntilConverged(h.s.Now().Add(10*time.Second), func() bool { return done }); err != nil {
		t.Fatal(err)
	}
	if err = h.s.RunFor(cfg.OrphanGrace + 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if len(h.gc.deleted) == 0 {
		t.Fatal("no orphan collection")
	}
	t.Logf("acknowledged %s; orphan deletions=%v", oi.Version, h.gc.deleted)
	h.s.HealAll()
	if err = h.s.RunFor(120 * time.Second); err != nil {
		t.Fatal(err)
	}
	if !h.converged() {
		t.Fatal("cluster did not converge after heal")
	}
	for _, n := range h.up() {
		rd, e := n.OCC().Reader()
		if e != nil {
			t.Fatal(e)
		}
		if !rd.Record(occ.ManifestKey("g", "k")).Live() {
			t.Fatal("manifest unexpectedly not live")
		}
		if rd.Record(occ.TagKey("g", "k", "~lost/"+oi.Version)).Live() {
			t.Fatal("loss unexpectedly recorded")
		}
		t.Logf("%s: live=true lost_record=false lost_declared=%d", n.ID(), n.Stats().LostDeclared)
	}
	requireLiveOnlyIfReadableAfterHeal(t, h, "g", "k", oi.Version)
}
```

**Other requested conclusions.**

- The specific record-overwrite mechanism is fixed. Different ticks/holders use different keys; repeated writes of the same key have identical values. OCC persists the records before collection, replication merges them independently, and restart rebuilds `orphaned` by scanning metadata in `expectedAll`. Object tombstones and later rewrites do not delete these bucket-scoped records, and a new object version does not inherit the old version's records. I found no ordinary supported operation that erases these entries. The remaining failure above misses an *unwritten* shard, not a recorded removal.
- `checkLiveReadableOrLost` is actually called after healing in the full-node put/get, bit-rot, crash and partition scenarios. It enumerates live manifests from one converged metadata replica, reads each through a chosen live node, and permits an error only with a matching version's loss record. Its self-check passes by proving the oracle rejects a live object with more than m corrupt shards. This checks all listed keys at convergence, not every possible reading node or schedule. The new reproduction covers a schedule absent from the existing scenarios.
- The flip regression constructs the post-crash mixed metadata state and exercises the actual saga retry. It does not literally crash/restart a second concurrent coordinator. The owner check and existing Pointer reconciliation support the fix; the test's scope is accurately limited to the critical retry state.
- Transport callbacks are once-only while the runtime loop is active: early refusal schedules one timer; all registered-request paths use `complete`, which clears pending before calling back; stale timer generations cannot fire again. `Send` has no callback; `serve` guards reply delivery; service `call` guards completion with its local once flag and CAS state. Shutdown stops the loop and can suppress callbacks, explicitly permitted by `internal/env/env.go:70–71` when the caller incarnation closes. The fix does not claim to deliver callbacks after shutdown.
- No new product concurrency, wall-clock use, or unordered iteration was introduced in `internal/node`. Per-version iteration is sorted; each shard list comes from the sorted inventory union. Determinism lint passed over the 11 gate packages. `git diff main..HEAD -- internal/env internal/sim` is empty. The changed tests strengthen checks; none is removed or weakened.
- The fuzz retry is acceptable for this short operational gate. It requires a progress line, the indented bare deadline line, exactly one FAIL, and no crasher/panic/hung-worker marker; the second failure is fatal and retries are reported. Synthetic cases verified one retry followed by success, repeated deadline failure, and immediate refusal of setup-only, incidental-message, crasher, panic, hung-worker, and multiple-FAIL output. This is an output signature, not proof that a rare nonreproducing stall was harmless; that existing residual limitation remains. No new live fuzz campaign was run for this arbitration.

**Verification by running.**

- `docker compose run --rm -T dev go test -count=1 ./internal/node/... ./internal/noderuntime/... ./internal/storage/... ./cmd/...`: PASS (node 135.212 s; runtime 7.559 s; storage 5.130 s; HTTP 1.902 s; both command packages passed).
- `go test -race -count=1 -p 2 ./internal/noderuntime/... ./internal/storage/... ./cmd/...` through Docker with `GOMAXPROCS=2`: PASS.
- Node regression subset under `-race -count=1`: PASS (2.364 s).
- Old tree exported with `git archive`; current changed test files overlaid except `oracle_test.go`, which deliberately remained old: the five behavioral regression classes and connection/config tests fail for their intended reasons, not build failures. The 1/2/6-stripe coverage test and readable-or-lost oracle self-check pass on both trees; neither is represented as an old-code failure. The oversized-peer test also passed old code in this run; the new up-front validation is confirmed by code inspection.
- New unwritten-shard scenario: FAIL on HEAD, reproduced twice, including 120 simulated seconds after heal and explicit absence of loss records on every node.
- Determinism lint: PASS, 72 files in 11 packages. Initial lint invocation named a nonexistent command subdirectory; corrected to build the lint module's root command and reran successfully.
- Fuzz-script synthetic signature checks: all eight expected outcomes held (host shell with a temporary fake `go`, not product Go execution).
- `go test -count=1 ./internal/node -run Prop -rapid.checks=2000 -timeout 10m` through Docker with `GOMAXPROCS=2`: PASS (135.692 s).
- `go vet ./internal/node/... ./internal/noderuntime/... ./internal/storage/... ./cmd/...` through Docker: PASS.
- `DST_SEEDS=50 GOMAXPROCS=2 go test -count=1 ./internal/node -run 'DST$' -timeout 15m` through Docker: PASS (all ten named DST scenarios, including migration cases).

**Verification by reading.**

Read both the review-2 findings and fix brief, all changed Go code/tests, the relevant inventory, metadata replication, object deletion, migration, transport, timer, service, disk and quorum paths; PLAN 2.5, 2.6, Chunk 6, 7.5 and 9.1; build notes and prior independent harness results. The reported 662-check harness and concurrent full gate were not rerun by this arbitration. No project file other than this report was written; temporary exports, regression source and logs remain under `/tmp`. The pre-existing/concurrent deletion of `agent_work/06-integration/gates/result.json` was not modified.
