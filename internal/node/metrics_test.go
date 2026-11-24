package node_test

import (
	"bytes"
	"github.com/nickstrad/object-store/internal/metrics"
	"github.com/nickstrad/object-store/internal/node"
	"github.com/nickstrad/object-store/internal/sim"
	"github.com/nickstrad/object-store/internal/storage"
	"testing"
	"time"
)

func nodeObsValue(s metrics.Snapshot, name string, labels ...string) uint64 {
	for _, p := range s.Points {
		if p.Name != name || len(p.Labels) != len(labels) {
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
func TestMetricsNodeSourcesAndOffEquivalence(t *testing.T) {
	run := func(enabled bool) ([]byte, []metrics.Snapshot) {
		cfg := node.Config{MetricsEnabled: enabled, Layout: storage.Layout{K: 1, M: 0, StripeSize: 64}}
		h, err := newHarness(19, 3, cfg, sim.NetConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunFor(3 * time.Second); err != nil {
			t.Fatal(err)
		}
		n := h.node(h.ids[0])
		if err := n.CreateBucket(storage.BucketSpec{Name: "observed", Region: "r0"}); err != nil {
			t.Fatal(err)
		}
		if _, err := h.syncPut(t, h.ids[0], storage.PutRequest{Bucket: "observed", Key: "key", Data: []byte("hello")}); err != nil {
			t.Fatal(err)
		}
		_, data, err := h.syncGet(t, h.ids[0], "observed", "key")
		if err != nil || string(data) != "hello" {
			t.Fatal(string(data), err)
		}
		snapshots := []metrics.Snapshot{}
		for _, src := range n.MetricSources() {
			snapshots = append(snapshots, src.Snapshot())
		}
		b := metrics.Batch{SchemaVersion: 1, NodeID: "n", Region: "r", BootID: "b", Sequence: 1, StartUnixNano: 1, TimeUnixNano: 1, Snapshots: snapshots}
		if err := metrics.Validate(b); err != nil {
			t.Fatal(err)
		}
		return n.EncodeState(), snapshots
	}
	off, empty := run(false)
	on, snapshots := run(true)
	if !bytes.Equal(off, on) {
		t.Fatal("metrics changed protocol state digest")
	}
	for _, s := range empty {
		if len(s.Points) != 0 {
			t.Fatal("off source has points")
		}
	}
	want := []string{"node", "gossip", "storage", "erasure", "merkle", "occ", "saga"}
	for i, s := range snapshots {
		if s.Component != want[i] || len(s.Points) == 0 {
			t.Fatalf("source %d: %+v", i, s)
		}
	}
	if nodeObsValue(snapshots[0], "objstore_node_operations_total", "put", "ok") != 1 || nodeObsValue(snapshots[0], "objstore_node_operations_total", "get", "ok") != 1 {
		t.Fatal("public operation not counted once")
	}
	if nodeObsValue(snapshots[3], "objstore_erasure_operations_total", "encode", "ok") != 1 {
		t.Fatalf("scoped codec not aggregated: %+v", snapshots[3])
	}
	if nodeObsValue(snapshots[0], "objstore_node_operations_in_flight") != 0 {
		t.Fatal("inflight leak")
	}
}
