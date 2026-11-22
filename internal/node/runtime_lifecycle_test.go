package node

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

func soloLayout() storage.Layout { return storage.Layout{K: 1, M: 0, StripeSize: 1024} }

func TestRuntimeLifecycleInABubble(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newLoopRuntime(t)
		ctx := context.Background()
		if err := rt.Start(ctx); err != nil {
			t.Fatal(err)
		}
		if err := rt.Start(ctx); !errors.Is(err, ErrConfig) {
			t.Fatalf("second start: %v", err)
		}
		time.Sleep(time.Second)
		if err := rt.CreateBucket(ctx, storage.BucketSpec{Name: "b"}); err != nil {
			t.Fatal(err)
		}
		oi, err := rt.PutObject(ctx, storage.PutRequest{Bucket: "b", Key: "k", Data: []byte("hello")})
		if err != nil {
			t.Fatal(err)
		}
		_, data, err := rt.GetObject(ctx, "b", "k")
		if err != nil || string(data) != "hello" {
			t.Fatalf("get %q %v", data, err)
		}
		if _, err := rt.PutObject(ctx, storage.PutRequest{Bucket: "b", Key: "k", Data: []byte("x"), Cond: storage.Condition{IfMatch: oi.ETag}}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Minute)
		if err := rt.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		if err := rt.Close(); err != nil {
			t.Fatalf("double close: %v", err)
		}
		if _, err := rt.HeadObject(ctx, "b", "k"); !errors.Is(err, env.ErrClosed) {
			t.Fatalf("use after close: %v", err)
		}
		if err := rt.Start(ctx); !errors.Is(err, env.ErrClosed) {
			t.Fatalf("start after close: %v", err)
		}
	})
}

func TestRuntimeCloseDuringStartup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newLoopRuntime(t)
		block := make(chan struct{})
		errc := make(chan error, 1)
		go func() {
			<-block
			errc <- rt.Start(context.Background())
		}()
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
		close(block)
		if err := <-errc; !errors.Is(err, env.ErrClosed) {
			t.Fatalf("start after a concurrent close: %v", err)
		}
	})
}

func TestRuntimeStartHonoursItsContext(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newLoopRuntime(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := rt.Start(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("start with a canceled context: %v", err)
		}
		if _, err := rt.Members(context.Background()); !errors.Is(err, env.ErrClosed) {
			t.Fatalf("use after a canceled start: %v", err)
		}
	})
}

func TestRuntimeCallsBeforeStartAreNotReady(t *testing.T) {
	rt := newLoopRuntime(t)
	if _, err := rt.Health(context.Background()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("health before start: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeCancelMidPutLeavesOnlyCollectableShards(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newLoopRuntime(t)
		rt.cfg.Node.GCGrace = time.Minute
		bg := context.Background()
		if err := rt.Start(bg); err != nil {
			t.Fatal(err)
		}
		if err := rt.CreateBucket(bg, storage.BucketSpec{Name: "b"}); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(bg)
		hold := make(chan struct{})
		rt.post(func() { <-hold })
		errc := make(chan error, 1)
		go func() {
			_, err := rt.PutObject(ctx, storage.PutRequest{Bucket: "b", Key: "k", Data: make([]byte, 4096)})
			errc <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-errc; !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled put: %v", err)
		}
		close(hold)
		time.Sleep(time.Second)
		synctest.Wait()
		if _, err := rt.HeadObject(bg, "b", "k"); err != nil && !isNotFound(err) {
			t.Fatalf("head after a canceled put: %v", err)
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		orphans, err := syncCall(bg, rt, "check", func(n *Node) (int, error) {
			names, err := n.e.Disk.List("shards/")
			if err != nil {
				return 0, err
			}
			rd, _ := n.occ.Reader()
			live := 0
			for _, o := range n.liveObjects(rd, "b") {
				live += len(o.rec.Manifest.Stripes)
			}
			return len(names) - live, nil
		})
		if err != nil || orphans != 0 {
			t.Fatalf("%d shards no manifest refers to remain after the grace period (%v)", orphans, err)
		}
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	})
}
