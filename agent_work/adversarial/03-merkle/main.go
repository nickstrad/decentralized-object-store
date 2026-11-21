package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	m "github.com/nickstrad/decentralized-object-store/internal/merkle"
	"math/rand/v2"
	"os"
	"sort"
	"strings"
)

const seed = 731903

var broken int

func require(ok bool, f string, a ...any) {
	if !ok {
		panic(fmt.Sprintf(f, a...))
	}
}
func must(e error) {
	if e != nil {
		panic(e)
	}
}
func check(c, name string, f func() string) {
	if only := os.Getenv("MERKLE_CHECK"); only != "" && only != name {
		return
	}
	defer func() {
		if x := recover(); x != nil {
			broken++
			fmt.Printf("BROKEN %s %s: seed=%d %s\n", c, name, seed, strings.ReplaceAll(fmt.Sprint(x), "\n", " "))
		}
	}()
	s := f()
	fmt.Printf("HELD %s %s: %s\n", c, name, s)
}
func tree(d int, es []m.Entry) *m.Tree { t, e := m.Build(d, es); must(e); return t }
func entries(n, kl int) []m.Entry {
	es := make([]m.Entry, n)
	for i := range es {
		k := fmt.Sprintf("key-%08d", i)
		if kl > len(k) {
			k += strings.Repeat("x", kl-len(k))
		}
		es[i] = m.Entry{Key: k, Hash: m.Sum([]byte(k))}
	}
	return es
}
func compare(a, b *m.Tree) m.Result {
	r, e := m.Compare(a, b, m.Limits{Frontier: m.MaxRequestNodes})
	must(e)
	return r
}
func oracle(es []m.Entry, level, depth int) m.Hash {
	if len(es) == 0 {
		return m.Hash{}
	}
	if level == depth {
		sort.Slice(es, func(i, j int) bool { return es[i].Key < es[j].Key })
		b := binary.BigEndian.AppendUint32([]byte{0}, uint32(len(es)))
		for _, e := range es {
			b = binary.BigEndian.AppendUint32(b, uint32(len(e.Key)))
			b = append(b, []byte(e.Key)...)
			b = append(b, e.Hash[:]...)
		}
		return sha256.Sum256(b)
	}
	groups := make([][]m.Entry, 16)
	for _, e := range es {
		h := sha256.Sum256([]byte(e.Key))
		n := int(h[level/2])
		if level%2 == 0 {
			n >>= 4
		}
		groups[n&15] = append(groups[n&15], e)
	}
	b := []byte{1, byte(level)}
	for _, g := range groups {
		h := oracle(g, level+1, depth)
		b = append(b, h[:]...)
	}
	return sha256.Sum256(b)
}
func expected(a, b *m.Tree) []m.Difference {
	am, bm := map[string]m.Hash{}, map[string]m.Hash{}
	keys := map[string]bool{}
	for _, e := range a.Entries() {
		am[e.Key] = e.Hash
		keys[e.Key] = true
	}
	for _, e := range b.Entries() {
		bm[e.Key] = e.Hash
		keys[e.Key] = true
	}
	out := []m.Difference{}
	for k := range keys {
		x, ax := am[k]
		y, by := bm[k]
		if ax != by || x != y {
			out = append(out, m.Difference{Key: k, Local: x, Remote: y, HasLocal: ax, HasRemote: by})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}
func exact(a, b *m.Tree, r m.Result) {
	w := expected(a, b)
	require(!r.Truncated, "unexpected truncation")
	require(len(w) == len(r.Diffs), "diff count got=%d want=%d", len(r.Diffs), len(w))
	for i := range w {
		require(w[i] == r.Diffs[i], "diff mismatch index=%d", i)
	}
}
func trees() {
	check("C1", "reference-roots", func() string {
		for d := 1; d <= 16; d++ {
			es := entries(130, 0)
			es = append(es, m.Entry{Key: "", Hash: m.Hash{}}, m.Entry{Key: "a\x00b", Hash: m.Sum([]byte{0, 1})})
			t := tree(d, es)
			require(t.Root() == oracle(es, 0, d), "reference mismatch depth=%d", d)
		}
		return "independent recursive encoding agrees at all 16 depths including binary and empty keys"
	})
	check("C13", "structure-boundaries", func() string {
		sets := [][]m.Entry{nil, {{Key: ""}}, {{Key: "a"}}, {{Key: "a\x00"}}, {{Key: "ab", Hash: m.Sum([]byte("c"))}}, {{Key: "a", Hash: m.Sum([]byte("bc"))}}, {{Key: strings.Repeat("\x00", 514)}}, {{Key: "a"}, {Key: "b"}}}
		for d := 1; d <= 16; d++ {
			seen := map[m.Hash]int{}
			for i, es := range sets {
				h := tree(d, es).Root()
				j, ok := seen[h]
				require(!ok, "depth=%d distinct maps=%d,%d same root", d, j, i)
				seen[h] = i
			}
		}
		return "128 structured maps distinguish empty, absent, boundary shifts and inner-sized zero keys"
	})
	check("C2", "model-updates", func() string {
		r := rand.New(rand.NewPCG(seed, 9))
		for _, d := range []int{1, 3, 16} {
			t := tree(d, nil)
			model := map[string]m.Hash{}
			for i := 0; i < 1000; i++ {
				k := fmt.Sprint(r.IntN(100))
				if r.IntN(3) == 0 {
					_, ok := model[k]
					require(t.Delete(k) == ok, "delete result step=%d", i)
					delete(model, k)
				} else {
					h := m.Sum([]byte(fmt.Sprint(r.Uint64())))
					must(t.Set(k, h))
					must(t.Set(k, h))
					model[k] = h
				}
				require(t.Len() == len(model), "length step=%d", i)
				for k, h := range model {
					x, ok := t.Get(k)
					require(ok && h == x, "get step=%d", i)
				}
			}
			for k := range model {
				t.Delete(k)
			}
			require(t.Root().IsZero() && t.Len() == 0, "not empty")
		}
		return "3000 mixed updates, same-value writes and deletions match a map"
	})
	check("C14", "rebuild-sequence", func() string {
		r := rand.New(rand.NewPCG(seed, 14))
		t := tree(4, nil)
		for i := 0; i < 500; i++ {
			k := fmt.Sprint(r.IntN(200))
			if i%3 == 0 {
				t.Delete(k)
			} else {
				must(t.Set(k, m.Sum([]byte(fmt.Sprint(i)))))
			}
			b := tree(4, t.Entries())
			require(t.Root() == b.Root() && t.Len() == b.Len() && t.LeafLoad() == b.LeafLoad(), "step=%d", i)
		}
		return "500 transitions agree with bottom-up Build in root, length and leaf load"
	})
	check("C15", "permutations", func() string {
		es := entries(257, 0)
		r := rand.New(rand.NewPCG(seed, 15))
		for _, d := range []int{1, 4, 16} {
			want := tree(d, es).Root()
			for i := 0; i < 12; i++ {
				r.Shuffle(len(es), func(i, j int) { es[i], es[j] = es[j], es[i] })
				t := tree(d, nil)
				for _, e := range es {
					must(t.Set(e.Key, e.Hash))
				}
				require(t.Root() == want, "depth=%d permutation=%d", d, i)
			}
		}
		return "36 shuffled incremental constructions have identical roots"
	})
	check("C3", "empty-single-equal", func() string {
		for d := 1; d <= 16; d++ {
			a, b := tree(d, nil), tree(d, []m.Entry{{Key: ""}})
			require(a.Root().IsZero(), "empty depth=%d", d)
			r := compare(a, b)
			exact(a, b, r)
			require(r.Cost.RoundTrips == d+1, "depth=%d trips=%d", d, r.Cost.RoundTrips)
			r = compare(b, b)
			require(r.Cost.RoundTrips == 1 && r.Cost.Hashes == 1, "equal cost depth=%d", d)
		}
		return "all depths have zero empty roots, Depth+1 single-key trips and one-hash self-diffs"
	})
	check("C4", "depth-boundaries", func() string {
		for _, d := range []int{-1, 0, 17, 256} {
			_, e := m.New(d)
			require(errors.Is(e, m.ErrDepth), "New depth=%d err=%v", d, e)
			_, e = m.Build(d, nil)
			require(errors.Is(e, m.ErrDepth), "Build depth=%d", d)
		}
		for n, w := range map[int]int{0: 1, 1: 1, 16: 1, 17: 2, 256: 2, 257: 3, 1000: 3, 10000: 4, 100000: 5} {
			require(m.DepthFor(n) == w, "n=%d", n)
		}
		return "invalid depths rejected and nine DepthFor boundaries match"
	})
	check("C5", "key-and-duplicate-limits", func() string {
		t := tree(2, nil)
		must(t.Set(strings.Repeat("x", 1024), m.Hash{}))
		root := t.Root()
		require(errors.Is(t.Set(strings.Repeat("x", 1025), m.Hash{}), m.ErrKey) && t.Root() == root, "oversize Set")
		_, e := m.Build(2, []m.Entry{{Key: "a"}, {Key: "a"}})
		require(errors.Is(e, m.ErrKey), "duplicate err=%v", e)
		_, e = m.Build(2, []m.Entry{{Key: strings.Repeat("x", 1025)}})
		require(errors.Is(e, m.ErrKey), "oversize Build")
		return "1024-byte key accepted; 1025-byte and duplicate keys rejected atomically"
	})
	check("C6", "snapshot-and-returned-slices", func() string {
		t := tree(4, entries(90, 0))
		s := t.Snapshot()
		root := s.Root()
		es := s.Entries()
		es[0].Hash = m.Hash{}
		ks := s.Keys()
		ks[0] = "changed"
		for _, e := range t.Entries() {
			t.Delete(e.Key)
		}
		must(t.Set("new", m.Hash{}))
		require(s.Root() == root && s.Len() == 90, "snapshot mutated")
		must(s.Set("independent", m.Hash{}))
		require(t.Len() == 1, "reverse isolation")
		return "writes in either snapshot and edits to returned entry/key slices remain isolated"
	})
	check("C11", "diff-oracle", func() string {
		r := rand.New(rand.NewPCG(seed, 11))
		for i := 0; i < 200; i++ {
			d := 1 + r.IntN(16)
			a, b := tree(d, entries(r.IntN(250), 0)), tree(d, entries(r.IntN(250), 0))
			for j := 0; j < 20; j++ {
				must(b.Set(fmt.Sprint(r.IntN(400)), m.Sum([]byte(fmt.Sprint(i, j)))))
			}
			exact(a, b, compare(a, b))
			exact(b, a, compare(b, a))
		}
		return "400 directional diffs match an independent map union with every hash and presence bit"
	})
	check("C12", "cost-grid", func() string {
		for _, n := range []int{1, 16, 257, 1000, 10000, 100000} {
			d := m.DepthFor(n)
			a := tree(d, entries(n, 0))
			for _, delta := range []int{1, 7, 100} {
				b := a.Snapshot()
				for i := 0; i < delta; i++ {
					must(b.Set(fmt.Sprintf("new-%d", i), m.Sum([]byte{byte(i)})))
				}
				r := compare(a, b)
				exact(a, b, r)
				bound := 1 + 16*delta*d + delta*b.LeafLoad()
				require(r.Cost.Hashes <= bound && r.Cost.RoundTrips <= d+1, "N=%d d=%d depth=%d cost=%+v bound=%d", n, delta, d, r.Cost, bound)
			}
		}
		return "18 size/divergence pairs through N=100000 satisfy the published leaf-load hash bound"
	})
	check("C6", "mutate-during-diff", func() string {
		a, b := tree(4, entries(100, 0)), tree(4, entries(130, 0))
		before := a.Snapshot()
		s := a.Diff(m.Limits{})
		steps := 0
		for !s.Done() {
			q, ok := s.Request()
			require(ok, "no request")
			must(a.Set(fmt.Sprint(steps), m.Sum(q)))
			for _, e := range before.Entries() {
				a.Delete(e.Key)
			}
			p, e := b.Answer(q)
			must(e)
			must(s.Feed(p))
			steps++
		}
		exact(before, b, s.Result())
		return "initiator mutations between every request leave the original snapshot diff intact"
	})
	check("C9", "small-frontier-convergence", func() string {
		for _, cap := range []int{1, 3, 7} {
			a, b := tree(4, nil), tree(4, entries(140, 0))
			rounds := 0
			truncated := false
			for a.Root() != b.Root() && rounds < 150 {
				r, e := m.Compare(a, b, m.Limits{Frontier: cap})
				must(e)
				truncated = truncated || r.Truncated
				require(len(r.Diffs) > 0, "no progress cap=%d", cap)
				for _, x := range r.Diffs {
					h, ok := b.Get(x.Key)
					require(ok && x.HasRemote && x.Remote == h && !x.HasLocal, "spurious diff")
					must(a.Set(x.Key, x.Remote))
				}
				rounds++
			}
			require(a.Root() == b.Root() && truncated, "cap=%d rounds=%d", cap, rounds)
		}
		return "caps 1, 3 and 7 truncate truthfully and repeated repairs converge"
	})
	largeChecks()
}

func ground(n, kl int, depths ...int) []m.Entry {
	out := []m.Entry{}
	for i := 0; len(out) < n; i++ {
		k := fmt.Sprintf("ground-%08d", i)
		k += strings.Repeat("z", kl-len(k))
		h := sha256.Sum256([]byte(k))
		if h[0]>>4 == 0 && (len(depths) == 0 || depths[0] == 1 || h[0] == 0) {
			out = append(out, m.Entry{Key: k, Hash: m.Sum([]byte(k))})
		}
	}
	return out
}
func largeChecks() {
	check("C5", "leaf-cap", func() string {
		es := ground(4097, 32)
		t := tree(1, es[:4096])
		root := t.Root()
		require(errors.Is(t.Set(es[4096].Key, es[4096].Hash), m.ErrLeafFull) && root == t.Root(), "Set cap")
		_, e := m.Build(1, es)
		require(errors.Is(e, m.ErrLeafFull), "Build cap err=%v", e)
		must(t.Set(es[0].Key, m.Hash{}))
		t.Delete(es[1].Key)
		must(t.Set(es[4096].Key, es[4096].Hash))
		return "4096-entry leaf permits updates/replacement and rejects entry 4097 atomically"
	})
	check("C8", "single-leaf-reply-size", func() string {
		es := ground(992, 1024)
		prev, err := tree(1, es[:991]).Answer([]byte{1, 1, 1, 1, 0})
		must(err)
		require(len(prev) <= m.MaxReplyBytes, "991-key control exceeds cap")
		b := tree(1, es)
		p, e := b.Answer([]byte{1, 1, 1, 1, 0})
		must(e)
		require(len(p) <= m.MaxReplyBytes, "Build(1, ground(992,1024)); Answer(0101010100) returned %d bytes > %d", len(p), m.MaxReplyBytes)
		return "single legal leaf stays within MaxReplyBytes"
	})
	check("C8", "multi-leaf-batching", func() string {
		a, b := tree(2, nil), tree(2, entries(4000, 1024))
		s := a.Diff(m.Limits{})
		for !s.Done() {
			q, _ := s.Request()
			p, e := b.Answer(q)
			must(e)
			require(len(p) <= m.MaxReplyBytes, "reply bytes=%d", len(p))
			must(s.Feed(p))
		}
		exact(a, b, s.Result())
		require(s.Result().Cost.RoundTrips > 3, "did not batch")
		return fmt.Sprintf("4000 long keys complete exactly in %d trips with each reply <=1 MiB", s.Result().Cost.RoundTrips)
	})
	check("C12", "batched-round-trip-bound", func() string {
		a, b := tree(1, nil), tree(1, entries(984, 1024))
		require(compare(a, tree(1, entries(983, 1024))).Cost.RoundTrips == 2, "983-key control unexpectedly batches")
		r := compare(a, b)
		exact(a, b, r)
		require(r.Cost.RoundTrips <= 2, "N=984 depth=1 d=984 keylen=1024: trips=%d > Depth+1=2", r.Cost.RoundTrips)
		return "batched replies obey Depth+1"
	})
	check("C31", "clustered-hash-cost", func() string {
		es := ground(46, 32, 2)
		small := tree(2, es[:45])
		changed := small.Snapshot()
		must(changed.Set(es[0].Key, m.Hash{}))
		require(compare(small, changed).Cost.Hashes == 48, "45-key control")
		a := tree(2, es)
		b := a.Snapshot()
		must(b.Set(es[0].Key, m.Hash{}))
		r := compare(a, b)
		exact(a, b, r)
		require(r.Cost.Hashes <= 1+32+b.LeafLoad(), "leaf-load bound")
		require(r.Cost.Hashes <= 16*3, "N=46 depth=DepthFor(46)=2 d=1 same-leaf keys: hashes=%d > plan 16*d*(Depth+1)=48; leaf-load bound holds", r.Cost.Hashes)
		return "clustered leaf satisfies simple level-only bound"
	})
}
func wire() {
	check("C7", "protocol-state", func() string {
		a, b := tree(3, nil), tree(3, []m.Entry{{Key: "x"}})
		s := a.Diff(m.Limits{})
		require(errors.Is(s.Feed([]byte{4}), m.ErrSession), "Feed without Request")
		for i := 0; i < 4; i++ {
			q, ok := s.Request()
			require(ok && int(q[2]) == i, "level=%d", i)
			q2, _ := s.Request()
			require(bytes.Equal(q, q2), "request unstable")
			if i == 0 {
				require(len(q) == 35, "root len")
			}
			p, e := b.Answer(q)
			must(e)
			must(s.Feed(p))
		}
		require(s.Done() && errors.Is(s.Feed([]byte{4}), m.ErrSession), "terminal state")
		r := compare(b, b)
		require(r.Cost.BytesSent == 35 && r.Cost.BytesReceived == 1, "equal bytes")
		return "four ordered levels, stable repeated requests, 35+1 equal bytes and ErrSession guards"
	})
	check("C10", "malformed-wire", func() string {
		t := tree(3, nil)
		for _, q := range [][]byte{nil, {1}, {1, 3}, {1, 3, 1, 0}, {1, 3, 1, 0x81, 0, 0}, {1, 3, 1, 1, 1}, {1, 3, 1, 2, 0, 0}} {
			_, e := t.Answer(q)
			require(errors.Is(e, m.ErrMalformed), "request=%x err=%v", q, e)
		}
		for _, p := range [][]byte{nil, {4, 0}, {2, 0, 0}, {2, 0, 1, 128, 0}, {3, 0, 1, 0}, {2, 0, 1, 0, 1, 0}} {
			s := t.Diff(m.Limits{})
			s.Request()
			e := s.Feed(p)
			require(errors.Is(e, m.ErrMalformed) && s.Done(), "reply=%x err=%v", p, e)
		}
		return "13 truncated, noncanonical, unordered and wrong-kind messages rejected"
	})
	check("C17", "random-wire", func() string {
		r := rand.New(rand.NewPCG(seed, 17))
		t := tree(3, entries(30, 0))
		for i := 0; i < 20000; i++ {
			b := make([]byte, r.IntN(300))
			for j := range b {
				b[j] = byte(r.Uint32())
			}
			_, e := t.Answer(b)
			require(e == nil || errors.Is(e, m.ErrMalformed) || errors.Is(e, m.ErrDepth), "request err=%v iteration=%d", e, i)
			s := t.Diff(m.Limits{})
			s.Request()
			e = s.Feed(b)
			require(e == nil || errors.Is(e, m.ErrMalformed) || errors.Is(e, m.ErrDepth), "reply err=%v iteration=%d", e, i)
		}
		return "20000 deterministic byte strings produce no panic or unexpected error class"
	})
	check("C25", "depth-wire", func() string {
		t := tree(3, nil)
		for _, q := range [][]byte{{1, 0, 0}, {1, 17, 0}, {1, 3, 4}} {
			_, e := t.Answer(q)
			require(errors.Is(e, m.ErrDepth), "request=%x err=%v", q, e)
		}
		_, e := m.Compare(t, tree(4, nil), m.Limits{})
		require(errors.Is(e, m.ErrDepth), "different depths err=%v", e)
		return "depth zero, overflow, excessive level and peer depth mismatch reject with ErrDepth"
	})
	check("C16", "changing-responder", func() string {
		r := rand.New(rand.NewPCG(seed, 16))
		for trial := 0; trial < 200; trial++ {
			depth := 1 + r.IntN(16)
			cap := 1 + r.IntN(16)
			a := tree(depth, entries(50, 0))
			root := a.Root()
			s := a.Diff(m.Limits{Frontier: cap})
			n := 0
			for !s.Done() && n < depth+cap+1 {
				q, _ := s.Request()
				b := tree(depth, entries(r.IntN(200), 0))
				must(b.Set(fmt.Sprint(n, trial), m.Hash{}))
				p, e := b.Answer(q)
				must(e)
				e = s.Feed(p)
				require(e == nil || errors.Is(e, m.ErrMalformed) || errors.Is(e, m.ErrDepth), "err=%v", e)
				n++
			}
			require(s.Done() && n <= depth+cap && a.Root() == root, "trial=%d depth=%d cap=%d trips=%d", trial, depth, cap, n)
		}
		return "200 changing-peer sessions terminate within Depth+Frontier without changing the initiator"
	})
	check("C16", "one-leaf-per-reply", func() string {
		a, b := tree(1, nil), tree(1, entries(200, 0))
		s := a.Diff(m.Limits{Frontier: 16})
		for !s.Done() {
			q, _ := s.Request()
			if q[2] == 1 {
				q = []byte{1, 1, 1, 1, q[4]}
			}
			p, e := b.Answer(q)
			must(e)
			must(s.Feed(p))
		}
		exact(a, b, s.Result())
		require(s.Result().Cost.RoundTrips <= 17, "bounded progress")
		return fmt.Sprintf("well-formed prefix replies finish in %d trips (Depth+Frontier bound)", s.Result().Cost.RoundTrips)
	})
	check("C24", "leaf-path-validation", func() string {
		a, b := tree(1, nil), tree(1, []m.Entry{{Key: "x"}})
		s := a.Diff(m.Limits{})
		q, _ := s.Request()
		p, e := b.Answer(q)
		must(e)
		must(s.Feed(p))
		s.Request()
		p = []byte{3, 1, 1, 1, 1, 'y'}
		p = append(p, make([]byte, 32)...)
		require(errors.Is(s.Feed(p), m.ErrMalformed) && s.Done(), "wrong-path leaf accepted")
		return "a sorted validly encoded key in the wrong leaf is rejected"
	})
}
func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "tree":
		trees()
	case "wire":
		wire()
	case "sim":
		simChecks()
	default:
		os.Exit(2)
	}
	if broken > 0 {
		os.Exit(1)
	}
}
