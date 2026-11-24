package node_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/metrics"
	"github.com/nickstrad/object-store/internal/node"
	"github.com/nickstrad/object-store/internal/sim"
	"github.com/nickstrad/object-store/internal/storage"
)

var metricCostSink []metrics.Snapshot

// This fixture uses real object writes and the in-memory simulator disk. Setup
// includes metadata persistence and inventory maintenance, but no host file I/O.
func metricCostNode(tb testing.TB, objects int) *node.Node {
	tb.Helper()
	table, err := node.CodeTable()
	if err != nil {
		tb.Fatal(err)
	}
	s := sim.New(sim.Config{Seed: 19, Codes: table})
	ids := sim.NodeIDs(3)
	cfg := node.Config{MetricsEnabled: true, Seeds: ids, Layout: storage.Layout{K: 1, M: 0, StripeSize: 64}}
	if err := s.AddCluster(ids, 3, node.NewFactory(cfg)); err != nil {
		tb.Fatal(err)
	}
	if err := s.RunFor(3 * time.Second); err != nil {
		tb.Fatal(err)
	}
	n := s.Process(ids[0]).(*node.Node)
	if err := n.CreateBucket(storage.BucketSpec{Name: "cost", Region: "r0"}); err != nil {
		tb.Fatal(err)
	}
	for i := range objects {
		done := false
		var putErr error
		n.PutObject(storage.PutRequest{Bucket: "cost", Key: fmt.Sprintf("key-%06d", i), Data: []byte("snapshot-cost")}, func(_ storage.ObjectInfo, err error) {
			putErr, done = err, true
		})
		if err := s.RunUntilConverged(s.Now().Add(10*time.Second), func() bool { return done }); err != nil {
			tb.Fatal(err)
		}
		if putErr != nil {
			tb.Fatal(putErr)
		}
	}
	// Maintenance builds the actual shard-inventory Merkle tree.
	if err := s.RunFor(2 * time.Second); err != nil {
		tb.Fatal(err)
	}
	snapshots := collectCostSources(n)
	metadata := nodeObsValue(snapshots[0], "objstore_node_metadata_records")
	shards := nodeObsValue(snapshots[2], "objstore_storage_local_shards")
	entries := nodeObsValue(snapshots[4], "objstore_merkle_entries")
	if metadata < uint64(objects) || shards < uint64(objects) || entries < metadata+shards {
		tb.Fatalf("fixture did not populate state: objects=%d metadata=%d shards=%d merkle=%d", objects, metadata, shards, entries)
	}
	tb.Logf("objects=%d metadata=%d local_shards=%d merkle_entries=%d", objects, metadata, shards, entries)
	if err := metrics.Validate(metrics.Batch{SchemaVersion: 1, NodeID: "n", Region: "r", BootID: "b", Sequence: 1, StartUnixNano: 1, TimeUnixNano: 1, Snapshots: snapshots}); err != nil {
		tb.Fatal(err)
	}
	return n
}

func collectCostSources(n *node.Node) []metrics.Snapshot {
	sources := n.MetricSources()
	out := make([]metrics.Snapshot, len(sources))
	for i, source := range sources {
		out[i] = source.Snapshot()
	}
	return out
}

// Cardinality and allocations are deterministic assertions; CPU cost is reported
// by the benchmark instead of a wall-clock threshold that depends on the host.
func TestMetricsSnapshotCostIndependentOfObjectCount(t *testing.T) {
	var counts []int
	var allocs float64
	for _, objects := range []int{1, 100, 1000} {
		t.Run(fmt.Sprintf("objects=%d", objects), func(t *testing.T) {
			n := metricCostNode(t, objects)
			got := collectCostSources(n)
			if counts == nil {
				counts = make([]int, len(got))
				for i, snapshot := range got {
					counts[i] = len(snapshot.Points)
				}
			}
			if len(got) != 7 || len(got) != len(counts) {
				t.Fatalf("core sources=%d, want 7", len(got))
			}
			for i, snapshot := range got {
				if len(snapshot.Points) == 0 || len(snapshot.Points) != counts[i] {
					t.Fatalf("component %s point count=%d, baseline=%d", snapshot.Component, len(snapshot.Points), counts[i])
				}
			}
			gotAllocs := testing.AllocsPerRun(10, func() { metricCostSink = collectCostSources(n) })
			if objects == 1 {
				allocs = gotAllocs
			} else if gotAllocs > allocs {
				t.Fatalf("snapshot allocations grew: baseline=%g, objects=%d allocations=%g", allocs, objects, gotAllocs)
			}
			t.Logf("snapshot allocations=%g point_counts=%v", gotAllocs, counts)
		})
	}
}

func BenchmarkMetricsSnapshotObjectCount(b *testing.B) {
	for _, objects := range []int{1, 100, 1000} {
		b.Run(fmt.Sprintf("objects=%d", objects), func(b *testing.B) {
			n := metricCostNode(b, objects)
			b.ReportAllocs()
			for b.Loop() {
				metricCostSink = collectCostSources(n)
			}
		})
	}
}
