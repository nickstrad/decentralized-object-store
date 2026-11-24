package node

import (
	"strings"
	"testing"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/metrics"
	"github.com/nickstrad/object-store/internal/storage"
)

type metricInventoryDisk struct {
	env.Disk
	blobs        map[string][]byte
	reads, lists int
}

func (d *metricInventoryDisk) Read(name string) ([]byte, error) {
	d.reads++
	b, ok := d.blobs[name]
	if !ok {
		return nil, env.ErrNotFound
	}
	return b, nil
}
func (d *metricInventoryDisk) List(prefix string) ([]string, error) {
	d.lists++
	var out []string
	for name := range d.blobs {
		if strings.HasPrefix(name, prefix) {
			out = append(out, name)
		}
	}
	return out, nil
}
func (d *metricInventoryDisk) Write(name string, b []byte) error { d.blobs[name] = b; return nil }
func (d *metricInventoryDisk) Sync(string) error                 { return nil }
func (d *metricInventoryDisk) Delete(name string) error          { delete(d.blobs, name); return nil }
func sharedMetricValue(s metrics.Snapshot, name string) uint64 {
	for _, p := range s.Points {
		if p.Name == name {
			return p.Gauge + p.Counter
		}
	}
	return 0
}
func TestMetricsSharedStorageConstructionDoesNotScan(t *testing.T) {
	id := storage.ShardID{Bucket: "bucket", Version: 1}
	blob, err := storage.EncodeShard(storage.ShardHeader{K: 1, Version: 1}, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	disk := &metricInventoryDisk{blobs: map[string][]byte{id.BlobName(): blob}}
	e := env.Env{Disk: disk}
	cfg := Config{MetricsEnabled: true, Layout: storage.Layout{K: 1, StripeSize: 64}}
	owner, err := storage.NewNode(e, storage.Config{MetricsEnabled: true, Layout: cfg.Layout, Members: storage.StaticMembership{}})
	if err != nil {
		t.Fatal(err)
	}
	if disk.lists != 1 || disk.reads != 1 {
		t.Fatalf("initial scan: lists=%d reads=%d", disk.lists, disk.reads)
	}
	n := &Node{e: e, cfg: cfg, store: owner, scoped: map[env.Region]*storage.Node{}}
	g := newMigrator(n)
	for _, makeFacade := range []func() *storage.Node{func() *storage.Node { return n.scope("region-a") }, func() *storage.Node { return g.copier("region-b") }} {
		reads, lists := disk.reads, disk.lists
		facade := makeFacade()
		if facade == nil || facade == owner {
			t.Fatal("missing separate storage facade")
		}
		if disk.lists != lists || disk.reads != reads {
			t.Fatalf("shared construction scanned: extra lists=%d reads=%d", disk.lists-lists, disk.reads-reads)
		}
		if sharedMetricValue(facade.Snapshot(), "objstore_storage_local_shards") != 1 {
			t.Fatal("shared inventory lost")
		}
		codec, err := facade.NewCodec(1, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := codec.Encode(codec.Split([]byte("x"))); err != nil {
			t.Fatal(err)
		}
		if err := facade.Store().Delete(id); err != nil {
			t.Fatal(err)
		}
		if sharedMetricValue(owner.Snapshot(), "objstore_storage_local_shards") != 0 {
			t.Fatal("delete did not reach owner")
		}
		if err := facade.Store().Put(id, blob); err != nil {
			t.Fatal(err)
		}
		if sharedMetricValue(owner.Snapshot(), "objstore_storage_local_bytes") != uint64(len(blob)) {
			t.Fatal("put did not reach owner")
		}
	}
	var encodes uint64
	for _, p := range owner.ErasureSnapshot().Points {
		if p.Name == "objstore_erasure_operations_total" && p.Labels[0].Value == "encode" {
			encodes += p.Counter
		}
	}
	if encodes != 2 {
		t.Fatalf("shared codec encodes = %d, want 2", encodes)
	}
}
