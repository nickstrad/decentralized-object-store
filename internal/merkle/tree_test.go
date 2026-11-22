package merkle_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/merkle"
)

// TestRootKnownVectors proves that Tree.Root matches pinned SHA-256 test
// vectors and an independent reference implementation, including that an empty
// key with a zero hash differs from an absent key.
func TestRootKnownVectors(t *testing.T) {
	cases := []struct {
		name  string
		depth int
		m     map[string]merkle.Hash
		want  string
	}{
		{"an empty tree has the zero root", 1, nil, strings.Repeat("0", 64)},
		{"one key at depth one", 1, map[string]merkle.Hash{"a": sum("1")}, vectorOneKey},
		{"three keys at depth two", 2, map[string]merkle.Hash{"a": sum("1"), "b": sum("2"), "c": sum("3")}, vectorThreeKeys},
		{"an empty key with a zero hash is present, not absent", 1, map[string]merkle.Hash{"": {}}, vectorEmptyKey},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tr := treeOf(t, c.depth, c.m)
			if got := tr.Root().String(); got != c.want {
				t.Errorf("root = %s, want %s", got, c.want)
			}
			if ref := refRoot(c.depth, c.m); tr.Root() != ref {
				t.Errorf("root = %s, reference = %s", tr.Root(), ref)
			}
		})
	}
	t.Run("the reference agrees on a hundred keys at every depth from one to four", func(t *testing.T) {
		m := map[string]merkle.Hash{}
		for i := 0; i < 100; i++ {
			m[fmt.Sprintf("key-%d", i)] = sum(fmt.Sprint(i))
		}
		for depth := 1; depth <= 4; depth++ {
			if got, want := treeOf(t, depth, m).Root(), refRoot(depth, m); got != want {
				t.Errorf("depth %d: root %s, reference %s", depth, got, want)
			}
		}
	})
}

const (
	vectorOneKey    = "04b2bb96cd443f5054027c6cfe799fbd18e4962401c6c4ab6fe35941338f8635"
	vectorThreeKeys = "be75e1f2f99d61a4b07af0eb80a9adc4ba8fd61fce9afedcbe59689705d312b6"
	vectorEmptyKey  = "0b16244ae8a9afe68737b66c8fc1d99c20810eb4151d3264a6b831b9881ebad2"
)

// TestInsertDeleteUpdate proves that incremental Set and Delete operations keep
// Len, Get, and Root equal to the reference oracle and restore the empty root
// once all keys are removed.
func TestInsertDeleteUpdate(t *testing.T) {
	tr, err := merkle.New(2)
	if err != nil {
		t.Fatal(err)
	}
	empty := tr.Root()
	steps := []struct {
		name string
		do   func() error
		want map[string]merkle.Hash
	}{
		{"insert a", func() error { return tr.Set("a", sum("1")) }, map[string]merkle.Hash{"a": sum("1")}},
		{"insert b", func() error { return tr.Set("b", sum("2")) }, map[string]merkle.Hash{"a": sum("1"), "b": sum("2")}},
		{"update a", func() error { return tr.Set("a", sum("3")) }, map[string]merkle.Hash{"a": sum("3"), "b": sum("2")}},
		{"update a to the same hash", func() error { return tr.Set("a", sum("3")) }, map[string]merkle.Hash{"a": sum("3"), "b": sum("2")}},
		{"delete b", func() error {
			if !tr.Delete("b") {
				return errors.New("delete b reported absent")
			}
			return nil
		}, map[string]merkle.Hash{"a": sum("3")}},
		{"delete a missing key", func() error {
			if tr.Delete("zz") {
				return errors.New("delete zz reported present")
			}
			return nil
		}, map[string]merkle.Hash{"a": sum("3")}},
		{"delete a", func() error {
			if !tr.Delete("a") {
				return errors.New("delete a reported absent")
			}
			return nil
		}, map[string]merkle.Hash{}},
	}
	for _, s := range steps {
		if err := s.do(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		if tr.Len() != len(s.want) {
			t.Fatalf("%s: Len = %d, want %d", s.name, tr.Len(), len(s.want))
		}
		if got, want := tr.Root(), refRoot(2, s.want); got != want {
			t.Fatalf("%s: root %s, want %s", s.name, got, want)
		}
		for k, h := range s.want {
			if got, ok := tr.Get(k); !ok || got != h {
				t.Fatalf("%s: Get(%q) = %s %v, want %s", s.name, k, got, ok, h)
			}
		}
	}
	if tr.Root() != empty {
		t.Fatalf("after deleting everything the root is %s, want the empty root %s", tr.Root(), empty)
	}
	if _, ok := tr.Get("a"); ok {
		t.Fatal("Get(a) found a deleted key")
	}
}

// TestEmptyAndSingle proves empty-tree zero roots, single-key diffs in Depth+1
// round trips, and self-diffs in 1 round trip and 1 hash across every valid
// depth from MinDepth to MaxDepth.
func TestEmptyAndSingle(t *testing.T) {
	for depth := merkle.MinDepth; depth <= merkle.MaxDepth; depth++ {
		empty := treeOf(t, depth, nil)
		if !empty.Root().IsZero() || empty.Len() != 0 || empty.LeafLoad() != 0 || len(empty.Keys()) != 0 {
			t.Fatalf("depth %d: empty tree root %s len %d", depth, empty.Root(), empty.Len())
		}
		single := treeOf(t, depth, map[string]merkle.Hash{"only": {}})
		if single.Root().IsZero() {
			t.Fatalf("depth %d: a one-key tree has the empty root", depth)
		}
		if single.Root() != refRoot(depth, map[string]merkle.Hash{"only": {}}) {
			t.Fatalf("depth %d: one-key root differs from the reference", depth)
		}
		res, err := merkle.Compare(empty, single, merkle.Limits{})
		if err != nil {
			t.Fatal(err)
		}
		want := []merkle.Difference{{Key: "only", HasRemote: true}}
		if len(res.Diffs) != 1 || res.Diffs[0] != want[0] {
			t.Fatalf("depth %d: diff %+v, want %+v", depth, res.Diffs, want)
		}
		if res.Cost.RoundTrips != depth+1 {
			t.Fatalf("depth %d: %d round trips, want %d", depth, res.Cost.RoundTrips, depth+1)
		}
		res, err = merkle.Compare(single, single.Snapshot(), merkle.Limits{})
		if err != nil || len(res.Diffs) != 0 || res.Cost.RoundTrips != 1 || res.Cost.Hashes != 1 {
			t.Fatalf("depth %d: self diff %+v %v", depth, res, err)
		}
	}
}

// TestNewAndDepthFor proves that New and Build reject out-of-range depths with
// ErrDepth and that DepthFor selects the smallest depth with 16^depth >= n.
func TestNewAndDepthFor(t *testing.T) {
	for _, d := range []int{merkle.MinDepth - 1, merkle.MaxDepth + 1, -5, 255} {
		if _, err := merkle.New(d); !errors.Is(err, merkle.ErrDepth) {
			t.Errorf("New(%d) = %v, want ErrDepth", d, err)
		}
		if _, err := merkle.Build(d, nil); !errors.Is(err, merkle.ErrDepth) {
			t.Errorf("Build(%d) = %v, want ErrDepth", d, err)
		}
	}
	cases := []struct{ n, want int }{
		{-1, 1}, {0, 1}, {1, 1}, {16, 1}, {17, 2}, {256, 2}, {257, 3}, {1000, 3}, {10000, 4},
		{100000, 5}, {1 << 62, merkle.MaxDepth},
	}
	for _, c := range cases {
		if got := merkle.DepthFor(c.n); got != c.want {
			t.Errorf("DepthFor(%d) = %d, want %d", c.n, got, c.want)
		}
	}
}

// TestKeyLimits proves that Set and Build enforce MaxKeyLen and that Build
// rejects duplicate keys with ErrKey.
func TestKeyLimits(t *testing.T) {
	tr := treeOf(t, 1, nil)
	long := strings.Repeat("x", merkle.MaxKeyLen+1)
	if err := tr.Set(long, sum("v")); !errors.Is(err, merkle.ErrKey) {
		t.Fatalf("Set of a %d-byte key = %v, want ErrKey", len(long), err)
	}
	if err := tr.Set(long[:merkle.MaxKeyLen], sum("v")); err != nil {
		t.Fatalf("Set of a %d-byte key: %v", merkle.MaxKeyLen, err)
	}
	if _, err := merkle.Build(1, []merkle.Entry{{Key: long}}); !errors.Is(err, merkle.ErrKey) {
		t.Fatalf("Build with a long key = %v, want ErrKey", err)
	}
	if _, err := merkle.Build(1, []merkle.Entry{{Key: "a"}, {Key: "a"}}); !errors.Is(err, merkle.ErrKey) {
		t.Fatalf("Build with a duplicate key = %v, want ErrKey", err)
	}
}

// TestLeafFull proves that Set and Build enforce MaxLeafEntries with
// ErrLeafFull while still allowing updates to an existing key in a full leaf.
func TestLeafFull(t *testing.T) {
	keys := keysInLeaf(t, 1, 0, merkle.MaxLeafEntries+1)
	tr := treeOf(t, 1, nil)
	var entries []merkle.Entry
	for i, k := range keys {
		err := tr.Set(k, sum(k))
		if i < merkle.MaxLeafEntries && err != nil {
			t.Fatalf("Set %d: %v", i, err)
		}
		if i == merkle.MaxLeafEntries && !errors.Is(err, merkle.ErrLeafFull) {
			t.Fatalf("Set past the leaf limit = %v, want ErrLeafFull", err)
		}
		entries = append(entries, merkle.Entry{Key: k, Hash: sum(k)})
	}
	if tr.LeafLoad() != merkle.MaxLeafEntries || tr.Len() != merkle.MaxLeafEntries {
		t.Fatalf("leaf load %d len %d", tr.LeafLoad(), tr.Len())
	}
	if err := tr.Set(keys[0], sum("new")); err != nil {
		t.Fatalf("updating a key in a full leaf: %v", err)
	}
	if _, err := merkle.Build(1, entries); !errors.Is(err, merkle.ErrLeafFull) {
		t.Fatalf("Build past the leaf limit = %v, want ErrLeafFull", err)
	}
}

// keysInLeaf grinds keys whose first depth SHA-256 nibbles match leaf.
func keysInLeaf(t testing.TB, depth int, leaf byte, n int) []string {
	t.Helper()
	var out []string
	for i := 0; len(out) < n; i++ {
		k := fmt.Sprintf("grind-%d", i)
		p := keyPath(k)
		ok := true
		for j := 0; j < depth; j++ {
			if nibbleOf(p, j) != int(leaf>>(4*(depth-1-j)))&0x0f {
				ok = false
			}
		}
		if ok {
			out = append(out, k)
		}
	}
	return out
}

// TestSnapshotIsolation proves that mutations to a Tree after Snapshot leave
// the snapshot's root, length, and keys unchanged.
func TestSnapshotIsolation(t *testing.T) {
	tr := treeOf(t, 2, map[string]merkle.Hash{"a": sum("1"), "b": sum("2")})
	snap := tr.Snapshot()
	before := snap.Root()
	if err := tr.Set("c", sum("3")); err != nil {
		t.Fatal(err)
	}
	tr.Delete("a")
	if snap.Root() != before || snap.Len() != 2 {
		t.Fatalf("snapshot changed after writes to the tree")
	}
	if _, ok := snap.Get("a"); !ok {
		t.Fatal("snapshot lost a key deleted from the tree later")
	}
}

// TestKeysAndEntriesSorted proves that Keys and Entries return sorted slices
// and that Build accepts unsorted input slices.
func TestKeysAndEntriesSorted(t *testing.T) {
	m := map[string]merkle.Hash{}
	for i := 0; i < 200; i++ {
		m[fmt.Sprintf("k%03d", i)] = sum(fmt.Sprint(i))
	}
	tr := treeOf(t, 3, m)
	want := entriesOf(m)
	got := tr.Entries()
	keys := tr.Keys()
	if len(got) != len(want) || len(keys) != len(want) {
		t.Fatalf("entries %d keys %d, want %d", len(got), len(keys), len(want))
	}
	for i := range want {
		if got[i] != want[i] || keys[i] != want[i].Key {
			t.Fatalf("entry %d: %+v / %q, want %+v", i, got[i], keys[i], want[i])
		}
	}
	b, err := merkle.Build(3, []merkle.Entry{want[2], want[0], want[1]})
	if err != nil {
		t.Fatal(err)
	}
	if b.Len() != 3 || b.Root() != refRoot(3, map[string]merkle.Hash{want[0].Key: want[0].Hash, want[1].Key: want[1].Hash, want[2].Key: want[2].Hash}) {
		t.Fatal("Build of an unsorted slice differs from the reference")
	}
}

// TestHashString proves SHA-256 hex formatting and IsZero on Hash values.
func TestHashString(t *testing.T) {
	h := sum("abc")
	if h.String() != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("Sum(abc) = %s", h)
	}
	if h.IsZero() || !(merkle.Hash{}).IsZero() {
		t.Fatal("IsZero wrong")
	}
}
