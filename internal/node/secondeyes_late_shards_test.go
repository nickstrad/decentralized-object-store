package node_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

// TestLateShardAfterOrphanCollectionBeforeAbsenceSavesObject proves a delayed
// writer shard that lands on its holder after other shards were orphaned—but
// before absence records fire—keeps k verifiable shards and saves the object.
func TestLateShardAfterOrphanCollectionBeforeAbsenceSavesObject(t *testing.T) {
	cfg := smallConfig()
	cfg.Layout = storage.Layout{K: 2, M: 2, StripeSize: 1024}
	cfg.OrphanGrace = 30 * time.Second
	cfg.RequestTimeout = 120 * time.Second
	logs := &[]string{}
	cfg.Log = func(s string) { *logs = append(*logs, s) }
	h, err := newHarness(1, 4, cfg, fixedNet())
	if err != nil {
		t.Fatal(err)
	}
	h.gc.off = true
	if err := h.s.RunFor(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	w, lateHolder := h.ids[3], h.ids[2]
	if err := h.node(w).CreateBucket(storage.BucketSpec{Name: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Clog(w, lateHolder); err != nil {
		t.Fatal(err)
	}
	data := []byte("saved-by-late-shard-before-absence")
	done := false
	var oi storage.ObjectInfo
	h.node(w).PutObject(storage.PutRequest{Bucket: "g", Key: "k", Data: data}, func(o storage.ObjectInfo, err error) {
		done, oi = true, o
		if err != nil {
			t.Errorf("put: %v", err)
		}
		if err := h.s.Partition([]env.NodeID{w}, h.ids[:3]); err != nil {
			t.Error(err)
		}
	})
	landed := func() bool {
		return len(shardsByStripe(h, h.ids[0], "g")) == 1 && len(shardsByStripe(h, h.ids[1], "g")) == 1
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(10*time.Second), landed); err != nil {
		t.Fatal(err)
	}
	for _, id := range []env.NodeID{h.ids[0], h.ids[1]} {
		if err := h.s.SetLinkLoss(w, id, sim.PPM); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(10*time.Second), func() bool { return done }); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(cfg.OrphanGrace + 10*time.Second); err != nil {
		t.Fatal(err)
	}
	if len(h.gc.deleted) == 0 {
		t.Fatal("expected orphan collection")
	}
	if len(shardsByStripe(h, lateHolder, "g")) != 0 {
		t.Fatal("lateHolder already received shard while clogged")
	}
	h.s.HealAll()
	if err := h.s.RunUntilConverged(h.s.Now().Add(time.Second), func() bool {
		return len(shardsByStripe(h, lateHolder, "g")) == 1
	}); err != nil {
		t.Fatalf("late shard never landed on %s: %v", lateHolder, err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(60*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(30 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Err(); err != nil {
		t.Fatal(err)
	}
	requireReadableNotLost(t, orphanRun{h: h, oi: oi, data: data, logs: logs})
}

// TestLateShardDuringRebuildPreventsAbsenceRecord proves stateAbsent's fresh
// local read sees a good shard that arrived while a rebuild was in flight,
// preventing a false absence record and letting the stripe recover.
func TestLateShardDuringRebuildPreventsAbsenceRecord(t *testing.T) {
	cfg := smallConfig()
	cfg.Layout = storage.Layout{K: 2, M: 2, StripeSize: 1024}
	cfg.RequestTimeout = 5 * time.Second
	cfg.MaintainPeriod = 500 * time.Millisecond
	logs := &[]string{}
	cfg.Log = func(s string) { *logs = append(*logs, s) }
	h, err := newHarness(1, 4, cfg, fixedNet())
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
	oi, data, man := putEverywhere(t, h, 700)
	h0 := man.Stripes[0].Shards[0].Node
	h1 := man.Stripes[0].Shards[1].Node
	h2 := man.Stripes[0].Shards[2].Node
	n0, _ := holderOf(man, 0, h0)
	n1, _ := holderOf(man, 0, h1)
	n2, _ := holderOf(man, 0, h2)
	sid2, _ := storage.ParseBlobName(n2)
	good2, ok := h.s.Disk(h2).Peek(n2)
	if !ok {
		t.Fatal("missing good2")
	}
	for _, id := range h.ids {
		if id != h2 {
			if err := h.s.Clog(id, h2); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := h.s.BitRot(h0, n0); err != nil {
		t.Fatal(err)
	}
	if err := h.s.BitRot(h1, n1); err != nil {
		t.Fatal(err)
	}
	if err := h.s.BitRot(h2, n2); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(cfg.MaintainPeriod + 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	var putErr error
	replied := false
	h.node(h2).OnRequest(h0, append([]byte{2}, storage.EncodeShardRequest(storage.OpPutShard, sid2, good2)...), func(_ []byte, err error) {
		putErr, replied = err, true
	})
	if err := h.s.RunUntilConverged(h.s.Now().Add(50*time.Millisecond), func() bool { return replied }); err != nil {
		t.Fatal(err)
	}
	if putErr != nil {
		t.Fatalf("late OpPutShard during rebuild failed: %v", putErr)
	}
	h.s.HealAll()
	if err := h.s.RunUntilConverged(h.s.Now().Add(60*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(20 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Err(); err != nil {
		t.Fatal(err)
	}
	if h.node(h2).Stats().AbsentRecords != 0 {
		t.Fatalf("%s wrote an absence record even though a good shard arrived during rebuild", h2)
	}
	requireReadableNotLost(t, orphanRun{h: h, oi: oi, data: data, logs: logs})
}

// TestLateShardAfterAbsenceBeforeDeclarationRefused proves a holder refuses a
// late OpPutShard with ErrFenced as soon as its absence record is committed,
// even before the designated declarer has declared the version lost.
func TestLateShardAfterAbsenceBeforeDeclarationRefused(t *testing.T) {
	cfg := smallConfig()
	cfg.Layout = storage.Layout{K: 2, M: 2, StripeSize: 1024}
	cfg.OrphanGrace = 30 * time.Second
	cfg.RequestTimeout = 120 * time.Second
	h, err := newHarness(1, 5, cfg, fixedNet())
	if err != nil {
		t.Fatal(err)
	}
	h.gc.off = true
	if err := h.s.RunFor(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	var key string
	var man *storage.Manifest
	var w env.NodeID
	w = h.ids[4]
	for idx := range 30 {
		k := "probe-" + string(rune('a'+idx))
		o, err := h.syncPut(t, w, storage.PutRequest{Bucket: "g", Key: k, Data: []byte("probe")})
		if err != nil {
			t.Fatal(err)
		}
		m := h.mans[o.Version]
		if _, isH := holderOf(m, 0, h.ids[0]); !isH {
			key, man = k, m
			break
		}
	}
	if man == nil {
		t.Fatal("no key excluded n000")
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Pause(h.ids[0]); err != nil {
		t.Fatal(err)
	}
	target := man.Stripes[0].Shards[0].Node
	tName, _ := holderOf(man, 0, target)
	tSid, _ := storage.ParseBlobName(tName)
	good, _ := h.s.Disk(target).Peek(tName)
	for _, ref := range man.Stripes[0].Shards[:3] {
		nm, _ := holderOf(man, 0, ref.Node)
		if err := h.s.BitRot(ref.Node, nm); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(3*time.Second), func() bool {
		rd, err := h.node(target).OCC().Reader()
		return err == nil && rd.Record(node.AbsentKeyForTest(tSid, target)).Live()
	}); err != nil {
		t.Fatal(err)
	}
	if len(h.declared) != 0 {
		t.Fatalf("declaration already ran before late shard check: %v", h.declared)
	}
	var got error
	replied := false
	h.node(target).OnRequest(w, append([]byte{2}, storage.EncodeShardRequest(storage.OpPutShard, tSid, good)...), func(_ []byte, err error) {
		got, replied = err, true
	})
	if err := h.s.RunUntilConverged(h.s.Now().Add(time.Second), func() bool { return replied }); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(got, node.ErrFenced) {
		t.Fatalf("late shard after absence record before declaration got %v, want ErrFenced", got)
	}
	if h.node(target).Stats().AbsentRefused == 0 {
		t.Fatal("AbsentRefused was not incremented")
	}
	if err := h.s.Resume(h.ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(30*time.Second), func() bool {
		return slices.Contains(h.declared, man.Version.String()) && h.converged()
	}); err != nil {
		t.Fatalf("declaration never completed for %s (%s): %v", key, man.Version, err)
	}
	if err := h.s.Err(); err != nil {
		t.Fatal(err)
	}
}

// TestResidualCaseBWipedHolderBeforeDeclaration exercises the documented
// disk-wipe residual case (b): when a holder with an absence record on an
// already-below-k stripe is wiped and receives a held shard write before
// metadata sync, the subsequent declaration triggers the converse oracle
// because k verifiable shards now exist on disk.
func TestResidualCaseBWipedHolderBeforeDeclaration(t *testing.T) {
	cfg := smallConfig()
	cfg.Layout = storage.Layout{K: 2, M: 2, StripeSize: 1024}
	cfg.RequestTimeout = 120 * time.Second
	h, err := newHarness(1, 10, cfg, fixedNet())
	if err != nil {
		t.Fatal(err)
	}
	h.gc.off = true
	if err := h.s.RunFor(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	w, a := h.ids[9], h.ids[0]
	target := h.ids[1]
	if err := h.s.Clog(w, target); err != nil {
		t.Fatal(err)
	}
	var oi storage.ObjectInfo
	var man *storage.Manifest
	for idx := range 40 {
		k := "resb-" + string(rune('a'+idx))
		o, err := h.syncPut(t, w, storage.PutRequest{Bucket: "g", Key: k, Data: make([]byte, 700)})
		if err != nil {
			t.Fatal(err)
		}
		m := h.mans[o.Version]
		_, hasN0 := holderOf(m, 0, a)
		_, hasW := holderOf(m, 0, w)
		_, hasT := holderOf(m, 0, target)
		if !hasN0 && !hasW && hasT {
			oi, man = o, m
			break
		}
	}
	if man == nil {
		t.Fatal("could not place object with target in placement and n000, w outside")
	}
	tName, _ := holderOf(man, 0, target)
	if err := h.s.Pause(a); err != nil {
		t.Fatal(err)
	}
	rotted := 0
	for _, ref := range man.Stripes[0].Shards {
		if ref.Node == target || rotted == 2 {
			continue
		}
		nm, _ := holderOf(man, 0, ref.Node)
		if err := h.s.BitRot(ref.Node, nm); err != nil {
			t.Fatal(err)
		}
		rotted++
	}
	have3 := func() bool {
		c := 0
		for i, ref := range man.Stripes[0].Shards {
			if n := h.node(ref.Node); n != nil {
				if rd, err := n.OCC().Reader(); err == nil && rd.Record(node.AbsentKeyForTest(man.ShardID(0, i), ref.Node)).Live() {
					c++
				}
			}
		}
		return c > man.Layout.M
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(4*time.Second), have3); err != nil {
		t.Fatalf("did not get >m absence records before gossip timeout: %v", err)
	}
	tSid, _ := storage.ParseBlobName(tName)
	tAbsKey := node.AbsentKeyForTest(tSid, target)
	replicated := func() bool {
		for _, n := range h.up() {
			if n.ID() == target {
				continue
			}
			if rd, err := n.OCC().Reader(); err == nil && rd.Record(tAbsKey).Live() {
				return true
			}
		}
		return false
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(3*time.Second), replicated); err != nil {
		t.Fatalf("target's absence record never replicated before wipe: %v", err)
	}
	if len(h.declared) != 0 {
		t.Fatalf("declared too early: %v", h.declared)
	}
	if err := h.s.WipeRejoin(target); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Unclog(w, target); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(time.Second), func() bool {
		_, ok := h.s.Disk(target).Peek(tName)
		return ok
	}); err != nil {
		t.Fatalf("clogged shard never landed on wiped target: %v", err)
	}
	if got := h.verifiableShards(man, 0); got < man.Layout.K {
		t.Fatalf("expected >= k=%d verifiable shards on disk, got %d", man.Layout.K, got)
	}
	if err := h.s.Resume(a); err != nil {
		t.Fatal(err)
	}
	err = h.s.RunFor(10 * time.Second)
	if err == nil {
		t.Fatalf("expected residual case (b) to declare %s lost while k shards exist on disk (triggering converse oracle), but sim had no error (declared=%v)", oi.Version, h.declared)
	}
	t.Logf("confirmed documented residual case (b): %v", err)
}
