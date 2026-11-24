package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nickstrad/object-store/internal/metrics"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type collectFunc func(context.Context) (metrics.Batch, error)

func (f collectFunc) Collect(c context.Context) (metrics.Batch, error) { return f(c) }

type sink struct {
	mu      sync.Mutex
	batches []metrics.Batch
	err     error
	closes  int
}

func (s *sink) Publish(c context.Context, b metrics.Batch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, b)
	return s.err
}
func (s *sink) Shutdown(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closes++
	return nil
}
func (s *sink) setErr(err error) {
	s.mu.Lock()
	s.err = err
	s.mu.Unlock()
}
func (s *sink) snapshotBatches() []metrics.Batch {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]metrics.Batch, len(s.batches))
	copy(out, s.batches)
	return out
}
func (s *sink) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closes
}
func TestConfig(t *testing.T) {
	for _, c := range []Config{{Adapter: "missing"}, {Interval: "bad"}, {Timeout: "bad"}, {Interval: "1ms"}, {Interval: "2h"}, {Timeout: "0s"}, {Interval: "1s", Timeout: "2s"}, {Settings: json.RawMessage(`[]`)}, {Settings: json.RawMessage(`{`)}} {
		if _, e := Open(context.Background(), c, Identity{}); !errors.Is(e, ErrConfig) {
			t.Fatalf("%+v: %v", c, e)
		}
	}
	for _, raw := range []string{`{"unknown":1}`, `null`, `{"enabled":"yes"}`, `{} {}`, `{"enabled":null}`, `{"interval":null}`} {
		var c Config
		if json.Unmarshal([]byte(raw), &c) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	var c Config
	if e := json.Unmarshal([]byte(`{"enabled":false,"settings":null}`), &c); e != nil {
		t.Fatal(e)
	}
	called := 0
	factories["test"] = func(context.Context, Identity, json.RawMessage) (Adapter, error) { called++; return &sink{}, nil }
	defer delete(factories, "test")
	c = Config{Adapter: "test", Settings: json.RawMessage(`{"not_checked":true}`)}
	a, e := Open(context.Background(), c, Identity{})
	if e != nil || a != nil || called != 0 {
		t.Fatal(a, e, called)
	}
	c.Enabled = true
	id := NewIdentity("node", "region")
	a, e = Open(context.Background(), c, id)
	if e != nil || a == nil || called != 1 {
		t.Fatal(a, e, called)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = Open(ctx, c, id); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e = Open(context.Background(), c, Identity{}); !errors.Is(e, ErrConfig) {
		t.Fatal(e)
	}
}
func TestDisabledRunner(t *testing.T) {
	c := Config{}
	r, e := NewRunner(c, Identity{}, collectFunc(func(context.Context) (metrics.Batch, error) { t.Fatal("off collected"); return metrics.Batch{}, nil }), nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = r.Run(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = r.Shutdown(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = r.Run(context.Background()); !errors.Is(e, ErrClosed) {
		t.Fatal(e)
	}
	if _, e = NewRunner(Config{Adapter: "bad"}, Identity{}, nil, nil, nil); e == nil {
		t.Fatal("bad name")
	}
	if _, e = NewRunner(Config{Enabled: true}, Identity{}, nil, nil, nil); e == nil {
		t.Fatal("nil dependencies")
	}
	if _, e = NewRunner(Config{Enabled: true}, Identity{}, collectFunc(nil), &sink{}, nil); e == nil {
		t.Fatal("bad identity")
	}
}
func TestRunnerLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		id := NewIdentity("node", "region")
		s := &sink{}
		calls := 0
		var logs atomic.Int64
		c := Config{Enabled: true, Interval: "100ms", Timeout: "40ms"}
		collector := collectFunc(func(ctx context.Context) (metrics.Batch, error) {
			calls++
			if calls == 2 {
				return metrics.Batch{}, errors.New("collect")
			}
			if calls == 3 {
				return metrics.Batch{Snapshots: []metrics.Snapshot{{Component: "bad"}}}, nil
			}
			return metrics.Batch{Snapshots: []metrics.Snapshot{{Component: "node", Points: []metrics.Point{{Name: "objstore_node_ready", Gauge: 1}}}}}, nil
		})
		r, e := NewRunner(c, id, collector, s, func(string, ...any) { logs.Add(1) })
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var wg sync.WaitGroup
		wg.Go(func() { _ = r.Run(ctx) })
		synctest.Wait()
		batches := s.snapshotBatches()
		if len(batches) != 1 || batches[0].Sequence != 1 || batches[0].BootID != id.BootID {
			t.Fatal(batches)
		}
		s.setErr(ErrPartial)
		time.Sleep(300 * time.Millisecond)
		synctest.Wait()
		s.setErr(errors.New("export"))
		time.Sleep(200 * time.Millisecond)
		synctest.Wait()
		if e = r.Shutdown(context.Background()); e != nil {
			t.Fatal(e)
		}
		wg.Wait()
		if logs.Load() != 3 || r.counts[countCollectErr] != 2 || r.counts[countExportPartial] == 0 || r.counts[countExportErr] == 0 {
			t.Fatal(logs.Load(), r.counts)
		}
		_ = r.Shutdown(context.Background())
		if s.closeCount() != 1 {
			t.Fatal(s.closeCount())
		}
	})
}
func TestRunnerTimeoutAndJoin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		id := NewIdentity("node", "region")
		s := &sink{}
		r, e := NewRunner(Config{Enabled: true, Interval: "100ms", Timeout: "40ms"}, id, collectFunc(func(c context.Context) (metrics.Batch, error) { <-c.Done(); return metrics.Batch{}, c.Err() }), s, nil)
		if e != nil {
			t.Fatal(e)
		}
		go func() { _ = r.Run(context.Background()) }()
		synctest.Wait()
		time.Sleep(50 * time.Millisecond)
		synctest.Wait()
		if e = r.Shutdown(context.Background()); e != nil {
			t.Fatal(e)
		}
		if r.counts[countCollectErr] != 1 || len(s.snapshotBatches()) != 0 {
			t.Fatal(r.counts)
		}
	})
}

func TestRunnerSlowCyclesCoalesce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		id := NewIdentity("node", "region")
		s := &sink{}
		var calls atomic.Int64
		r, err := NewRunner(Config{Enabled: true, Interval: "100ms", Timeout: "90ms"}, id,
			collectFunc(func(ctx context.Context) (metrics.Batch, error) {
				calls.Add(1)
				// A misbehaving synchronous source still cannot cause overlapping work.
				time.Sleep(350 * time.Millisecond)
				return metrics.Batch{}, ctx.Err()
			}), s, nil)
		if err != nil {
			t.Fatal(err)
		}
		go func() { _ = r.Run(context.Background()) }()
		synctest.Wait()
		time.Sleep(350 * time.Millisecond)
		synctest.Wait()
		if calls.Load() != 2 {
			t.Fatal(calls.Load())
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		defer cancel()
		if err := r.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal(err)
		}
		time.Sleep(350 * time.Millisecond)
		synctest.Wait()
		if err := r.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if r.counts[countSkippedTicks] != 4 || s.closeCount() != 1 || len(s.snapshotBatches()) != 0 {
			t.Fatal(r.counts, s.closeCount(), len(s.snapshotBatches()))
		}
	})
}
