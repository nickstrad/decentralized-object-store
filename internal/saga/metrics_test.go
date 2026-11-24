package saga_test

import (
	"github.com/nickstrad/object-store/internal/metrics"
	"github.com/nickstrad/object-store/internal/saga"
	"testing"
	"time"
)

func sagaMetric(s metrics.Snapshot, name string, labels ...string) uint64 {
	for _, p := range s.Points {
		if p.Name != "objstore_saga_"+name || len(p.Labels) != len(labels) {
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
func TestMetricsSagaRetryCompensationRecovery(t *testing.T) {
	f := newFx(2, 2)
	f.do[0] = []result{retry(false), ok()}
	f.do[1] = []result{permanent()}
	u := newSingle(t, 9, f, saga.Config{MetricsEnabled: true, Policy: testPolicy()})
	u.begin(t, 1, "observed", nil)
	u.run(t, 3*time.Second)
	eng := hostOf(u.s).eng
	s := eng.Snapshot()
	if sagaMetric(s, "starts_total") != 1 || sagaMetric(s, "step_attempts_total") < 3 || sagaMetric(s, "step_failures_total") != 2 || sagaMetric(s, "retries_total") == 0 || sagaMetric(s, "compensations_total") == 0 || sagaMetric(s, "completions_total", "compensated") != 1 || sagaMetric(s, "active") != 0 {
		t.Fatalf("bad saga metrics: %+v", s)
	}
	if err := u.s.Crash(hostID); err != nil {
		t.Fatal(err)
	}
	if err := u.s.Restart(hostID); err != nil {
		t.Fatal(err)
	}
	u.run(t, 4*time.Second)
	s = hostOf(u.s).eng.Snapshot()
	if sagaMetric(s, "recoveries_total") != 1 || sagaMetric(s, "starts_total") != 0 || sagaMetric(s, "completions_total", "compensated") != 0 {
		t.Fatalf("recovery recounted: %+v", s)
	}
}
