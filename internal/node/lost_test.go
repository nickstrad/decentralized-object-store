package node_test

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

type orphanCase struct {
	nodes      int
	layout     storage.Layout
	quorum     int
	stripes    int
	missing    int
	lastOnly   bool
	keepers    []int
	beforeHeal func(t *testing.T, h *harness, oi storage.ObjectInfo)
	lost       bool
}

type orphanRun struct {
	h    *harness
	oi   storage.ObjectInfo
	data []byte
	logs *[]string
}

func fixedNet() sim.NetConfig {
	fixed := sim.Range{Min: 2 * time.Millisecond, Max: 2 * time.Millisecond}
	net := sim.DefaultNetConfig()
	net.Latency, net.InterRegionLatency = fixed, fixed
	return net
}

func lossHarness(t *testing.T, nodes int, layout storage.Layout, quorum int) (*harness, *[]string) {
	t.Helper()
	cfg := smallConfig()
	cfg.Layout = layout
	cfg.AckQuorum = quorum
	cfg.OrphanGrace = 30 * time.Second
	logs := &[]string{}
	cfg.Log = func(s string) { *logs = append(*logs, s) }
	h, err := newHarness(1, nodes, cfg, fixedNet())
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
	return h, logs
}

func runOrphanCase(t *testing.T, c orphanCase) orphanRun {
	t.Helper()
	h, logs := lossHarness(t, c.nodes, c.layout, c.quorum)
	h.gc.off = true
	w := h.ids[c.nodes-1]
	group := []env.NodeID{w}
	for _, i := range c.keepers {
		group = append(group, h.ids[i])
	}
	var rest []env.NodeID
	for _, id := range h.ids {
		if !slices.Contains(group, id) {
			rest = append(rest, id)
		}
	}
	var missing env.NodeID
	if c.missing >= 0 {
		missing = h.ids[c.missing]
		if !c.lastOnly {
			if err := h.s.SetLinkLoss(w, missing, sim.PPM); err != nil {
				t.Fatal(err)
			}
		}
	}
	size := (c.stripes-1)*c.layout.StripeSize + c.layout.StripeSize/2
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*7 + 3)
	}
	done := false
	var oi storage.ObjectInfo
	h.node(w).PutObject(storage.PutRequest{Bucket: "g", Key: "k", Data: data}, func(o storage.ObjectInfo, err error) {
		done, oi = true, o
		if err != nil {
			t.Errorf("put: %v", err)
		}
		if err := h.s.Partition(group, rest); err != nil {
			t.Error(err)
		}
	})
	last := uint32(c.stripes - 1)
	if c.lastOnly {
		early := func() bool {
			got := shardsByStripe(h, missing, "g")
			for s := range c.stripes - 1 {
				if got[uint32(s)] != 1 {
					return false
				}
			}
			return true
		}
		if err := h.s.RunUntilConverged(h.s.Now().Add(time.Second), early); err != nil {
			t.Fatalf("the first %d stripes never reached %s: %v", c.stripes-1, missing, err)
		}
		for _, id := range h.ids[:c.nodes-1] {
			if shardsByStripe(h, id, "g")[last] != 0 {
				t.Fatalf("stripe %d reached %s together with the first window; the case needs it later", last, id)
			}
		}
		if err := h.s.SetLinkLoss(w, missing, sim.PPM); err != nil {
			t.Fatal(err)
		}
	}
	landed := func() bool {
		for _, id := range h.ids[:c.nodes-1] {
			got := shardsByStripe(h, id, "g")
			want := c.stripes
			if id == missing {
				want = 0
				if c.lastOnly {
					want = c.stripes - 1
				}
			}
			if len(got) != want {
				return false
			}
		}
		return true
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(10*time.Second), landed); err != nil {
		t.Fatalf("the shards never landed as the case needs: %v", err)
	}
	if done {
		t.Fatal("the put completed before the writer's metadata was cut off")
	}
	for _, id := range rest {
		if err := h.s.SetLinkLoss(w, id, sim.PPM); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(10*time.Second), func() bool { return done }); err != nil {
		t.Fatal(err)
	}
	if missing != "" && shardsByStripe(h, missing, "g")[last] != 0 {
		t.Fatalf("stripe %d reached %s through a dropped link", last, missing)
	}
	if err := h.s.RunFor(h.cfg.OrphanGrace + 10*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, id := range rest {
		if left := shardsByStripe(h, id, "g"); len(left) != 0 {
			t.Fatalf("%s still holds orphaned shards %v after the orphan grace", id, left)
		}
	}
	if len(h.gc.deleted) == 0 {
		t.Fatal("no orphan collection")
	}
	if c.beforeHeal != nil {
		c.beforeHeal(t, h, oi)
	}
	h.s.HealAll()
	if err := h.s.RunUntilConverged(h.s.Now().Add(60*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(30 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Err(); err != nil {
		t.Fatal(err)
	}
	r := orphanRun{h: h, oi: oi, data: data, logs: logs}
	if c.lost {
		requireDeclaredLost(t, r)
	} else {
		requireReadableNotLost(t, r)
	}
	return r
}

func requireDeclaredLost(t *testing.T, r orphanRun) {
	t.Helper()
	h := r.h
	if err := h.s.RunUntilConverged(h.s.Now().Add(60*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	for _, n := range h.up() {
		rd, err := n.OCC().Reader()
		if err != nil {
			t.Fatal(err)
		}
		if rd.Record(occ.ManifestKey("g", "k")).Live() {
			t.Fatalf("%s: version %s is still live in metadata although it cannot be rebuilt", n.ID(), r.oi.Version)
		}
		if !rd.Record(occ.TagKey("g", "k", "~lost/"+r.oi.Version)).Live() {
			t.Fatalf("%s: no loss record names version %s", n.ID(), r.oi.Version)
		}
		_, _, err = h.syncGet(t, n.ID(), "g", "k")
		if !isNotFoundErr(err) {
			t.Fatalf("%s: get of the declared-lost object returned %v, want not found", n.ID(), err)
		}
	}
	counted := false
	for _, n := range h.up() {
		for _, c := range n.Health().Counters {
			if c.Name == "lost_objects" && c.Value > 0 {
				counted = true
			}
		}
	}
	if !counted {
		t.Fatal("no node reports the lost object on its health surface")
	}
	logged := false
	for _, l := range *r.logs {
		if strings.Contains(l, "version "+r.oi.Version+" lost") {
			logged = true
		}
	}
	if !logged {
		t.Fatal("no log line declares the version lost")
	}
	if !slices.Contains(h.declared, r.oi.Version) {
		t.Fatal("the loss oracle saw no declaration")
	}
	if err := h.s.RunFor(h.cfg.GCGrace + 10*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, id := range h.ids {
		if left := shardsOf(h, id, r.oi.Version); len(left) > 0 {
			t.Fatalf("%s still holds %v of the declared-lost version after the grace period", id, left)
		}
	}
}

func requireReadableNotLost(t *testing.T, r orphanRun) {
	t.Helper()
	h := r.h
	for _, n := range h.up() {
		rd, err := n.OCC().Reader()
		if err != nil {
			t.Fatal(err)
		}
		if !rd.Record(occ.ManifestKey("g", "k")).Live() {
			t.Fatalf("%s: the recoverable object is not live", n.ID())
		}
		if rd.Record(occ.TagKey("g", "k", "~lost/"+r.oi.Version)).Live() {
			t.Fatalf("%s: a recoverable version is declared lost", n.ID())
		}
		_, got, err := h.syncGet(t, n.ID(), "g", "k")
		if err != nil || !bytes.Equal(got, r.data) {
			t.Fatalf("%s: get of a recoverable object: %d bytes, %v", n.ID(), len(got), err)
		}
		if n.Stats().LostDeclared != 0 {
			t.Fatalf("%s declared %d objects lost", n.ID(), n.Stats().LostDeclared)
		}
	}
	if len(h.declared) != 0 {
		t.Fatalf("declarations %v on a recoverable object", h.declared)
	}
	man := h.mans[r.oi.Version]
	if man == nil {
		t.Fatal("no manifest of the version was committed")
	}
	full := func() bool {
		for s := range man.Stripes {
			if h.verifiableShards(man, s) != len(man.Stripes[s].Shards) {
				return false
			}
		}
		return true
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(60*time.Second), full); err != nil {
		t.Fatalf("the holders never rebuilt every shard of the recoverable version: %v", err)
	}
}

func TestAnOrphanedWriteWithAnUnwrittenShardIsDeclaredLost(t *testing.T) {
	runOrphanCase(t, orphanCase{nodes: 4, layout: storage.Layout{K: 2, M: 2, StripeSize: 1024}, stripes: 1, missing: 2, lost: true})
}

func TestAnOrphanedWriteWithEnoughShardsLeftStaysReadable(t *testing.T) {
	runOrphanCase(t, orphanCase{nodes: 4, layout: storage.Layout{K: 2, M: 2, StripeSize: 1024}, stripes: 1, missing: -1, keepers: []int{2}, lost: false})
}

func TestAnOrphanedWriteWithAnUnwrittenShardIsDeclaredLostAtFourPlusTwo(t *testing.T) {
	runOrphanCase(t, orphanCase{nodes: 6, layout: storage.Layout{K: 4, M: 2, StripeSize: 1024}, quorum: 5, stripes: 1, missing: 4, keepers: []int{2, 3}, lost: true})
}

func TestAnOrphanedWriteWithEnoughShardsLeftStaysReadableAtFourPlusTwo(t *testing.T) {
	runOrphanCase(t, orphanCase{nodes: 6, layout: storage.Layout{K: 4, M: 2, StripeSize: 1024}, quorum: 5, stripes: 1, missing: -1, keepers: []int{2, 3, 4}, lost: false})
}

func onlyStripeUnrecoverable(stripe int) func(t *testing.T, h *harness, oi storage.ObjectInfo) {
	return func(t *testing.T, h *harness, oi storage.ObjectInfo) {
		t.Helper()
		man := h.mans[oi.Version]
		if man == nil {
			t.Fatal("no manifest of the version was committed")
		}
		for s := range man.Stripes {
			got := h.verifiableShards(man, s)
			if (s == stripe) != (got < man.Layout.K) {
				t.Fatalf("stripe %d has %d verifiable shards before the heal; the case needs only stripe %d below k=%d", s, got, stripe, man.Layout.K)
			}
		}
	}
}

func TestAMultiStripeWriteWithOneUnrecoverableStripeIsDeclaredLost(t *testing.T) {
	runOrphanCase(t, orphanCase{nodes: 6, layout: storage.Layout{K: 4, M: 2, StripeSize: 1024}, quorum: 5, stripes: storage.DefaultWindow + 1, missing: 4, lastOnly: true, keepers: []int{2, 3, 4}, beforeHeal: onlyStripeUnrecoverable(storage.DefaultWindow), lost: true})
}

func TestAMultiStripeWriteWithOneStripeRottedDuringThePartitionIsDeclaredLost(t *testing.T) {
	rot := func(t *testing.T, h *harness, oi storage.ObjectInfo) {
		man := h.mans[oi.Version]
		if man == nil || len(man.Stripes) < 2 {
			t.Fatal("no two-stripe manifest")
		}
		ref := man.Stripes[1].Shards
		for i, sh := range ref {
			if sh.Node == h.ids[2] {
				if err := h.s.BitRot(sh.Node, man.ShardID(1, i).BlobName()); err != nil {
					t.Fatal(err)
				}
				onlyStripeUnrecoverable(1)(t, h, oi)
				return
			}
		}
		t.Fatalf("%s holds no shard of stripe 1", h.ids[2])
	}
	runOrphanCase(t, orphanCase{nodes: 6, layout: storage.Layout{K: 4, M: 2, StripeSize: 1024}, quorum: 5, stripes: 2, missing: -1, keepers: []int{2, 3, 4}, beforeHeal: rot, lost: true})
}

func putEverywhere(t *testing.T, h *harness, size int) (storage.ObjectInfo, []byte, *storage.Manifest) {
	t.Helper()
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(i*5 + 1)
	}
	oi, err := h.syncPut(t, h.ids[len(h.ids)-1], storage.PutRequest{Bucket: "g", Key: "k", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	man := h.mans[oi.Version]
	full := func() bool {
		for s := range man.Stripes {
			if h.verifiableShards(man, s) != len(man.Stripes[s].Shards) {
				return false
			}
		}
		return h.converged()
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(30*time.Second), full); err != nil {
		t.Fatal(err)
	}
	return oi, data, man
}

func holderOf(man *storage.Manifest, stripe int, id env.NodeID) (string, bool) {
	for i, sh := range man.Stripes[stripe].Shards {
		if sh.Node == id {
			return man.ShardID(stripe, i).BlobName(), true
		}
	}
	return "", false
}

func TestAHolderThatCannotBeHeardFromBlocksALossDeclaration(t *testing.T) {
	for _, mode := range []string{"partitioned", "paused"} {
		t.Run(mode, func(t *testing.T) {
			h, logs := lossHarness(t, 4, storage.Layout{K: 2, M: 2, StripeSize: 1024}, 0)
			oi, data, man := putEverywhere(t, h, 700)
			a, b, c := h.ids[0], h.ids[1], h.ids[2]
			if mode == "paused" {
				if err := h.s.Pause(c); err != nil {
					t.Fatal(err)
				}
			} else if err := h.s.Isolate(c); err != nil {
				t.Fatal(err)
			}
			for _, id := range []env.NodeID{a, b} {
				name, ok := holderOf(man, 0, id)
				if !ok {
					t.Fatalf("%s holds no shard", id)
				}
				if err := h.s.BitRot(id, name); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.s.RunFor(2 * time.Minute); err != nil {
				t.Fatal(err)
			}
			for _, n := range h.up() {
				if n.Stats().LostDeclared != 0 {
					t.Fatalf("%s declared the object lost while %s, holding a good shard, could not be heard from", n.ID(), c)
				}
			}
			if len(h.declared) != 0 {
				t.Fatalf("declared lost while a holder was away: %v", h.declared)
			}
			h.s.HealAll()
			if err := h.s.RunUntilConverged(h.s.Now().Add(60*time.Second), h.converged); err != nil {
				t.Fatal(err)
			}
			if err := h.s.RunFor(30 * time.Second); err != nil {
				t.Fatal(err)
			}
			requireReadableNotLost(t, orphanRun{h: h, oi: oi, data: data, logs: logs})
		})
	}
}

func TestMoreThanMShardsRottedBeyondRepairIsDeclaredLost(t *testing.T) {
	for _, tc := range []struct {
		name   string
		nodes  int
		layout storage.Layout
		rot    int
	}{
		{"k2m2", 4, storage.Layout{K: 2, M: 2, StripeSize: 1024}, 3},
		{"k4m2", 6, storage.Layout{K: 4, M: 2, StripeSize: 1024}, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, logs := lossHarness(t, tc.nodes, tc.layout, 0)
			oi, data, man := putEverywhere(t, h, 3*tc.layout.StripeSize/2)
			for _, id := range h.ids[:tc.rot] {
				name, ok := holderOf(man, 1, id)
				if !ok {
					t.Fatalf("%s holds no shard of stripe 1", id)
				}
				if err := h.s.BitRot(id, name); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.s.RunFor(60 * time.Second); err != nil {
				t.Fatal(err)
			}
			if err := h.s.Err(); err != nil {
				t.Fatal(err)
			}
			rd, err := h.up()[0].OCC().Reader()
			if err != nil {
				t.Fatal(err)
			}
			for _, k := range rd.Keys() {
				if strings.Contains(k, "~orphan/") {
					t.Fatalf("orphan record %q in a case with no orphan collection", k)
				}
			}
			requireDeclaredLost(t, orphanRun{h: h, oi: oi, data: data, logs: logs})
		})
	}
}

func TestOneRottedStripeWithinMIsRepairedNotDeclaredLost(t *testing.T) {
	h, logs := lossHarness(t, 4, storage.Layout{K: 2, M: 2, StripeSize: 1024}, 0)
	oi, data, man := putEverywhere(t, h, 1500)
	for _, id := range h.ids[:2] {
		name, ok := holderOf(man, 1, id)
		if !ok {
			t.Fatalf("%s holds no shard of stripe 1", id)
		}
		if err := h.s.BitRot(id, name); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.s.RunFor(60 * time.Second); err != nil {
		t.Fatal(err)
	}
	requireReadableNotLost(t, orphanRun{h: h, oi: oi, data: data, logs: logs})
}
