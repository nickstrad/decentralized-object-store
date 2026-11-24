package gossip_test

import (
	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/gossip"
	"github.com/nickstrad/object-store/internal/metrics"
	"testing"
	"time"
)

func gossipMetric(s metrics.Snapshot, name string, labels ...string) uint64 {
	for _, p := range s.Points {
		if p.Name != "objstore_gossip_"+name {
			continue
		}
		if len(p.Labels) != len(labels) {
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
func TestMetricsProbeTimeoutRecovery(t *testing.T) {
	h := newHarness(t, "a", gossip.Config{MetricsEnabled: true, ProbeInterval: time.Second, ProbeTimeout: 100 * time.Millisecond})
	announce(h, "b", "c")
	h.node.Start()
	h.clock.advance(3 * time.Second)
	s := h.node.Snapshot()
	if gossipMetric(s, "probes_total", "direct", "timeout") == 0 {
		t.Fatal("no direct timeout")
	}
	if gossipMetric(s, "members", "alive") == 0 {
		t.Fatal("missing live membership")
	}
	h.node.OnDatagram("x", []byte("bad"))
	if gossipMetric(h.node.Snapshot(), "malformed_total") != 1 {
		t.Fatal("malformed input not counted")
	}
	// Deliver an ACK to the current direct/indirect probe, then a duplicate.
	sent := h.net.take()
	for i := len(sent) - 1; i >= 0; i-- {
		m, err := gossip.Decode(sent[i].msg)
		if err == nil && m.Kind == gossip.KindPing {
			h.deliver(sent[i].to, gossip.Message{Kind: gossip.KindAck, Seq: m.Seq})
			h.deliver(sent[i].to, gossip.Message{Kind: gossip.KindAck, Seq: m.Seq})
			break
		}
	}
	batch := metrics.Batch{SchemaVersion: 1, NodeID: "a", Region: "r", BootID: "b", Sequence: 1, StartUnixNano: 1, TimeUnixNano: 1, Snapshots: []metrics.Snapshot{h.node.Snapshot()}}
	if err := metrics.Validate(batch); err != nil {
		t.Fatal(err)
	}
	off := newHarness(t, env.NodeID("off"), gossip.Config{})
	if len(off.node.Snapshot().Points) != 0 {
		t.Fatal("off snapshot")
	}
}
