package main

import (
	"fmt"
	"github.com/nickstrad/object-store/internal/env"
	s "github.com/nickstrad/object-store/internal/storage"
	"math/rand"
	"reflect"
	"slices"
)

func members(sizes ...int) s.StaticMembership {
	var ms s.StaticMembership
	for reg, n := range sizes {
		for j := 0; j < n; j++ {
			ms = append(ms, s.Member{ID: env.NodeID(fmt.Sprintf("r%d-n%03d", reg, j)), Region: env.Region(fmt.Sprintf("r%d", reg))})
		}
	}
	return ms
}
func validPlacement(ms s.StaticMembership, ids []env.NodeID, n, cap int) {
	check(len(ids) == n, "placement short")
	counts := map[env.Region]int{}
	seen := map[env.NodeID]bool{}
	for _, id := range ids {
		check(!seen[id], "duplicate %s", id)
		seen[id] = true
		found := false
		for _, m := range ms {
			if m.ID == id {
				counts[m.Region]++
				found = true
				break
			}
		}
		check(found, "unknown %s", id)
	}
	for reg, c := range counts {
		check(c <= cap, "region %s holds %d cap=%d", reg, c, cap)
	}
}
func placement() {
	attack("C17", "region-cap-adversarial", func() string {
		r := rand.New(rand.NewSource(220211))
		for _, sizes := range [][]int{{7, 7, 6}, {1, 1, 10}, {1, 2, 3}, {100, 1, 1}, {3, 3}, {20}, {2, 2, 2, 2}, {1, 1, 1, 1, 1, 1}} {
			ms := members(sizes...)
			cap := 1
			for {
				total := 0
				for _, n := range sizes {
					total += min(n, cap)
				}
				if total >= 6 {
					break
				}
				cap++
			}
			cap = max(cap, 2)
			for i := 0; i < 500; i++ {
				key := fmt.Sprintf("seed220211/%d", i)
				ids, err := s.Place(ms, key, 6, 2)
				must(err)
				validPlacement(ms, ids, 6, cap)
				shuffled := slices.Clone(ms)
				r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
				again, err := s.Place(shuffled, key, 6, 2)
				must(err)
				check(reflect.DeepEqual(ids, again), "order matters")
			}
		}
		return "4000 placements across eight balanced/skewed region layouts meet independent feasible cap and order invariance"
	})
	attack("C18", "placement-load", func() string {
		ms := members(7, 7, 6)
		counts := map[env.NodeID]int{}
		for i := 0; i < 3000; i++ {
			ids, err := s.Place(ms, fmt.Sprintf("independent-seed-220211/%d", i), 6, 2)
			must(err)
			for _, id := range ids {
				counts[id]++
			}
		}
		lo, hi := 3000, 0
		for _, m := range ms {
			v := counts[m.ID]
			lo = min(lo, v)
			hi = max(hi, v)
			check(v >= 700 && v <= 1100, "node=%s count=%d outside published [700,1100]", m.ID, v)
		}
		return fmt.Sprintf("3000 stripes on 7/7/6 nodes: min=%d max=%d within published [700,1100]", lo, hi)
	})
	attack("C19", "replace-churn", func() string {
		ms := members(7, 7, 6)
		for trial := 0; trial < 1000; trial++ {
			key := fmt.Sprint(trial)
			ids, err := s.Place(ms, key, 6, 2)
			must(err)
			remove := ids[trial%6]
			left := slices.DeleteFunc(slices.Clone(ms), func(m s.Member) bool { return m.ID == remove })
			rep, err := s.Replace(left, key, ids, 2)
			must(err)
			validPlacement(left, rep, 6, 2)
			for i := range ids {
				check(ids[i] == rep[i] || ids[i] == remove, "moved live shard trial=%d slot=%d", trial, i)
			}
			added := append(slices.Clone(ms), s.Member{ID: "new", Region: "r0"})
			rep, err = s.Replace(added, key, ids, 2)
			must(err)
			check(reflect.DeepEqual(ids, rep), "addition moved holders")
			non := slices.DeleteFunc(slices.Clone(ms), func(m s.Member) bool { return !slices.Contains(ids, m.ID) && m.ID == ms[0].ID })
			rep, err = s.Replace(non, key, ids, 2)
			must(err)
			check(reflect.DeepEqual(ids, rep), "nonholder removal moved holders")
		}
		return "1000 holder removals replace exactly one slot; additions and nonholder changes retain every slot"
	})
	attack("C17,C19", "replace-cap-shrink", func() string {
		old := members(6, 6)
		all := members(6, 6, 6)
		for i := 0; i < 1000; i++ {
			ids, err := s.Place(old, fmt.Sprint(i), 6, 2)
			must(err)
			rep, err := s.Replace(all, fmt.Sprint(i), ids, 2)
			must(err)
			validPlacement(all, rep, 6, 2)
			changed := 0
			for j := range ids {
				if ids[j] != rep[j] {
					changed++
				}
			}
			check(changed == 2, "cap shrink changed %d instead of necessary two", changed)
		}
		return "1000 transitions from two to three regions move exactly two necessary shards"
	})
}

func extraPlacement() {
	attack("C17", "placement-invalid", func() string {
		for _, ms := range []s.StaticMembership{{}, {{ID: "a"}}, {{ID: "a"}, {ID: "a"}}, {{ID: "a"}, {ID: ""}}} {
			_, err := s.Place(ms, "x", 2, 1)
			check(err != nil, "invalid membership accepted %v", ms)
		}
		for _, nm := range [][2]int{{0, 0}, {-1, 0}, {2, -1}, {21, 2}} {
			_, err := s.Place(members(7, 7, 6), "x", nm[0], nm[1])
			check(err != nil, "invalid n,m=%v accepted", nm)
		}
		return "empty/short/duplicate/empty-ID memberships and invalid counts rejected; nil membership known review finding skipped"
	})
	attack("C19", "place-churn-sets", func() string {
		for _, sizes := range [][]int{{7, 7, 6}, {5, 5, 5, 5}, {10, 1, 1}} {
			ms := members(sizes...)
			for trial := 0; trial < 500; trial++ {
				key := fmt.Sprint(trial)
				before, err := s.Place(ms, key, 6, 2)
				must(err)
				removed := ms[trial%len(ms)].ID
				afterMS := slices.DeleteFunc(slices.Clone(ms), func(m s.Member) bool { return m.ID == removed })
				if s.RegionCap(ms, 6, 2) != s.RegionCap(afterMS, 6, 2) {
					continue
				}
				after, err := s.Place(afterMS, key, 6, 2)
				must(err)
				out, in := 0, 0
				for _, id := range before {
					if !slices.Contains(after, id) {
						out++
					}
				}
				for _, id := range after {
					if !slices.Contains(before, id) {
						in++
					}
				}
				want := 0
				if slices.Contains(before, removed) {
					want = 1
				}
				check(out == want && in == want, "sizes=%v trial=%d removed=%s moves=%d/%d", sizes, trial, removed, out, in)
			}
		}
		return "up to 1500 raw Place deletions across three region shapes move only the necessary holder set at unchanged cap"
	})
}
