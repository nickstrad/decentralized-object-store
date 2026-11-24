package node

import (
	"github.com/nickstrad/object-store/internal/merkle"
	"github.com/nickstrad/object-store/internal/metrics"
	"strings"
)

type metricState struct {
	points           map[string]metrics.Point
	depth            int
	inventoryEntries uint64
}

func metricKey(name string, labels ...string) string { return name + "|" + strings.Join(labels, "|") }
func newMetricState(enabled bool) *metricState {
	if !enabled {
		return nil
	}
	m := &metricState{points: make(map[string]metrics.Point)}
	m.points[metricKey("node_operations_total", "put", "ok")] = metrics.Point{Name: "objstore_node_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "put"}, {Name: "outcome", Value: "ok"}}}
	m.points[metricKey("node_operations_total", "put", "error")] = metrics.Point{Name: "objstore_node_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "put"}, {Name: "outcome", Value: "error"}}}
	m.points[metricKey("node_operations_total", "get", "ok")] = metrics.Point{Name: "objstore_node_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "get"}, {Name: "outcome", Value: "ok"}}}
	m.points[metricKey("node_operations_total", "get", "error")] = metrics.Point{Name: "objstore_node_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "get"}, {Name: "outcome", Value: "error"}}}
	m.points[metricKey("node_operations_total", "delete", "ok")] = metrics.Point{Name: "objstore_node_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "delete"}, {Name: "outcome", Value: "ok"}}}
	m.points[metricKey("node_operations_total", "delete", "error")] = metrics.Point{Name: "objstore_node_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "delete"}, {Name: "outcome", Value: "error"}}}
	m.points[metricKey("node_operations_total", "other", "ok")] = metrics.Point{Name: "objstore_node_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "other"}, {Name: "outcome", Value: "ok"}}}
	m.points[metricKey("node_operations_total", "other", "error")] = metrics.Point{Name: "objstore_node_operations_total", Labels: []metrics.Label{{Name: "operation", Value: "other"}, {Name: "outcome", Value: "error"}}}
	m.points[metricKey("node_rebuilds_total", "ok")] = metrics.Point{Name: "objstore_node_rebuilds_total", Labels: []metrics.Label{{Name: "outcome", Value: "ok"}}}
	m.points[metricKey("node_rebuilds_total", "error")] = metrics.Point{Name: "objstore_node_rebuilds_total", Labels: []metrics.Label{{Name: "outcome", Value: "error"}}}
	m.points[metricKey("node_rebuilt_bytes_total")] = metrics.Point{Name: "objstore_node_rebuilt_bytes_total", Labels: []metrics.Label{}}
	m.points[metricKey("node_scrubbed_total")] = metrics.Point{Name: "objstore_node_scrubbed_total", Labels: []metrics.Label{}}
	m.points[metricKey("node_scrub_corrupt_total")] = metrics.Point{Name: "objstore_node_scrub_corrupt_total", Labels: []metrics.Label{}}
	m.points[metricKey("node_collected_total", "obsolete")] = metrics.Point{Name: "objstore_node_collected_total", Labels: []metrics.Label{{Name: "reason", Value: "obsolete"}}}
	m.points[metricKey("node_collected_total", "loser")] = metrics.Point{Name: "objstore_node_collected_total", Labels: []metrics.Label{{Name: "reason", Value: "loser"}}}
	m.points[metricKey("node_collected_total", "orphan")] = metrics.Point{Name: "objstore_node_collected_total", Labels: []metrics.Label{{Name: "reason", Value: "orphan"}}}
	m.points[metricKey("node_lost_declared_total")] = metrics.Point{Name: "objstore_node_lost_declared_total", Labels: []metrics.Label{}}
	m.points[metricKey("node_quota_rejections_total")] = metrics.Point{Name: "objstore_node_quota_rejections_total", Labels: []metrics.Label{}}
	m.points[metricKey("node_migrations_ended_total", "ok")] = metrics.Point{Name: "objstore_node_migrations_ended_total", Labels: []metrics.Label{{Name: "outcome", Value: "ok"}}}
	m.points[metricKey("node_migrations_ended_total", "error")] = metrics.Point{Name: "objstore_node_migrations_ended_total", Labels: []metrics.Label{{Name: "outcome", Value: "error"}}}
	m.points[metricKey("node_ready")] = metrics.Point{Name: "objstore_node_ready", Labels: []metrics.Label{}}
	m.points[metricKey("node_operations_in_flight")] = metrics.Point{Name: "objstore_node_operations_in_flight", Labels: []metrics.Label{}}
	m.points[metricKey("node_rebuilds_in_flight")] = metrics.Point{Name: "objstore_node_rebuilds_in_flight", Labels: []metrics.Label{}}
	m.points[metricKey("node_metadata_records")] = metrics.Point{Name: "objstore_node_metadata_records", Labels: []metrics.Label{}}
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
	s := metrics.Snapshot{Component: "node"}
	if m == nil {
		return s
	}
	for _, k := range []string{"node_operations_total|put|ok", "node_operations_total|put|error", "node_operations_total|get|ok", "node_operations_total|get|error", "node_operations_total|delete|ok", "node_operations_total|delete|error", "node_operations_total|other|ok", "node_operations_total|other|error", "node_rebuilds_total|ok", "node_rebuilds_total|error", "node_rebuilt_bytes_total|", "node_scrubbed_total|", "node_scrub_corrupt_total|", "node_collected_total|obsolete", "node_collected_total|loser", "node_collected_total|orphan", "node_lost_declared_total|", "node_quota_rejections_total|", "node_migrations_ended_total|ok", "node_migrations_ended_total|error", "node_ready|", "node_operations_in_flight|", "node_rebuilds_in_flight|", "node_metadata_records|"} {
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

type metricSourceFunc func() metrics.Snapshot

func (f metricSourceFunc) Snapshot() metrics.Snapshot { return f() }
func (n *Node) MetricSources() []metrics.Source {
	return []metrics.Source{n, n.gossip, n.store, metricSourceFunc(n.store.ErasureSnapshot), metricSourceFunc(n.merkleSnapshot), n.occ, n.engine}
}
func (n *Node) Snapshot() metrics.Snapshot {
	if n.metric != nil {
		var ready uint64
		if n.Ready() {
			ready = 1
		}
		n.metric.set("node_ready", ready)
		n.metric.set("node_rebuilds_in_flight", uint64(n.rebuilding))
		n.metric.set("node_metadata_records", n.occ.MetricRecordCount())
	}
	return n.metric.snapshot()
}
func (n *Node) operationDone(op string, err error) {
	n.metric.adjust("node_operations_in_flight", -1)
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	n.metric.add("node_operations_total", 1, op, outcome)
}

func (n *Node) merkleSnapshot() metrics.Snapshot {
	s := n.meta.Snapshot()
	if n.metric == nil {
		return s
	}
	for i := range s.Points {
		if s.Points[i].Name == "objstore_merkle_entries" {
			if ^uint64(0)-s.Points[i].Gauge < n.metric.inventoryEntries {
				s.Points[i].Gauge = ^uint64(0)
			} else {
				s.Points[i].Gauge += n.metric.inventoryEntries
			}
		}
	}
	return s
}
func (n *Node) inventorySet(inv *inventory, name string, sum merkle.Hash) error {
	before := inv.actual.Len()
	err := inv.actual.Set(name, sum)
	if n.metric != nil {
		n.metric.inventoryEntries += uint64(inv.actual.Len() - before)
	}
	return err
}
func (n *Node) inventoryDelete(inv *inventory, name string) bool {
	removed := inv.actual.Delete(name)
	if removed && n.metric != nil {
		n.metric.inventoryEntries--
	}
	return removed
}

func (n *Node) beginMetricOperation() bool {
	if n.metric == nil {
		return false
	}
	n.metric.depth++
	if n.metric.depth != 1 {
		return false
	}
	n.metric.adjust("node_operations_in_flight", 1)
	return true
}
func (n *Node) endMetricOperation(outer bool, op string, err error) {
	if n.metric == nil {
		return
	}
	n.metric.depth--
	if outer {
		n.operationDone(op, err)
	}
}
