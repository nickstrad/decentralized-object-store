package saga

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
	m.points[metricKey("saga_starts_total")] = metrics.Point{Name: "objstore_saga_starts_total", Labels: []metrics.Label{}}
	m.points[metricKey("saga_completions_total", "completed")] = metrics.Point{Name: "objstore_saga_completions_total", Labels: []metrics.Label{{Name: "outcome", Value: "completed"}}}
	m.points[metricKey("saga_completions_total", "compensated")] = metrics.Point{Name: "objstore_saga_completions_total", Labels: []metrics.Label{{Name: "outcome", Value: "compensated"}}}
	m.points[metricKey("saga_completions_total", "failed")] = metrics.Point{Name: "objstore_saga_completions_total", Labels: []metrics.Label{{Name: "outcome", Value: "failed"}}}
	m.points[metricKey("saga_step_attempts_total")] = metrics.Point{Name: "objstore_saga_step_attempts_total", Labels: []metrics.Label{}}
	m.points[metricKey("saga_step_failures_total")] = metrics.Point{Name: "objstore_saga_step_failures_total", Labels: []metrics.Label{}}
	m.points[metricKey("saga_compensations_total")] = metrics.Point{Name: "objstore_saga_compensations_total", Labels: []metrics.Label{}}
	m.points[metricKey("saga_compensation_failures_total")] = metrics.Point{Name: "objstore_saga_compensation_failures_total", Labels: []metrics.Label{}}
	m.points[metricKey("saga_retries_total")] = metrics.Point{Name: "objstore_saga_retries_total", Labels: []metrics.Label{}}
	m.points[metricKey("saga_recoveries_total")] = metrics.Point{Name: "objstore_saga_recoveries_total", Labels: []metrics.Label{}}
	m.points[metricKey("saga_active")] = metrics.Point{Name: "objstore_saga_active", Labels: []metrics.Label{}}
	m.points[metricKey("saga_suspended")] = metrics.Point{Name: "objstore_saga_suspended", Labels: []metrics.Label{}}
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
	s := metrics.Snapshot{Component: "saga"}
	if m == nil {
		return s
	}
	for _, k := range []string{"saga_starts_total|", "saga_completions_total|completed", "saga_completions_total|compensated", "saga_completions_total|failed", "saga_step_attempts_total|", "saga_step_failures_total|", "saga_compensations_total|", "saga_compensation_failures_total|", "saga_retries_total|", "saga_recoveries_total|", "saga_active|", "saga_suspended|"} {
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

func (g *Engine) Snapshot() metrics.Snapshot { return g.metric.snapshot() }
