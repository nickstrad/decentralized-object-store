package node_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

func smallConfig() node.Config {
	c := dstConfig()
	c.Layout = storage.Layout{K: 2, M: 1, StripeSize: 1024}
	return c
}

func smallCluster(t *testing.T, seed int64) *harness {
	t.Helper()
	h, err := newHarness(seed, 3, smallConfig(), sim.DefaultNetConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) syncPut(t *testing.T, id env.NodeID, req storage.PutRequest) (storage.ObjectInfo, error) {
	t.Helper()
	var (
		oi   storage.ObjectInfo
		err  error
		done bool
	)
	h.node(id).PutObject(req, func(o storage.ObjectInfo, e error) { oi, err, done = o, e, true })
	if done {
		t.Fatal("put callback ran inside the call")
	}
	if rerr := h.s.RunUntilConverged(h.s.Now().Add(10*time.Second), func() bool { return done }); rerr != nil {
		t.Fatalf("put never completed: %v", rerr)
	}
	return oi, err
}

func (h *harness) syncGet(t *testing.T, id env.NodeID, bucket, key string) (storage.ObjectInfo, []byte, error) {
	t.Helper()
	var (
		oi   storage.ObjectInfo
		data []byte
		err  error
		done bool
	)
	h.node(id).GetObject(bucket, key, func(o storage.ObjectInfo, d []byte, e error) { oi, data, err, done = o, d, e, true })
	if done {
		t.Fatal("get callback ran inside the call")
	}
	if rerr := h.s.RunUntilConverged(h.s.Now().Add(10*time.Second), func() bool { return done }); rerr != nil {
		t.Fatalf("get never completed: %v", rerr)
	}
	return oi, data, err
}

// TestQuotaRefusesAPutOverTheLimit proves a node tracks local bucket byte and
// object usage and refuses a PUT that exceeds QuotaBytes with
// occ.ErrQuotaExceeded.
func TestQuotaRefusesAPutOverTheLimit(t *testing.T) {
	h := smallCluster(t, 1)
	n := h.node(h.ids[0])
	if err := n.CreateBucket(storage.BucketSpec{Name: "q", QuotaBytes: 250}); err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if _, err := h.syncPut(t, h.ids[0], storage.PutRequest{Bucket: "q", Key: fmt.Sprint(i), Data: make([]byte, 100)}); err != nil {
			t.Fatal(err)
		}
	}
	q, err := n.Quota("q")
	if err != nil || q.UsedBytes != 200 || q.UsedObjects != 2 {
		t.Fatalf("quota %+v %v", q, err)
	}
	if _, err := h.syncPut(t, h.ids[0], storage.PutRequest{Bucket: "q", Key: "x", Data: make([]byte, 100)}); !errors.Is(err, occ.ErrQuotaExceeded) {
		t.Fatalf("third put: %v, want ErrQuotaExceeded", err)
	}
}
