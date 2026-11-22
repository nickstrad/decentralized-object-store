package node_test

import (
	"bytes"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
	"strings"
)

// TestLocalTransientDiskIOErrorMustNotDeclareHealthyObjectLost proves
// stateAbsent ignores transient disk read I/O errors (neither not found nor
// corrupt) so intact shards on k=2,m=0 and k=2,m=1 stripes never produce
// absence records or false loss declarations.
func TestLocalTransientDiskIOErrorMustNotDeclareHealthyObjectLost(t *testing.T) {
	t.Run("k2m0_two_transient_read_errors_on_one_holder", func(t *testing.T) {
		h, _ := lossHarness(t, 3, storage.Layout{K: 2, M: 0, StripeSize: 1024}, 2)
		oi, data, man := putEverywhere(t, h, 700)
		holder := man.Stripes[0].Shards[0].Node
		name, _ := holderOf(man, 0, holder)
		if err := h.s.FailReads(holder, name, 2); err != nil {
			t.Fatal(err)
		}
		err := h.s.RunFor(20 * time.Second)
		if err != nil {
			t.Fatalf("sim failed (converse oracle or invariant): %v", err)
		}
		if len(h.declared) != 0 {
			t.Fatalf("healthy object %s declared lost after transient disk ErrIO: %v", oi.Version, h.declared)
		}
		if h.node(holder).Stats().AbsentRecords != 0 {
			t.Fatalf("%s wrote %d absence records on a transient disk ErrIO", holder, h.node(holder).Stats().AbsentRecords)
		}
		for _, n := range h.up() {
			_, got, err := h.syncGet(t, n.ID(), "g", "k")
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("%s: get failed after transient ErrIO: %d bytes, %v", n.ID(), len(got), err)
			}
		}
	})

	t.Run("k2m1_one_rot_plus_sequential_transient_read_errors_on_healthy_holders", func(t *testing.T) {
		h, _ := lossHarness(t, 3, storage.Layout{K: 2, M: 1, StripeSize: 1024}, 3)
		oi, data, man := putEverywhere(t, h, 700)
		h0 := man.Stripes[0].Shards[0].Node
		h1 := man.Stripes[0].Shards[1].Node
		h2 := man.Stripes[0].Shards[2].Node
		n0, _ := holderOf(man, 0, h0)
		n1, _ := holderOf(man, 0, h1)
		n2, _ := holderOf(man, 0, h2)
		if err := h.s.BitRot(h2, n2); err != nil {
			t.Fatal(err)
		}
		if err := h.s.FailReads(h0, n0, 3); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunFor(2 * time.Second); err != nil {
			t.Fatalf("sim failed after first transient ErrIO: %v", err)
		}
		if err := h.s.BitRot(h2, n2); err != nil {
			t.Fatal(err)
		}
		if err := h.s.FailReads(h1, n1, 3); err != nil {
			t.Fatal(err)
		}
		err := h.s.RunFor(20 * time.Second)
		if err != nil {
			t.Fatalf("sim failed (converse oracle or invariant): %v", err)
		}
		if len(h.declared) != 0 {
			t.Fatalf("recoverable k=2,m=1 object %s declared lost after transient disk ErrIO: %v", oi.Version, h.declared)
		}
		if h.node(h0).Stats().AbsentRecords != 0 || h.node(h1).Stats().AbsentRecords != 0 {
			t.Fatalf("false absence records written on transient ErrIO: h0=%d h1=%d", h.node(h0).Stats().AbsentRecords, h.node(h1).Stats().AbsentRecords)
		}
		for _, n := range h.up() {
			_, got, err := h.syncGet(t, n.ID(), "g", "k")
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("%s: get failed: %d bytes, %v", n.ID(), len(got), err)
			}
		}
	})
}

// TestLossDeclarationRacesConcurrentOverwriteAndDelete proves a loss
// declaration racing a concurrent overwrite settles cleanly under LWW (either
// v2 stays readable or its discard is counted), and racing a concurrent delete
// converges usage and collects all shards of the lost version.
func TestLossDeclarationRacesConcurrentOverwriteAndDelete(t *testing.T) {
	for _, seed := range []int64{1, 2, 3, 4} {
		t.Run(fmt.Sprintf("concurrent_overwrite/seed=%d", seed), func(t *testing.T) {
			cfg := smallConfig()
			cfg.Layout = storage.Layout{K: 2, M: 2, StripeSize: 1024}
			h, err := newHarness(seed, 6, cfg, fixedNet())
			if err != nil {
				t.Fatal(err)
			}
			if err := h.s.RunFor(3 * time.Second); err != nil {
				t.Fatal(err)
			}
			if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: "g"}); err != nil {
				t.Fatal(err)
			}
			if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
				t.Fatal(err)
			}
			oi1, _, man1 := putEverywhere(t, h, 700)
			if err := h.s.Isolate(h.ids[0]); err != nil {
				t.Fatal(err)
			}
			rotted := 0
			for _, ref := range man1.Stripes[0].Shards {
				if ref.Node == h.ids[0] || rotted == 3 {
					continue
				}
				nm, _ := holderOf(man1, 0, ref.Node)
				if err := h.s.BitRot(ref.Node, nm); err != nil {
					t.Fatal(err)
				}
				rotted++
			}
			if rotted < 3 {
				nm, ok := holderOf(man1, 0, h.ids[0])
				if ok {
					_ = h.s.BitRot(h.ids[0], nm)
				}
				h.s.HealAll()
				if err := h.s.RunUntilConverged(h.s.Now().Add(30*time.Second), func() bool {
					cnt := 0
					rd, e := h.node(h.ids[1]).OCC().Reader()
					if e != nil {
						return false
					}
					for i, ref := range man1.Stripes[0].Shards {
						if rd.Record(node.AbsentKeyForTest(man1.ShardID(0, i), ref.Node)).Live() {
							cnt++
						}
					}
					return cnt > man1.Layout.M
				}); err != nil {
					t.Fatal(err)
				}
			}
			h.s.HealAll()
			if err := h.s.RunUntilConverged(h.s.Now().Add(30*time.Second), func() bool {
				rd, e := h.node(h.ids[0]).OCC().Reader()
				if e != nil {
					return false
				}
				cnt := 0
				for i, ref := range man1.Stripes[0].Shards {
					if rd.Record(node.AbsentKeyForTest(man1.ShardID(0, i), ref.Node)).Live() {
						cnt++
					}
				}
				return cnt > man1.Layout.M
			}); err != nil {
				t.Fatal(err)
			}
			if err := h.s.Partition([]env.NodeID{h.ids[0]}, h.ids[1:]); err != nil {
				t.Fatal(err)
			}
			data2 := []byte("fresh-concurrent-overwrite-payload-v2")
			oi2, err := h.syncPut(t, h.ids[1], storage.PutRequest{Bucket: "g", Key: "k", Data: data2})
			if err != nil {
				t.Fatal(err)
			}
			if err := h.s.RunFor(3 * time.Second); err != nil {
				t.Fatal(err)
			}
			h.s.HealAll()
			if err := h.s.RunUntilConverged(h.s.Now().Add(60*time.Second), h.converged); err != nil {
				t.Fatal(err)
			}
			if err := h.s.RunUntilConverged(h.s.Now().Add(30*time.Second), func() bool { return h.quotaAgrees("g") }); err != nil {
				t.Fatalf("quota never matched recount after concurrent overwrite + declaration: %v", err)
			}
			if err := h.s.Err(); err != nil {
				t.Fatal(err)
			}
			rd, err := h.node(h.ids[0]).OCC().Reader()
			if err != nil {
				t.Fatal(err)
			}
			if rd.Record(occ.ManifestKey("g", "k")).Live() {
				for _, n := range h.up() {
					gotInfo, got, err := h.syncGet(t, n.ID(), "g", "k")
					if err != nil || !bytes.Equal(got, data2) || gotInfo.Version != oi2.Version {
						t.Fatalf("%s: winning overwrite v2 unreadable or wrong: info=%+v len=%d err=%v", n.ID(), gotInfo, len(got), err)
					}
				}
			} else {
				if _, err := h.acks.Check(rd, func(k string) bool { return k == occ.ManifestKey("g", "k") }); err != nil {
					t.Fatalf("overwrite v2 disappeared without a counted discard: %v", err)
				}
				if h.node(h.ids[0]).Health().Discards == 0 && h.node(h.ids[1]).Health().Discards == 0 {
					t.Fatal("discarded overwrite v2 was not counted on Health().Discards")
				}
			}
			_ = oi1
		})
	}

	t.Run("concurrent_delete", func(t *testing.T) {
		h, _ := lossHarness(t, 6, storage.Layout{K: 2, M: 2, StripeSize: 1024}, 0)
		oi1, _, man1 := putEverywhere(t, h, 700)
		for _, ref := range man1.Stripes[0].Shards[:3] {
			nm, _ := holderOf(man1, 0, ref.Node)
			if err := h.s.BitRot(ref.Node, nm); err != nil {
				t.Fatal(err)
			}
		}
		if err := h.s.RunUntilConverged(h.s.Now().Add(30*time.Second), func() bool {
			rd, e := h.node(h.ids[0]).OCC().Reader()
			if e != nil {
				return false
			}
			cnt := 0
			for i, ref := range man1.Stripes[0].Shards {
				if rd.Record(node.AbsentKeyForTest(man1.ShardID(0, i), ref.Node)).Live() {
					cnt++
				}
			}
			return cnt > man1.Layout.M
		}); err != nil {
			t.Fatal(err)
		}
		if err := h.s.Partition([]env.NodeID{h.ids[0]}, h.ids[1:]); err != nil {
			t.Fatal(err)
		}
		if _, err := h.node(h.ids[1]).DeleteObject("g", "k", storage.Condition{}); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunFor(3 * time.Second); err != nil {
			t.Fatal(err)
		}
		h.s.HealAll()
		if err := h.s.RunUntilConverged(h.s.Now().Add(60*time.Second), h.converged); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunUntilConverged(h.s.Now().Add(30*time.Second), func() bool { return h.quotaAgrees("g") }); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunFor(h.cfg.GCGrace + 10*time.Second); err != nil {
			t.Fatal(err)
		}
		if err := h.s.Err(); err != nil {
			t.Fatal(err)
		}
		for _, id := range h.ids {
			if left := shardsOf(h, id, oi1.Version); len(left) > 0 {
				t.Fatalf("%s still holds shards %v", id, left)
			}
		}
		q, err := h.node(h.ids[0]).Quota("g")
		if err != nil || q.UsedObjects != 0 || q.UsedBytes != 0 {
			t.Fatalf("unexpected quota after concurrent delete+declaration: %+v, err=%v", q, err)
		}
	})
}

// TestDualDeclarersInPartitionedCluster proves two lowest-ID nodes in separate
// halves of a partition can each declare an already-evidenced lost version and
// converge after heal with accurate usage and zero leftover shards.
func TestDualDeclarersInPartitionedCluster(t *testing.T) {
	h, _ := lossHarness(t, 6, storage.Layout{K: 2, M: 2, StripeSize: 1024}, 0)
	oi, _, man := putEverywhere(t, h, 700)
	if err := h.s.Pause(h.ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Pause(h.ids[3]); err != nil {
		t.Fatal(err)
	}
	h.s.HealAll()
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	nonHolders := []env.NodeID{}
	for _, id := range h.ids {
		if _, ok := holderOf(man, 0, id); !ok {
			nonHolders = append(nonHolders, id)
		}
	}
	var key string
	var oi2 storage.ObjectInfo
	var man2 *storage.Manifest
	for idx := range 50 {
		k := fmt.Sprintf("k%d", idx)
		o, err := h.syncPut(t, h.ids[5], storage.PutRequest{Bucket: "g", Key: k, Data: make([]byte, 700)})
		if err != nil {
			t.Fatal(err)
		}
		m := h.mans[o.Version]
		_, h0 := holderOf(m, 0, h.ids[0])
		_, h3 := holderOf(m, 0, h.ids[3])
		if !h0 && !h3 {
			key, oi2, man2 = k, o, m
			break
		}
	}
	if man2 == nil {
		t.Fatal("could not find key whose placement excludes n000 and n003")
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(30*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Pause(h.ids[0]); err != nil {
		t.Fatal(err)
	}
	for _, ref := range man2.Stripes[0].Shards[:3] {
		nm, _ := holderOf(man2, 0, ref.Node)
		if err := h.s.BitRot(ref.Node, nm); err != nil {
			t.Fatal(err)
		}
	}
	haveRecords := func() bool {
		rd, err := h.node(h.ids[3]).OCC().Reader()
		if err != nil {
			return false
		}
		c := 0
		for i, ref := range man2.Stripes[0].Shards {
			if rd.Record(node.AbsentKeyForTest(man2.ShardID(0, i), ref.Node)).Live() {
				c++
			}
		}
		return c > man2.Layout.M
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(15*time.Second), haveRecords); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Resume(h.ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Partition(h.ids[:3], h.ids[3:]); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(15 * time.Second); err != nil {
		t.Fatal(err)
	}
	declA := h.node(h.ids[0]).Stats().LostDeclared
	declB := h.node(h.ids[3]).Stats().LostDeclared
	t.Logf("during partition: n000 LostDeclared=%d, n003 LostDeclared=%d", declA, declB)
	h.s.HealAll()
	if err := h.s.RunUntilConverged(h.s.Now().Add(60*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(30*time.Second), func() bool { return h.quotaAgrees("g") }); err != nil {
		t.Fatalf("quota disagreed after dual declaration: %v", err)
	}
	if err := h.s.RunFor(h.cfg.GCGrace + 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Err(); err != nil {
		t.Fatal(err)
	}
	for _, n := range h.up() {
		rd, _ := n.OCC().Reader()
		if rd.Record(occ.ManifestKey("g", key)).Live() {
			t.Fatalf("%s: key %s still live", n.ID(), key)
		}
		if !rd.Record(occ.TagKey("g", key, "~lost/"+oi2.Version)).Live() {
			t.Fatalf("%s: ~lost/%s missing", n.ID(), oi2.Version)
		}
	}
	for _, id := range h.ids {
		if left := shardsOf(h, id, oi2.Version); len(left) > 0 {
			t.Fatalf("%s still holds shards of %s: %v", id, oi2.Version, left)
		}
	}
	_ = oi
	_ = nonHolders
}

// TestNonHolderAndCrossStripeAbsenceRecordsIgnored proves absence records from
// non-holders and m-or-fewer records spread across different stripes never
// trigger a loss declaration.
func TestNonHolderAndCrossStripeAbsenceRecordsIgnored(t *testing.T) {
	h, logs := lossHarness(t, 6, storage.Layout{K: 2, M: 2, StripeSize: 1024}, 0)
	oi, data, man := putEverywhere(t, h, 2*1024)
	if len(man.Stripes) != 2 {
		t.Fatalf("want 2 stripes, got %d", len(man.Stripes))
	}
	n0 := h.node(h.ids[0])
	for i, ref := range man.Stripes[0].Shards {
		var wrong env.NodeID
		for _, id := range h.ids {
			if id != ref.Node {
				wrong = id
				break
			}
		}
		k := node.AbsentKeyForTest(man.ShardID(0, i), wrong)
		if _, err := n0.OCC().SetScalar(k, []byte{1}); err != nil {
			t.Fatal(err)
		}
		n0.RecordMeta(k)
	}
	for s := range 2 {
		for i := range man.Layout.M {
			ref := man.Stripes[s].Shards[i]
			k := node.AbsentKeyForTest(man.ShardID(s, i), ref.Node)
			if _, err := n0.OCC().SetScalar(k, []byte{1}); err != nil {
				t.Fatal(err)
			}
			n0.RecordMeta(k)
		}
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(30*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(15 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Err(); err != nil {
		t.Fatal(err)
	}
	requireReadableNotLost(t, orphanRun{h: h, oi: oi, data: data, logs: logs})
}

// TestLossDeclarationAfterBucketMigration proves an object migrated to a new
// region whose target shards rot beyond m is declared lost cleanly and passes
// the converse oracle even though its manifest was installed by Retire.
func TestLossDeclarationAfterBucketMigration(t *testing.T) {
	cfg := smallConfig()
	cfg.Layout = storage.Layout{K: 2, M: 1, StripeSize: 1024}
	cfg.ViewSettle = 2 * time.Second
	h, err := newHarness(1, 9, cfg, fixedNet())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: "m", Region: "r0"}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	data := []byte("migrated-object-payload")
	if _, err := h.syncPut(t, h.ids[0], storage.PutRequest{Bucket: "m", Key: "k", Data: data}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	mi, err := h.node(h.ids[0]).StartMigration("m", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(60*time.Second), func() bool {
		st, err := h.node(h.ids[0]).Migration(mi.ID)
		return err == nil && st.State == saga.StateCompleted.String()
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	rd, err := h.node(h.ids[0]).OCC().Reader()
	if err != nil {
		t.Fatal(err)
	}
	val, _, err := rd.Get(occ.ManifestKey("m", "k"))
	if err != nil {
		t.Fatal(err)
	}
	man, err := manifestOf(val)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range man.Stripes[0].Shards[:2] {
		nm, _ := holderOf(man, 0, ref.Node)
		if err := h.s.BitRot(ref.Node, nm); err != nil {
			t.Fatal(err)
		}
	}
	runErr := h.s.RunFor(30 * time.Second)
	if runErr != nil {
		t.Fatalf("sim failed when declaring a migrated object lost: %v", runErr)
	}
	if !slices.Contains(h.declared, man.Version.String()) {
		t.Fatalf("migrated version %s was not declared lost", man.Version)
	}
	for _, n := range h.up() {
		rd, _ := n.OCC().Reader()
		if rd.Record(occ.ManifestKey("m", "k")).Live() {
			t.Fatalf("%s: migrated lost object still live", n.ID())
		}
		if !rd.Record(occ.TagKey("m", "k", "~lost/"+man.Version.String())).Live() {
			t.Fatalf("%s: ~lost/%s missing", n.ID(), man.Version)
		}
	}
}

type holdStep struct {
	saga.Step
	hold *bool
}

func (s holdStep) Do(c saga.Call, done func([]byte, error)) {
	if *s.hold {
		done(nil, node.ErrUnsettled)
		return
	}
	s.Step.Do(c, done)
}

// TestStagedMigrationCopyNeverWritesAbsenceWhileStaged proves rotting shards
// of a staged ~stage/<saga> migration copy never produces absence records or a
// loss declaration while the live source manifest is intact.
func TestStagedMigrationCopyNeverWritesAbsenceWhileStaged(t *testing.T) {
	cfg := smallConfig()
	cfg.Layout = storage.Layout{K: 2, M: 1, StripeSize: 1024}
	cfg.ViewSettle = 2 * time.Second
	holdFlip := true
	cfg.WrapMigration = func(def saga.Definition) saga.Definition {
		steps := append([]saga.Step(nil), def.Steps...)
		steps[saga.MigrationPivot] = holdStep{Step: steps[saga.MigrationPivot], hold: &holdFlip}
		def.Steps = steps
		return def
	}
	h, err := newHarness(1, 9, cfg, fixedNet())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: "m", Region: "r0"}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	if _, err := h.syncPut(t, h.ids[0], storage.PutRequest{Bucket: "m", Key: "k", Data: []byte("staged-payload")}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	mi, err := h.node(h.ids[0]).StartMigration("m", "r1")
	if err != nil {
		t.Fatal(err)
	}
	var stagedMan *storage.Manifest
	hasStaged := func() bool {
		rd, err := h.node(h.ids[0]).OCC().Reader()
		if err != nil {
			return false
		}
		sk := occ.TagKey("m", "k", "~stage/"+mi.ID)
		val, _, err := rd.Get(sk)
		if err != nil {
			return false
		}
		m, err := node.ManifestOfStaged(val)
		if err != nil {
			return false
		}
		stagedMan = m
		return h.converged()
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(30*time.Second), hasStaged); err != nil {
		t.Fatalf("staged copy never appeared: %v", err)
	}
	for _, ref := range stagedMan.Stripes[0].Shards[:2] {
		nm, _ := holderOf(stagedMan, 0, ref.Node)
		if err := h.s.BitRot(ref.Node, nm); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.s.RunFor(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	for _, n := range h.up() {
		if n.Stats().AbsentRecords != 0 {
			t.Fatalf("%s wrote %d absence records for a staged migration copy", n.ID(), n.Stats().AbsentRecords)
		}
		rd, _ := n.OCC().Reader()
		for _, k := range rd.Keys() {
			if strings.Contains(k, "~absent/") {
				t.Fatalf("%s has absence record %q for a staged copy", n.ID(), k)
			}
		}
	}
	if len(h.declared) != 0 {
		t.Fatalf("unexpected declaration for staged copy: %v", h.declared)
	}
}

// TestSlashHolderIDsEndToEndLossDeclaration proves nodes whose NodeID contains
// slashes write, replicate, and tally absence records to declare an
// unrecoverable object lost end to end.
func TestSlashHolderIDsEndToEndLossDeclaration(t *testing.T) {
	table, err := node.CodeTable()
	if err != nil {
		t.Fatal(err)
	}
	ids := []env.NodeID{"dc1/r0/n0", "dc1/r0/n1", "dc1/r1/n2", "dc1/r1/n3"}
	cfg := smallConfig()
	cfg.Layout = storage.Layout{K: 2, M: 2, StripeSize: 1024}
	cfg.Seeds = ids[:3]
	s := sim.New(sim.Config{Seed: 1, Codes: table, Net: fixedNet()})
	for i, id := range ids {
		reg := env.Region("r0")
		if i >= 2 {
			reg = env.Region("r1")
		}
		if err := s.AddNode(sim.NodeSpec{ID: id, Region: reg}, node.NewFactory(cfg)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RunFor(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	n0 := s.Process(ids[0]).(*node.Node)
	if err := n0.CreateBucket(storage.BucketSpec{Name: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RunFor(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	done := false
	var oi storage.ObjectInfo
	n0.PutObject(storage.PutRequest{Bucket: "g", Key: "k", Data: []byte("slash-id-payload")}, func(o storage.ObjectInfo, err error) {
		done, oi = true, o
		if err != nil {
			t.Errorf("put: %v", err)
		}
	})
	if err := s.RunUntilConverged(s.Now().Add(5*time.Second), func() bool { return done }); err != nil {
		t.Fatal(err)
	}
	if err := s.RunFor(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	rd0, _ := n0.OCC().Reader()
	val, _, err := rd0.Get(occ.ManifestKey("g", "k"))
	if err != nil {
		t.Fatal(err)
	}
	man, err := manifestOf(val)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range man.Stripes[0].Shards[:3] {
		nm, _ := holderOf(man, 0, ref.Node)
		if err := s.BitRot(ref.Node, nm); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RunFor(30 * time.Second); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		nd := s.Process(id).(*node.Node)
		rd, _ := nd.OCC().Reader()
		if rd.Record(occ.ManifestKey("g", "k")).Live() {
			t.Fatalf("%s: slash-ID object %s still live", id, oi.Version)
		}
		if !rd.Record(occ.TagKey("g", "k", "~lost/"+oi.Version)).Live() {
			t.Fatalf("%s: slash-ID ~lost/%s missing", id, oi.Version)
		}
	}
}
