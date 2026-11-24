package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nickstrad/object-store/internal/metrics"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type collectFunc func(context.Context) (metrics.Batch, error)

func (f collectFunc) Collect(c context.Context) (metrics.Batch, error) { return f(c) }

type sink struct {
	batches []metrics.Batch
	err     error
	closes  int
}

func (s *sink) Publish(c context.Context, b metrics.Batch) error {
	s.batches = append(s.batches, b)
	return s.err
}
func (s *sink) Shutdown(context.Context) error { s.closes++; return nil }
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
		logs := 0
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
		r, e := NewRunner(c, id, collector, s, func(string, ...any) { logs++ })
		if e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var wg sync.WaitGroup
		wg.Go(func() { _ = r.Run(ctx) })
		synctest.Wait()
		if len(s.batches) != 1 || s.batches[0].Sequence != 1 || s.batches[0].BootID != id.BootID {
			t.Fatal(s.batches)
		}
		s.err = ErrPartial
		time.Sleep(300 * time.Millisecond)
		synctest.Wait()
		s.err = errors.New("export")
		time.Sleep(200 * time.Millisecond)
		synctest.Wait()
		if logs != 3 || r.counts[1] != 2 || r.counts[4] == 0 || r.counts[3] == 0 {
			t.Fatal(logs, r.counts)
		}
		if e = r.Shutdown(context.Background()); e != nil {
			t.Fatal(e)
		}
		wg.Wait()
		_ = r.Shutdown(context.Background())
		if s.closes != 1 {
			t.Fatal(s.closes)
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
		if r.counts[1] != 1 || len(s.batches) != 0 {
			t.Fatal(r.counts)
		}
		if e = r.Shutdown(context.Background()); e != nil {
			t.Fatal(e)
		}
	})
}

func TestRunnerSlowCyclesCoalesce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		id := NewIdentity("node", "region")
		s := &sink{}
		calls := 0
		r, err := NewRunner(Config{Enabled: true, Interval: "100ms", Timeout: "90ms"}, id,
			collectFunc(func(ctx context.Context) (metrics.Batch, error) {
				calls++
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
		if calls != 2 || r.counts[5] != 2 {
			t.Fatal(calls, r.counts)
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
		if s.closes != 1 || len(s.batches) != 0 {
			t.Fatal(s)
		}
	})
}
