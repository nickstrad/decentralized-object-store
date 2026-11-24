package merkle

import (
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
	m.points[metricKey("merkle_rounds_total", "equal")] = metrics.Point{Name: "objstore_merkle_rounds_total", Labels: []metrics.Label{{Name: "outcome", Value: "equal"}}}
	m.points[metricKey("merkle_rounds_total", "repaired")] = metrics.Point{Name: "objstore_merkle_rounds_total", Labels: []metrics.Label{{Name: "outcome", Value: "repaired"}}}
	m.points[metricKey("merkle_rounds_total", "error")] = metrics.Point{Name: "objstore_merkle_rounds_total", Labels: []metrics.Label{{Name: "outcome", Value: "error"}}}
	m.points[metricKey("merkle_hashes_compared_total")] = metrics.Point{Name: "objstore_merkle_hashes_compared_total", Labels: []metrics.Label{}}
	m.points[metricKey("merkle_differences_total")] = metrics.Point{Name: "objstore_merkle_differences_total", Labels: []metrics.Label{}}
	m.points[metricKey("merkle_repairs_total", "ok")] = metrics.Point{Name: "objstore_merkle_repairs_total", Labels: []metrics.Label{{Name: "outcome", Value: "ok"}}}
	m.points[metricKey("merkle_repairs_total", "error")] = metrics.Point{Name: "objstore_merkle_repairs_total", Labels: []metrics.Label{{Name: "outcome", Value: "error"}}}
	m.points[metricKey("merkle_budget_exhaustions_total")] = metrics.Point{Name: "objstore_merkle_budget_exhaustions_total", Labels: []metrics.Label{}}
	m.points[metricKey("merkle_rounds_in_flight")] = metrics.Point{Name: "objstore_merkle_rounds_in_flight", Labels: []metrics.Label{}}
	m.points[metricKey("merkle_entries")] = metrics.Point{Name: "objstore_merkle_entries", Labels: []metrics.Label{}}
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
	s := metrics.Snapshot{Component: "merkle"}
	if m == nil {
		return s
	}
	for _, k := range []string{"merkle_rounds_total|equal", "merkle_rounds_total|repaired", "merkle_rounds_total|error", "merkle_hashes_compared_total|", "merkle_differences_total|", "merkle_repairs_total|ok", "merkle_repairs_total|error", "merkle_budget_exhaustions_total|", "merkle_rounds_in_flight|", "merkle_entries|"} {
		p := m.points[k]
		p.Labels = append([]metrics.Label(nil), p.Labels...)
		s.Points = append(s.Points, p)
	}
	return s
}

func (w *Worker) Snapshot() metrics.Snapshot {
	if w.metric != nil {
		w.metric.set("merkle_entries", uint64(w.tree.Len()))
		var busy uint64
		if w.busy {
			busy = 1
		}
		w.metric.set("merkle_rounds_in_flight", busy)
	}
	return w.metric.snapshot()
}

func (w *Worker) ObserveComparison(res Result, err error) {
	outcome := "equal"
	if err != nil {
		outcome = "error"
	} else if len(res.Diffs) > 0 || res.Truncated {
		outcome = "repaired"
	}
	w.metric.add("merkle_rounds_total", 1, outcome)
	w.metric.add("merkle_hashes_compared_total", uint64(res.Cost.Hashes))
	w.metric.add("merkle_differences_total", uint64(len(res.Diffs)))
	if res.Truncated {
		w.metric.add("merkle_budget_exhaustions_total", 1)
	}
}
