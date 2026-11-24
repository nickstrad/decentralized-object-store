// Package adaptertest provides the common acceptance suite for every metrics sink.
package adaptertest

import (
	"context"
	"errors"
	"github.com/nickstrad/object-store/internal/metrics"
	"github.com/nickstrad/object-store/internal/telemetry"
	"github.com/nickstrad/object-store/internal/testutil/leakcheck"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type Fixture struct {
	Adapter  telemetry.Adapter
	Readback func(context.Context) (metrics.Batch, error)
	FailNext func()
}

// Identity is stable so protocol fixtures can normalize transports without random metadata.
func Identity() telemetry.Identity {
	return telemetry.Identity{NodeID: "node\"\\\n", Region: "local", BootID: "boot", StartUnixNano: 1}
}
func Batch() metrics.Batch {
	id := Identity()
	return metrics.Batch{SchemaVersion: 1, NodeID: id.NodeID, Region: id.Region, BootID: id.BootID, StartUnixNano: 1, TimeUnixNano: 2, Sequence: 1, Snapshots: []metrics.Snapshot{{Component: "http", Points: []metrics.Point{{Name: "objstore_http_requests_total", Labels: []metrics.Label{{Name: "operation", Value: "health"}, {Name: "status_class", Value: "2xx"}}}, {Name: "objstore_http_requests_in_flight"}, {Name: "objstore_http_request_duration_nanoseconds_total", Labels: []metrics.Label{{Name: "operation", Value: "health"}}, Counter: 17}, {Name: "objstore_http_request_bytes_total", Counter: 23}}}}}
}
func projection(b metrics.Batch) []metrics.Point {
	var p []metrics.Point
	for _, s := range b.Snapshots {
		p = append(p, s.Points...)
	}
	for i := range p {
		p[i].Labels = append([]metrics.Label(nil), p[i].Labels...)
		slices.SortFunc(p[i].Labels, func(a, b metrics.Label) int { return strings.Compare(a.Name, b.Name) })
	}
	slices.SortFunc(p, func(a, b metrics.Point) int {
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		for i := 0; i < len(a.Labels) && i < len(b.Labels); i++ {
			if c := strings.Compare(a.Labels[i].Name, b.Labels[i].Name); c != 0 {
				return c
			}
			if c := strings.Compare(a.Labels[i].Value, b.Labels[i].Value); c != 0 {
				return c
			}
		}
		return len(a.Labels) - len(b.Labels)
	})
	return p
}

// Numeric formats such as Prometheus may round integers above 2^53. File
// adapters must additionally assert exact uint64 values and metadata themselves.
func sameProjection(a, b metrics.Batch) bool {
	ap, bp := projection(a), projection(b)
	if len(ap) != len(bp) {
		return false
	}
	for i := range ap {
		if float64(ap[i].Counter) != float64(bp[i].Counter) || float64(ap[i].Gauge) != float64(bp[i].Gauge) {
			return false
		}
		ap[i].Counter, bp[i].Counter, ap[i].Gauge, bp[i].Gauge = 0, 0, 0, 0
	}
	return reflect.DeepEqual(ap, bp)
}

func Run(t *testing.T, makeFixture func(*testing.T) Fixture) {
	fresh := func(t *testing.T) Fixture {
		baseline := leakcheck.Take()
		t.Cleanup(func() {
			if err := baseline.Leaked(time.Second); err != nil {
				t.Error(err)
			}
		})
		f := makeFixture(t)
		t.Cleanup(func() {
			ctx, c := context.WithTimeout(context.Background(), time.Second)
			defer c()
			if e := f.Adapter.Shutdown(ctx); e != nil {
				t.Error(e)
			}
		})
		return f
	}
	t.Run("values-and-replacement", func(t *testing.T) {
		f := fresh(t)
		b := Batch()
		for i, v := range []uint64{0, 7, 1<<53 + 1, math.MaxInt64} {
			b.Sequence = uint64(i + 1)
			b.TimeUnixNano += 1
			b.Snapshots[0].Points[0].Counter = v
			b.Snapshots[0].Points[1].Gauge = uint64(i % 2)
			if e := f.Adapter.Publish(context.Background(), b); e != nil {
				t.Fatal(e)
			}
			got, e := f.Readback(context.Background())
			if e != nil {
				t.Fatal(e)
			}
			if !sameProjection(got, b) {
				t.Fatalf("got %#v want %#v", projection(got), projection(b))
			}
			if e = f.Adapter.Publish(context.Background(), b); e != nil {
				t.Fatal(e)
			}
			got, e = f.Readback(context.Background())
			if e != nil || !sameProjection(got, b) {
				t.Fatal("duplicate added", e)
			}
		}
		want := metrics.Clone(b)
		if e := f.Adapter.Publish(context.Background(), b); e != nil {
			t.Fatal(e)
		}
		b.Snapshots[0].Points[0].Labels[0].Value = "mutated"
		got, e := f.Readback(context.Background())
		if e != nil || !sameProjection(got, want) {
			t.Fatal("retained caller memory", e)
		}
	})
	t.Run("new-process-starts-at-zero", func(t *testing.T) {
		old := fresh(t)
		b := Batch()
		b.Snapshots[0].Points[0].Counter = 99
		if err := old.Adapter.Publish(context.Background(), b); err != nil {
			t.Fatal(err)
		}
		if err := old.Adapter.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		next := fresh(t)
		if err := next.Adapter.Publish(context.Background(), Batch()); err != nil {
			t.Fatal(err)
		}
		got, err := next.Readback(context.Background())
		if err != nil || !sameProjection(got, Batch()) {
			t.Fatal("new sink retained old cumulative state", err)
		}
	})

	t.Run("reject-invalid", func(t *testing.T) {
		f := fresh(t)
		for _, mutate := range []func(*metrics.Batch){func(b *metrics.Batch) { b.Snapshots[0].Points[0].Name = "unknown" }, func(b *metrics.Batch) {
			b.Snapshots[0].Points = append(b.Snapshots[0].Points, b.Snapshots[0].Points[0])
		}, func(b *metrics.Batch) { b.Snapshots[0].Points[0].Labels[0].Name = "unknown" }, func(b *metrics.Batch) { b.Snapshots[0].Points[0].Labels[0] = b.Snapshots[0].Points[0].Labels[1] }, func(b *metrics.Batch) { b.Snapshots[0].Points[0].Gauge = 1 }, func(b *metrics.Batch) { b.Snapshots[0].Points = make([]metrics.Point, metrics.MaxPoints+1) }} {
			b := Batch()
			mutate(&b)
			if f.Adapter.Publish(context.Background(), b) == nil {
				t.Fatal("invalid batch accepted")
			}
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if !errors.Is(f.Adapter.Publish(ctx, Batch()), context.Canceled) {
			t.Fatal("canceled publish")
		}
	})
	t.Run("sink-recovery", func(t *testing.T) {
		f := fresh(t)
		if f.FailNext == nil {
			t.Skip("no external sink")
		}
		f.FailNext()
		if f.Adapter.Publish(context.Background(), Batch()) == nil {
			t.Fatal("sink failure ignored")
		}
		if e := f.Adapter.Publish(context.Background(), Batch()); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("shutdown", func(t *testing.T) {
		f := fresh(t)
		for range 2 {
			if e := f.Adapter.Shutdown(context.Background()); e != nil {
				t.Fatal(e)
			}
		}
		if !errors.Is(f.Adapter.Publish(context.Background(), Batch()), telemetry.ErrClosed) {
			t.Fatal("publish after shutdown")
		}
	})
}
