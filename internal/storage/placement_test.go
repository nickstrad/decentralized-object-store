package storage_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
	"pgregory.net/rapid"
)

func members(perRegion ...int) storage.StaticMembership {
	var out storage.StaticMembership
	n := 0
	for r, c := range perRegion {
		for i := 0; i < c; i++ {
			out = append(out, storage.Member{ID: env.NodeID(fmt.Sprintf("n%03d", n)), Region: env.Region(fmt.Sprintf("r%d", r))})
			n++
		}
	}
	return out
}

func regionCounts(ms storage.StaticMembership, placed []env.NodeID) map[env.Region]int {
	region := map[env.NodeID]env.Region{}
	for _, m := range ms {
		region[m.ID] = m.Region
	}
	out := map[env.Region]int{}
	for _, id := range placed {
		out[region[id]]++
	}
	return out
}

func TestPlacementTable(t *testing.T) {
	cases := []struct {
		name      string
		ms        storage.StaticMembership
		n, m      int
		maxRegion int
		err       error
	}{
		{"default 4+2 over 3 regions of 7 puts two shards in each region", members(7, 7, 6), 6, 2, 2, nil},
		{"4+2 over 6 regions still caps at m", members(3, 3, 3, 3, 3, 3), 6, 2, 2, nil},
		{"4+2 over 2 regions cannot honour m and spreads evenly", members(5, 5), 6, 2, 3, nil},
		{"4+2 over 1 region puts all six there", members(10), 6, 2, 6, nil},
		{"a small region forces the others to take more", members(1, 1, 10), 6, 2, 4, nil},
		{"exactly n members uses every member", members(2, 2, 2), 6, 2, 2, nil},
		{"m=0 spreads one per region when it can", members(3, 3, 3), 3, 0, 1, nil},
		{"fewer members than shards is ErrUnavailable", members(2, 2, 1), 6, 2, 0, storage.ErrUnavailable},
		{"n=0 is ErrInvalid", members(3), 0, 0, 0, storage.ErrInvalid},
		{"negative m is ErrInvalid", members(3), 2, -1, 0, storage.ErrInvalid},
		{"duplicate member ids are ErrInvalid", append(members(3), storage.Member{ID: "n000", Region: "r1"}), 2, 1, 0, storage.ErrInvalid},
		{"an empty member id is ErrInvalid", append(members(3), storage.Member{ID: "", Region: "r1"}), 2, 1, 0, storage.ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for stripe := uint32(0); stripe < 50; stripe++ {
				key := storage.StripeKey("b", 7, stripe)
				got, err := storage.Place(c.ms, key, c.n, c.m)
				if c.err != nil {
					if !errors.Is(err, c.err) {
						t.Fatalf("Place error %v, want %v", err, c.err)
					}
					return
				}
				mustNil(t, err)
				if len(got) != c.n {
					t.Fatalf("placed %d shards, want %d", len(got), c.n)
				}
				seen := map[env.NodeID]bool{}
				for _, id := range got {
					if seen[id] {
						t.Fatalf("node %s holds two shards of one stripe: %v", id, got)
					}
					seen[id] = true
				}
				worst := 0
				for _, v := range regionCounts(c.ms, got) {
					worst = max(worst, v)
				}
				if worst > c.maxRegion {
					t.Fatalf("a region holds %d shards, cap %d: %v", worst, c.maxRegion, got)
				}
				if got2, _ := storage.Place(c.ms, key, c.n, c.m); !slices.Equal(got, got2) {
					t.Fatal("Place is not deterministic")
				}
			}
		})
	}
}

func TestPlacementSpreadsLoad(t *testing.T) {
	ms := members(7, 7, 6)
	load := map[env.NodeID]int{}
	const stripes = 3000
	for s := uint32(0); s < stripes; s++ {
		got, err := storage.Place(ms, storage.StripeKey("b", 1, s), 6, 2)
		mustNil(t, err)
		for _, id := range got {
			load[id]++
		}
	}
	const minLoad, maxLoad = 700, 1100
	for _, m := range ms {
		if load[m.ID] < minLoad || load[m.ID] > maxLoad {
			t.Fatalf("node %s holds %d of %d shards, want within [%d, %d]", m.ID, load[m.ID], stripes*6, minLoad, maxLoad)
		}
	}
}

func TestPlacementIgnoresMemberOrder(t *testing.T) {
	ms := members(4, 4, 4)
	rev := slices.Clone(ms)
	slices.Reverse(rev)
	for s := uint32(0); s < 20; s++ {
		key := storage.StripeKey("b", 3, s)
		a, err := storage.Place(ms, key, 6, 2)
		mustNil(t, err)
		b, err := storage.Place(rev, key, 6, 2)
		mustNil(t, err)
		if !slices.Equal(a, b) {
			t.Fatalf("member order changed placement: %v vs %v", a, b)
		}
	}
}

func TestRegionCap(t *testing.T) {
	cases := []struct {
		ms   storage.StaticMembership
		n, m int
		want int
	}{
		{members(7, 7, 6), 6, 2, 2},
		{members(5, 5), 6, 2, 3},
		{members(1, 1, 10), 6, 2, 4},
		{members(10), 6, 2, 6},
		{members(3, 3, 3), 3, 0, 1},
		{members(3, 3, 3), 6, 4, 4},
		{members(2), 6, 2, 0},
	}
	for _, c := range cases {
		if got := storage.RegionCap(c.ms, c.n, c.m); got != c.want {
			t.Fatalf("RegionCap(%v, %d, %d) = %d, want %d", c.ms, c.n, c.m, got, c.want)
		}
	}
}

func TestReplaceKeepsLiveShards(t *testing.T) {
	ms := members(7, 7, 6)
	key := storage.StripeKey("b", 5, 0)
	cur, err := storage.Place(ms, key, 6, 2)
	mustNil(t, err)
	gone := cur[3]
	var after storage.StaticMembership
	for _, m := range ms {
		if m.ID != gone {
			after = append(after, m)
		}
	}
	next, err := storage.Replace(after, key, cur, 2)
	mustNil(t, err)
	for i := range cur {
		if i == 3 {
			if next[i] == gone || slices.Contains(cur, next[i]) {
				t.Fatalf("slot 3 moved to %s", next[i])
			}
			continue
		}
		if next[i] != cur[i] {
			t.Fatalf("slot %d moved from %s to %s although its node is still a member", i, cur[i], next[i])
		}
	}
	for _, v := range regionCounts(after, next) {
		if v > 2 {
			t.Fatalf("Replace broke the region cap: %v", next)
		}
	}
	if _, err := storage.Replace(members(2, 2), key, cur, 2); !errors.Is(err, storage.ErrUnavailable) {
		t.Fatalf("Replace onto too few members: %v", err)
	}
	if _, err := storage.Replace(ms, key, nil, 2); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("Replace of an empty placement: %v", err)
	}
	dup := slices.Clone(cur)
	dup[1] = dup[0]
	fixed, err := storage.Replace(ms, key, dup, 2)
	mustNil(t, err)
	if fixed[0] != cur[0] || fixed[1] == cur[0] {
		t.Fatalf("Replace kept a duplicate: %v", fixed)
	}
}

func drawMembership(t *rapid.T, label string) storage.StaticMembership {
	regions := rapid.IntRange(1, 5).Draw(t, label+"Regions")
	counts := make([]int, regions)
	for i := range counts {
		counts[i] = rapid.IntRange(1, 8).Draw(t, fmt.Sprintf("%sRegion%d", label, i))
	}
	return members(counts...)
}

func TestPropPlacementStable(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ms := drawMembership(t, "base")
		k := rapid.IntRange(1, 6).Draw(t, "k")
		m := rapid.IntRange(0, 3).Draw(t, "m")
		n := k + m
		if len(ms) < n+1 {
			t.Skip("too few members")
		}
		key := storage.StripeKey("b", storage.VersionID(rapid.Uint64().Draw(t, "version")), rapid.Uint32().Draw(t, "stripe"))
		before, err := storage.Place(ms, key, n, m)
		if err != nil {
			t.Fatal(err)
		}
		var after storage.StaticMembership
		var changed env.NodeID
		removed := rapid.Bool().Draw(t, "remove")
		if removed {
			i := rapid.IntRange(0, len(ms)-1).Draw(t, "victim")
			changed = ms[i].ID
			after = append(slices.Clone(ms[:i]), ms[i+1:]...)
		} else {
			changed = "new-member"
			region := env.Region(fmt.Sprintf("r%d", rapid.IntRange(0, 5).Draw(t, "newRegion")))
			after = append(slices.Clone(ms), storage.Member{ID: changed, Region: region})
		}
		if storage.RegionCap(ms, n, m) != storage.RegionCap(after, n, m) {
			t.Skip("the change moved the region cap")
		}
		got, err := storage.Place(after, key, n, m)
		if err != nil {
			t.Fatal(err)
		}
		heldBefore := slices.Contains(before, changed)
		heldAfter := slices.Contains(got, changed)
		if removed && !heldBefore || !removed && !heldAfter {
			if !slices.Equal(before, got) {
				t.Fatalf("a change to %s, which holds no shard, moved shards: %v -> %v", changed, before, got)
			}
			return
		}
		out, in := diff(before, got), diff(got, before)
		if len(out) != 1 || len(in) != 1 {
			t.Fatalf("a change to %s moved %d shards out and %d in: %v -> %v", changed, len(out), len(in), before, got)
		}
		if removed && out[0] != changed || !removed && in[0] != changed {
			t.Fatalf("the moved shard is not the changed member's: out %v in %v", out, in)
		}
		next, err := storage.Replace(after, key, before, m)
		if err != nil {
			t.Fatal(err)
		}
		moved := 0
		for i := range before {
			if next[i] != before[i] {
				moved++
			}
		}
		if removed && moved != 1 || !removed && moved != 0 {
			t.Fatalf("Replace moved %d shards for a change to %s", moved, changed)
		}
	})
}

func diff(a, b []env.NodeID) []env.NodeID {
	var out []env.NodeID
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}
