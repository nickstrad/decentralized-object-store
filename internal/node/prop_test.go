package node_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/gossip"
	"github.com/nickstrad/object-store/internal/node"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/sim"
	"github.com/nickstrad/object-store/internal/storage"
	"pgregory.net/rapid"
)

type chaosNet struct {
	net      env.Transport
	dup      bool
	lossPPM  int
	draw     func(n int) int
	dupCount *int
	lost     *int
}

func (c chaosNet) Send(to env.NodeID, msg []byte) error { return c.net.Send(to, msg) }

func (c chaosNet) Request(to env.NodeID, req []byte, timeout time.Duration, cb func([]byte, error)) {
	if c.dup {
		*c.dupCount++
		c.net.Request(to, req, timeout, func([]byte, error) {})
	}
	c.net.Request(to, req, timeout, func(resp []byte, err error) {
		if err == nil && c.draw(1_000_000) < c.lossPPM {
			*c.lost++
			cb(nil, fmt.Errorf("chaos: response lost: %w", env.ErrTimeout))
			return
		}
		cb(resp, err)
	})
}

func propCluster(t *rapid.T, seed int64, dup bool, lossPPM int, dupCount, lost *int) *harness {
	cfg := realConfig()
	table, err := node.CodeTable()
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{s: sim.New(sim.Config{Seed: seed, Codes: table}), ids: sim.NodeIDs(6)}
	cfg.Seeds = h.ids[:3]
	h.cfg = cfg
	inner := node.NewFactory(cfg)
	w := h.s.WorkloadRand()
	factory := func(e env.Env) env.Process {
		return inner(e.WithNet(chaosNet{net: e.Net, dup: dup, lossPPM: lossPPM, draw: w.IntN, dupCount: dupCount, lost: lost}))
	}
	if err := h.s.AddCluster(h.ids, dstRegions, factory); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(4 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: "p"}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.metaConverged); err != nil {
		t.Fatal(err)
	}
	return h
}

type propOp struct {
	put    bool
	key    string
	data   []byte
	writer int
	reader int
}

func propOps(t *rapid.T) []propOp {
	return rapid.SliceOfN(rapid.Custom(func(t *rapid.T) propOp {
		size := rapid.SampledFrom([]int{0, 1, 2, 3, 1021, 64 << 10, 64<<10 + 1, 3*64<<10 + 5}).Draw(t, "size")
		return propOp{
			put:    rapid.Bool().Draw(t, "put"),
			key:    rapid.SampledFrom([]string{"a", "b", "c"}).Draw(t, "key"),
			data:   bytes.Repeat([]byte{rapid.Byte().Draw(t, "byte")}, size),
			writer: rapid.IntRange(0, 5).Draw(t, "writer"),
			reader: rapid.IntRange(0, 5).Draw(t, "reader"),
		}
	}), 1, 6).Draw(t, "ops")
}

func runProp(t *rapid.T, h *harness, ops []propOp) map[string][]byte {
	acked := map[string][]byte{}
	for _, op := range ops {
		n := h.node(h.ids[op.writer])
		if op.put {
			var done bool
			var perr error
			n.PutObject(storage.PutRequest{Bucket: "p", Key: op.key, Data: op.data}, func(_ storage.ObjectInfo, err error) { perr, done = err, true })
			if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), func() bool { return done }); err != nil {
				t.Fatal(err)
			}
			switch {
			case perr == nil:
				acked[op.key] = op.data
			case errors.Is(perr, storage.ErrUnavailable):
			default:
				t.Fatalf("put %s: %v", op.key, perr)
			}
		} else if _, err := n.DeleteObject("p", op.key, storage.Condition{}); err == nil {
			delete(acked, op.key)
		} else if !errors.Is(err, occ.ErrNotFound) {
			t.Fatalf("delete %s: %v", op.key, err)
		}
		if err := h.s.RunUntilConverged(h.s.Now().Add(30*time.Second), h.metaConverged); err != nil {
			t.Fatal(err)
		}
	}
	for _, key := range []string{"a", "b", "c"} {
		for _, op := range ops {
			var got []byte
			var gerr error
			done := false
			h.node(h.ids[op.reader]).GetObject("p", key, func(_ storage.ObjectInfo, b []byte, err error) { got, gerr, done = b, err, true })
			if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), func() bool { return done }); err != nil {
				t.Fatal(err)
			}
			want, ok := acked[key]
			switch {
			case !ok && !errors.Is(gerr, occ.ErrNotFound):
				t.Fatalf("get of deleted or never written %s: %v", key, gerr)
			case ok && (gerr != nil || !bytes.Equal(got, want)):
				t.Fatalf("get %s from %s: %d bytes, %v; want the %d bytes last acknowledged", key, h.ids[op.reader], len(got), gerr, len(want))
			}
		}
	}
	return acked
}

// TestPropObjectRoundTripThroughTheFullPath proves random PUT/DELETE sequences
// across 6 simulated nodes and boundary sizes always read back the last
// acknowledged bytes or ErrNotFound after metadata converges.
func TestPropObjectRoundTripThroughTheFullPath(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var dups, lost int
		h := propCluster(t, rapid.Int64().Draw(t, "seed"), false, 0, &dups, &lost)
		runProp(t, h, propOps(t))
	})
}

// TestPropRemoteOperationsAreIdempotent proves duplicating every RPC and
// dropping 10% of replies never double-applies writes or corrupts bucket quota
// usage once converged.
func TestPropRemoteOperationsAreIdempotent(t *testing.T) {
	lossy := 0
	rapid.Check(t, func(t *rapid.T) {
		var dups, lost int
		h := propCluster(t, rapid.Int64().Draw(t, "seed"), true, 100_000, &dups, &lost)
		acked := runProp(t, h, propOps(t))
		var want occ.Usage
		for _, b := range acked {
			want.Bytes += uint64(len(b))
			want.Objects++
		}
		if err := h.s.RunUntilConverged(h.s.Now().Add(30*time.Second), func() bool { return h.metaConverged() && h.quotaAgreesWith("p", want) }); err != nil {
			t.Fatalf("usage never matched the acknowledged objects %+v after duplicated requests: %v", want, err)
		}
		if dups == 0 {
			t.Fatal("no request was duplicated")
		}
		if lost > 0 {
			lossy++
		}
	})
	if lossy == 0 {
		t.Fatal("no check lost a response; the property never exercised a lost reply")
	}
}

func (h *harness) quotaAgreesWith(bucket string, want occ.Usage) bool {
	for _, n := range h.up() {
		q, err := n.Quota(bucket)
		if err != nil || q.UsedBytes != want.Bytes || q.UsedObjects != want.Objects {
			return false
		}
	}
	return true
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
