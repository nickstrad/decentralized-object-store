package erasure

import (
	"github.com/nickstrad/object-store/internal/metrics"
	"testing"
)

func metricValue(s metrics.Snapshot, name, op string) uint64 {
	for _, p := range s.Points {
		if p.Name != "objstore_erasure_"+name {
			continue
		}
		if op == "" {
			return p.Counter
		}
		for _, l := range p.Labels {
			if l.Name == "operation" && l.Value == op {
				return p.Counter
			}
		}
	}
	return 0
}
func TestMeteredCodec(t *testing.T) {
	c, err := NewMetered(2, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	shards := c.Split([]byte("abcdef"))
	if err := c.Encode(shards); err != nil {
		t.Fatal(err)
	}
	shards[0] = nil
	shards[3] = nil
	if err := c.ReconstructData(shards); err != nil {
		t.Fatal(err)
	}
	if shards[3] != nil {
		t.Fatal("data-only reconstruction filled parity")
	}
	s := c.Snapshot()
	if got := metricValue(s, "reconstructed_shards_total", ""); got != 1 {
		t.Fatal(got)
	}
	if got := metricValue(s, "input_bytes_total", "encode"); got != 6 {
		t.Fatal(got)
	}
	if got := metricValue(s, "output_bytes_total", "encode"); got != 12 {
		t.Fatal(got)
	}
	if got := metricValue(s, "output_bytes_total", "reconstruct"); got != 3 {
		t.Fatal(got)
	}
	if err := c.Reconstruct(shards); err != nil {
		t.Fatal(err)
	}
	if got := metricValue(c.Snapshot(), "reconstructed_shards_total", ""); got != 2 {
		t.Fatal(got)
	}
	before := c.state.input
	if err := c.Encode(nil); err == nil {
		t.Fatal("invalid encode accepted")
	}
	if c.state.input != before || c.state.operations[0][1] != 1 {
		t.Fatal("failed encode accounting")
	}
	b := metrics.Batch{SchemaVersion: 1, NodeID: "n", Region: "r", BootID: "b", Sequence: 1, StartUnixNano: 1, TimeUnixNano: 2, Snapshots: []metrics.Snapshot{c.Snapshot()}}
	if err := metrics.Validate(b); err != nil {
		t.Fatal(err)
	}
	s.Points[0].Labels[0].Value = "damaged"
	if err := metrics.Validate(metrics.Batch{SchemaVersion: 1, NodeID: "n", Region: "r", BootID: "b", Sequence: 1, StartUnixNano: 1, TimeUnixNano: 2, Snapshots: []metrics.Snapshot{c.Snapshot()}}); err != nil {
		t.Fatal(err)
	}
	c.state.operations[0][0] = ^uint64(0)
	if err := c.Encode(c.Split([]byte("x"))); err != nil {
		t.Fatal(err)
	}
	if c.state.operations[0][0] != ^uint64(0) {
		t.Fatal("counter wrapped")
	}
	off, _ := NewMetered(2, 2, false)
	if err := off.Encode(off.Split(nil)); err != nil {
		t.Fatal(err)
	}
	if off.state != nil || len(off.Snapshot().Points) != 0 {
		t.Fatal("disabled metrics allocate state")
	}
}
