package merkle_test

import (
	"errors"
	"testing"

	"github.com/nickstrad/object-store/internal/merkle"
	"pgregory.net/rapid"
)

// TestPropDiffComplete proves that Compare finds the exact set of differing and
// one-sided keys with both hashes, zero missed and zero spurious, while
// matching the exact hash and round-trip shape.
func TestPropDiffComplete(t *testing.T) {
	rapid.Check(t, propDiffComplete)
}

// TestPropDiffCost proves that every diff exchanges at most
// 1 + 16*d*Depth + d*LeafLoad(remote) hashes and matches the exact honest
// round-trip shape.
func TestPropDiffCost(t *testing.T) {
	rapid.Check(t, propDiffCost)
}

// TestPropRootIffEqual proves that two trees of the same depth have equal roots
// if and only if they contain the exact same key-to-hash map.
func TestPropRootIffEqual(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := genPair(t)
		a := treeOf(t, p.depth, p.local)
		b := treeOf(t, p.depth, p.remote)
		equal := len(refDiff(p.local, p.remote)) == 0
		if (a.Root() == b.Root()) != equal {
			t.Fatalf("roots equal=%v, maps equal=%v", a.Root() == b.Root(), equal)
		}
		if a.Root() != refRoot(p.depth, p.local) {
			t.Fatal("root differs from the reference")
		}
	})
}

// TestPropIncrementalEqualsRebuild proves via a stateful model that arbitrary
// sequences of Set, Delete, and Snapshot always match Build from scratch in
// root, length, and LeafLoad while keeping earlier snapshots immutable.
func TestPropIncrementalEqualsRebuild(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		depth := rapid.IntRange(merkle.MinDepth, 4).Draw(t, "depth")
		tr := treeOf(t, depth, nil)
		model := map[string]merkle.Hash{}
		snaps := []*merkle.Tree{}
		roots := []merkle.Hash{}
		t.Repeat(map[string]func(*rapid.T){
			"set": func(t *rapid.T) {
				k, h := genKey(t, "key"), genHash(t, "hash")
				if err := tr.Set(k, h); err != nil {
					t.Fatal(err)
				}
				model[k] = h
			},
			"delete": func(t *rapid.T) {
				k := genKey(t, "key")
				_, had := model[k]
				if got := tr.Delete(k); got != had {
					t.Fatalf("Delete(%q) = %v, model has it: %v", k, got, had)
				}
				delete(model, k)
			},
			"snapshot": func(t *rapid.T) {
				snaps = append(snaps, tr.Snapshot())
				roots = append(roots, tr.Root())
			},
			"": func(t *rapid.T) {
				b, err := merkle.Build(depth, entriesOf(model))
				if err != nil {
					t.Fatal(err)
				}
				if tr.Root() != b.Root() || tr.Len() != len(model) || b.Len() != len(model) {
					t.Fatalf("incremental root %s len %d, rebuild root %s len %d, model %d", tr.Root(), tr.Len(), b.Root(), b.Len(), len(model))
				}
				if tr.LeafLoad() != b.LeafLoad() {
					t.Fatalf("leaf load %d vs %d", tr.LeafLoad(), b.LeafLoad())
				}
				for i, s := range snaps {
					if s.Root() != roots[i] {
						t.Fatalf("snapshot %d changed", i)
					}
				}
			},
		})
	})
}

// TestPropOrderIndependent proves that inserting or building the same entries
// in any permutation produces the same root hash.
func TestPropOrderIndependent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := genPair(t)
		entries := entriesOf(p.local)
		perm := rapid.Permutation(entries).Draw(t, "perm")
		a := treeOf(t, p.depth, nil)
		for _, e := range perm {
			if err := a.Set(e.Key, e.Hash); err != nil {
				t.Fatal(err)
			}
		}
		b, err := merkle.Build(p.depth, perm)
		if err != nil {
			t.Fatal(err)
		}
		c := treeOf(t, p.depth, p.local)
		if a.Root() != c.Root() || b.Root() != c.Root() {
			t.Fatalf("insertion order changed the root: %s %s %s", a.Root(), b.Root(), c.Root())
		}
	})
}

// TestPropHostileRepliesEndTheSession proves that random bytes, answers from a
// different tree at every step, or forged small cut replies always terminate
// the Session within Depth + 5*Frontier round trips with nil, ErrMalformed, or
// ErrDepth and at most Frontier*MaxLeafEntries differences.
func TestPropHostileRepliesEndTheSession(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := genPair(t)
		local := treeOf(t, p.depth, p.local)
		frontier := rapid.IntRange(1, 64).Draw(t, "frontier")
		s := local.Diff(merkle.Limits{Frontier: frontier})
		bound := hostileTripBound(local.Depth(), frontier)
		trips := 0
		for {
			req, ok := s.Request()
			if !ok {
				break
			}
			trips++
			if trips > bound {
				t.Fatalf("session made %d round trips, bound %d", trips, bound)
			}
			var resp []byte
			switch rapid.IntRange(0, 3).Draw(t, "liar") {
			case 0:
				resp = rapid.SliceOfN(rapid.Byte(), 0, 64).Draw(t, "bytes")
			case 1:
				other := genPair(t)
				lie, err := merkle.Build(p.depth, entriesOf(other.remote))
				if err != nil {
					t.Fatal(err)
				}
				resp, err = lie.Answer(req)
				if err != nil {
					t.Fatal(err)
				}
				if resp[0] == 0x03 {
					resp[0] = 0x05
				}
			default:
				other := genPair(t)
				lie, err := merkle.Build(p.depth, entriesOf(other.remote))
				if err != nil {
					t.Fatal(err)
				}
				resp, err = lie.Answer(req)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Feed(resp); err != nil {
				if !errors.Is(err, merkle.ErrMalformed) && !errors.Is(err, merkle.ErrDepth) {
					t.Fatalf("Feed error %v is neither ErrMalformed nor ErrDepth", err)
				}
				break
			}
		}
		if !s.Done() {
			t.Fatal("session not done")
		}
		res := s.Result()
		if res.Cost.RoundTrips != trips {
			t.Fatalf("cost says %d round trips, ran %d", res.Cost.RoundTrips, trips)
		}
		if len(res.Diffs) > frontier*merkle.MaxLeafEntries {
			t.Fatalf("%d differences from a frontier of %d", len(res.Diffs), frontier)
		}
	})
}

// FuzzMerkleDiff drives the completeness and exact-cost property through Go's
// coverage-guided fuzzer.
func FuzzMerkleDiff(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{255, 255, 255, 255, 0, 0, 0, 0, 1})
	f.Fuzz(rapid.MakeFuzz(propDiffComplete))
}
