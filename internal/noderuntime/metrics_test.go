package noderuntime

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/metrics"
	"github.com/nickstrad/object-store/internal/node"
)

type extraMetrics struct{ calls int }

func (e *extraMetrics) Snapshot() metrics.Snapshot {
	e.calls++
	return metrics.Snapshot{Component: "http"}
}
func metricsRuntime(t *testing.T, enabled bool) *Runtime {
	t.Helper()
	rt, err := NewRuntime(RuntimeConfig{ID: "solo", Region: "local", DataDir: t.TempDir(), NoNetwork: true, MetricsEnabled: enabled, Node: node.Config{Layout: soloLayout()}})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}
func TestMetricsCollectorLifecycleAndIsolation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := metricsRuntime(t, true)
		extra := &extraMetrics{}
		collector := rt.MetricsCollector(extra)
		if _, err := collector.Collect(context.Background()); !errors.Is(err, node.ErrNotReady) {
			t.Fatal(err)
		}
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer rt.Close()
		batch, err := collector.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(batch.Snapshots) != 9 || extra.calls != 1 {
			t.Fatalf("snapshots %d extra %d", len(batch.Snapshots), extra.calls)
		}
		batch.SchemaVersion = 1
		batch.Sequence = 1
		batch.NodeID = "solo"
		batch.Region = "local"
		batch.BootID = "boot"
		batch.StartUnixNano = 1
		batch.TimeUnixNano = 1
		if err := metrics.Validate(batch); err != nil {
			t.Fatal(err)
		}
		for i := range batch.Snapshots {
			for j := range batch.Snapshots[i].Points {
				batch.Snapshots[i].Points[j].Counter = 999
			}
		}
		fresh, err := collector.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range fresh.Snapshots {
			for _, p := range s.Points {
				if p.Counter == 999 {
					t.Fatal("aliased snapshot")
				}
			}
		}
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := collector.Collect(context.Background()); !errors.Is(err, env.ErrClosed) {
			t.Fatal(err)
		}
	})
}
func TestMetricsDisabledNeverSamplesSources(t *testing.T) {
	rt := metricsRuntime(t, false)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	extra := &extraMetrics{}
	b, err := rt.MetricsCollector(extra).Collect(context.Background())
	if err != nil || len(b.Snapshots) != 0 || extra.calls != 0 || len(rt.Snapshot().Points) != 0 {
		t.Fatalf("off: %+v %v calls %d", b, err, extra.calls)
	}
	if rt.cfg.Node.MetricsEnabled {
		t.Fatal("node enabled while runtime off")
	}
}
func TestMetricsStalledLoopCancelsWithoutQueuedReadsOrAbandonment(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := metricsRuntime(t, true)
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer rt.Close()
		entered, release := make(chan struct{}), make(chan struct{})
		rt.post(func() { close(entered); <-release })
		<-entered
		collector := rt.MetricsCollector()
		for range 20 {
			ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
			b, err := collector.Collect(ctx)
			cancel()
			if !errors.Is(err, context.DeadlineExceeded) || len(b.Snapshots) != 0 {
				t.Fatalf("partial or uncanceled: %+v %v", b, err)
			}
			if len(rt.metricGate) != 0 || len(rt.ctrl) != 0 {
				t.Fatal("metrics reads accumulated")
			}
		}
		close(release)
		if _, err := collector.Collect(context.Background()); err != nil {
			t.Fatal(err)
		}
		if rt.Abandoned() != 0 {
			t.Fatal("metrics counted abandoned")
		}
	})
}
func TestMetricsCanceledDuringReadRetainsSingleGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := metricsRuntime(t, true)
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer rt.Close()
		ctx, cancel := context.WithCancel(context.Background())
		rt.metricGate <- struct{}{}
		read := metricRead{ctx: ctx, result: make(chan []metrics.Snapshot, 1)}
		cancel()
		rt.metricReads <- read
		synctest.Wait()
		if len(rt.metricGate) != 0 || len(read.result) != 0 || rt.Abandoned() != 0 {
			t.Fatal("canceled read not discarded")
		}
	})
}
