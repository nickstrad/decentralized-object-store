package merkle_test

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/merkle"
	"pgregory.net/rapid"
)

func sum(s string) merkle.Hash { return merkle.Sum([]byte(s)) }

func entriesOf(m map[string]merkle.Hash) []merkle.Entry {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]merkle.Entry, 0, len(keys))
	for _, k := range keys {
		out = append(out, merkle.Entry{Key: k, Hash: m[k]})
	}
	return out
}

func treeOf(t testing.TB, depth int, m map[string]merkle.Hash) *merkle.Tree {
	t.Helper()
	tr, err := merkle.New(depth)
	if err != nil {
		t.Fatalf("New(%d): %v", depth, err)
	}
	for _, e := range entriesOf(m) {
		if err := tr.Set(e.Key, e.Hash); err != nil {
			t.Fatalf("Set(%q): %v", e.Key, err)
		}
	}
	return tr
}

func keyPath(key string) [32]byte { return sha256.Sum256([]byte(key)) }

func nibbleOf(p [32]byte, i int) int {
	b := p[i/2]
	if i%2 == 0 {
		return int(b >> 4)
	}
	return int(b & 0x0f)
}

type refItem struct {
	key  string
	hash merkle.Hash
	path [32]byte
}

func refRoot(depth int, m map[string]merkle.Hash) merkle.Hash {
	var items []refItem
	for _, e := range entriesOf(m) {
		items = append(items, refItem{key: e.Key, hash: e.Hash, path: keyPath(e.Key)})
	}
	return refNode(0, depth, items)
}

func refNode(level, depth int, items []refItem) merkle.Hash {
	if len(items) == 0 {
		return merkle.Hash{}
	}
	if level == depth {
		slices.SortFunc(items, func(a, b refItem) int { return strings.Compare(a.key, b.key) })
		buf := []byte{0x00}
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(items)))
		for _, it := range items {
			buf = binary.BigEndian.AppendUint32(buf, uint32(len(it.key)))
			buf = append(buf, it.key...)
			buf = append(buf, it.hash[:]...)
		}
		return sha256.Sum256(buf)
	}
	buf := []byte{0x01, byte(level)}
	for i := 0; i < 16; i++ {
		var sub []refItem
		for _, it := range items {
			if nibbleOf(it.path, level) == i {
				sub = append(sub, it)
			}
		}
		h := refNode(level+1, depth, sub)
		buf = append(buf, h[:]...)
	}
	return sha256.Sum256(buf)
}

func refDiff(local, remote map[string]merkle.Hash) []merkle.Difference {
	var out []merkle.Difference
	for _, e := range entriesOf(local) {
		r, ok := remote[e.Key]
		if !ok {
			out = append(out, merkle.Difference{Key: e.Key, Local: e.Hash, HasLocal: true})
		} else if r != e.Hash {
			out = append(out, merkle.Difference{Key: e.Key, Local: e.Hash, Remote: r, HasLocal: true, HasRemote: true})
		}
	}
	for _, e := range entriesOf(remote) {
		if _, ok := local[e.Key]; !ok {
			out = append(out, merkle.Difference{Key: e.Key, Remote: e.Hash, HasRemote: true})
		}
	}
	slices.SortFunc(out, func(a, b merkle.Difference) int { return strings.Compare(a.Key, b.Key) })
	return out
}

func checkComplete(local, remote map[string]merkle.Hash, res merkle.Result) error {
	want := refDiff(local, remote)
	if res.Truncated {
		return fmt.Errorf("diff truncated with %d differences", len(want))
	}
	if len(res.Diffs) != len(want) {
		return fmt.Errorf("diff found %d differences, want %d", len(res.Diffs), len(want))
	}
	for i := range want {
		if res.Diffs[i] != want[i] {
			return fmt.Errorf("difference %d: got %+v, want %+v", i, res.Diffs[i], want[i])
		}
	}
	return nil
}

func costBound(d, depth, leafLoad int) int {
	return 1 + 16*d*depth + d*leafLoad
}

func checkCost(t *rapid.T, local, remote *merkle.Tree, d int, res merkle.Result) {
	bound := costBound(d, local.Depth(), remote.LeafLoad())
	if res.Cost.Hashes > bound {
		t.Fatalf("hashes %d > bound %d (d=%d depth=%d leafload=%d)", res.Cost.Hashes, bound, d, local.Depth(), remote.LeafLoad())
	}
	if res.Cost.RoundTrips > local.Depth()+1 {
		t.Fatalf("round trips %d > depth+1 = %d", res.Cost.RoundTrips, local.Depth()+1)
	}
	if d == 0 && res.Cost.RoundTrips != 1 {
		t.Fatalf("equal trees took %d round trips", res.Cost.RoundTrips)
	}
}

type mapPair struct {
	depth  int
	local  map[string]merkle.Hash
	remote map[string]merkle.Hash
}

func genKey(t *rapid.T, label string) string {
	return rapid.OneOf(
		rapid.StringMatching(`[a-c]{0,3}`),
		rapid.StringN(0, 12, -1),
		rapid.Custom(func(t *rapid.T) string {
			return fmt.Sprintf("k%d", rapid.IntRange(0, 5000).Draw(t, "n"))
		}),
	).Draw(t, label)
}

func genHash(t *rapid.T, label string) merkle.Hash {
	switch rapid.IntRange(0, 3).Draw(t, label+"-kind") {
	case 0:
		return merkle.Hash{}
	case 1:
		var h merkle.Hash
		h[31] = byte(rapid.IntRange(0, 3).Draw(t, label+"-small"))
		return h
	default:
		return merkle.Sum([]byte(rapid.String().Draw(t, label)))
	}
}

func genPair(t *rapid.T) mapPair {
	n := rapid.IntRange(0, 300).Draw(t, "n")
	base := map[string]merkle.Hash{}
	for i := 0; i < n; i++ {
		base[genKey(t, "key")] = genHash(t, "hash")
	}
	depth := rapid.OneOf(rapid.Just(merkle.DepthFor(len(base))), rapid.IntRange(merkle.MinDepth, 4)).Draw(t, "depth")
	remote := map[string]merkle.Hash{}
	for k, v := range base {
		remote[k] = v
	}
	keys := slices.Sorted(func(yield func(string) bool) {
		for k := range base {
			if !yield(k) {
				return
			}
		}
	})
	steps := rapid.IntRange(0, 40).Draw(t, "steps")
	for i := 0; i < steps; i++ {
		switch rapid.IntRange(0, 2).Draw(t, "op") {
		case 0:
			remote[genKey(t, "ins")] = genHash(t, "ins-hash")
		case 1:
			if len(keys) > 0 {
				delete(remote, keys[rapid.IntRange(0, len(keys)-1).Draw(t, "del")])
			}
		case 2:
			if len(keys) > 0 {
				k := keys[rapid.IntRange(0, len(keys)-1).Draw(t, "flip")]
				h := remote[k]
				bit := rapid.IntRange(0, 255).Draw(t, "bit")
				h[bit/8] ^= 1 << (bit % 8)
				remote[k] = h
			}
		}
	}
	if rapid.Bool().Draw(t, "swap") {
		return mapPair{depth: depth, local: remote, remote: base}
	}
	return mapPair{depth: depth, local: base, remote: remote}
}

func runPair(t *rapid.T, p mapPair) (*merkle.Tree, *merkle.Tree, merkle.Result) {
	local := treeOf(t, p.depth, p.local)
	remote := treeOf(t, p.depth, p.remote)
	res, err := merkle.Compare(local, remote, merkle.Limits{})
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	return local, remote, res
}

func propDiffComplete(t *rapid.T) {
	p := genPair(t)
	local, remote, res := runPair(t, p)
	if err := checkComplete(p.local, p.remote, res); err != nil {
		t.Fatal(err)
	}
	checkCost(t, local, remote, len(refDiff(p.local, p.remote)), res)
}

func propDiffCost(t *rapid.T) {
	p := genPair(t)
	local, remote, res := runPair(t, p)
	checkCost(t, local, remote, len(refDiff(p.local, p.remote)), res)
}
