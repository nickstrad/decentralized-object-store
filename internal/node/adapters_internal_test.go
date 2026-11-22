package node

import (
	"slices"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/gossip"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

// TestGossipViewAdaptsMembershipForEveryConsumer proves gossipView and
// regionView expose live members, non-self peers, region filtering, and drop
// crashed members once gossip declares them dead.
func TestGossipViewAdaptsMembershipForEveryConsumer(t *testing.T) {
	table, err := CodeTable()
	if err != nil {
		t.Fatal(err)
	}
	s := sim.New(sim.Config{Seed: 5, Codes: table})
	ids := []env.NodeID{"a", "b", "c", "d"}
	views := map[env.NodeID]*gossipView{}
	for i, id := range ids {
		r := env.Region([]string{"r0", "r1", "r0", "r2"}[i])
		err := s.AddNode(sim.NodeSpec{ID: id, Region: r}, func(e env.Env) env.Process {
			g, err := gossip.New(e, gossip.Config{Seeds: []env.NodeID{"a"}})
			if err != nil {
				t.Fatal(err)
			}
			views[id] = &gossipView{g: g, self: id}
			return g
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RunFor(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	v := views["c"]
	var got []env.NodeID
	for _, m := range v.Members() {
		got = append(got, m.ID)
	}
	if !slices.Equal(got, ids) {
		t.Fatalf("storage members %v, want %v", got, ids)
	}
	if p := v.Peers(); slices.Contains(p, "c") || len(p) != 3 {
		t.Fatalf("peers %v must be the three others", p)
	}
	if r := v.InRegion("r0"); !slices.Equal(r, []env.NodeID{"a", "c"}) {
		t.Fatalf("region r0 holds %v", r)
	}
	rv := regionView{v: v, region: "r2"}
	if ms := rv.Members(); len(ms) != 1 || ms[0].ID != "d" || ms[0].Region != "r2" {
		t.Fatalf("region view %v", ms)
	}
	if err := s.Crash("d"); err != nil {
		t.Fatal(err)
	}
	if err := s.RunFor(40 * time.Second); err != nil {
		t.Fatal(err)
	}
	if ms := rv.Members(); len(ms) != 0 {
		t.Fatalf("a dead member stays placeable: %v", ms)
	}
}

// TestChannelPrefixRoundTrips proves withChannel prepends the exact channel byte.
func TestChannelPrefixRoundTrips(t *testing.T) {
	b := withChannel(chOCC, []byte{9, 8})
	if !slices.Equal(b, []byte{chOCC, 9, 8}) {
		t.Fatalf("got %v", b)
	}
}
