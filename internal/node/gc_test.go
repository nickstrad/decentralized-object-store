package node_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
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

func TestAWriteWhoseShardsWereCollectedAsOrphansIsRecordedLost(t *testing.T) {
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
	h.node(w).PutObject(storage.PutRequest{Bucket: "g", Key: "k", Data: make([]byte, 3000)}, func(o storage.ObjectInfo, err error) {
		done, oi = true, o
		if err != nil {
			t.Errorf("put: %v", err)
		}
		if err := h.s.Isolate(w); err != nil {
			t.Error(err)
		}
	})
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
		if rd.Record(occ.ManifestKey("g", "k")).Live() {
			if _, _, err := h.syncGet(t, n.ID(), "g", "k"); err != nil {
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
		t.Fatalf("version %s lost its shards to orphan collection and no node reports a lost object", oi.Version)
	}
	rd, _ := h.up()[0].OCC().Reader()
	if !rd.Record(occ.TagKey("g", "k", "~lost/"+oi.Version)).Live() {
		t.Fatal("no replicated loss record names the version")
	}
	_ = fmt.Sprint
}

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
