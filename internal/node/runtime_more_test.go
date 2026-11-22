package node

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

func TestRuntimeServiceSurfaceInABubble(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var logged []string
		rt, err := NewRuntime(RuntimeConfig{ID: "solo", Region: "r0", DataDir: t.TempDir(), NoNetwork: true, Node: Config{Layout: soloLayout()},
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

func TestRuntimeDatagramsToSelfAndQueueOverflow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt, err := NewRuntime(RuntimeConfig{ID: "solo", DataDir: t.TempDir(), NoNetwork: true, DatagramQueue: 1, Node: Config{Layout: soloLayout()}})
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
			if err := net.Send("solo", []byte{chGossip, 1}); err != nil {
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

func TestMigrationClaimOfAWronglyRoutedBucketIsPermanent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newLoopRuntime(t)
		ctx := context.Background()
		if err := rt.Start(ctx); err != nil {
			t.Fatal(err)
		}
		if err := rt.CreateBucket(ctx, storage.BucketSpec{Name: "b", Region: "r0"}); err != nil {
			t.Fatal(err)
		}
		in := saga.MigrationInput{Bucket: "b", From: "r2", To: "r1"}
		errc := make(chan error, 2)
		rt.post(func() {
			c := saga.Call{Key: saga.Key{Saga: "s", Step: 0}, Input: in.Encode()}
			rt.node.mig.Claim(c, in, func(err error) { errc <- err })
			bad := saga.MigrationInput{Bucket: "nob", From: "r0", To: "r1"}
			rt.node.mig.Claim(c, bad, func(err error) { errc <- err })
		})
		for range 2 {
			if err := <-errc; !errors.Is(err, saga.ErrPermanent) {
				t.Fatalf("claim: %v, want a permanent refusal", err)
			}
		}
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	})
}
