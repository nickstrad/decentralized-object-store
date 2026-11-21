package merkle_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/merkle"
)

func TestDiffProtocolSteps(t *testing.T) {
	m := map[string]merkle.Hash{}
	for i := 0; i < 40; i++ {
		m[fmt.Sprintf("key-%d", i)] = sum(fmt.Sprint(i))
	}
	local := treeOf(t, 2, m)
	m["key-7"] = sum("changed")
	remote := treeOf(t, 2, m)

	s := local.Diff(merkle.Limits{})
	if s.Done() {
		t.Fatal("a new session is already done")
	}
	if err := s.Feed([]byte{0x04}); !errors.Is(err, merkle.ErrSession) {
		t.Fatalf("Feed before Request = %v, want ErrSession", err)
	}
	for level := 0; level <= 2; level++ {
		req, ok := s.Request()
		if !ok {
			t.Fatalf("level %d: session ended early", level)
		}
		again, _ := s.Request()
		if string(again) != string(req) {
			t.Fatalf("level %d: Request is not stable while waiting", level)
		}
		if req[0] != 0x01 || req[1] != 2 || int(req[2]) != level {
			t.Fatalf("level %d: request header % x", level, req[:3])
		}
		if level == 0 && len(req) != 3+32 {
			t.Fatalf("root request is %d bytes, want 35", len(req))
		}
		if level > 0 && (req[3] != 1 || len(req) != 4+(level+1)/2) {
			t.Fatalf("level %d: request % x names more than the one mismatched node", level, req)
		}
		resp, err := remote.Answer(req)
		if err != nil {
			t.Fatalf("level %d: Answer: %v", level, err)
		}
		wantKind := byte(0x02)
		if level == 2 {
			wantKind = 0x03
		}
		if resp[0] != wantKind || int(resp[1]) != level {
			t.Fatalf("level %d: response header % x", level, resp[:2])
		}
		if err := s.Feed(resp); err != nil {
			t.Fatalf("level %d: Feed: %v", level, err)
		}
	}
	if !s.Done() || s.Err() != nil {
		t.Fatalf("session done=%v err=%v", s.Done(), s.Err())
	}
	if _, ok := s.Request(); ok {
		t.Fatal("a finished session asks for another request")
	}
	if err := s.Feed([]byte{0x04}); !errors.Is(err, merkle.ErrSession) {
		t.Fatalf("Feed after the end = %v, want ErrSession", err)
	}
	res := s.Result()
	want := merkle.Difference{Key: "key-7", Local: sum("7"), Remote: sum("changed"), HasLocal: true, HasRemote: true}
	if len(res.Diffs) != 1 || res.Diffs[0] != want {
		t.Fatalf("diffs %+v, want %+v", res.Diffs, want)
	}
	if res.Cost.RoundTrips != 3 || res.Truncated {
		t.Fatalf("cost %+v truncated %v", res.Cost, res.Truncated)
	}
	if res.Cost.Hashes > 1+16*2+remote.LeafLoad() {
		t.Fatalf("hashes %d over the one-key bound", res.Cost.Hashes)
	}
	if res.Cost.BytesSent == 0 || res.Cost.BytesReceived == 0 || res.Cost.Nodes != 3 {
		t.Fatalf("cost accounting %+v", res.Cost)
	}

	t.Run("equal roots end after one round trip", func(t *testing.T) {
		s := local.Diff(merkle.Limits{})
		req, _ := s.Request()
		resp, err := local.Answer(req)
		if err != nil || len(resp) != 1 || resp[0] != 0x04 {
			t.Fatalf("equal answer % x %v", resp, err)
		}
		if err := s.Feed(resp); err != nil || !s.Done() {
			t.Fatalf("equal feed: %v done=%v", err, s.Done())
		}
	})
}

func TestDiffLeafRepliesAreBatched(t *testing.T) {
	remote := map[string]merkle.Hash{}
	for i := 0; i < 1600; i++ {
		remote[fmt.Sprintf("%04d-%s", i, strings.Repeat("v", 1000))] = sum(fmt.Sprint(i))
	}
	lt := treeOf(t, 1, nil)
	rt := treeOf(t, 1, remote)
	res, err := merkle.Compare(lt, rt, merkle.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkComplete(nil, remote, res); err != nil {
		t.Fatal(err)
	}
	if res.Cost.RoundTrips <= 2 {
		t.Fatalf("1.6 MB of leaf entries came back in %d round trips; the reply cap should split them", res.Cost.RoundTrips)
	}
	if res.Cost.BytesReceived < 1600*1000 {
		t.Fatalf("received %d bytes", res.Cost.BytesReceived)
	}
}

func TestDiffFrontierTruncates(t *testing.T) {
	remote := map[string]merkle.Hash{}
	for i := 0; i < 300; i++ {
		remote[fmt.Sprintf("k%d", i)] = sum(fmt.Sprint(i))
	}
	rt := treeOf(t, 2, remote)
	lt := treeOf(t, 2, nil)
	lim := merkle.Limits{Frontier: 4}
	sawTruncated := false
	for round := 1; ; round++ {
		res, err := merkle.Compare(lt, rt, lim)
		if err != nil {
			t.Fatal(err)
		}
		if res.Cost.RoundTrips > lt.Depth()+1 {
			t.Fatalf("round %d: %d round trips", round, res.Cost.RoundTrips)
		}
		if res.Cost.Nodes > 1+lim.Frontier*lt.Depth() {
			t.Fatalf("round %d: visited %d nodes with frontier %d", round, res.Cost.Nodes, lim.Frontier)
		}
		if res.Truncated {
			sawTruncated = true
			if len(res.Diffs) == 0 {
				t.Fatalf("round %d: truncated without a difference", round)
			}
		}
		if len(res.Diffs) == 0 {
			if res.Truncated {
				t.Fatalf("round %d: truncated with nothing found", round)
			}
			break
		}
		for _, d := range res.Diffs {
			if !d.HasRemote || d.HasLocal || remote[d.Key] != d.Remote {
				t.Fatalf("round %d: spurious difference %+v", round, d)
			}
			if err := lt.Set(d.Key, d.Remote); err != nil {
				t.Fatal(err)
			}
		}
		if round > 300 {
			t.Fatal("truncated diffs made no progress")
		}
	}
	if lt.Root() != rt.Root() {
		t.Fatal("trees differ after repairing every truncated diff")
	}
	if !sawTruncated {
		t.Fatal("no round reported Truncated with 300 differences and a frontier of 4")
	}
}

func TestDiffErrors(t *testing.T) {
	two := treeOf(t, 2, map[string]merkle.Hash{"a": sum("1")})
	three := treeOf(t, 3, map[string]merkle.Hash{"a": sum("1")})

	t.Run("trees of different depth are not comparable", func(t *testing.T) {
		_, err := merkle.Compare(two, three, merkle.Limits{})
		if !errors.Is(err, merkle.ErrDepth) {
			t.Fatalf("Compare = %v, want ErrDepth", err)
		}
	})
	t.Run("a request deeper than the tree is a depth overflow", func(t *testing.T) {
		_, err := two.Answer([]byte{0x01, 2, 3, 1, 0x00, 0x00})
		if !errors.Is(err, merkle.ErrDepth) {
			t.Fatalf("Answer = %v, want ErrDepth", err)
		}
		_, err = two.Answer([]byte{0x01, merkle.MaxDepth + 1, 0})
		if !errors.Is(err, merkle.ErrDepth) {
			t.Fatalf("Answer with depth %d = %v, want ErrDepth", merkle.MaxDepth+1, err)
		}
	})
	t.Run("garbage requests are malformed", func(t *testing.T) {
		for _, b := range [][]byte{nil, {0x09}, {0x01, 2}, {0x01, 2, 0}, {0x01, 2, 1, 0}, {0x01, 2, 1, 1}} {
			if _, err := two.Answer(b); !errors.Is(err, merkle.ErrMalformed) {
				t.Errorf("Answer(% x) = %v, want ErrMalformed", b, err)
			}
		}
	})
	t.Run("a malformed node in a reply fails the session", func(t *testing.T) {
		replies := [][]byte{
			nil,
			{0x04, 0x00},
			{0x02, 0, 1, 0x80},
			{0x02, 1, 1, 0x00, 0x00},
			{0x02, 0, 2, 0x00, 0x00, 0x00, 0x00},
			{0x03, 0, 1, 0},
			append([]byte{0x02, 0, 1, 0x80, 0x00}, make([]byte, 32)...),
		}
		for _, b := range replies {
			s := two.Diff(merkle.Limits{})
			if _, ok := s.Request(); !ok {
				t.Fatal("no request")
			}
			err := s.Feed(b)
			if !errors.Is(err, merkle.ErrMalformed) {
				t.Errorf("Feed(% x) = %v, want ErrMalformed", b, err)
			}
			if !s.Done() || !errors.Is(s.Err(), merkle.ErrMalformed) {
				t.Errorf("Feed(% x): done=%v err=%v", b, s.Done(), s.Err())
			}
			if _, ok := s.Request(); ok {
				t.Errorf("Feed(% x): a failed session asks for more", b)
			}
		}
	})
	t.Run("an equal reply below the root is malformed", func(t *testing.T) {
		other := treeOf(t, 2, map[string]merkle.Hash{"b": sum("1")})
		s := two.Diff(merkle.Limits{})
		req, _ := s.Request()
		resp, err := other.Answer(req)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Feed(resp); err != nil {
			t.Fatal(err)
		}
		if err := s.Feed([]byte{0x04}); !errors.Is(err, merkle.ErrSession) {
			t.Fatalf("Feed without a request = %v", err)
		}
		if _, ok := s.Request(); !ok {
			t.Fatal("no second request")
		}
		if err := s.Feed([]byte{0x04}); !errors.Is(err, merkle.ErrMalformed) {
			t.Fatalf("equal at level 1 = %v, want ErrMalformed", err)
		}
	})
	t.Run("a leaf entry outside the requested leaf is malformed", func(t *testing.T) {
		lt := treeOf(t, 1, map[string]merkle.Hash{"a": sum("1")})
		liar := treeOf(t, 1, nil)
		for i := 0; i < 40; i++ {
			if err := liar.Set(fmt.Sprint(i), sum("x")); err != nil {
				t.Fatal(err)
			}
		}
		s := lt.Diff(merkle.Limits{})
		req, _ := s.Request()
		resp, _ := liar.Answer(req)
		if err := s.Feed(resp); err != nil {
			t.Fatal(err)
		}
		req, _ = s.Request()
		honest, err := liar.Answer(req)
		if err != nil {
			t.Fatal(err)
		}
		forged := forgeLeafReply(t, req, honest)
		if err := s.Feed(forged); !errors.Is(err, merkle.ErrMalformed) {
			t.Fatalf("forged leaf = %v, want ErrMalformed", err)
		}
	})
	t.Run("Fail abandons a session with the transport error", func(t *testing.T) {
		s := two.Diff(merkle.Limits{})
		s.Request()
		cause := errors.New("link down")
		s.Fail(cause)
		if !s.Done() || !errors.Is(s.Err(), cause) {
			t.Fatalf("done=%v err=%v", s.Done(), s.Err())
		}
		s.Fail(errors.New("second"))
		if !errors.Is(s.Err(), cause) {
			t.Fatal("a second Fail replaced the first error")
		}
	})
}

func forgeLeafReply(t *testing.T, req, honest []byte) []byte {
	t.Helper()
	if honest[0] != 0x03 {
		t.Fatalf("expected a leaf reply, got % x", honest[:2])
	}
	n := int(req[3])
	out := []byte{0x03, 1, byte(n)}
	for i := 0; i < n; i++ {
		out = append(out, 1, 1, 'Z')
		h := sum("forged")
		out = append(out, h[:]...)
	}
	if keyPath("Z")[0]>>4 == req[4]>>4 {
		t.Fatal("pick another forged key")
	}
	return out
}
