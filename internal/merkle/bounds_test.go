package merkle_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/merkle"
	"pgregory.net/rapid"
)

func groundKeys(n, keyLen, leafNibbles int) []string {
	return groundKeysFrom("ground", n, keyLen, leafNibbles)
}

func groundKeysFrom(label string, n, keyLen, leafNibbles int) []string {
	var out []string
	for i := 0; len(out) < n; i++ {
		k := fmt.Sprintf("%s-%08d", label, i)
		k += strings.Repeat("z", keyLen-len(k))
		h := sha256.Sum256([]byte(k))
		ok := h[0]>>4 == 0
		if leafNibbles >= 2 {
			ok = h[0] == 0
		}
		if ok {
			out = append(out, k)
		}
	}
	return out
}

func builtOf(t fataler, depth int, m map[string]merkle.Hash) *merkle.Tree {
	t.Helper()
	tr, err := merkle.Build(depth, entriesOf(m))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return tr
}

func paddedKeys(n, keyLen int) map[string]merkle.Hash {
	m := map[string]merkle.Hash{}
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("key-%08d", i)
		k += strings.Repeat("x", keyLen-len(k))
		m[k] = sum(k)
	}
	return m
}

func TestDiffBoundsPinned(t *testing.T) {
	t.Run("C8 one leaf of 992 maximal keys is split under MaxReplyBytes", func(t *testing.T) {
		remote := map[string]merkle.Hash{}
		for _, k := range groundKeys(992, merkle.MaxKeyLen, 1) {
			remote[k] = sum(k)
		}
		dr := drive(t, treeOf(t, 1, nil), builtOf(t, 1, remote), merkle.Limits{})
		if err := checkComplete(nil, remote, dr.res); err != nil {
			t.Fatal(err)
		}
		if len(dr.leafSizes) != 2 || dr.res.Cost.RoundTrips != 3 {
			t.Fatalf("leaf replies %v, round trips %d; want 2 and 3", dr.leafSizes, dr.res.Cost.RoundTrips)
		}
		checkShape(t, 1, nil, remote, dr)
	})
	t.Run("a full leaf of maximal keys is split into five replies", func(t *testing.T) {
		remote := map[string]merkle.Hash{}
		for _, k := range groundKeys(merkle.MaxLeafEntries, merkle.MaxKeyLen, 1) {
			remote[k] = sum(k)
		}
		dr := drive(t, treeOf(t, 1, nil), builtOf(t, 1, remote), merkle.Limits{})
		if err := checkComplete(nil, remote, dr.res); err != nil {
			t.Fatal(err)
		}
		if len(dr.leafSizes) != 5 {
			t.Fatalf("leaf replies %v, want 5", dr.leafSizes)
		}
		checkShape(t, 1, nil, remote, dr)
	})
	t.Run("C12 984 long keys fit one leaf reply with exact sizing", func(t *testing.T) {
		for _, n := range []int{983, 984} {
			remote := paddedKeys(n, merkle.MaxKeyLen)
			dr := drive(t, treeOf(t, 1, nil), builtOf(t, 1, remote), merkle.Limits{})
			if err := checkComplete(nil, remote, dr.res); err != nil {
				t.Fatal(err)
			}
			if dr.res.Cost.RoundTrips != 2 {
				t.Fatalf("n=%d: %d round trips, want Depth+1 = 2", n, dr.res.Cost.RoundTrips)
			}
			checkShape(t, 1, nil, remote, dr)
		}
	})
	t.Run("C12 the first extra batch appears exactly when the leaf bytes pass one reply", func(t *testing.T) {
		fits := func(k int) bool {
			sh := expectedShape(1, nil, paddedKeys(k, merkle.MaxKeyLen))
			return sh.leafBytes+2+uvarintLen(sh.leaves) <= merkle.MaxReplyBytes
		}
		lo, hi := 1, 2000
		for lo+1 < hi {
			mid := (lo + hi) / 2
			if fits(mid) {
				lo = mid
			} else {
				hi = mid
			}
		}
		n := lo
		if !fits(n) || fits(n+1) {
			t.Fatalf("search ended at %d", n)
		}
		for _, k := range []int{n, n + 1} {
			remote := paddedKeys(k, merkle.MaxKeyLen)
			dr := drive(t, treeOf(t, 1, nil), builtOf(t, 1, remote), merkle.Limits{})
			want := 2
			if k > n {
				want = 3
			}
			if dr.res.Cost.RoundTrips != want {
				t.Fatalf("%d keys: %d round trips, want %d", k, dr.res.Cost.RoundTrips, want)
			}
			checkShape(t, 1, nil, remote, dr)
		}
	})
	t.Run("C21 a responder answering one leaf per reply costs one trip per leaf", func(t *testing.T) {
		remote := map[string]merkle.Hash{"prefix-0": sum("0"), "prefix-1": sum("1")}
		if prefixOf("prefix-0", 1) == prefixOf("prefix-1", 1) {
			t.Fatal("fixture keys share a leaf")
		}
		rt := builtOf(t, 1, remote)
		s := treeOf(t, 1, nil).Diff(merkle.Limits{})
		for {
			req, ok := s.Request()
			if !ok {
				break
			}
			resp, err := rt.Answer(req)
			if err != nil {
				t.Fatal(err)
			}
			if resp[0] == 0x03 {
				resp = firstLeafOnly(t, resp)
			}
			if err := s.Feed(resp); err != nil {
				t.Fatal(err)
			}
		}
		res := s.Result()
		if err := checkComplete(nil, remote, res); err != nil {
			t.Fatal(err)
		}
		if res.Cost.RoundTrips != 1+2 {
			t.Fatalf("round trips %d, want Depth + 2 leaf replies = 3", res.Cost.RoundTrips)
		}
		if res.Cost.RoundTrips > hostileTripBound(1, merkle.DefaultFrontier) {
			t.Fatal("over the any-peer bound")
		}
	})
	t.Run("C31 a clustered leaf costs its load in hashes", func(t *testing.T) {
		for _, n := range []int{45, 46} {
			local := map[string]merkle.Hash{}
			for _, k := range groundKeys(n, 32, 2) {
				local[k] = sum(k)
			}
			if merkle.DepthFor(n) != 2 {
				t.Fatalf("DepthFor(%d) = %d", n, merkle.DepthFor(n))
			}
			remote := map[string]merkle.Hash{}
			for k, v := range local {
				remote[k] = v
			}
			remote[groundKeys(1, 32, 2)[0]] = merkle.Hash{}
			dr := drive(t, treeOf(t, 2, local), treeOf(t, 2, remote), merkle.Limits{})
			checkShape(t, 2, local, remote, dr)
			if want := 1 + 1 + 1 + n; dr.res.Cost.Hashes != want {
				t.Fatalf("n=%d: %d hashes, want root+1+1+leaf load = %d", n, dr.res.Cost.Hashes, want)
			}
			if dr.res.Cost.Hashes > costBound(1, 2, n) {
				t.Fatal("over the leaf-load bound")
			}
			if over := dr.res.Cost.Hashes > 16*1*(2+1); over != (n > 45) {
				t.Fatalf("n=%d: 16·d·(Depth+1) exceeded=%v", n, over)
			}
		}
	})
}

func TestDiffRejectsCheapCuts(t *testing.T) {
	t.Run("a cut reply that is not full is malformed", func(t *testing.T) {
		remote := map[string]merkle.Hash{}
		for _, k := range groundKeys(50, 32, 1) {
			remote[k] = sum(k)
		}
		rt := builtOf(t, 1, remote)
		s := treeOf(t, 1, nil).Diff(merkle.Limits{})
		req, _ := s.Request()
		resp, err := rt.Answer(req)
		if err != nil || s.Feed(resp) != nil {
			t.Fatalf("root step: %v", err)
		}
		req, _ = s.Request()
		resp, err = rt.Answer(req)
		if err != nil {
			t.Fatal(err)
		}
		body := resp[3:]
		kl := int(body[1])
		cut := append([]byte{0x05, 1, 1, 1}, body[1:2+kl+32]...)
		if err := s.Feed(cut); !errors.Is(err, merkle.ErrMalformed) {
			t.Fatalf("a one-entry cut reply = %v, want ErrMalformed", err)
		}
	})
	t.Run("pieces of one leaf past MaxLeafEntries are malformed", func(t *testing.T) {
		honest := map[string]merkle.Hash{}
		for _, k := range groundKeysFrom("a", merkle.MaxLeafEntries, merkle.MaxKeyLen, 1) {
			honest[k] = sum(k)
		}
		extra := map[string]merkle.Hash{}
		for _, k := range groundKeysFrom("b", 1200, merkle.MaxKeyLen, 1) {
			extra[k] = sum(k)
		}
		at, bt := builtOf(t, 1, honest), builtOf(t, 1, extra)
		s := treeOf(t, 1, nil).Diff(merkle.Limits{Frontier: 1})
		var err error
		for trips := 0; trips < 20; trips++ {
			req, ok := s.Request()
			if !ok {
				break
			}
			resp, aerr := at.Answer(req)
			if aerr != nil {
				t.Fatal(aerr)
			}
			if resp[0] == 0x03 {
				resp, aerr = bt.Answer(req)
				if aerr != nil {
					t.Fatal(aerr)
				}
			}
			if err = s.Feed(resp); err != nil {
				break
			}
		}
		if !errors.Is(err, merkle.ErrMalformed) {
			t.Fatalf("a leaf grown past MaxLeafEntries across pieces = %v, want ErrMalformed", err)
		}
	})
}

func TestDiffRejectsARepeatedPiece(t *testing.T) {
	remote := map[string]merkle.Hash{}
	for _, k := range groundKeys(1500, merkle.MaxKeyLen, 1) {
		remote[k] = sum(k)
	}
	rt := builtOf(t, 1, remote)
	s := treeOf(t, 1, nil).Diff(merkle.Limits{Frontier: 1})
	trips := 0
	var err error
	for {
		req, ok := s.Request()
		if !ok {
			break
		}
		trips++
		if req[2] == 1 {
			req = []byte{0x01, 1, 1, 1, req[4]}
		}
		resp, aerr := rt.Answer(req)
		if aerr != nil {
			t.Fatal(aerr)
		}
		if err = s.Feed(resp); err != nil {
			break
		}
	}
	if !errors.Is(err, merkle.ErrMalformed) || trips != 3 {
		t.Fatalf("a responder that ignores the resume key: err=%v after %d round trips, want ErrMalformed after 3", err, trips)
	}
}

func firstLeafOnly(t *testing.T, resp []byte) []byte {
	t.Helper()
	if resp[0] != 0x03 || resp[2] < 1 {
		t.Fatalf("unexpected leaf reply header % x", resp[:3])
	}
	if resp[2] == 1 {
		return resp
	}
	body := resp[3:]
	m := int(body[0])
	end := 1
	for i := 0; i < m; i++ {
		kl := int(body[end])
		end += 1 + kl + 32
	}
	return append([]byte{0x03, resp[1], 1}, body[:end]...)
}

func genLongPair(t *rapid.T) mapPair {
	depth := rapid.IntRange(1, 2).Draw(t, "depth")
	keyLen := rapid.IntRange(16, merkle.MaxKeyLen).Draw(t, "keylen")
	n := rapid.IntRange(0, 1400).Draw(t, "n")
	remote := map[string]merkle.Hash{}
	for i := 0; i < n; i++ {
		k := fmt.Sprintf("%d-", i)
		k += strings.Repeat("y", keyLen-len(k))
		remote[k] = merkle.Hash{byte(i), byte(i >> 8), 1}
	}
	local := map[string]merkle.Hash{}
	keep := rapid.IntRange(0, 100).Draw(t, "keep-percent")
	for k, v := range remote {
		if int(v[0])*100/256 < keep {
			local[k] = v
		}
	}
	return mapPair{depth: depth, local: local, remote: remote}
}

func TestPropLongKeyDiffShape(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := genLongPair(t)
		local, remote := builtOf(t, p.depth, p.local), builtOf(t, p.depth, p.remote)
		dr := drive(t, local, remote, merkle.Limits{})
		if err := checkComplete(p.local, p.remote, dr.res); err != nil {
			t.Fatal(err)
		}
		checkShape(t, p.depth, p.local, p.remote, dr)
		checkCost(t, local, remote, len(refDiff(p.local, p.remote)), dr.res)
	})
}
