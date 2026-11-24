package node

import (
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/storage"
)

type retainingMetricClock struct {
	env.Clock
	callback func()
}

func (c *retainingMetricClock) AfterFunc(_ time.Duration, fn func()) env.Timer {
	c.callback = fn
	return nil
}

func TestMetricsDisabledObjectCallbackAllocations(t *testing.T) {
	clock := &retainingMetricClock{}
	n := &Node{e: env.Env{ID: "n", Clock: clock}}
	var putCalls, getCalls int
	put := func(storage.ObjectInfo, error) { putCalls++ }
	get := func(storage.ObjectInfo, []byte, error) { getCalls++ }
	for _, tc := range []struct {
		name           string
		direct, public func()
	}{
		{"put", func() { n.observedPutObject(storage.PutRequest{}, put) }, func() { n.PutObject(storage.PutRequest{}, put) }},
		{"get", func() { n.observedGetObject("bucket", "key", get) }, func() { n.GetObject("bucket", "key", get) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseline := testing.AllocsPerRun(100, tc.direct)
			disabled := testing.AllocsPerRun(100, tc.public)
			if disabled != baseline {
				t.Fatalf("disabled allocations = %g, original path = %g", disabled, baseline)
			}
			clock.callback()
			t.Logf("allocations/call: original=%g disabled=%g", baseline, disabled)
		})
	}
	if putCalls != 1 || getCalls != 1 {
		t.Fatalf("callback calls: put=%d get=%d, want 1 each", putCalls, getCalls)
	}
}
