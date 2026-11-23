package node_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/sim"
	"github.com/nickstrad/object-store/internal/storage"
)

func shardsOf(h *harness, id env.NodeID, v string) []string {
	var out []string
	for _, name := range h.s.Disk(id).Names() {
		if strings.HasPrefix(name, "shards/") && strings.Contains(name, "/"+v+"/") {
			out = append(out, name)
		}
	}
	return out
}

// TestCorruptShardOfADeadVersionIsCollected proves a bit-rotted shard of a
// deleted object is collected after GCGrace and does not cause an endless
// inventory start/stop loop.
func TestCorruptShardOfADeadVersionIsCollected(t *testing.T) {
	h := smallCluster(t, 1)
	n := h.node(h.ids[0])
	if err := n.CreateBucket(storage.BucketSpec{Name: "g"}); err != nil {
		t.Fatal(err)
	}
	oi, err := h.syncPut(t, h.ids[0], storage.PutRequest{Bucket: "g", Key: "k", Data: make([]byte, 3000)})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	if _, err := n.DeleteObject("g", "k", storage.Condition{}); err != nil {
		t.Fatal(err)
	}
	holder, rotted := env.NodeID(""), ""
	for _, id := range h.ids {
		if names := shardsOf(h, id, oi.Version); len(names) > 0 {
			holder, rotted = id, names[0]
			break
		}
	}
	if holder == "" {
		t.Fatal("no shard of the deleted version on any disk")
	}
	if err := h.s.BitRot(holder, rotted); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(h.cfg.GCGrace + 10*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, id := range h.ids {
		if left := shardsOf(h, id, oi.Version); len(left) > 0 {
			t.Fatalf("shards %v of a deleted version (one bit-rotted) still on %s after the grace period", left, id)
		}
	}
	starts := h.node(holder).Stats().InventoryStart
	if err := h.s.RunFor(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	if again := h.node(holder).Stats().InventoryStart; again != starts {
		t.Fatalf("the holder keeps restarting its inventory (%d starts, then %d)", starts, again)
	}
}

// TestDeathEvidenceGetsItsOwnGracePeriod proves GCGrace is counted from when
// the holder first observes the ~gc/ marker rather than from when the shard
// first became locally unreferenced.
func TestDeathEvidenceGetsItsOwnGracePeriod(t *testing.T) {
	cfg := smallConfig()
	cfg.GCGrace = 10 * time.Second
	cfg.AckQuorum = 2
	h, err := newHarness(1, 4, cfg, sim.DefaultNetConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	w, holder := h.ids[0], h.ids[1]
	if err := h.node(w).CreateBucket(storage.BucketSpec{Name: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	h.gc.onWrite = func(id env.NodeID, name string) {
		if id == holder && strings.HasPrefix(name, "shards/g/") {
			h.gc.onWrite = nil
			if err := h.s.Isolate(holder); err != nil {
				t.Error(err)
			}
		}
	}
	first, err := h.syncPut(t, w, storage.PutRequest{Bucket: "g", Key: "k", Data: make([]byte, 2000)})
	if err != nil {
		t.Fatal(err)
	}
	if len(shardsOf(h, holder, first.Version)) == 0 {
		t.Fatal("the holder got no shard of the first version")
	}
	if err := h.s.RunFor(2 * cfg.GCGrace); err != nil {
		t.Fatal(err)
	}
	if _, err := h.syncPut(t, w, storage.PutRequest{Bucket: "g", Key: "k", Data: make([]byte, 10)}); err != nil {
		t.Fatal(err)
	}
	h.s.HealAll()
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), func() bool {
		rd, err := h.node(holder).OCC().Reader()
		return err == nil && rd.Record(occ.TagKey("g", "", "~gc/"+first.Version)).Live()
	}); err != nil {
		t.Fatal(err)
	}
	evidence := h.s.Now()
	if err := h.s.RunFor(cfg.GCGrace / 2); err != nil {
		t.Fatal(err)
	}
	if len(shardsOf(h, holder, first.Version)) == 0 {
		t.Fatalf("the holder collected the replaced version %v after its death evidence arrived, inside the grace period", h.s.Now().Sub(evidence))
	}
	if err := h.s.RunFor(cfg.GCGrace + 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if left := shardsOf(h, holder, first.Version); len(left) > 0 {
		t.Fatalf("the replaced version's shards %v are still on the holder after the grace period", left)
	}
}

// TestAWriteWhoseShardsWereCollectedAsOrphansIsRecordedLost proves 1-, 2-, and
// 6-stripe objects whose shards are collected as orphans while the writer is
// isolated are declared lost on record after heal instead of staying live and
// unreadable.
func TestAWriteWhoseShardsWereCollectedAsOrphansIsRecordedLost(t *testing.T) {
	cfg := smallConfig()
	stripes := []int{1, 2, storage.DefaultWindow + 2}
	for _, n := range stripes {
		size := (n-1)*cfg.Layout.StripeSize + cfg.Layout.StripeSize/2
		t.Run(fmt.Sprintf("stripes=%d", n), func(t *testing.T) {
			orphanedWriteIsLiveOnlyIfReadable(t, size)
		})
	}
}

func orphanedWriteIsLiveOnlyIfReadable(t *testing.T, size int) {
	cfg := smallConfig()
	cfg.OrphanGrace = 30 * time.Second
	h, err := newHarness(1, 3, cfg, sim.DefaultNetConfig())
	if err != nil {
		t.Fatal(err)
	}
	h.gc.off = true
	if err := h.s.RunFor(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	w := h.ids[0]
	if err := h.node(w).CreateBucket(storage.BucketSpec{Name: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	done := false
	var oi storage.ObjectInfo
	h.node(w).PutObject(storage.PutRequest{Bucket: "g", Key: "k", Data: make([]byte, size)}, func(o storage.ObjectInfo, err error) {
		done, oi = true, o
		if err != nil {
			t.Errorf("put: %v", err)
		}
		if err := h.s.Isolate(w); err != nil {
			t.Error(err)
		}
	})
	stripes := cfg.Layout.Stripes(int64(size))
	landed := func() bool {
		for _, id := range h.ids[1:] {
			if len(shardsByStripe(h, id, "g")) != stripes {
				return false
			}
		}
		return true
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(10*time.Second), landed); err != nil {
		t.Fatalf("the shards of all %d stripes never landed: %v", stripes, err)
	}
	if done {
		t.Fatal("the put completed before the writer was cut off")
	}
	for _, id := range h.ids[1:] {
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
		t.Fatal("no holder collected the orphaned shards; the scenario did not reach the residual case")
	}
	requireLiveOnlyIfReadableAfterHeal(t, h, "g", "k", oi.Version)
}

func requireLiveOnlyIfReadableAfterHeal(t *testing.T, h *harness, bucket, key, version string) {
	t.Helper()
	h.s.HealAll()
	if err := h.s.RunUntilConverged(h.s.Now().Add(60*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	for _, n := range h.up() {
		rd, err := n.OCC().Reader()
		if err != nil {
			t.Fatal(err)
		}
		if rd.Record(occ.ManifestKey(bucket, key)).Live() {
			if _, _, err := h.syncGet(t, n.ID(), bucket, key); err != nil {
				t.Fatalf("%s: the write is live in metadata and unreadable: %v", n.ID(), err)
			}
		}
	}
	lost := false
	for _, n := range h.up() {
		for _, c := range n.Health().Counters {
			if c.Name == "lost_objects" && c.Value > 0 {
				lost = true
			}
		}
	}
	if !lost {
		t.Fatalf("version %s lost its shards to orphan collection and no node reports a lost object", version)
	}
	rd, err := h.up()[0].OCC().Reader()
	if err != nil {
		t.Fatal(err)
	}
	if !rd.Record(occ.TagKey(bucket, key, "~lost/"+version)).Live() {
		t.Fatal("no replicated loss record names the version")
	}
}

func shardsByStripe(h *harness, id env.NodeID, bucket string) map[uint32]int {
	out := map[uint32]int{}
	for _, name := range h.s.Disk(id).Names() {
		sid, err := storage.ParseBlobName(name)
		if err == nil && sid.Bucket == bucket {
			out[sid.Stripe]++
		}
	}
	return out
}

// TestOrphanRecordsAccumulateAcrossMaintenanceTicks proves multi-stripe shards
// collected as orphans on separate maintenance ticks do not overwrite earlier
// records and the unrecoverable object is declared lost after heal.
func TestOrphanRecordsAccumulateAcrossMaintenanceTicks(t *testing.T) {
	cfg := smallConfig()
	cfg.Layout = storage.Layout{K: 2, M: 2, StripeSize: 1024}
	cfg.OrphanGrace = 30 * time.Second
	cfg.RequestTimeout = 5 * time.Second
	fixed := sim.Range{Min: 2 * time.Millisecond, Max: 2 * time.Millisecond}
	net := sim.DefaultNetConfig()
	net.Latency, net.InterRegionLatency = fixed, fixed
	h, err := newHarness(1, 4, cfg, net)
	if err != nil {
		t.Fatal(err)
	}
	h.gc.off = true
	if err := h.s.RunFor(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	x, a, y, w := h.ids[0], h.ids[1], h.ids[2], h.ids[3]
	if err := h.node(w).CreateBucket(storage.BucketSpec{Name: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	window := storage.DefaultWindow
	size := window*cfg.Layout.StripeSize + cfg.Layout.StripeSize/2
	done := false
	var oi storage.ObjectInfo
	h.node(w).PutObject(storage.PutRequest{Bucket: "g", Key: "k", Data: make([]byte, size)}, func(o storage.ObjectInfo, err error) {
		done, oi = true, o
		if err != nil {
			t.Errorf("put: %v", err)
		}
		if err := h.s.Isolate(w); err != nil {
			t.Error(err)
		}
	})
	early := func() bool {
		got := shardsByStripe(h, a, "g")
		for s := range window {
			if got[uint32(s)] != 1 {
				return false
			}
		}
		return true
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(time.Second), early); err != nil {
		t.Fatalf("the first %d stripes never reached %s: %v", window, a, err)
	}
	for _, id := range []env.NodeID{x, a, y} {
		if got := shardsByStripe(h, id, "g")[uint32(window)]; got != 0 {
			t.Fatalf("stripe %d reached %s together with the first window; the scenario needs it later", window, id)
		}
	}
	if err := h.s.Clog(w, a); err != nil {
		t.Fatal(err)
	}
	if err := h.s.SetLinkLoss(w, y, sim.PPM); err != nil {
		t.Fatal(err)
	}
	gap := cfg.MaintainPeriod + cfg.MaintainPeriod/2
	if err := h.s.RunFor(gap); err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("the put completed without the late shard; the scenario needs the writer to wait for it")
	}
	for _, id := range []env.NodeID{x, a} {
		if err := h.s.SetLinkLoss(w, id, sim.PPM); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.s.Unclog(w, a); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(5*time.Second), func() bool { return done }); err != nil {
		t.Fatal(err)
	}
	if got := shardsByStripe(h, a, "g")[uint32(window)]; got != 1 {
		t.Fatalf("stripe %d never reached %s after the clog", window, a)
	}
	if got := shardsByStripe(h, y, "g")[uint32(window)]; got != 0 {
		t.Fatalf("stripe %d reached %s through a dropped link", window, y)
	}
	if err := h.s.RunFor(cfg.OrphanGrace + 10*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, id := range []env.NodeID{x, a, y} {
		if left := shardsByStripe(h, id, "g"); len(left) != 0 {
			t.Fatalf("%s still holds orphaned shards %v after the orphan grace", id, left)
		}
	}
	requireLiveOnlyIfReadableAfterHeal(t, h, "g", "k", oi.Version)
}

// TestANodeRefusesShardDeletesOverTheWire proves inbound OpDeleteShard RPCs
// are rejected and leave the shard on disk.
func TestANodeRefusesShardDeletesOverTheWire(t *testing.T) {
	h := smallCluster(t, 1)
	n := h.node(h.ids[0])
	if err := n.CreateBucket(storage.BucketSpec{Name: "w"}); err != nil {
		t.Fatal(err)
	}
	oi, err := h.syncPut(t, h.ids[0], storage.PutRequest{Bucket: "w", Key: "k", Data: make([]byte, 100)})
	if err != nil {
		t.Fatal(err)
	}
	var holder env.NodeID
	var name string
	for _, id := range h.ids {
		if names := shardsOf(h, id, oi.Version); len(names) > 0 {
			holder, name = id, names[0]
			break
		}
	}
	sid, err := storage.ParseBlobName(name)
	if err != nil {
		t.Fatal(err)
	}
	var got error
	replied := false
	h.node(holder).OnRequest(h.ids[2], append([]byte{2}, storage.EncodeShardRequest(storage.OpDeleteShard, sid, nil)...), func(_ []byte, err error) { got, replied = err, true })
	if err := h.s.RunUntilConverged(h.s.Now().Add(time.Second), func() bool { return replied }); err != nil {
		t.Fatal(err)
	}
	if got == nil || len(shardsOf(h, holder, oi.Version)) == 0 {
		t.Fatalf("a shard delete over the wire was served (%v); the shard is gone: %v", got, len(shardsOf(h, holder, oi.Version)) == 0)
	}
}

// TestLiveReadableOrLostOracleCatchesAnUnreadableLiveKey proves
// checkLiveReadableOrLost fails when a live object loses more than m shards
// without a ~lost/ record.
func TestLiveReadableOrLostOracleCatchesAnUnreadableLiveKey(t *testing.T) {
	h := smallCluster(t, 1)
	n := h.node(h.ids[0])
	if err := n.CreateBucket(storage.BucketSpec{Name: "o"}); err != nil {
		t.Fatal(err)
	}
	oi, err := h.syncPut(t, h.ids[0], storage.PutRequest{Bucket: "o", Key: "k", Data: make([]byte, 100)})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	h.checkLiveReadableOrLost(h.s)
	if err := h.s.Err(); err != nil {
		t.Fatalf("the oracle failed a readable key: %v", err)
	}
	rotted := 0
	for _, id := range h.ids[1:] {
		for _, name := range shardsOf(h, id, oi.Version) {
			if err := h.s.BitRot(id, name); err != nil {
				t.Fatal(err)
			}
			rotted++
		}
	}
	if rotted < 2 {
		t.Fatalf("rotted %d shards, need 2", rotted)
	}
	h.checkLiveReadableOrLost(h.s)
	if h.s.Err() == nil {
		t.Fatal("the oracle passed a live key whose stripe lost more than m shards and that no record declares lost")
	}
}
