package gossip

import (
	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/metrics"
	"strings"
)

type metricState struct{ points map[string]metrics.Point }

func metricKey(name string, labels ...string) string { return name + "|" + strings.Join(labels, "|") }
func newMetricState(enabled bool) *metricState {
	if !enabled {
		return nil
	}
	m := &metricState{points: make(map[string]metrics.Point)}
	m.points[metricKey("gossip_probes_total", "direct", "ok")] = metrics.Point{Name: "objstore_gossip_probes_total", Labels: []metrics.Label{{Name: "kind", Value: "direct"}, {Name: "outcome", Value: "ok"}}}
	m.points[metricKey("gossip_probes_total", "direct", "timeout")] = metrics.Point{Name: "objstore_gossip_probes_total", Labels: []metrics.Label{{Name: "kind", Value: "direct"}, {Name: "outcome", Value: "timeout"}}}
	m.points[metricKey("gossip_probes_total", "direct", "error")] = metrics.Point{Name: "objstore_gossip_probes_total", Labels: []metrics.Label{{Name: "kind", Value: "direct"}, {Name: "outcome", Value: "error"}}}
	m.points[metricKey("gossip_probes_total", "indirect", "ok")] = metrics.Point{Name: "objstore_gossip_probes_total", Labels: []metrics.Label{{Name: "kind", Value: "indirect"}, {Name: "outcome", Value: "ok"}}}
	m.points[metricKey("gossip_probes_total", "indirect", "timeout")] = metrics.Point{Name: "objstore_gossip_probes_total", Labels: []metrics.Label{{Name: "kind", Value: "indirect"}, {Name: "outcome", Value: "timeout"}}}
	m.points[metricKey("gossip_probes_total", "indirect", "error")] = metrics.Point{Name: "objstore_gossip_probes_total", Labels: []metrics.Label{{Name: "kind", Value: "indirect"}, {Name: "outcome", Value: "error"}}}
	m.points[metricKey("gossip_membership_transitions_total", "alive")] = metrics.Point{Name: "objstore_gossip_membership_transitions_total", Labels: []metrics.Label{{Name: "state", Value: "alive"}}}
	m.points[metricKey("gossip_membership_transitions_total", "suspect")] = metrics.Point{Name: "objstore_gossip_membership_transitions_total", Labels: []metrics.Label{{Name: "state", Value: "suspect"}}}
	m.points[metricKey("gossip_membership_transitions_total", "dead")] = metrics.Point{Name: "objstore_gossip_membership_transitions_total", Labels: []metrics.Label{{Name: "state", Value: "dead"}}}
	m.points[metricKey("gossip_membership_transitions_total", "left")] = metrics.Point{Name: "objstore_gossip_membership_transitions_total", Labels: []metrics.Label{{Name: "state", Value: "left"}}}
	m.points[metricKey("gossip_syncs_total", "ok")] = metrics.Point{Name: "objstore_gossip_syncs_total", Labels: []metrics.Label{{Name: "outcome", Value: "ok"}}}
	m.points[metricKey("gossip_syncs_total", "error")] = metrics.Point{Name: "objstore_gossip_syncs_total", Labels: []metrics.Label{{Name: "outcome", Value: "error"}}}
	m.points[metricKey("gossip_malformed_total")] = metrics.Point{Name: "objstore_gossip_malformed_total", Labels: []metrics.Label{}}
	m.points[metricKey("gossip_members", "alive")] = metrics.Point{Name: "objstore_gossip_members", Labels: []metrics.Label{{Name: "state", Value: "alive"}}}
	m.points[metricKey("gossip_members", "suspect")] = metrics.Point{Name: "objstore_gossip_members", Labels: []metrics.Label{{Name: "state", Value: "suspect"}}}
	m.points[metricKey("gossip_members", "dead")] = metrics.Point{Name: "objstore_gossip_members", Labels: []metrics.Label{{Name: "state", Value: "dead"}}}
	m.points[metricKey("gossip_members", "left")] = metrics.Point{Name: "objstore_gossip_members", Labels: []metrics.Label{{Name: "state", Value: "left"}}}
	m.points[metricKey("gossip_updates_queued")] = metrics.Point{Name: "objstore_gossip_updates_queued", Labels: []metrics.Label{}}
	m.points[metricKey("gossip_probes_in_flight")] = metrics.Point{Name: "objstore_gossip_probes_in_flight", Labels: []metrics.Label{}}
	return m
}
func (m *metricState) add(name string, n uint64, labels ...string) {
	if m == nil {
		return
	}
	k := metricKey(name, labels...)
	p, ok := m.points[k]
	if !ok {
		return
	}
	if ^uint64(0)-p.Counter < n {
		p.Counter = ^uint64(0)
	} else {
		p.Counter += n
	}
	m.points[k] = p
}
func (m *metricState) set(name string, n uint64, labels ...string) {
	if m == nil {
		return
	}
	k := metricKey(name, labels...)
	p, ok := m.points[k]
	if !ok {
		return
	}
	p.Gauge = n
	m.points[k] = p
}
func (m *metricState) snapshot() metrics.Snapshot {
	s := metrics.Snapshot{Component: "gossip"}
	if m == nil {
		return s
	}
	for _, k := range []string{"gossip_probes_total|direct|ok", "gossip_probes_total|direct|timeout", "gossip_probes_total|direct|error", "gossip_probes_total|indirect|ok", "gossip_probes_total|indirect|timeout", "gossip_probes_total|indirect|error", "gossip_membership_transitions_total|alive", "gossip_membership_transitions_total|suspect", "gossip_membership_transitions_total|dead", "gossip_membership_transitions_total|left", "gossip_syncs_total|ok", "gossip_syncs_total|error", "gossip_malformed_total|", "gossip_members|alive", "gossip_members|suspect", "gossip_members|dead", "gossip_members|left", "gossip_updates_queued|", "gossip_probes_in_flight|"} {
		p := m.points[k]
		p.Labels = append([]metrics.Label(nil), p.Labels...)
		s.Points = append(s.Points, p)
	}
	return s
}

func (m *metricState) adjust(name string, delta int64, labels ...string) {
	if m == nil {
		return
	}
	k := metricKey(name, labels...)
	p := m.points[k]
	if delta < 0 {
		d := uint64(-delta)
		if d > p.Gauge {
			p.Gauge = 0
		} else {
			p.Gauge -= d
		}
	} else if ^uint64(0)-p.Gauge < uint64(delta) {
		p.Gauge = ^uint64(0)
	} else {
		p.Gauge += uint64(delta)
	}
	m.points[k] = p
}

func (n *Node) applyMetricMember(u Member) bool {
	old, had := n.view.Get(u.ID)
	if !n.view.Apply(u) {
		return false
	}
	if !had || old.State != u.State {
		if had {
			n.metric.adjust("gossip_members", -1, old.State.String())
		}
		n.metric.adjust("gossip_members", 1, u.State.String())
		n.metric.add("gossip_membership_transitions_total", 1, u.State.String())
	}
	return true
}
func (n *Node) removeMetricMember(id env.NodeID) {
	if old, ok := n.view.Get(id); ok {
		n.metric.adjust("gossip_members", -1, old.State.String())
	}
	n.view.Remove(id)
}
func (n *Node) Snapshot() metrics.Snapshot {
	if n.metric != nil {
		n.metric.set("gossip_updates_queued", uint64(len(n.queue)))
		var busy uint64
		if n.probe.active && !n.probe.acked {
			busy = 1
		}
		n.metric.set("gossip_probes_in_flight", busy)
	}
	return n.metric.snapshot()
}
