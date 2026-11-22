package noderuntime

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
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
		if err := rt.Start(ctx); !errors.Is(err, node.ErrConfig) {
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
	if _, err := rt.Health(context.Background()); !errors.Is(err, node.ErrNotReady) {
		t.Fatalf("health before start: %v", err)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
}
