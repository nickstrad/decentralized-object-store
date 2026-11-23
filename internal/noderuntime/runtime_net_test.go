package noderuntime_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/gossip"
	"github.com/nickstrad/object-store/internal/node"
	"github.com/nickstrad/object-store/internal/noderuntime"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/storage"
	"github.com/nickstrad/object-store/internal/testutil/leakcheck"
)

func TestMain(m *testing.M) { os.Exit(leakcheck.Main(m, 5*time.Second)) }

func realConfig() node.Config {
	return node.Config{
		Layout:         storage.Layout{K: 2, M: 1, StripeSize: 64 << 10},
		Gossip:         gossip.Config{ProbeInterval: 200 * time.Millisecond, ProbeTimeout: 80 * time.Millisecond, SyncInterval: time.Second},
		OCCPeriod:      500 * time.Millisecond,
		MetaPeriod:     300 * time.Millisecond,
		MaintainPeriod: 200 * time.Millisecond,
		ViewSettle:     500 * time.Millisecond,
		GCGrace:        2 * time.Second,
	}
}

type realCluster struct {
	rts  []*noderuntime.Runtime
	dirs []string
	ids  []env.NodeID
	cfg  node.Config
}

func startRealWith(t testing.TB, n int, cfg node.Config, pick func(int) ([]string, error)) (*realCluster, error) {
	t.Helper()
	var usedIDs []env.NodeID
	var usedCfg node.Config
	rts, dirs, err := noderuntime.StartLoopbackCluster(t, n, pick, func(i int, ids []env.NodeID, dir string) noderuntime.RuntimeConfig {
		c := cfg
		c.Seeds = ids
		usedIDs = ids
		usedCfg = c
		return noderuntime.RuntimeConfig{ID: ids[i], Region: env.Region(fmt.Sprintf("r%d", i%3)), DataDir: dir, Node: c}
	})
	if err != nil {
		return nil, err
	}
	c := &realCluster{rts: rts, dirs: dirs, ids: usedIDs, cfg: usedCfg}
	t.Cleanup(func() {
		for _, rt := range c.rts {
			if err := rt.Close(); err != nil {
				t.Error(err)
			}
		}
	})
	return c, nil
}

func startReal(t *testing.T, n int, cfg node.Config) *realCluster {
	t.Helper()
	c, err := startRealWith(t, n, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func eventually(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("not within %v: %s", timeout, what)
		}
		<-tick.C
	}
}

func (c *realCluster) joined(n int) bool {
	for _, rt := range c.rts {
		ms, err := rt.Members(context.Background())
		if err != nil {
			return false
		}
		live := 0
		for _, m := range ms {
			if m.State == "alive" {
				live++
			}
		}
		if live != n {
			return false
		}
	}
	return true
}

// TestLoopbackClusterServesObjectsAcrossNodes proves three loopback nodes join,
// replicate buckets and multi-stripe objects, enforce If-None-Match/If-Match,
// and report ready health.
func TestLoopbackClusterServesObjectsAcrossNodes(t *testing.T) {
	c := startReal(t, 3, realConfig())
	ctx := context.Background()
	eventually(t, 20*time.Second, "three nodes see each other", func() bool { return c.joined(3) })
	if err := c.rts[0].CreateBucket(ctx, storage.BucketSpec{Name: "b"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 20*time.Second, "the bucket reaches node 1", func() bool {
		_, err := c.rts[1].ListObjects(ctx, "b", "", 0)
		return err == nil
	})
	data := bytes.Repeat([]byte("0123456789"), 20_000)
	oi, err := c.rts[1].PutObject(ctx, storage.PutRequest{Bucket: "b", Key: "k", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	eventually(t, 20*time.Second, "node 2 reads the object", func() bool {
		_, b, err := c.rts[2].GetObject(ctx, "b", "k")
		got = b
		return err == nil
	})
	if !bytes.Equal(got, data) {
		t.Fatal("node 2 read wrong bytes")
	}
	if _, err := c.rts[1].PutObject(ctx, storage.PutRequest{Bucket: "b", Key: "k", Data: []byte("x"), Cond: storage.Condition{IfNoneMatch: true}}); !errors.Is(err, occ.ErrPreconditionFailed) {
		t.Fatalf("create-only put over an existing object: %v", err)
	}
	if _, err := c.rts[1].PutObject(ctx, storage.PutRequest{Bucket: "b", Key: "k", Data: []byte("y"), Cond: storage.Condition{IfMatch: oi.ETag}}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.rts[1].PutObject(ctx, storage.PutRequest{Bucket: "b", Key: "k", Data: []byte("z"), Cond: storage.Condition{IfMatch: oi.ETag}}); !errors.Is(err, occ.ErrPreconditionFailed) {
		t.Fatalf("put with a stale etag: %v", err)
	}
	h, err := c.rts[0].Health(ctx)
	if err != nil || !h.Ready || h.LiveMembers != 3 {
		t.Fatalf("health %+v %v", h, err)
	}
}

// TestShutdownUnderLoadReturnsEveryCall proves shutting down a node while 16
// concurrent writers are active returns every in-flight call cleanly and
// rejects later calls with env.ErrClosed.
func TestShutdownUnderLoadReturnsEveryCall(t *testing.T) {
	c := startReal(t, 3, realConfig())
	ctx := context.Background()
	eventually(t, 20*time.Second, "three nodes see each other", func() bool { return c.joined(3) })
	if err := c.rts[0].CreateBucket(ctx, storage.BucketSpec{Name: "b"}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var once sync.Once
	loaded := make(chan struct{})
	errs := make(chan error, 64)
	for i := range 16 {
		wg.Go(func() {
			for j := range 4 {
				_, err := c.rts[0].PutObject(ctx, storage.PutRequest{Bucket: "b", Key: fmt.Sprintf("k%d-%d", i, j), Data: bytes.Repeat([]byte{byte(i)}, 30_000)})
				once.Do(func() { close(loaded) })
				if err != nil {
					errs <- err
					return
				}
			}
		})
	}
	<-loaded
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := c.rts[0].Shutdown(sctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, env.ErrClosed) && !errors.Is(err, storage.ErrUnavailable) && !errors.Is(err, node.ErrStopped) {
			t.Errorf("a put interrupted by shutdown failed with %v", err)
		}
	}
	if _, err := c.rts[0].PutObject(ctx, storage.PutRequest{Bucket: "b", Key: "late", Data: []byte("x")}); !errors.Is(err, env.ErrClosed) {
		t.Fatalf("put after shutdown: %v", err)
	}
}

// TestCanaryShutdownLeak plants an untracked goroutine in Runtime.Start (not
// registered with rt.wg) and proves leakcheck.Snapshot.Leaked catches it until
// released.
func TestCanaryShutdownLeak(t *testing.T) {
	before := leakcheck.Take()
	release := make(chan struct{})
	noderuntime.SetCanaryUntrackedGoroutine(release)
	t.Cleanup(func() { noderuntime.SetCanaryUntrackedGoroutine(nil) })
	rts, _, err := noderuntime.StartLoopbackCluster(t, 1, nil, func(_ int, ids []env.NodeID, dir string) noderuntime.RuntimeConfig {
		return noderuntime.RuntimeConfig{ID: ids[0], Region: "r0", DataDir: dir, Node: realConfig()}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rts[0].Close(); err != nil {
		t.Fatal(err)
	}
	if err := before.Leaked(200 * time.Millisecond); !errors.Is(err, leakcheck.ErrLeak) {
		t.Fatalf("a goroutine started without the runtime's WaitGroup passed the leak check: %v", err)
	}
	close(release)
	if err := before.Leaked(5 * time.Second); err != nil {
		t.Fatalf("leak check still failing after the canary goroutine exited: %v", err)
	}
}

// TestRealRuntimeLeavesNoGoroutineBehind proves starting a 2-node loopback
// cluster, waiting for join, and closing both nodes leaks zero goroutines.
func TestRealRuntimeLeavesNoGoroutineBehind(t *testing.T) {
	before := leakcheck.Take()
	c := startReal(t, 2, realConfig())
	eventually(t, 20*time.Second, "two nodes see each other", func() bool { return c.joined(2) })
	for _, rt := range c.rts {
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := before.Leaked(5 * time.Second); err != nil {
		t.Fatal(err)
	}
}

// TestStartRealRetriesLostPortWithoutLeaking proves startRealWith retries a
// cluster onto fresh ports on UDP address-in-use without leaking goroutines,
// fails immediately on non-address-in-use errors, and stops after five
// attempts when a port remains blocked.
func TestStartRealRetriesLostPortWithoutLeaking(t *testing.T) {
	t.Run("retries on UDP address already in use without leaking", func(t *testing.T) {
		before := leakcheck.Take()
		blocker, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := blocker.Close(); err != nil {
				t.Error(err)
			}
		}()
		blockedAddr := blocker.LocalAddr().String()
		picks := 0
		var firstAddrs []string
		pick := func(n int) ([]string, error) {
			picks++
			if picks == 1 {
				free, err := noderuntime.FreeLoopbackAddrs(n - 1)
				if err != nil {
					return nil, err
				}
				firstAddrs = append(slices.Clone(free), blockedAddr)
				return slices.Clone(firstAddrs), nil
			}
			return noderuntime.FreeLoopbackAddrs(n)
		}
		c, err := startRealWith(t, 3, realConfig(), pick)
		if err != nil {
			t.Fatalf("startRealWith did not survive lost UDP port %s: %v", blockedAddr, err)
		}
		if picks != 2 {
			t.Fatalf("address picker called %d times, want 2", picks)
		}
		for _, rt := range c.rts {
			if string(rt.ID()) == blockedAddr {
				t.Fatalf("cluster node used blocked address %s", blockedAddr)
			}
			if slices.Contains(firstAddrs, string(rt.ID())) {
				t.Fatalf("cluster node reused first-attempt address %s", rt.ID())
			}
		}
		eventually(t, 20*time.Second, "three nodes join after port retry", func() bool { return c.joined(3) })
		for _, rt := range c.rts {
			if err := rt.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if err := before.Leaked(5 * time.Second); err != nil {
			t.Fatalf("goroutines leaked after retry and close: %v", err)
		}
	})

	t.Run("non-address-in-use error fails at once without leaking", func(t *testing.T) {
		before := leakcheck.Take()
		picks := 0
		pick := func(n int) ([]string, error) {
			picks++
			free, err := noderuntime.FreeLoopbackAddrs(n - 1)
			if err != nil {
				return nil, err
			}
			return append(free, "127.0.0.1:99999"), nil
		}
		c, err := startRealWith(t, 3, realConfig(), pick)
		if err == nil {
			t.Fatalf("startRealWith succeeded on invalid port: %+v", c)
		}
		if picks != 1 {
			t.Fatalf("non-address-in-use error retried: picker called %d times, want 1 (err=%v)", picks, err)
		}
		if strings.Contains(err.Error(), "address already in use") {
			t.Fatalf("unexpected address-in-use error: %v", err)
		}
		if err := before.Leaked(5 * time.Second); err != nil {
			t.Fatalf("goroutines leaked after non-address-in-use start failure: %v", err)
		}
	})

	t.Run("exhausting retries fails with last error without leaking", func(t *testing.T) {
		before := leakcheck.Take()
		blocker, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := blocker.Close(); err != nil {
				t.Error(err)
			}
		}()
		blockedAddr := blocker.LocalAddr().String()
		picks := 0
		pick := func(n int) ([]string, error) {
			picks++
			free, err := noderuntime.FreeLoopbackAddrs(n - 1)
			if err != nil {
				return nil, err
			}
			return append(free, blockedAddr), nil
		}
		c, err := startRealWith(t, 2, realConfig(), pick)
		if err == nil {
			t.Fatalf("startRealWith succeeded despite permanently blocked port %s: %+v", blockedAddr, c)
		}
		if picks != 5 {
			t.Fatalf("address picker called %d times, want 5", picks)
		}
		if !strings.Contains(err.Error(), "address already in use") {
			t.Fatalf("got error %v, want address already in use", err)
		}
		if err := before.Leaked(5 * time.Second); err != nil {
			t.Fatalf("goroutines leaked after exhausting start retries: %v", err)
		}
	})
}
