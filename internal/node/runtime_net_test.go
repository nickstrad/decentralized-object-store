package node_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/gossip"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
	"github.com/nickstrad/decentralized-object-store/internal/testutil/leakcheck"
)

func TestMain(m *testing.M) { os.Exit(leakcheck.Main(m, 5*time.Second)) }

func freeAddr(t testing.TB) string {
	t.Helper()
	for range 20 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		u, err := net.ListenPacket("udp", addr)
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		if err == nil {
			if err := u.Close(); err != nil {
				t.Fatal(err)
			}
			return addr
		}
	}
	t.Fatal("no free loopback port")
	return ""
}

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
	rts []*node.Runtime
}

func startReal(t *testing.T, n int, cfg node.Config) *realCluster {
	t.Helper()
	var ids []env.NodeID
	for range n {
		ids = append(ids, env.NodeID(freeAddr(t)))
	}
	cfg.Seeds = ids
	c := &realCluster{}
	for i, id := range ids {
		rt, err := node.NewRuntime(node.RuntimeConfig{ID: id, Region: env.Region(fmt.Sprintf("r%d", i%3)), DataDir: t.TempDir(), Node: cfg})
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		c.rts = append(c.rts, rt)
	}
	t.Cleanup(func() {
		for _, rt := range c.rts {
			if err := rt.Close(); err != nil {
				t.Error(err)
			}
		}
	})
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

func TestCanaryShutdownLeak(t *testing.T) {
	before := leakcheck.Take()
	release := make(chan struct{})
	node.SetCanaryUntrackedGoroutine(release)
	t.Cleanup(func() { node.SetCanaryUntrackedGoroutine(nil) })
	rt, err := node.NewRuntime(node.RuntimeConfig{ID: env.NodeID(freeAddr(t)), Region: "r0", DataDir: t.TempDir(), Node: realConfig()})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := rt.Close(); err != nil {
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
