package merkle_test

import (
	"github.com/nickstrad/object-store/internal/merkle"
	"github.com/nickstrad/object-store/internal/metrics"
	"github.com/nickstrad/object-store/internal/sim"
	"testing"
	"time"
)

func merkleMetric(s metrics.Snapshot, name string, labels ...string) uint64 {
	for _, p := range s.Points {
		if p.Name != "objstore_merkle_"+name || len(p.Labels) != len(labels) {
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
func TestMetricsMerkleEqualAndRepair(t *testing.T) {
	s := sim.New(sim.Config{Seed: 5, Codes: codes()})
	ids := sim.NodeIDs(2)
	cfg := storeConfig{peers: ids, worker: merkle.Config{MetricsEnabled: true, Depth: 2, Period: 20 * time.Millisecond}}
	if err := s.AddCluster(ids, 1, storeFactory(cfg)); err != nil {
		t.Fatal(err)
	}
	if err := s.RunFor(100 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	a := storeOf(s, ids[0])
	b := storeOf(s, ids[1])
	if merkleMetric(a.w.Snapshot(), "rounds_total", "equal") == 0 {
		t.Fatal("no equal rounds")
	}
	if err := a.ClientWrite([]byte("repair this content")); err != nil {
		t.Fatal(err)
	}
	if err := s.RunFor(time.Second); err != nil {
		t.Fatal(err)
	}
	if merkleMetric(b.w.Snapshot(), "repairs_total", "ok") == 0 || merkleMetric(b.w.Snapshot(), "differences_total") == 0 || merkleMetric(b.w.Snapshot(), "entries") != 1 {
		t.Fatalf("repair not counted: %+v", b.w.Snapshot())
	}
	if b.w.Tree().Len() != 1 {
		t.Fatal("tree copy API changed")
	}
}
