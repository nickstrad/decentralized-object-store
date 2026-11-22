package occ_test

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
)

func TestDiscardGapNeverClaimed(t *testing.T) {
	for _, secondWriter := range []string{"a", "b"} {
		t.Run("next2 written by "+secondWriter, func(t *testing.T) {
			a, ca, la := newLoggedStore(t, "a", 1, 1)
			b, cb, _ := newLoggedStore(t, "b", 1, 2)
			c, _, lc := newLoggedStore(t, "c", 1, 3)
			mustPut(t, a, "k", "one", occ.IfNoneMatch())
			mustMerge(t, b, a.Doc())
			ca.now += 10
			cb.now += 20
			lost := mustPut(t, a, "k", "lost", occ.IfMatch(a.Record("k").ETag()))
			mustMerge(t, c, a.Doc())
			mustPut(t, b, "k", "winner", occ.IfMatch(b.Record("k").ETag()))
			mustMerge(t, a, b.Doc())
			if len(la.discards) != 1 || la.discards[0].Loser.ID() != lost.ID() {
				t.Fatal("setup: a must discard its own write")
			}
			ca.now += 100
			next1 := mustPut(t, a, "k", "next1", occ.IfMatch(a.Record("k").ETag()))
			w := a
			if secondWriter == "b" {
				mustMerge(t, b, a.Doc())
				cb.now += 1000
				w = b
			} else {
				ca.now += 100
			}
			next2 := mustPut(t, w, "k", "next2", occ.IfMatch(w.Record("k").ETag()))
			for _, v := range []occ.Version{next1, next2} {
				if v.Supersedes(lost) {
					t.Fatalf("%s claims the discarded dot %s:%d it never saw: clock %s", value(occ.NewRecord(v)), lost.Dot().Actor, lost.Dot().Counter, v.Clock())
				}
			}
			mustMerge(t, c, w.Doc())
			if c.LWWDiscards() != 1 || len(lc.discards) != 1 || lc.discards[0].Loser.ID() != lost.ID() || len(lc.lines) != 1 {
				t.Fatalf("c held the lost version and replaced it with next2 that never saw it: discards %d, hooks %d, lines %d", c.LWWDiscards(), len(lc.discards), len(lc.lines))
			}
		})
	}
}

func TestDotsAreNeverReused(t *testing.T) {
	a, ca, la := newLoggedStore(t, "a", 1, 1)
	b, cb, _ := newLoggedStore(t, "b", 1, 2)
	c, _, _ := newLoggedStore(t, "c", 1, 3)
	dots := map[occ.Dot]occ.VersionID{}
	note := func(v occ.Version) {
		t.Helper()
		if id, ok := dots[v.Dot()]; ok && id != v.ID() {
			t.Fatalf("dot %s:%d issued twice", v.Dot().Actor, v.Dot().Counter)
		}
		dots[v.Dot()] = v.ID()
	}
	note(mustPut(t, a, "k", "one", occ.IfNoneMatch()))
	mustMerge(t, b, a.Doc())
	ca.now += 10
	cb.now += 20
	lost := mustPut(t, a, "k", "lost", occ.IfMatch(a.Record("k").ETag()))
	note(lost)
	mustMerge(t, c, a.Doc())
	holding := c.Doc()
	note(mustPut(t, b, "k", "winner", occ.IfMatch(b.Record("k").ETag())))
	mustMerge(t, a, b.Doc())
	if len(la.discards) != 1 || la.discards[0].Loser.ID() != lost.ID() {
		t.Fatal("setup: a must discard its own write")
	}
	for i := range 6 {
		w, clk := a, ca
		if i%3 == 2 {
			mustMerge(t, b, a.Doc())
			w, clk = b, cb
		}
		clk.now += 100
		next := mustPut(t, w, "k", fmt.Sprintf("next%d", i+1), occ.IfMatch(w.Record("k").ETag()))
		note(next)
		if w == b {
			mustMerge(t, a, b.Doc())
		}
		if next.Supersedes(lost) {
			t.Fatalf("write %d claims the discarded dot: clock %s", i+1, next.Clock())
		}
		fresh, _, lf := newLoggedStore(t, "c", 1, 3)
		mustMerge(t, fresh, holding)
		mustMerge(t, fresh, w.Doc())
		if len(lf.discards) != 1 || lf.discards[0].Loser.ID() != lost.ID() {
			t.Fatalf("after write %d a node that held the lost version replaced it with no record", i+1)
		}
	}
}

func TestIncomingConcurrentLoserIsRecorded(t *testing.T) {
	a, b, _, lb, _, _ := concurrentPair(t)
	base := a.Doc()
	et := a.Record("k").ETag()
	left := mustPut(t, a, "k", "left", occ.IfMatch(et))
	right := mustPut(t, b, "k", "right", occ.IfMatch(et))
	if !lwwLess(left, right) {
		t.Fatal("setup: b's later write must win")
	}
	if mustMerge(t, b, a.Doc()) {
		t.Fatal("rejecting an incoming loser does not change the document")
	}
	if b.LWWDiscards() != 1 || len(lb.lines) != 1 || len(lb.discards) != 1 || len(b.DiscardTail()) != 1 {
		t.Fatalf("b rejected a concurrent acknowledged write with no record: discards %d, lines %d", b.LWWDiscards(), len(lb.lines))
	}
	d := lb.discards[0]
	if d.Key != "k" || d.Loser.ID() != left.ID() || d.Winner.ID() != right.ID() || !strings.Contains(lb.lines[0], "loser="+left.ID().String()) {
		t.Fatal("the record of the rejected loser names the wrong versions")
	}
	for range 5 {
		mustMerge(t, b, a.Doc())
	}
	if b.LWWDiscards() != 1 || len(lb.lines) != 1 {
		t.Fatal("anti-entropy re-sent the same loser and it was counted again")
	}
	t.Run("old news records nothing", func(t *testing.T) {
		mustMerge(t, b, base)
		if b.LWWDiscards() != 1 {
			t.Fatal("a version the held one descends from was recorded as a loss")
		}
		c, cc, lc := newLoggedStore(t, "c", 1, 3)
		mustMerge(t, c, b.Doc())
		cc.now += 500
		mustPut(t, c, "k", "on-top", occ.IfMatch(c.Record("k").ETag()))
		mustMerge(t, c, base)
		mustMerge(t, c, b.Doc())
		if c.LWWDiscards() != 0 || len(lc.lines) != 0 {
			t.Fatal("versions below the held one, one and two steps down, were recorded as losses")
		}
	})
	t.Run("a failed persist leaves no trace and the retry records once", func(t *testing.T) {
		f := &flakyPersist{}
		c := &fakeClock{now: 1_000_000}
		l := &discardLog{}
		cfg := storeConfig("x", 1, 9, c)
		cfg.Persist, cfg.Log, cfg.OnDiscard = f.persist, l.sink, l.hook
		x, err := occ.NewStore(cfg)
		if err != nil {
			t.Fatal(err)
		}
		mustMerge(t, x, b.Doc())
		f.fail = true
		before := x.EncodeState()
		if _, err := x.Merge(a.Doc()); !errors.Is(err, env.ErrIO) {
			t.Fatalf("err = %v, want the disk error", err)
		}
		if !slices.Equal(before, x.EncodeState()) || x.LWWDiscards() != 0 || len(l.lines)+len(l.discards) != 0 || len(x.DiscardTail()) != 0 {
			t.Fatal("a failed persist of a rejected loser left a trace")
		}
		f.fail = false
		mustMerge(t, x, a.Doc())
		mustMerge(t, x, a.Doc())
		if x.LWWDiscards() != 1 || len(l.lines) != 1 || len(l.discards) != 1 {
			t.Fatalf("after the disk recovered: discards %d, lines %d", x.LWWDiscards(), len(l.lines))
		}
	})
}

func loserDoc(i int, wall int64) *occ.Doc {
	d := occ.NewDoc()
	d.MergeVersion("k", version(fmt.Sprintf("w%04d", i), 1, occ.VersionVector{}, false, "lose", st(wall, 0, fmt.Sprintf("w%04d", i))))
	return d
}

func TestDiscardDedupIsBounded(t *testing.T) {
	var blobs [][]byte
	c := &fakeClock{now: 1_000_000}
	l := &discardLog{}
	cfg := storeConfig("a", 1, 1, c)
	cfg.Log, cfg.OnDiscard = l.sink, l.hook
	cfg.Persist = func(b []byte) error { blobs = append(blobs, b); return nil }
	s, err := occ.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mustPut(t, s, "k", "held", occ.IfNoneMatch())
	n := occ.DiscardDedupLen + 1
	for i := range n {
		mustMerge(t, s, loserDoc(i, 10))
	}
	if s.LWWDiscards() != uint64(n) || len(l.lines) != n {
		t.Fatalf("%d distinct losers gave %d records", n, s.LWWDiscards())
	}
	for i := 1; i < n; i++ {
		mustMerge(t, s, loserDoc(i, 10))
	}
	if s.LWWDiscards() != uint64(n) {
		t.Fatal("a loser still inside the de-duplication window was counted again")
	}
	re, err := occ.OpenStore(cfg, blobs[len(blobs)-1])
	if err != nil {
		t.Fatal(err)
	}
	mustMerge(t, re, loserDoc(n-1, 10))
	if re.LWWDiscards() != uint64(n) {
		t.Fatal("the de-duplication window did not survive a restart")
	}
	mustMerge(t, s, loserDoc(0, 10))
	if s.LWWDiscards() != uint64(n+1) || len(l.lines) != n+1 {
		t.Fatal("beyond the window a re-sent loser is counted again: the documented over-count did not happen")
	}
}

type diskLog struct {
	blobs [][]byte
}

func (d *diskLog) persist(b []byte) error {
	d.blobs = append(d.blobs, slices.Clone(b))
	return nil
}

func TestDiscardRecordSurvivesCrashBeforePublish(t *testing.T) {
	disk := &diskLog{}
	c := &fakeClock{now: 1_000_000}
	l := &discardLog{}
	cfg := storeConfig("a", 1, 1, c)
	cfg.Persist, cfg.Log, cfg.OnDiscard = disk.persist, l.sink, l.hook
	s, err := occ.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	mine := mustPut(t, s, "k", "mine", occ.IfNoneMatch())
	mustMerge(t, s, loserDoc(1, int64(c.now)+50))
	other := occ.NewDoc()
	other.MergeVersion("j", version("z", 7, occ.VersionVector{}, false, "z", st(1, 0, "z")))
	mustPut(t, s, "j", "j-mine", occ.IfNoneMatch())
	mustMerge(t, s, other)
	if len(l.lines) != 2 {
		t.Fatalf("setup: two discards, got %d", len(l.lines))
	}
	merged := -1
	for i, b := range disk.blobs {
		st, err := occ.DecodeStateBlob(b)
		if err != nil {
			t.Fatal(err)
		}
		if st.Discards == 1 && merged < 0 {
			merged = i
		}
	}
	if merged < 0 || merged+1 >= len(disk.blobs) {
		t.Fatal("setup: no durable merge followed by a drain write")
	}
	restart := func(blob []byte) (*occ.Store, *discardLog, *diskLog) {
		t.Helper()
		rl, rd := &discardLog{}, &diskLog{}
		rc := storeConfig("a", 0, 2, c)
		rc.Persist, rc.Log, rc.OnDiscard = rd.persist, rl.sink, rl.hook
		r, err := occ.OpenStore(rc, blob)
		if err != nil {
			t.Fatal(err)
		}
		return r, rl, rd
	}
	r, rl, rd := restart(disk.blobs[merged])
	if len(rl.lines) != 1 || len(rl.discards) != 1 || rl.discards[0].Loser.ID() != mine.ID() || rl.lines[0] != rl.discards[0].String() {
		t.Fatalf("a crash between the durable merge and the publish lost the record: lines %v", rl.lines)
	}
	if r.LWWDiscards() != 1 || len(r.DiscardTail()) != 1 || r.DiscardTail()[0].Loser.ID() != mine.ID() {
		t.Fatal("the restarted node does not show the discard it recovered")
	}
	if len(rd.blobs) == 0 {
		t.Fatal("the restarted node did not drain its outbox to disk")
	}
	again, al, _ := restart(rd.blobs[len(rd.blobs)-1])
	if len(al.lines) != 0 || len(again.DiscardTail()) != 1 {
		t.Fatal("a drained outbox was emitted again, or the tail did not survive")
	}
	_, dl, _ := restart(disk.blobs[merged+1])
	if len(dl.lines) != 0 {
		t.Fatal("the outbox was not drained after the publish")
	}
	last, ll, _ := restart(disk.blobs[len(disk.blobs)-1])
	tail := last.DiscardTail()
	if len(ll.lines) != 0 || len(tail) != 2 || tail[0].Loser.ID() != mine.ID() || tail[1].Key != "j" {
		t.Fatalf("the readable tail is not durable: %d entries", len(tail))
	}
	if _, err := occ.OpenStore(cfg, []byte("OCCS-garbage")); !errors.Is(err, occ.ErrMalformed) {
		t.Fatalf("a malformed blob opened: %v", err)
	}
}

func TestRecountObjectsNeverWraps(t *testing.T) {
	s, _ := newTestStore(t, "a", 1, 1)
	if _, err := s.PutObjectMeta("b", "x", occ.ObjectMeta{Size: math.MaxUint64}, occ.Unconditional()); err != nil {
		t.Fatal(err)
	}
	if u, err := s.RecountObjects("b"); err != nil || u.Bytes != math.MaxUint64 || u.Objects != 1 {
		t.Fatalf("one object at the limit: %+v %v", u, err)
	}
	if _, err := s.PutObjectMeta("b", "y", occ.ObjectMeta{Size: 1}, occ.Unconditional()); err != nil {
		t.Fatal(err)
	}
	u, err := s.RecountObjects("b")
	if !errors.Is(err, occ.ErrUsageOverflow) || u != (occ.Usage{}) {
		t.Fatalf("MaxUint64 + 1 bytes recounted as %+v, %v", u, err)
	}
	hook := func(key string) (occ.Usage, error) { return s.RecountObjects("b") }
	if _, _, err := s.RecountQuota(occ.QuotaKey("b"), 2, hook); !errors.Is(err, occ.ErrUsageOverflow) {
		t.Fatalf("a recount over an overflowing total wrote: %v", err)
	}
	if s.Record(occ.QuotaKey("b")).Present() {
		t.Fatal("an overflowing recount stored a quota")
	}
	codes, err := occ.Codes()
	if err != nil {
		t.Fatal(err)
	}
	c, m := codes.Encode(fmt.Errorf("wrapped: %w", occ.ErrUsageOverflow))
	if c != occ.CodeUsageOverflow || !errors.Is(codes.Decode(c, m), occ.ErrUsageOverflow) {
		t.Fatal("the overflow error does not round-trip the wire")
	}
}

func TestRecountNoopRechecksCurrentVersion(t *testing.T) {
	s, _ := newTestStore(t, "a", 1, 1)
	key := occ.QuotaKey("q")
	if _, err := s.WriteQuota(key, occ.Usage{Bytes: 5}, occ.IfNoneMatch()); err != nil {
		t.Fatal(err)
	}
	want := occ.Usage{Bytes: 5}
	calls := 0
	got, wrote, err := s.RecountQuota(key, 3, func(string) (occ.Usage, error) {
		calls++
		if calls == 1 {
			_, p, err := s.ReadQuota(key)
			if err != nil {
				return occ.Usage{}, err
			}
			if _, err := s.WriteQuota(key, occ.Usage{Bytes: 7}, p); err != nil {
				return occ.Usage{}, err
			}
		}
		return want, nil
	})
	if err != nil || got != want || !wrote || calls != 2 {
		t.Fatalf("got %+v wrote %v err %v calls %d", got, wrote, err, calls)
	}
	if u, _, _ := s.ReadQuota(key); u != want {
		t.Fatalf("the stale no-op left %+v stored", u)
	}
	calls = 0
	_, wrote, err = s.RecountQuota(key, 2, func(string) (occ.Usage, error) {
		calls++
		u, p, err := s.ReadQuota(key)
		if err != nil {
			return occ.Usage{}, err
		}
		if _, err := s.WriteQuota(key, occ.Usage{Bytes: u.Bytes + 1}, p); err != nil {
			return occ.Usage{}, err
		}
		return u, nil
	})
	if !errors.Is(err, occ.ErrPreconditionFailed) || wrote || calls != 2 {
		t.Fatalf("a recount that never saw a stable value must give up: wrote %v err %v calls %d", wrote, err, calls)
	}
	calls = 0
	got, wrote, err = s.RecountQuota(key, 1, func(string) (occ.Usage, error) {
		calls++
		u, _, err := s.ReadQuota(key)
		return u, err
	})
	if err != nil || wrote || calls != 1 {
		t.Fatalf("an already-right quota must be a no-op: %+v %v %v", got, wrote, err)
	}
}

func stateBlob(payload []byte) []byte {
	sum := sha256.Sum256(payload)
	return append(append([]byte("OCS2"), sum[:]...), payload...)
}

func TestOpenStoreRejectsHostileState(t *testing.T) {
	c := &fakeClock{now: 1_000_000}
	cfg := storeConfig("a", 0, 1, c)
	head := []byte{1, 0, 0}
	rows := map[string][]byte{
		"a tail over 64":      append(append(slices.Clone(head), 65), make([]byte, 65*40)...),
		"a window over 256":   append(append(slices.Clone(head), 0, 0, 0x81, 0x02), make([]byte, 257*16)...),
		"a truncated discard": append(slices.Clone(head), 1, 1, 'k', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0),
		"a truncated window":  append(slices.Clone(head), 0, 0, 1, 1, 2, 3),
	}
	for name, payload := range rows {
		if _, err := occ.OpenStore(cfg, stateBlob(payload)); !errors.Is(err, occ.ErrMalformed) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	good := append(slices.Clone(head), 0, 0, 0)
	good = append(good, occ.NewDoc().Encode()...)
	if _, err := occ.OpenStore(cfg, stateBlob(good)); err != nil {
		t.Fatalf("an empty state: %v", err)
	}
	if _, err := occ.OpenStore(occ.StoreConfig{Node: "a"}, stateBlob(good)); !errors.Is(err, env.ErrInvalidArgument) {
		t.Fatalf("a store without clock and rand: %v", err)
	}
}
