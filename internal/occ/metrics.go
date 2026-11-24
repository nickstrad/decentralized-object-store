package occ

import (
	"errors"
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
	m.points[metricKey("occ_writes_total", "ok")] = metrics.Point{Name: "objstore_occ_writes_total", Labels: []metrics.Label{{Name: "outcome", Value: "ok"}}}
	m.points[metricKey("occ_writes_total", "precondition_failed")] = metrics.Point{Name: "objstore_occ_writes_total", Labels: []metrics.Label{{Name: "outcome", Value: "precondition_failed"}}}
	m.points[metricKey("occ_writes_total", "error")] = metrics.Point{Name: "objstore_occ_writes_total", Labels: []metrics.Label{{Name: "outcome", Value: "error"}}}
	m.points[metricKey("occ_merges_total", "changed")] = metrics.Point{Name: "objstore_occ_merges_total", Labels: []metrics.Label{{Name: "outcome", Value: "changed"}}}
	m.points[metricKey("occ_merges_total", "unchanged")] = metrics.Point{Name: "objstore_occ_merges_total", Labels: []metrics.Label{{Name: "outcome", Value: "unchanged"}}}
	m.points[metricKey("occ_merges_total", "error")] = metrics.Point{Name: "objstore_occ_merges_total", Labels: []metrics.Label{{Name: "outcome", Value: "error"}}}
	m.points[metricKey("occ_replication_rounds_total", "ok")] = metrics.Point{Name: "objstore_occ_replication_rounds_total", Labels: []metrics.Label{{Name: "outcome", Value: "ok"}}}
	m.points[metricKey("occ_replication_rounds_total", "error")] = metrics.Point{Name: "objstore_occ_replication_rounds_total", Labels: []metrics.Label{{Name: "outcome", Value: "error"}}}
	m.points[metricKey("occ_replication_errors_total", "network")] = metrics.Point{Name: "objstore_occ_replication_errors_total", Labels: []metrics.Label{{Name: "kind", Value: "network"}}}
	m.points[metricKey("occ_replication_errors_total", "disk")] = metrics.Point{Name: "objstore_occ_replication_errors_total", Labels: []metrics.Label{{Name: "kind", Value: "disk"}}}
	m.points[metricKey("occ_replication_errors_total", "malformed")] = metrics.Point{Name: "objstore_occ_replication_errors_total", Labels: []metrics.Label{{Name: "kind", Value: "malformed"}}}
	m.points[metricKey("occ_lww_discards_total")] = metrics.Point{Name: "objstore_occ_lww_discards_total", Labels: []metrics.Label{}}
	m.points[metricKey("occ_persist_errors_total")] = metrics.Point{Name: "objstore_occ_persist_errors_total", Labels: []metrics.Label{}}
	m.points[metricKey("occ_records")] = metrics.Point{Name: "objstore_occ_records", Labels: []metrics.Label{}}
	m.points[metricKey("occ_lww_discards_persisted")] = metrics.Point{Name: "objstore_occ_lww_discards_persisted", Labels: []metrics.Label{}}
	m.points[metricKey("occ_discard_outbox_depth")] = metrics.Point{Name: "objstore_occ_discard_outbox_depth", Labels: []metrics.Label{}}
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
	s := metrics.Snapshot{Component: "occ"}
	if m == nil {
		return s
	}
	for _, k := range []string{"occ_writes_total|ok", "occ_writes_total|precondition_failed", "occ_writes_total|error", "occ_merges_total|changed", "occ_merges_total|unchanged", "occ_merges_total|error", "occ_replication_rounds_total|ok", "occ_replication_rounds_total|error", "occ_replication_errors_total|network", "occ_replication_errors_total|disk", "occ_replication_errors_total|malformed", "occ_lww_discards_total|", "occ_persist_errors_total|", "occ_records|", "occ_lww_discards_persisted|", "occ_discard_outbox_depth|"} {
		p := m.points[k]
		p.Labels = append([]metrics.Label(nil), p.Labels...)
		s.Points = append(s.Points, p)
	}
	return s
}

func (s *Store) Snapshot() metrics.Snapshot {
	if s.metric != nil {
		s.metric.set("occ_records", uint64(s.doc.Len()))
		s.metric.set("occ_lww_discards_persisted", s.discards)
		s.metric.set("occ_discard_outbox_depth", uint64(len(s.outbox)))
	}
	return s.metric.snapshot()
}
func (n *Node) Snapshot() metrics.Snapshot {
	out := n.metric.snapshot()
	if n.store == nil {
		return out
	}
	ss := n.store.Snapshot()
	for i := range out.Points {
		for _, p := range ss.Points {
			if metricKeyPoint(out.Points[i]) == metricKeyPoint(p) {
				if ^uint64(0)-out.Points[i].Counter < p.Counter {
					out.Points[i].Counter = ^uint64(0)
				} else {
					out.Points[i].Counter += p.Counter
				}
				out.Points[i].Gauge = p.Gauge
			}
		}
	}
	return out
}
func metricKeyPoint(p metrics.Point) string {
	s := p.Name
	for _, l := range p.Labels {
		s += "|" + l.Name + "=" + l.Value
	}
	return s
}
func (s *Store) writeMetric(err error) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
		if errors.Is(err, ErrPreconditionFailed) {
			outcome = "precondition_failed"
		}
	}
	s.metric.add("occ_writes_total", 1, outcome)
}

func (n *Node) MetricRecordCount() uint64 {
	if n.store == nil {
		return 0
	}
	return uint64(n.store.doc.Len())
}
