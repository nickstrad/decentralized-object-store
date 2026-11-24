package noderuntime

import (
	"context"
	"runtime"
	"slices"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/metrics"
	"github.com/nickstrad/object-store/internal/node"
)

type metricRead struct {
	ctx    context.Context
	result chan []metrics.Snapshot
}

type MetricsCollector struct {
	rt    *Runtime
	extra []metrics.Source
}

func (rt *Runtime) MetricsCollector(extra ...metrics.Source) *MetricsCollector {
	return &MetricsCollector{rt: rt, extra: slices.Clone(extra)}
}

func (rt *Runtime) readMetrics(read metricRead) {
	defer func() { <-rt.metricGate }()
	if read.ctx.Err() != nil {
		return
	}
	var snapshots []metrics.Snapshot
	for _, source := range rt.nd.MetricSources() {
		snapshots = append(snapshots, source.Snapshot())
	}
	read.result <- metrics.Clone(metrics.Batch{Snapshots: snapshots}).Snapshots
}

func (c *MetricsCollector) Collect(ctx context.Context) (metrics.Batch, error) {
	rt := c.rt
	if err := ctx.Err(); err != nil {
		return metrics.Batch{}, err
	}
	rt.mu.Lock()
	state := rt.state
	rt.mu.Unlock()
	if state == stateClosed {
		return metrics.Batch{}, env.ErrClosed
	}
	if state != stateRunning {
		return metrics.Batch{}, node.ErrNotReady
	}
	if !rt.cfg.MetricsEnabled {
		return metrics.Batch{}, nil
	}
	select {
	case rt.metricGate <- struct{}{}:
	case <-ctx.Done():
		return metrics.Batch{}, ctx.Err()
	case <-rt.ctx.Done():
		return metrics.Batch{}, env.ErrClosed
	}
	read := metricRead{ctx: ctx, result: make(chan []metrics.Snapshot, 1)}
	select {
	case rt.metricReads <- read:
	case <-ctx.Done():
		<-rt.metricGate
		return metrics.Batch{}, ctx.Err()
	case <-rt.ctx.Done():
		<-rt.metricGate
		return metrics.Batch{}, env.ErrClosed
	}
	var snapshots []metrics.Snapshot
	select {
	case snapshots = <-read.result:
	case <-ctx.Done():
		return metrics.Batch{}, ctx.Err()
	case <-rt.ctx.Done():
		return metrics.Batch{}, env.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return metrics.Batch{}, err
	}
	snapshots = append(snapshots, rt.Snapshot())
	for _, source := range c.extra {
		snapshots = append(snapshots, source.Snapshot())
	}
	if err := ctx.Err(); err != nil {
		return metrics.Batch{}, err
	}
	return metrics.Clone(metrics.Batch{Snapshots: snapshots}), nil
}

func (rt *Runtime) Snapshot() metrics.Snapshot {
	s := metrics.Snapshot{Component: "runtime"}
	if !rt.cfg.MetricsEnabled {
		return s
	}
	for _, value := range []struct {
		name  string
		value uint64
	}{
		{"datagrams_dropped_total", rt.Dropped()}, {"connections_refused_total", rt.Refused()},
		{"bad_frames_total", rt.BadFrames()}, {"calls_abandoned_total", rt.Abandoned()},
		{"unresolved_total", rt.Unresolved()},
	} {
		s.Points = append(s.Points, metrics.Point{Name: "objstore_runtime_" + value.name, Counter: value.value})
	}
	if rt.Panicked() {
		s.Points = append(s.Points, metrics.Point{Name: "objstore_runtime_loop_panics_total", Counter: 1})
	} else {
		s.Points = append(s.Points, metrics.Point{Name: "objstore_runtime_loop_panics_total"})
	}
	rt.mu.Lock()
	connections := len(rt.conns)
	running := uint64(0)
	if rt.state == stateRunning && rt.ctx.Err() == nil {
		running = 1
	}
	rt.mu.Unlock()
	for _, value := range []struct {
		name  string
		value uint64
	}{
		{"control_queue_depth", uint64(len(rt.ctrl))}, {"control_queue_capacity", uint64(cap(rt.ctrl))},
		{"datagram_queue_depth", uint64(len(rt.dgrams))}, {"datagram_queue_capacity", uint64(cap(rt.dgrams))},
		{"connections", uint64(connections)}, {"connection_capacity", uint64(rt.cfg.MaxConns)},
		{"running", running}, {"goroutines", uint64(runtime.NumGoroutine())},
	} {
		s.Points = append(s.Points, metrics.Point{Name: "objstore_runtime_" + value.name, Gauge: value.value})
	}
	return s
}
