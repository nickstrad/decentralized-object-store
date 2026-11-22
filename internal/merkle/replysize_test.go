package merkle

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"

	"pgregory.net/rapid"
)

const (
	decodeAllocPerByte = 4
	decodeAllocBase    = 64 << 10
	leafItemMaxBytes   = 2 + MaxKeyLen + 32 + 2
)

// allocated returns the minimum TotalAlloc across three calls to f to filter
// out one-off background runtime allocations.
func allocated(f func()) uint64 {
	best := ^uint64(0)
	for i := 0; i < 3; i++ {
		var a, b runtime.MemStats
		runtime.ReadMemStats(&a)
		f()
		runtime.ReadMemStats(&b)
		best = min(best, b.TotalAlloc-a.TotalAlloc)
	}
	return best
}

func checkDecodeAlloc(t interface{ Fatalf(string, ...any) }, b []byte) {
	n := allocated(func() { _, _ = decodeResponse(b) })
	if bound := uint64(decodeAllocPerByte*len(b) + decodeAllocBase); n > bound {
		t.Fatalf("decodeResponse allocated %d bytes from %d input bytes, bound %d", n, len(b), bound)
	}
}

func groundLeaf(n, keyLen int) []Entry {
	var out []Entry
	for i := 0; len(out) < n; i++ {
		k := fmt.Sprintf("ground-%08d", i)
		k += strings.Repeat("z", keyLen-len(k))
		if h := sha256.Sum256([]byte(k)); h[0]>>4 == 0 {
			out = append(out, Entry{Key: k, Hash: Sum([]byte(k))})
		}
	}
	slices.SortFunc(out, func(a, b Entry) int { return strings.Compare(a.Key, b.Key) })
	return out
}

// TestReplySizeBound proves that MaxRequestNodes caps the largest inner reply
// within MaxReplyBytes, that a single leaf of 992 maximal keys is cut within
// MaxReplyBytes, and that decodeResponse allocates proportionally to input size
// even on hostile or all-empty replies.
func TestReplySizeBound(t *testing.T) {
	t.Run("the frontier is capped so the largest inner reply fits", func(t *testing.T) {
		if got := (Limits{Frontier: 4096}).frontier(); got != MaxRequestNodes {
			t.Fatalf("frontier(4096) = %d, want MaxRequestNodes %d", got, MaxRequestNodes)
		}
		r := response{kind: msgInner, level: 3}
		for i := 0; i < MaxRequestNodes; i++ {
			r.bitmaps = append(r.bitmaps, 0xffff)
			for j := 0; j < Fanout; j++ {
				r.hashes = append(r.hashes, h32(byte(j+1)))
			}
		}
		b := encodeResponse(r)
		if len(b) > MaxReplyBytes {
			t.Fatalf("a full inner reply of MaxRequestNodes nodes is %d bytes", len(b))
		}
		if _, err := decodeResponse(b); err != nil {
			t.Fatal(err)
		}
		r.bitmaps = append(r.bitmaps, 0)
		if _, err := decodeResponse(encodeResponse(r)); !errors.Is(err, ErrMalformed) {
			t.Fatalf("an inner reply of MaxRequestNodes+1 nodes decoded: %v", err)
		}
	})
	t.Run("C8 a single leaf of 992 maximal keys answers within MaxReplyBytes", func(t *testing.T) {
		tr, err := Build(1, groundLeaf(992, MaxKeyLen))
		if err != nil {
			t.Fatal(err)
		}
		out, err := tr.Answer([]byte{0x01, 1, 1, 1, 0x00})
		if err != nil {
			t.Fatal(err)
		}
		if len(out) > MaxReplyBytes {
			t.Fatalf("reply %d bytes > %d", len(out), MaxReplyBytes)
		}
		r, err := decodeResponse(out)
		if err != nil || !r.more || len(r.leaves) != 1 {
			t.Fatalf("reply more=%v leaves=%d err=%v", r.more, len(r.leaves), err)
		}
		if len(out) <= MaxReplyBytes-leafItemMaxBytes {
			t.Fatalf("a cut reply of %d bytes is not full", len(out))
		}
	})
	t.Run("the reviewer's 32 KiB inner reply is rejected before allocation", func(t *testing.T) {
		b := append([]byte{0x02, 0x00, 0x80, 0x80, 0x01}, make([]byte, 32768)...)
		if _, err := decodeResponse(b); !errors.Is(err, ErrMalformed) {
			t.Fatalf("decodeResponse = %v", err)
		}
		checkDecodeAlloc(t, b)
	})
	t.Run("MaxRequestNodes empty inner nodes allocate in proportion to the input", func(t *testing.T) {
		r := response{kind: msgInner, level: 2, bitmaps: make([]uint16, MaxRequestNodes)}
		b := encodeResponse(r)
		if _, err := decodeResponse(b); err != nil {
			t.Fatal(err)
		}
		checkDecodeAlloc(t, b)
	})
	t.Run("MaxRequestNodes empty leaves allocate in proportion to the input", func(t *testing.T) {
		r := response{kind: msgLeaves, level: 2, leaves: make([][]Entry, MaxRequestNodes)}
		b := encodeResponse(r)
		if _, err := decodeResponse(b); err != nil {
			t.Fatal(err)
		}
		checkDecodeAlloc(t, b)
	})
}

func genReplyTree(t *rapid.T) *Tree {
	depth := rapid.IntRange(1, 3).Draw(t, "depth")
	keyLen := rapid.SampledFrom([]int{1, 8, 200, 900, MaxKeyLen}).Draw(t, "keylen")
	n := rapid.IntRange(0, 1300).Draw(t, "n")
	tr, err := New(depth)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("%x", i)
		if len(k) < keyLen {
			k += strings.Repeat("q", keyLen-len(k))
		}
		if err := tr.Set(k, Hash{byte(i), 1}); err != nil {
			t.Fatal(err)
		}
	}
	return tr
}

func genRequest(t *rapid.T, tr *Tree) request {
	level := rapid.IntRange(0, tr.depth).Draw(t, "level")
	req := request{depth: tr.depth, level: level}
	if level == 0 {
		if rapid.Bool().Draw(t, "equal-root") {
			req.root = tr.Root()
		} else {
			req.root = h32(9)
		}
		return req
	}
	width := uint64(1) << (4 * min(level, 3))
	count := rapid.IntRange(1, int(min(width, MaxRequestNodes))).Draw(t, "count")
	seen := map[uint64]bool{}
	for i := 0; i < count; i++ {
		v := rapid.Uint64Range(0, width-1).Draw(t, "path")
		p := v << (64 - 4*min(level, 3))
		if !seen[p] {
			seen[p] = true
			req.paths = append(req.paths, p)
		}
	}
	slices.Sort(req.paths)
	if level == tr.depth && rapid.Bool().Draw(t, "resume") {
		req.resume = true
		keys := tr.Keys()
		if len(keys) > 0 && rapid.Bool().Draw(t, "resume-from-tree") {
			req.after = keys[rapid.IntRange(0, len(keys)-1).Draw(t, "after")]
		} else {
			req.after = rapid.StringN(0, 8, -1).Draw(t, "after-any")
		}
	}
	return req
}

// TestPropReplySizeBound proves that Tree.Answer emits a decodable reply of at
// most MaxReplyBytes for every valid request across arbitrary trees and resume
// keys, always answers at least one leaf, fills early-stopping replies, and
// returns only entries strictly after the resume key.
func TestPropReplySizeBound(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		tr := genReplyTree(t)
		req := genRequest(t, tr)
		out, err := tr.Answer(encodeRequest(req))
		if err != nil {
			t.Fatalf("Answer of a valid request: %v", err)
		}
		if len(out) > MaxReplyBytes {
			t.Fatalf("reply %d bytes > MaxReplyBytes", len(out))
		}
		r, err := decodeResponse(out)
		if err != nil {
			t.Fatalf("reply does not decode: %v", err)
		}
		if r.kind != msgLeaves {
			return
		}
		if len(r.leaves) == 0 || len(r.leaves) > len(req.paths) {
			t.Fatalf("leaf reply answers %d of %d leaves", len(r.leaves), len(req.paths))
		}
		if (r.more || len(r.leaves) < len(req.paths)) && len(out) <= MaxReplyBytes-leafItemMaxBytes {
			t.Fatalf("a reply that stops early is %d bytes, not full", len(out))
		}
		if req.resume {
			for _, e := range r.leaves[0] {
				if e.Key <= req.after {
					t.Fatalf("resumed leaf returned %q, not after %q", e.Key, req.after)
				}
			}
		}
	})
}

// TestPropDecodeAllocationBound proves that decodeResponse allocates at most
// 4*len(b) + 64 KiB on arbitrary raw bytes, truncated empty-inner replies, and
// truncated real replies.
func TestPropDecodeAllocationBound(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var b []byte
		switch rapid.IntRange(0, 2).Draw(t, "shape") {
		case 0:
			b = rapid.SliceOfN(rapid.Byte(), 0, 4096).Draw(t, "raw")
		case 1:
			n := rapid.IntRange(0, MaxRequestNodes+2).Draw(t, "n")
			b = encodeResponse(response{kind: msgInner, level: 1, bitmaps: make([]uint16, n)})
			b = b[:rapid.IntRange(0, len(b)).Draw(t, "cut")]
		default:
			tr := genReplyTree(t)
			req := genRequest(t, tr)
			out, err := tr.Answer(encodeRequest(req))
			if err != nil {
				t.Fatal(err)
			}
			b = out[:rapid.IntRange(0, len(out)).Draw(t, "cut")]
		}
		checkDecodeAlloc(t, b)
	})
}
