package gossip_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/gossip"
)

var (
	propIDs     = []env.NodeID{"a", "b", "c", "d"}
	propRegions = []env.Region{"r0", "r1"}
	propStates  = []gossip.State{gossip.StateAlive, gossip.StateSuspect, gossip.StateDead, gossip.StateLeft}
)

func genUpdate(ids []env.NodeID, maxInc uint32) *rapid.Generator[gossip.Member] {
	return rapid.Custom(func(t *rapid.T) gossip.Member {
		return gossip.Member{
			ID:          rapid.SampledFrom(ids).Draw(t, "id"),
			Region:      rapid.SampledFrom(propRegions).Draw(t, "region"),
			Incarnation: rapid.Uint32Range(0, maxInc).Draw(t, "inc"),
			State:       rapid.SampledFrom(propStates).Draw(t, "state"),
		}
	})
}

func applyAll(v *gossip.View, ups []gossip.Member) {
	for _, u := range ups {
		v.Apply(u)
	}
}

func propViewMergeCommutative(t *rapid.T) {
	ups := rapid.SliceOfN(genUpdate(propIDs, 4), 0, 40).Draw(t, "updates")
	perm := rapid.Permutation(ups).Draw(t, "perm")
	split := rapid.IntRange(0, len(ups)).Draw(t, "split")

	in := gossip.NewView()
	applyAll(in, ups)
	other := gossip.NewView()
	applyAll(other, perm)
	applyAll(other, perm[:split])
	if !slices.Equal(in.All(), other.All()) {
		t.Fatalf("order changed the view:\n %+v\n %+v", in.All(), other.All())
	}

	left, right := gossip.NewView(), gossip.NewView()
	applyAll(left, ups[:split])
	applyAll(right, ups[split:])
	lr, rl := gossip.NewView(), gossip.NewView()
	applyAll(lr, left.All())
	applyAll(lr, right.All())
	applyAll(rl, right.All())
	applyAll(rl, left.All())
	if !slices.Equal(lr.All(), in.All()) || !slices.Equal(rl.All(), in.All()) {
		t.Fatalf("merging partial views disagrees with applying every update:\n lr %+v\n rl %+v\n in %+v", lr.All(), rl.All(), in.All())
	}

	for _, id := range propIDs {
		var best gossip.Member
		found := false
		for _, u := range ups {
			if u.ID == id && (!found || gossip.Supersedes(u, best)) {
				best, found = u, true
			}
		}
		got, ok := in.Get(id)
		if ok != found || (found && got != best) {
			t.Fatalf("member %s: view holds %+v (%v), the greatest update is %+v (%v)", id, got, ok, best, found)
		}
	}
}

func TestPropViewMergeIsCommutative(t *testing.T) {
	rapid.Check(t, propViewMergeCommutative)
}

func viewMergeSeeds() [][]byte {
	return [][]byte{
		{},
		{0, 0, 0, 0},
		{255, 255, 255, 255, 255, 255, 255, 255},
	}
}

func FuzzViewMergeLaws(f *testing.F) {
	for _, b := range viewMergeSeeds() {
		f.Add(b)
	}
	f.Fuzz(rapid.MakeFuzz(propViewMergeCommutative))
}

func TestFuzzSeedsRunWithoutSkipping(t *testing.T) {
	prop := rapid.MakeFuzz(propViewMergeCommutative)
	for i, b := range viewMergeSeeds() {
		skipped := false
		t.Run(fmt.Sprintf("FuzzViewMergeLaws seed %d of %d bytes", i, len(b)), func(t *testing.T) {
			defer func() { skipped = t.Skipped() }()
			prop(t, b)
		})
		if skipped {
			t.Errorf("seed %d (%d bytes) of FuzzViewMergeLaws skips under go test", i, len(b))
		}
	}
}

func propIncarnationMonotonic(t *rapid.T) {
	h := newHarness(t, "self", gossip.Config{DeadRetention: time.Hour})
	ids := []env.NodeID{"self", "a", "b", "c"}
	h.node.Start()
	lastSelf := h.node.Self().Incarnation
	last := map[env.NodeID]uint32{}
	steps := rapid.IntRange(1, 60).Draw(t, "steps")
	for i := 0; i < steps; i++ {
		switch rapid.IntRange(0, 4).Draw(t, "action") {
		case 0:
			ups := rapid.SliceOfN(genUpdate(ids, 6), 1, 5).Draw(t, "gossip")
			h.deliver(rapid.SampledFrom(ids[1:]).Draw(t, "from"), gossipMsg(ups...))
		case 1:
			h.clock.advance(time.Duration(rapid.Int64Range(0, int64(3*time.Second)).Draw(t, "advance")))
		case 2:
			kind := rapid.SampledFrom([]gossip.Kind{gossip.KindPing, gossip.KindAck, gossip.KindPingReq}).Draw(t, "kind")
			m := gossip.Message{Kind: kind, Seq: rapid.Uint32Range(0, 8).Draw(t, "seq")}
			if kind == gossip.KindPingReq {
				m.Target = rapid.SampledFrom(ids).Draw(t, "target")
			}
			m.Updates = rapid.SliceOfN(genUpdate(ids, 6), 0, 3).Draw(t, "piggyback")
			h.deliver(rapid.SampledFrom(ids[1:]).Draw(t, "from"), m)
		case 3:
			ups := rapid.SliceOfN(genUpdate(ids, 6), 0, 5).Draw(t, "sync")
			req, err := gossip.Message{Kind: gossip.KindSyncReq, Updates: ups}.Encode()
			if err != nil {
				t.Fatal(err)
			}
			h.node.OnRequest("a", req, func([]byte, error) {})
		case 4:
			for _, r := range h.net.takeRequests() {
				r.cb(nil, env.ErrTimeout)
			}
		}
		self := h.node.Self()
		if self.State != gossip.StateAlive {
			t.Fatalf("step %d: the node declared itself %s", i, self.State)
		}
		if self.Incarnation < lastSelf {
			t.Fatalf("step %d: own incarnation went from %d to %d", i, lastSelf, self.Incarnation)
		}
		lastSelf = self.Incarnation
		for _, m := range h.node.All() {
			if m.Incarnation < last[m.ID] {
				t.Fatalf("step %d: incarnation of %s went from %d to %d", i, m.ID, last[m.ID], m.Incarnation)
			}
			last[m.ID] = m.Incarnation
		}
		all := h.node.All()
		if !slices.IsSortedFunc(all, func(a, b gossip.Member) int { return compareID(a.ID, b.ID) }) {
			t.Fatalf("step %d: view not sorted: %+v", i, all)
		}
	}
}

func compareID(a, b env.NodeID) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func TestPropIncarnationMonotonic(t *testing.T) {
	rapid.Check(t, propIncarnationMonotonic)
	rapid.Check(t, func(t *rapid.T) {
		ups := rapid.SliceOfN(genUpdate(propIDs, 6), 0, 40).Draw(t, "updates")
		v := gossip.NewView()
		last := map[env.NodeID]uint32{}
		for _, u := range ups {
			v.Apply(u)
			for _, m := range v.All() {
				if m.Incarnation < last[m.ID] {
					t.Fatalf("view incarnation of %s went from %d to %d", m.ID, last[m.ID], m.Incarnation)
				}
				last[m.ID] = m.Incarnation
			}
		}
	})
}
