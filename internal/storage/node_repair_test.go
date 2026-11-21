package storage_test

import (
	"bytes"
	"errors"
	"runtime"
	"testing"
	"time"
	"weak"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

type shrinkingMembership struct {
	full  storage.StaticMembership
	after int
	calls int
}

func (m *shrinkingMembership) Members() []storage.Member {
	m.calls++
	if m.calls > m.after {
		return m.full[:2].Members()
	}
	return m.full.Members()
}

func TestNodePutPlacementFailureNeverCallsBackInsideTheCall(t *testing.T) {
	for _, window := range []int{1, 4} {
		for _, after := range []int{0, 1, 2} {
			codes, err := storage.Codes()
			mustNil(t, err)
			s := sim.New(sim.Config{Seed: 9, Codes: codes})
			ids := sim.NodeIDs(6)
			ms := &shrinkingMembership{full: clusterMembers(ids, 3), after: after}
			cfg := storage.Config{Layout: smallLayout(), Members: ms, Window: window}
			mustNil(t, s.AddCluster(ids, 3, nodeFactory(cfg)))
			mustNil(t, s.RunUntilQuiet())
			p := put(t, s, nodeOf(t, s, ids[0]), "b", "k", bytes.Repeat([]byte("x"), 64*4))
			if !errors.Is(p.err, storage.ErrUnavailable) {
				t.Fatalf("window %d, placement failing from stripe %d: %v", window, after, p.err)
			}
			if ms.calls != after+1 {
				t.Fatalf("window %d, placement failing from stripe %d: %d placements, want %d (no stripe launched after the failure)", window, after, ms.calls, after+1)
			}
		}
	}
}

func TestNodeCallbacksNeverRunInsideTheCall(t *testing.T) {
	s, ids, _ := newCluster(t, 12, 6, 3, smallLayout())
	n := nodeOf(t, s, ids[0])
	bad := sampleManifest(17)
	bad.Stripes = nil
	other := sampleManifest(17)
	other.Layout.K = 300
	for name, call := range map[string]func(func()){
		"put to a bad bucket": func(cb func()) { n.Put("Bad", "k", []byte("x"), func(*storage.Manifest, error) { cb() }) },
		"put of an empty key": func(cb func()) { n.Put("b", "", []byte("x"), func(*storage.Manifest, error) { cb() }) },
		"put of 0 bytes":      func(cb func()) { n.Put("b", "k", nil, func(*storage.Manifest, error) { cb() }) },
		"put of 1 byte":       func(cb func()) { n.Put("b", "k", []byte("x"), func(*storage.Manifest, error) { cb() }) },
		"get of nil":          func(cb func()) { n.Get(nil, func(storage.GetResult, error) { cb() }) },
		"get of invalid":      func(cb func()) { n.Get(bad, func(storage.GetResult, error) { cb() }) },
		"get of a bad layout": func(cb func()) { n.Get(other, func(storage.GetResult, error) { cb() }) },
		"get of a good one":   func(cb func()) { n.Get(sampleManifest(0), func(storage.GetResult, error) { cb() }) },
	} {
		returned, called := false, 0
		call(func() {
			called++
			if !returned {
				t.Errorf("%s: callback ran inside the call", name)
			}
		})
		returned = true
		mustNil(t, s.RunUntilQuiet())
		if called != 1 {
			t.Errorf("%s: callback ran %d times", name, called)
		}
	}
}

func TestNodePutWindowBoundsStripesAwaitingQuorum(t *testing.T) {
	const stripeSize = 4
	for _, stripes := range []int{2, 8, 64} {
		codes, err := storage.Codes()
		mustNil(t, err)
		lat := sim.Range{Min: time.Microsecond, Max: time.Microsecond}
		s := sim.New(sim.Config{Seed: 220211, Codes: codes, Net: sim.NetConfig{Latency: lat, InterRegionLatency: lat}})
		ids := sim.NodeIDs(6)
		cfg := storage.Config{Layout: storage.Layout{K: 4, M: 2, StripeSize: stripeSize}, Members: clusterMembers(ids, 3), Window: 1}
		mustNil(t, s.AddCluster(ids, 3, nodeFactory(cfg)))
		mustNil(t, s.RunUntilQuiet())
		mustNil(t, s.Pause(ids[5]))
		n := nodeOf(t, s, ids[0])
		data := bytes.Repeat([]byte{7}, stripes*stripeSize-3)
		done := false
		var ackedAt env.Time
		var st storage.NodeStats
		var man *storage.Manifest
		n.Put("b", "k", data, func(m *storage.Manifest, err error) {
			mustNil(t, err)
			done, ackedAt, st, man = true, s.Now(), n.Stats(), m
		})
		mustNil(t, s.RunFor(storage.DefaultRequestTimeout/2))
		if !done {
			t.Fatalf("%d stripes: the put did not ack with one paused holder", stripes)
		}
		if st.PutAwaitingPeak != 1 {
			t.Fatalf("%d stripes: %d stripes were awaiting quorum at once, Window is 1", stripes, st.PutAwaitingPeak)
		}
		if st.PutOutstanding != uint64(stripes) {
			t.Fatalf("%d stripes: %d put requests outstanding at the ack, want one per stripe to the paused holder", stripes, st.PutOutstanding)
		}
		want := uint64(0)
		for i := range man.Stripes {
			shard := (man.Layout.StripeLen(man.Size, i) + 3) / 4
			for idx, ref := range man.Stripes[i].Shards {
				if ref.Node == ids[5] {
					want += uint64(len(storage.EncodeShardRequest(storage.OpPutShard, man.ShardID(i, idx), make([]byte, storage.HeaderSize+shard))))
				}
			}
		}
		if st.PutOutstandingBytes != want {
			t.Fatalf("%d stripes: %d outstanding request bytes, want %d (one shard request per stripe)", stripes, st.PutOutstandingBytes, want)
		}
		mustNil(t, s.RunUntil(ackedAt.Add(storage.DefaultRequestTimeout+time.Millisecond)))
		after := n.Stats()
		if after.PutOutstanding != 0 || after.PutOutstandingBytes != 0 {
			t.Fatalf("%d stripes: %d requests (%d bytes) still outstanding one request timeout after the ack", stripes, after.PutOutstanding, after.PutOutstandingBytes)
		}
		mustNil(t, s.Resume(ids[5]))
		mustNil(t, s.RunUntilQuiet())
	}
}

func TestNodeGetUsesTheManifestLayout(t *testing.T) {
	codes, err := storage.Codes()
	mustNil(t, err)
	s := sim.New(sim.Config{Seed: 13, Codes: codes})
	ids := sim.NodeIDs(7)
	ms := clusterMembers(ids, 3)
	wide := storage.Config{Layout: storage.Layout{K: 4, M: 2, StripeSize: 64}, Members: ms}
	narrow := storage.Config{Layout: storage.Layout{K: 2, M: 1, StripeSize: 16}, Members: ms}
	for i, id := range ids {
		cfg := wide
		if i == 6 {
			cfg = narrow
		}
		mustNil(t, s.AddNode(sim.NodeSpec{ID: id, Region: ms[i].Region}, nodeFactory(cfg)))
	}
	mustNil(t, s.RunUntilQuiet())
	data := bytes.Repeat([]byte("per-object layout "), 20)
	p := put(t, s, nodeOf(t, s, ids[0]), "b", "wide", data)
	mustNil(t, p.err)
	g := get(t, s, nodeOf(t, s, ids[6]), p.man)
	if g.err != nil || !bytes.Equal(g.res.Data, data) {
		t.Fatalf("a 2+1 node reading a 4+2 object: %v", g.err)
	}
	q := put(t, s, nodeOf(t, s, ids[6]), "b", "narrow", data)
	mustNil(t, q.err)
	if q.man.Layout != narrow.Layout {
		t.Fatalf("the narrow node wrote layout %+v", q.man.Layout)
	}
	g = get(t, s, nodeOf(t, s, ids[1]), q.man)
	if g.err != nil || !bytes.Equal(g.res.Data, data) {
		t.Fatalf("a 4+2 node reading a 2+1 object: %v", g.err)
	}
}

func TestNodePutReleasesTheObjectAtTheAck(t *testing.T) {
	codes, err := storage.Codes()
	mustNil(t, err)
	lat := sim.Range{Min: time.Microsecond, Max: time.Microsecond}
	s := sim.New(sim.Config{Seed: 220211, Codes: codes, Net: sim.NetConfig{Latency: lat, InterRegionLatency: lat}})
	ids := sim.NodeIDs(6)
	cfg := storage.Config{Layout: storage.Layout{K: 4, M: 2, StripeSize: 1 << 10}, Members: clusterMembers(ids, 3), Window: 1}
	mustNil(t, s.AddCluster(ids, 3, nodeFactory(cfg)))
	mustNil(t, s.RunUntilQuiet())
	mustNil(t, s.Pause(ids[5]))
	n := nodeOf(t, s, ids[0])
	data := bytes.Repeat([]byte{9}, 16<<10)
	held := weak.Make(&data[0])
	done := false
	n.Put("b", "k", data, func(_ *storage.Manifest, err error) {
		mustNil(t, err)
		done = true
	})
	data = nil
	mustNil(t, s.RunFor(storage.DefaultRequestTimeout/2))
	if !done {
		t.Fatal("the put did not ack with one paused holder")
	}
	if n.Stats().PutOutstanding == 0 {
		t.Fatal("no request is outstanding after the ack; the test needs late requests")
	}
	runtime.GC()
	if held.Value() != nil {
		t.Fatalf("the object buffer is still reachable after the ack with %d late requests outstanding", n.Stats().PutOutstanding)
	}
	mustNil(t, s.Resume(ids[5]))
	mustNil(t, s.RunUntilQuiet())
}
