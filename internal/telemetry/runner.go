package telemetry

import (
	"context"
	"errors"
	"fmt"
	"github.com/nickstrad/object-store/internal/metrics"
	"math"
	"sync"
	"time"
)

type Runner struct {
	cfg                                             normalized
	id                                              Identity
	collector                                       Collector
	adapter                                         Adapter
	log                                             func(string, ...any)
	enabled                                         bool
	mu                                              sync.Mutex
	started, closed                                 bool
	cancel                                          context.CancelFunc
	done                                            chan struct{}
	shutdownOnce                                    sync.Once
	shutdownDone                                    chan struct{}
	shutdownErr                                     error
	counts                                          [6]uint64
	sequence                                        uint64
	collectionDuration, exportDuration, lastSuccess uint64
	failures                                        map[string]failureLog
}
type failureLog struct {
	at         time.Time
	suppressed uint64
}

func NewRunner(c Config, id Identity, collector Collector, adapter Adapter, log func(string, ...any)) (*Runner, error) {
	n, err := normalize(c)
	if err != nil {
		return nil, err
	}
	if c.Enabled {
		if collector == nil || adapter == nil {
			return nil, fmt.Errorf("%w: missing collector or adapter", ErrConfig)
		}
		if err := validateIdentity(id); err != nil {
			return nil, err
		}
	}
	return &Runner{cfg: n, id: id, collector: collector, adapter: adapter, log: log, enabled: c.Enabled, done: make(chan struct{}), shutdownDone: make(chan struct{}), failures: make(map[string]failureLog)}, nil
}
func inc(v *uint64) {
	if *v < math.MaxUint64 {
		*v++
	}
}
func (r *Runner) Run(parent context.Context) error {
	r.mu.Lock()
	if r.closed || r.started {
		r.mu.Unlock()
		return ErrClosed
	}
	r.started = true
	ctx, cancel := context.WithCancel(parent)
	r.cancel = cancel
	r.mu.Unlock()
	defer close(r.done)
	defer cancel()
	if !r.enabled {
		return nil
	}
	ticker := time.NewTicker(r.cfg.interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		start := time.Now()
		r.cycle(ctx)
		elapsed := time.Since(start)
		// One elapsed tick remains pending; only the others were coalesced.
		for missed := elapsed/r.cfg.interval - 1; missed > 0; missed-- {
			inc(&r.counts[5])
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
func (r *Runner) cycle(ctx context.Context) {
	started := time.Now()
	collectCtx, cancel := context.WithTimeout(ctx, r.cfg.timeout)
	b, err := r.collector.Collect(collectCtx)
	if err == nil {
		err = collectCtx.Err()
	}
	cancel()
	if err != nil {
		inc(&r.counts[1])
		r.collectionDuration = uint64(time.Since(started))
		r.report("collection", err)
		return
	}
	b = metrics.Clone(b)
	r.sequence++
	b.SchemaVersion = metrics.SchemaVersion
	b.Sequence = r.sequence
	b.NodeID = r.id.NodeID
	b.Region = r.id.Region
	b.BootID = r.id.BootID
	b.StartUnixNano = r.id.StartUnixNano
	b.TimeUnixNano = time.Now().UnixNano()
	b.Snapshots = append(b.Snapshots, r.snapshot())
	if err = metrics.Validate(b); err != nil {
		inc(&r.counts[1])
		r.collectionDuration = uint64(time.Since(started))
		r.report("validation", err)
		return
	}
	inc(&r.counts[0])
	r.collectionDuration = uint64(time.Since(started))
	started = time.Now()
	publishCtx, cancel := context.WithTimeout(ctx, r.cfg.timeout)
	err = r.adapter.Publish(publishCtx, b)
	if err == nil {
		err = publishCtx.Err()
	}
	cancel()
	r.exportDuration = uint64(time.Since(started))
	if err != nil {
		if errors.Is(err, ErrPartial) {
			inc(&r.counts[4])
		} else {
			inc(&r.counts[3])
		}
		r.report("export", err)
	} else {
		inc(&r.counts[2])
		r.lastSuccess = uint64(time.Now().Unix())
	}
}
func (r *Runner) snapshot() metrics.Snapshot {
	s := metrics.Snapshot{Component: "telemetry"}
	for i, v := range r.counts[:5] {
		name := "objstore_telemetry_collections_total"
		outcome := "ok"
		if i >= 2 {
			name = "objstore_telemetry_exports_total"
		}
		if i == 1 || i == 3 {
			outcome = "error"
		}
		if i == 4 {
			outcome = "partial"
		}
		s.Points = append(s.Points, metrics.Point{Name: name, Labels: []metrics.Label{{Name: "outcome", Value: outcome}}, Counter: v})
	}
	s.Points = append(s.Points, metrics.Point{Name: "objstore_telemetry_skipped_ticks_total", Counter: r.counts[5]}, metrics.Point{Name: "objstore_telemetry_last_success_unixtime", Gauge: r.lastSuccess}, metrics.Point{Name: "objstore_telemetry_collection_duration_nanoseconds", Gauge: r.collectionDuration}, metrics.Point{Name: "objstore_telemetry_export_duration_nanoseconds", Gauge: r.exportDuration})
	return s
}
func (r *Runner) report(class string, err error) {
	if r.log == nil {
		return
	}
	f := r.failures[class]
	if !f.at.IsZero() && time.Since(f.at) < time.Minute {
		inc(&f.suppressed)
		r.failures[class] = f
		return
	}
	// Sinks may include secrets in errors; report only the bounded failure class.
	r.log("observability %s failed (suppressed=%d)", class, f.suppressed)
	r.failures[class] = failureLog{at: time.Now()}
}
func (r *Runner) Shutdown(ctx context.Context) error {
	r.mu.Lock()
	r.closed = true
	started := r.started
	if r.cancel != nil {
		r.cancel()
	}
	r.mu.Unlock()
	if started {
		select {
		case <-r.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	r.shutdownOnce.Do(func() {
		if r.enabled && r.adapter != nil {
			r.shutdownErr = r.adapter.Shutdown(ctx)
		}
		close(r.shutdownDone)
	})
	select {
	case <-r.shutdownDone:
		return r.shutdownErr
	case <-ctx.Done():
		return ctx.Err()
	}
}
