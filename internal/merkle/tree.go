// Package merkle implements a fixed-depth, fanout-16 SHA-256 Merkle trie over a
// key-to-hash map, a level-by-level diff protocol, and a pull-only anti-entropy
// repair worker built against internal/env.
//
// Two rules govern its use. First, trees must share the same depth to be diffed
// (mismatched depths fail with ErrDepth), and Worker is single-threaded and
// add-only: it repairs missing or corrupted keys from peers but does not
// propagate deletes. Second, the tree is kept in memory and rebuilt from
// Repairer on Start; periodic scrubs re-hash stored bytes so silent bit-rot
// changes the local tree and triggers repair.
package merkle

import (
	"cmp"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
)

// Tree shape and entry bounds:
//   - Fanout: children per inner node (4 bits of SHA-256(key) per level).
//   - MinDepth, MaxDepth: valid tree depths (1..16, covering the first 64 bits
//     of SHA-256(key)).
//   - MaxKeyLen: maximum byte length of a key accepted by Set or Build.
//   - MaxLeafEntries: maximum entries held in a single leaf node.
const (
	Fanout         = 16
	MinDepth       = 1
	MaxDepth       = 16
	MaxKeyLen      = 1024
	MaxLeafEntries = 4096
)

// Domain-separation tags hashed as the first byte of leaf and inner encodings
// so a leaf preimage can never be interpreted as an inner node or vice versa.
const (
	tagLeaf  byte = 0x00
	tagInner byte = 0x01
)

// Hash is a 32-byte SHA-256 digest. The zero value represents an empty subtree
// (or an explicit zero value hash inside a present leaf entry).
type Hash [32]byte

// Sum returns the SHA-256 digest of data.
func Sum(data []byte) Hash { return sha256.Sum256(data) }

// String formats h as 64 lowercase hexadecimal characters.
func (h Hash) String() string { return hex.EncodeToString(h[:]) }

// IsZero reports whether h is the all-zero digest.
func (h Hash) IsZero() bool { return h == Hash{} }

// Entry pairs a key with its 32-byte content hash inside a Tree leaf.
type Entry struct {
	Key  string
	Hash Hash
}

// node is an immutable inner or leaf trie node; empty subtrees are nil rather
// than allocated.
type node struct {
	hash    Hash
	kids    *[Fanout]*node
	entries []Entry
}

func (n *node) sum() Hash {
	if n == nil {
		return Hash{}
	}
	return n.hash
}

func (n *node) kid(i int) *node {
	if n == nil || n.kids == nil {
		return nil
	}
	return n.kids[i]
}

// Tree is a persistent fanout-16 Merkle trie of fixed depth mapping keys to
// Hash values. Because nodes are immutable, Set and Delete copy only the path
// from root to leaf, and Snapshot is O(1). A Tree is not safe for concurrent
// use.
type Tree struct {
	depth int
	root  *node
	n     int
}

// New returns an empty Tree of the given depth, or ErrDepth if depth is
// outside [MinDepth, MaxDepth].
func New(depth int) (*Tree, error) {
	if depth < MinDepth || depth > MaxDepth {
		return nil, fmt.Errorf("merkle: new tree of depth %d: %w", depth, ErrDepth)
	}
	return &Tree{depth: depth}, nil
}

// DepthFor returns the smallest depth in [MinDepth, MaxDepth] with at least n
// leaf buckets (16^depth >= n), clamping non-positive counts to MinDepth.
func DepthFor(n int) int {
	d := MinDepth
	for leaves := Fanout; leaves < n && d < MaxDepth; leaves *= Fanout {
		d++
	}
	return d
}

// pathOf maps key to its 16-nibble trie path: the first 64 bits of SHA-256(key),
// read big-endian from most-significant nibble downward.
func pathOf(key string) uint64 {
	s := sha256.Sum256([]byte(key))
	return binary.BigEndian.Uint64(s[:8])
}

// nibbleAt extracts the i-th path nibble (0 at the root, up to 15).
func nibbleAt(p uint64, i int) int {
	return int(p>>(60-4*uint(i))) & 0x0f
}

// prefix masks p to its top level nibbles, zeroing all lower nibbles.
func prefix(p uint64, level int) uint64 {
	return p &^ (^uint64(0) >> (4 * uint(level)))
}

// child appends nibble i at depth level to a level-masked path prefix p.
func child(p uint64, level, i int) uint64 {
	return p | uint64(i)<<(60-4*uint(level))
}

// leafHash computes SHA-256(tagLeaf || u32be(len) || (u32be(len(key)) || key || hash)...).
// Explicit counts and length prefixes prevent key/value boundary ambiguity and
// distinguish a present key holding a zero hash from an absent key.
func leafHash(es []Entry) Hash {
	size := 5
	for _, e := range es {
		size += 4 + len(e.Key) + len(e.Hash)
	}
	buf := make([]byte, 0, size)
	buf = append(buf, tagLeaf)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(es)))
	for _, e := range es {
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(e.Key)))
		buf = append(buf, e.Key...)
		buf = append(buf, e.Hash[:]...)
	}
	return sha256.Sum256(buf)
}

func newLeaf(es []Entry) *node {
	if len(es) == 0 {
		return nil
	}
	return &node{hash: leafHash(es), entries: es}
}

// newInner hashes an inner node as SHA-256(tagInner || u8(level) || 16*32B child hashes),
// with nil children contributing 32 zero bytes, and collapses an all-nil node to nil.
func newInner(level int, kids *[Fanout]*node) *node {
	var buf [2 + Fanout*32]byte
	buf[0], buf[1] = tagInner, byte(level)
	empty := true
	for i, k := range kids {
		if k != nil {
			empty = false
			copy(buf[2+32*i:], k.hash[:])
		}
	}
	if empty {
		return nil
	}
	return &node{hash: sha256.Sum256(buf[:]), kids: kids}
}

func checkKey(key string) error {
	if len(key) > MaxKeyLen {
		return fmt.Errorf("merkle: key of %d bytes over %d: %w", len(key), MaxKeyLen, ErrKey)
	}
	return nil
}

func byKey(e Entry, key string) int { return strings.Compare(e.Key, key) }

// Depth returns the tree's fixed depth (number of inner levels; leaves sit at Depth).
func (t *Tree) Depth() int { return t.depth }

// Len returns the number of keys currently stored in t.
func (t *Tree) Len() int { return t.n }

// Root returns the tree's root digest, or the zero Hash when t is empty.
func (t *Tree) Root() Hash { return t.root.sum() }

// Snapshot returns an O(1) independent view of t's current state; subsequent
// Set or Delete calls on t do not affect the snapshot.
func (t *Tree) Snapshot() *Tree {
	c := *t
	return &c
}

func (t *Tree) nodeAt(level int, p uint64) *node {
	n := t.root
	for l := 0; l < level && n != nil; l++ {
		n = n.kid(nibbleAt(p, l))
	}
	return n
}

// Get returns the hash stored for key and reports whether key is present.
func (t *Tree) Get(key string) (Hash, bool) {
	leaf := t.nodeAt(t.depth, pathOf(key))
	if leaf == nil {
		return Hash{}, false
	}
	i, ok := slices.BinarySearchFunc(leaf.entries, key, byKey)
	if !ok {
		return Hash{}, false
	}
	return leaf.entries[i].Hash, true
}

// Set inserts or updates key with hash h in O(depth) node copies and hashes.
// It returns ErrKey if len(key) > MaxKeyLen, or ErrLeafFull if inserting a new
// key would push its leaf past MaxLeafEntries.
func (t *Tree) Set(key string, h Hash) error {
	if err := checkKey(key); err != nil {
		return err
	}
	root, added, err := t.set(t.root, 0, pathOf(key), Entry{Key: key, Hash: h})
	if err != nil {
		return err
	}
	t.root = root
	if added {
		t.n++
	}
	return nil
}

func (t *Tree) set(n *node, level int, p uint64, e Entry) (*node, bool, error) {
	if level == t.depth {
		var old []Entry
		if n != nil {
			old = n.entries
		}
		i, found := slices.BinarySearchFunc(old, e.Key, byKey)
		if found {
			// Returning the existing pointer lets ancestors skip re-hashing
			// when a key is updated to its current hash.
			if old[i].Hash == e.Hash {
				return n, false, nil
			}
			es := slices.Clone(old)
			es[i] = e
			return newLeaf(es), false, nil
		}
		if len(old) >= MaxLeafEntries {
			return nil, false, fmt.Errorf("merkle: set %q: leaf holds %d keys: %w", e.Key, len(old), ErrLeafFull)
		}
		es := make([]Entry, 0, len(old)+1)
		es = append(es, old[:i]...)
		es = append(es, e)
		es = append(es, old[i:]...)
		return newLeaf(es), true, nil
	}
	var kids [Fanout]*node
	if n != nil {
		kids = *n.kids
	}
	i := nibbleAt(p, level)
	c, added, err := t.set(kids[i], level+1, p, e)
	if err != nil {
		return nil, false, err
	}
	if c == kids[i] {
		return n, added, nil
	}
	kids[i] = c
	return newInner(level, &kids), added, nil
}

// Delete removes key from t and reports whether key was present.
func (t *Tree) Delete(key string) bool {
	root, ok := t.del(t.root, 0, pathOf(key), key)
	if ok {
		t.root = root
		t.n--
	}
	return ok
}

func (t *Tree) del(n *node, level int, p uint64, key string) (*node, bool) {
	if n == nil {
		return nil, false
	}
	if level == t.depth {
		i, found := slices.BinarySearchFunc(n.entries, key, byKey)
		if !found {
			return n, false
		}
		es := make([]Entry, 0, len(n.entries)-1)
		es = append(es, n.entries[:i]...)
		es = append(es, n.entries[i+1:]...)
		return newLeaf(es), true
	}
	kids := *n.kids
	i := nibbleAt(p, level)
	c, ok := t.del(kids[i], level+1, p, key)
	if !ok {
		return n, false
	}
	kids[i] = c
	return newInner(level, &kids), true
}

type item struct {
	path uint64
	e    Entry
}

// Build constructs a Tree of the given depth bottom-up from entries without
// modifying the input slice. It returns ErrDepth for an out-of-range depth,
// ErrKey for an overlong or duplicate key, or ErrLeafFull if any leaf exceeds
// MaxLeafEntries.
func Build(depth int, entries []Entry) (*Tree, error) {
	t, err := New(depth)
	if err != nil {
		return nil, err
	}
	items := make([]item, 0, len(entries))
	for _, e := range entries {
		if err := checkKey(e.Key); err != nil {
			return nil, err
		}
		items = append(items, item{path: pathOf(e.Key), e: e})
	}
	// Sorting by (path, key) groups each trie subtree into a contiguous slice
	// and places any duplicate keys adjacent for the check below.
	slices.SortFunc(items, func(a, b item) int {
		if c := cmp.Compare(a.path, b.path); c != 0 {
			return c
		}
		return strings.Compare(a.e.Key, b.e.Key)
	})
	for i := 1; i < len(items); i++ {
		if items[i].e.Key == items[i-1].e.Key {
			return nil, fmt.Errorf("merkle: build: duplicate key %q: %w", items[i].e.Key, ErrKey)
		}
	}
	root, err := t.build(0, items)
	if err != nil {
		return nil, err
	}
	t.root, t.n = root, len(items)
	return t, nil
}

func (t *Tree) build(level int, items []item) (*node, error) {
	if len(items) == 0 {
		return nil, nil
	}
	if level == t.depth {
		if len(items) > MaxLeafEntries {
			return nil, fmt.Errorf("merkle: build: leaf of %d keys: %w", len(items), ErrLeafFull)
		}
		es := make([]Entry, 0, len(items))
		for _, it := range items {
			es = append(es, it.e)
		}
		// Keys sharing a depth-nibble prefix may differ in lower path bits, so
		// re-sort by key to match Set's canonical leaf order.
		slices.SortFunc(es, func(a, b Entry) int { return strings.Compare(a.Key, b.Key) })
		return newLeaf(es), nil
	}
	var kids [Fanout]*node
	start := 0
	for i := 0; i < Fanout; i++ {
		end := start
		for end < len(items) && nibbleAt(items[end].path, level) == i {
			end++
		}
		k, err := t.build(level+1, items[start:end])
		if err != nil {
			return nil, err
		}
		kids[i] = k
		start = end
	}
	return newInner(level, &kids), nil
}

func (t *Tree) walk(n *node, fn func(*node)) {
	if n == nil {
		return
	}
	if n.kids == nil {
		fn(n)
		return
	}
	for _, k := range n.kids {
		t.walk(k, fn)
	}
}

// Entries returns all entries in t sorted by key.
func (t *Tree) Entries() []Entry {
	out := make([]Entry, 0, t.n)
	t.walk(t.root, func(n *node) { out = append(out, n.entries...) })
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(a.Key, b.Key) })
	return out
}

// Keys returns all keys in t in sorted order.
func (t *Tree) Keys() []string {
	out := make([]string, 0, t.n)
	t.walk(t.root, func(n *node) {
		for _, e := range n.entries {
			out = append(out, e.Key)
		}
	})
	slices.Sort(out)
	return out
}

// LeafLoad returns the largest entry count across all leaves in t, or 0 if t
// is empty.
func (t *Tree) LeafLoad() int {
	most := 0
	t.walk(t.root, func(n *node) { most = max(most, len(n.entries)) })
	return most
}
