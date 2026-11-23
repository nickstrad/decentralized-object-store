package noderuntime_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/storage"
)

// TestMeasureLoopbackRuntime measures idle goroutines/heap, sequential 1 MiB
// and 32 MiB PUT/GET latency and throughput, and peak goroutines under load on
// a 3-node loopback cluster when MEASURE_RUNTIME=1 is set.
func TestMeasureLoopbackRuntime(t *testing.T) {
	if os.Getenv("MEASURE_RUNTIME") == "" {
		t.Skip("set MEASURE_RUNTIME=1 to measure the loopback runtime")
	}
	cfg := realConfig()
	cfg.Layout = storage.Layout{K: 2, M: 1, StripeSize: 1 << 20}
	c := startReal(t, 3, cfg)
	ctx := context.Background()
	eventually(t, 20*time.Second, "joined", func() bool { return c.joined(3) })
	if err := c.rts[0].CreateBucket(ctx, storage.BucketSpec{Name: "m"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 20*time.Second, "bucket everywhere", func() bool {
		_, err := c.rts[2].ListObjects(ctx, "m", "", 1)
		return err == nil
	})
	time.Sleep(2 * time.Second)
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	t.Logf("MEASURE runtime/idle goroutines=%d heap_alloc_mib=%.1f (3 nodes in one process)", runtime.NumGoroutine(), float64(ms.HeapAlloc)/(1<<20))
	for _, size := range []int{1 << 20, 32 << 20} {
		data := bytes.Repeat([]byte{7}, size)
		n := 8
		if size > 1<<20 {
			n = 4
		}
		var putLat, getLat []time.Duration
		start := time.Now()
		for i := range n {
			t0 := time.Now()
			if _, err := c.rts[i%3].PutObject(ctx, storage.PutRequest{Bucket: "m", Key: fmt.Sprintf("s%d-%d", size, i), Data: data}); err != nil {
				t.Fatal(err)
			}
			putLat = append(putLat, time.Since(t0))
		}
		putTotal := time.Since(start)
		time.Sleep(time.Second)
		start = time.Now()
		for i := range n {
			t0 := time.Now()
			_, got, err := c.rts[(i+1)%3].GetObject(ctx, "m", fmt.Sprintf("s%d-%d", size, i))
			if err != nil || len(got) != size {
				t.Fatalf("get: %d bytes %v", len(got), err)
			}
			getLat = append(getLat, time.Since(t0))
		}
		getTotal := time.Since(start)
		slices.Sort(putLat)
		slices.Sort(getLat)
		mib := float64(size*n) / (1 << 20)
		t.Logf("MEASURE runtime/%dMiB put n=%d throughput=%.1fMiB/s p50=%v max=%v", size>>20, n, mib/putTotal.Seconds(), putLat[n/2].Round(time.Millisecond), putLat[n-1].Round(time.Millisecond))
		t.Logf("MEASURE runtime/%dMiB get n=%d throughput=%.1fMiB/s p50=%v max=%v", size>>20, n, mib/getTotal.Seconds(), getLat[n/2].Round(time.Millisecond), getLat[n-1].Round(time.Millisecond))
	}
	var wg sync.WaitGroup
	peak := 0
	stop := make(chan struct{})
	wg.Go(func() {
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				peak = max(peak, runtime.NumGoroutine())
			}
		}
	})
	var load sync.WaitGroup
	for i := range 16 {
		load.Go(func() {
			if _, err := c.rts[i%3].PutObject(ctx, storage.PutRequest{Bucket: "m", Key: fmt.Sprintf("l%d", i), Data: bytes.Repeat([]byte{1}, 1<<20)}); err != nil {
				t.Error(err)
			}
		})
	}
	load.Wait()
	close(stop)
	wg.Wait()
	t.Logf("MEASURE runtime/load goroutines_peak=%d with 16 concurrent 1 MiB puts", peak)
}
