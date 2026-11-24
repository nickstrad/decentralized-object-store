package storage

import (
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
)

type retainingMetricTransport struct {
	env.Transport
	callback func([]byte, error)
}

func (t *retainingMetricTransport) Request(_ env.NodeID, _ []byte, _ time.Duration, cb func([]byte, error)) {
	t.callback = cb
}

func TestMetricsDisabledRequestAllocations(t *testing.T) {
	transport := &retainingMetricTransport{}
	n := &Node{e: env.Env{Net: transport}, cfg: Config{RequestTimeout: time.Second}}
	var calls int
	callback := func([]byte, error) { calls++ }
	payload := []byte("request")
	baseline := testing.AllocsPerRun(100, func() {
		n.e.Net.Request("peer", payload, n.cfg.RequestTimeout, callback)
	})
	disabled := testing.AllocsPerRun(100, func() {
		n.request("peer", payload, callback)
	})
	if disabled != baseline {
		t.Fatalf("disabled request allocations = %g, direct transport = %g", disabled, baseline)
	}
	if n.stats.Requests != 101 {
		t.Fatalf("request stats = %d, want 101", n.stats.Requests)
	}
	transport.callback(nil, env.ErrTimeout)
	if calls != 1 {
		t.Fatalf("callback calls = %d, want 1", calls)
	}
	n.metric = newMetricState(true)
	enabled := testing.AllocsPerRun(100, func() {
		n.request("peer", payload, callback)
		transport.callback(nil, env.ErrTimeout)
	})
	if enabled <= disabled {
		t.Fatalf("enabled wrapper allocations = %g, want more than disabled %g", enabled, disabled)
	}
	if calls != 102 {
		t.Fatalf("callback calls = %d, want 102", calls)
	}
	t.Logf("allocations/request: direct=%g disabled=%g enabled=%g", baseline, disabled, enabled)
}
