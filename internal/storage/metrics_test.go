package storage_test

import (
	"errors"
	"github.com/nickstrad/object-store/internal/metrics"
	"github.com/nickstrad/object-store/internal/sim"
	"github.com/nickstrad/object-store/internal/storage"
	"testing"
)

func storageMetric(s metrics.Snapshot, name string, labels ...string) uint64 {
	for _, p := range s.Points {
		if p.Name != "objstore_storage_"+name || len(p.Labels) != len(labels) {
			continue
		}
		ok := true
		for i, l := range p.Labels {
			if l.Value != labels[i] {
				ok = false
			}
		}
		if ok {
			return p.Counter + p.Gauge
		}
	}
	return 0
}
func TestMetricsStorageInventoryAndCodecLifetime(t *testing.T) {
	s := sim.New(sim.Config{Seed: 4})
	ids := sim.NodeIDs(1)
	cfg := storage.Config{MetricsEnabled: true, Layout: storage.Layout{K: 1, M: 0, StripeSize: 64}, Members: clusterMembers(ids, 1)}
	if err := s.AddCluster(ids, 1, nodeFactory(cfg)); err != nil {
		t.Fatal(err)
	}
	if err := s.RunUntilQuiet(); err != nil {
		t.Fatal(err)
	}
	n := nodeOf(t, s, ids[0])
	codec, err := n.NewCodec(2, 1)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := storage.EncodeStripeWithCodec(codec, storage.VersionID(1), 0, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	id := storage.ShardID{Bucket: "bucket", Version: storage.VersionID(1), Index: 0}
	if err := n.Store().Put(id, blobs[0]); err != nil {
		t.Fatal(err)
	}
	if err := n.Store().Put(id, blobs[0]); err != nil {
		t.Fatal(err)
	}
	snap := n.Snapshot()
	if storageMetric(snap, "local_shards") != 1 || storageMetric(snap, "local_bytes") != uint64(len(blobs[0])) {
		t.Fatalf("inventory duplicated: %+v", snap)
	}
	corrupt := append([]byte(nil), blobs[0]...)
	corrupt[len(corrupt)-1] ^= 0xff
	if err := n.Store().Put(id, corrupt); !errors.Is(err, storage.ErrCorrupt) {
		t.Fatal(err)
	}
	if storageMetric(n.Snapshot(), "shard_operations_total", "put", "corrupt") != 1 {
		t.Fatal("corrupt not counted")
	}
	blobs[0] = nil
	data, _, err := storage.DecodeStripeWithCodec(codec, id.Version, 0, 7, blobs, nil)
	if err != nil || string(data) != "payload" {
		t.Fatal(string(data), err)
	}
	// New short-lived facades use one shared lifetime meter rather than resetting it.
	other, _ := n.NewCodec(1, 0)
	if err := other.Encode(other.Split([]byte("next"))); err != nil {
		t.Fatal(err)
	}
	var enc uint64
	for _, p := range n.ErasureSnapshot().Points {
		if p.Name == "objstore_erasure_operations_total" && p.Labels[0].Value == "encode" && p.Labels[1].Value == "ok" {
			enc = p.Counter
		}
	}
	if enc != 2 {
		t.Fatalf("codec lifetime reset: %d", enc)
	}
	if err := n.Store().Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := n.Store().GetBlob(id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatal(err)
	}
	if storageMetric(n.Snapshot(), "local_shards") != 0 || storageMetric(n.Snapshot(), "local_bytes") != 0 {
		t.Fatal("delete inventory leak")
	}
	// Startup reads durable local inventory, independently of the previous meter.
	if err := n.Store().Put(id, mustBlob(t, id)); err != nil {
		t.Fatal(err)
	}
	if err := s.Crash(ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.Restart(ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.RunUntilQuiet(); err != nil {
		t.Fatal(err)
	}
	if storageMetric(nodeOf(t, s, ids[0]).Snapshot(), "local_shards") != 1 {
		t.Fatal("startup inventory missing")
	}
}
func mustBlob(t *testing.T, id storage.ShardID) []byte {
	t.Helper()
	b, err := storage.EncodeShard(storage.ShardHeader{K: 1, M: 0, Version: id.Version, Stripe: id.Stripe, Index: int(id.Index)}, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}
