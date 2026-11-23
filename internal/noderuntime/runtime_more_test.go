package noderuntime

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/node"
	"github.com/nickstrad/object-store/internal/storage"
)

// TestRuntimeServiceSurfaceInABubble exercises every httpapi.Service method
// on Runtime in a synctest bubble without network sockets.
func TestRuntimeServiceSurfaceInABubble(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logged []string
		rt, err := NewRuntime(RuntimeConfig{ID: "solo", Region: "r0", DataDir: t.TempDir(), NoNetwork: true, Node: node.Config{Layout: soloLayout()},
			Logf: func(f string, a ...any) { logged = append(logged, f) }})
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		if err := rt.Start(ctx); err != nil {
			t.Fatal(err)
		}
		if rt.ID() != "solo" || rt.Addr() != "" || rt.Panicked() {
			t.Fatal("runtime accessors")
		}
		time.Sleep(time.Second)
		if err := rt.CreateBucket(ctx, storage.BucketSpec{Name: "b", Region: "r0"}); err != nil {
			t.Fatal(err)
		}
		oi, err := rt.PutObject(ctx, storage.PutRequest{Bucket: "b", Key: "k", Data: []byte("v")})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rt.PutTag(ctx, "b", "k", "t", "v", storage.Condition{}); err != nil {
			t.Fatal(err)
		}
		if ts, err := rt.Tags(ctx, "b", "k"); err != nil || len(ts) != 1 {
			t.Fatalf("tags %v %v", ts, err)
		}
		if _, err := rt.PutACL(ctx, "b", "p", "r", storage.Condition{}); err != nil {
			t.Fatal(err)
		}
		if ai, err := rt.ACL(ctx, "b", "p"); err != nil || ai.Perm != "r" {
			t.Fatalf("acl %v %v", ai, err)
		}
		if q, err := rt.SetQuota(ctx, "b", 10, 10); err != nil || q.LimitBytes != 10 {
			t.Fatalf("quota %v %v", q, err)
		}
		if q, err := rt.Quota(ctx, "b"); err != nil || q.UsedObjects != 1 {
			t.Fatalf("quota %v %v", q, err)
		}
		if l, err := rt.ListObjects(ctx, "b", "", 0); err != nil || len(l) != 1 {
			t.Fatalf("list %v %v", l, err)
		}
		if h, err := rt.HeadObject(ctx, "b", "k"); err != nil || h.ETag != oi.ETag {
			t.Fatalf("head %v %v", h, err)
		}
		if _, err := rt.StartMigration(ctx, "b", "r1"); err != nil {
			t.Fatalf("migration start: %v", err)
		}
		if _, err := rt.DeleteObject(ctx, "b", "k", storage.Condition{IfMatch: oi.ETag}); err != nil {
			t.Fatal(err)
		}
		if h, err := rt.Health(ctx); err != nil || len(h.Counters) == 0 {
			t.Fatalf("health %v %v", h, err)
		}
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
		_ = logged
	})
}

// TestRuntimeDatagramsToSelfAndQueueOverflow proves self-datagrams enqueue
// without sockets, oversized datagrams return env.ErrTooLarge, full queues drop
// and count datagrams, and post-close sends return env.ErrClosed.
func TestRuntimeDatagramsToSelfAndQueueOverflow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt, err := NewRuntime(RuntimeConfig{ID: "solo", DataDir: t.TempDir(), NoNetwork: true, DatagramQueue: 1, Node: node.Config{Layout: soloLayout()}})
		if err != nil {
			t.Fatal(err)
		}
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		hold := make(chan struct{})
		rt.post(func() { <-hold })
		net := &realNet{rt: rt}
		for range 3 {
			if err := net.Send("solo", []byte{1, 1}); err != nil {
				t.Fatal(err)
			}
		}
		if err := net.Send("solo", make([]byte, MaxDatagram+1)); !errors.Is(err, env.ErrTooLarge) {
			t.Fatalf("oversized datagram: %v", err)
		}
		if err := net.Send("other", []byte{1}); !errors.Is(err, env.ErrUnreachable) {
			t.Fatalf("send without a network: %v", err)
		}
		if rt.Dropped() < 2 {
			t.Fatalf("dropped %d datagrams from a full queue, want at least 2", rt.Dropped())
		}
		close(hold)
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
		if err := net.Send("solo", []byte{1}); !errors.Is(err, env.ErrClosed) {
			t.Fatalf("send after close: %v", err)
		}
	})
}

// TestRuntimeRecoversAPanicOnTheLoop proves a panic posted to the node loop is
// recovered, recorded in Panicked, rejects subsequent calls with env.ErrClosed,
// and still permits Close.
func TestRuntimeRecoversAPanicOnTheLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newLoopRuntime(t)
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		rt.post(func() { var m map[int]int; m[1] = 1 })
		synctest.Wait()
		if !rt.Panicked() {
			t.Fatal("the loop did not record its panic")
		}
		if _, err := rt.Members(context.Background()); !errors.Is(err, env.ErrClosed) {
			t.Fatalf("use after a panic: %v", err)
		}
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// TestRuntimeConfigValidation proves invalid ID, region, data directory, queue
// bounds, host:port, listen address, and nested node config all fail with
// node.ErrConfig.
func TestRuntimeConfigValidation(t *testing.T) {
	base := RuntimeConfig{ID: "h:1", Region: "r", DataDir: "/d", Node: node.Config{Layout: storage.Layout{K: 2, M: 1, StripeSize: 1024}}}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	rows := []func(*RuntimeConfig){
		func(c *RuntimeConfig) { c.ID = "" },
		func(c *RuntimeConfig) { c.Region = env.Region(strings.Repeat("r", 256)) },
		func(c *RuntimeConfig) { c.DataDir = "" },
		func(c *RuntimeConfig) { c.ControlQueue = -1 },
		func(c *RuntimeConfig) { c.ID = "nohostport" },
		func(c *RuntimeConfig) { c.Listen = "bad" },
		func(c *RuntimeConfig) { c.Node.ScrubBatch = -1 },
	}
	for i, mut := range rows {
		c := base
		mut(&c)
		if err := c.Validate(); !errors.Is(err, node.ErrConfig) {
			t.Errorf("row %d: %v", i, err)
		}
		if _, err := NewRuntime(c); err == nil {
			t.Errorf("row %d: runtime built", i)
		}
	}
}

// TestNoConnectionIsTrackedAfterALoopPanic proves track refuses newly accepted
// connections after the node loop has panicked.
func TestNoConnectionIsTrackedAfterALoopPanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newLoopRuntime(t)
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		rt.post(func() { var m map[int]int; m[1] = 1 })
		synctest.Wait()
		if !rt.Panicked() {
			t.Fatal("the loop did not record its panic")
		}
		a, b := net.Pipe()
		defer func() {
			_ = a.Close()
			_ = b.Close()
		}()
		if rt.track(a) {
			rt.untrack(a)
			t.Fatal("a connection accepted after the loop panicked was tracked and would be served")
		}
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// TestNoConnectionIsTrackedAfterTheRuntimeContextEnds proves track refuses
// newly accepted connections once rt.ctx is canceled.
func TestNoConnectionIsTrackedAfterTheRuntimeContextEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newLoopRuntime(t)
		if err := rt.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		rt.cancel()
		synctest.Wait()
		a, b := net.Pipe()
		defer func() {
			_ = a.Close()
			_ = b.Close()
		}()
		if rt.track(a) {
			rt.untrack(a)
			t.Fatal("a connection accepted after the runtime context ended was tracked")
		}
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	})
}
