package objload

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nickstrad/object-store/internal/storage"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
)

// maxLag is how far the sender may fall behind its schedule before it skips
// overdue requests (counted as missed) instead of sending them in a burst.
const maxLag = time.Second

// setupAttempts and setupPause bound the wait for a node to accept the
// bucket creations at start.
var (
	setupAttempts = 30
	setupPause    = time.Second
)

// Run creates the buckets if they are missing, then sends cfg.Rate requests
// per second, planned from cfg.Seed, to svcs (svcs[i] serves cfg.Addrs[i])
// until cfg.Duration elapses or ctx is cancelled. It writes a status line to
// out every cfg.Report and returns the summary of the run. Requests in flight
// when the run stops finish within cfg.Timeout. Failed requests are counted,
// never fatal: Run returns an error only for an invalid configuration.
func Run(ctx context.Context, cfg Config, svcs []httpapi.Service, out io.Writer) (Summary, error) {
	planner, err := NewPlanner(cfg)
	if err != nil {
		return Summary{}, err
	}
	if len(svcs) != len(cfg.Addrs) {
		return Summary{}, fmt.Errorf("%w: %d services for %d addresses", ErrConfig, len(svcs), len(cfg.Addrs))
	}
	dur := "until interrupted"
	if cfg.Duration > 0 {
		dur = "for " + cfg.Duration.String()
	}
	fmt.Fprintf(out, "objload: %g req/s %s over %d workers to %s\n", cfg.Rate, dur, cfg.Workers, strings.Join(cfg.Addrs, ","))
	fmt.Fprintf(out, "objload: profile %s, %d keys in each of %d buckets (%s-*), payloads %d-%d bytes, seed %d\n",
		cfg.Profile, cfg.Keys, cfg.Buckets, cfg.BucketPrefix, cfg.MinSize, cfg.MaxSize, cfg.Seed)
	createBuckets(ctx, cfg, svcs, out)

	rec := &Recorder{}
	jobs := make(chan Request, cfg.Workers)
	reqCtx := context.WithoutCancel(ctx)
	var wg sync.WaitGroup
	for range cfg.Workers {
		wg.Go(func() {
			for r := range jobs {
				c, cancel := context.WithTimeout(reqCtx, cfg.Timeout)
				t0 := time.Now()
				err := Execute(c, svcs[r.Addr], r)
				d := time.Since(t0)
				cancel()
				rec.Record(r.Op, Classify(err), d)
			}
		})
	}

	start := time.Now()
	pacer := NewPacer(start, cfg.Rate, maxLag)
	var end, tick <-chan time.Time
	if cfg.Duration > 0 {
		t := time.NewTimer(cfg.Duration)
		defer t.Stop()
		end = t.C
	}
	if cfg.Report > 0 {
		t := time.NewTicker(cfg.Report)
		defer t.Stop()
		tick = t.C
	}
	last := start
	timer := time.NewTimer(0)
	defer timer.Stop()
send:
	for {
		due, skipped := pacer.Next(time.Now())
		rec.Miss(skipped)
		timer.Reset(time.Until(due))
		for waiting := true; waiting; {
			select {
			case <-ctx.Done():
				break send
			case <-end:
				break send
			case now := <-tick:
				fmt.Fprintln(out, StatusLine(now.Sub(start), now.Sub(last), rec.TakeInterval(), cfg.Rate))
				last = now
			case <-timer.C:
				waiting = false
			}
		}
		select {
		case jobs <- planner.Next():
		default:
			rec.Miss(1)
		}
	}
	stopped := time.Now()
	close(jobs)
	wg.Wait()
	return Summary{Counts: rec.Total(), Elapsed: stopped.Sub(start), Target: cfg.Rate, Seed: cfg.Seed}, nil
}

func createBuckets(ctx context.Context, cfg Config, svcs []httpapi.Service, out io.Writer) {
	for b := range cfg.Buckets {
		name := BucketName(cfg.BucketPrefix, b)
		i := b % len(svcs)
		for attempt := 1; ; attempt++ {
			c, cancel := context.WithTimeout(ctx, cfg.Timeout)
			err := svcs[i].CreateBucket(c, storage.BucketSpec{Name: name})
			cancel()
			if err == nil || Classify(err).Status == http.StatusConflict {
				break
			}
			if attempt >= setupAttempts || ctx.Err() != nil {
				fmt.Fprintf(out, "objload: bucket %s was not created (%v); continuing, and requests to it will fail\n", name, err)
				break
			}
			if attempt == 1 {
				fmt.Fprintf(out, "objload: waiting for %s to create bucket %s: %v\n", cfg.Addrs[i], name, err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(setupPause):
			}
		}
	}
}
