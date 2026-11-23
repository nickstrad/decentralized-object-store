package occ_test

import (
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/occ"
)

type state struct {
	name  string
	build func(t *testing.T, s *occ.Store) occ.ETag
}

var states = []state{
	{"absent", func(t *testing.T, s *occ.Store) occ.ETag {
		return mustPut(t, s, "other", "x", occ.IfNoneMatch()).ETag()
	}},
	{"a live version", func(t *testing.T, s *occ.Store) occ.ETag {
		stale := mustPut(t, s, "k", "v0", occ.IfNoneMatch()).ETag()
		mustPut(t, s, "k", "v1", occ.IfMatch(stale))
		return stale
	}},
	{"a tombstone", func(t *testing.T, s *occ.Store) occ.ETag {
		stale := mustPut(t, s, "k", "v0", occ.IfNoneMatch()).ETag()
		if _, err := s.Delete("k", occ.IfMatch(stale)); err != nil {
			t.Fatal(err)
		}
		return stale
	}},
}

type preRow struct {
	name string
	make func(cur, stale occ.ETag) occ.Precondition
}

var preRows = []preRow{
	{"no precondition", func(cur, stale occ.ETag) occ.Precondition { return occ.Unconditional() }},
	{"If-None-Match *", func(cur, stale occ.ETag) occ.Precondition { return occ.IfNoneMatch() }},
	{"If-Match current etag", func(cur, stale occ.ETag) occ.Precondition { return occ.IfMatch(cur) }},
	{"If-Match stale etag", func(cur, stale occ.ETag) occ.Precondition { return occ.IfMatch(stale) }},
	{"If-Match zero etag", func(cur, stale occ.ETag) occ.Precondition { return occ.IfMatch(occ.ETag{}) }},
}

var (
	okW error
	pf  = occ.ErrPreconditionFailed
	nf  = occ.ErrNotFound
)

func runCASTable(t *testing.T, del bool, want [][]error) {
	for si, st := range states {
		for pi, pr := range preRows {
			w := want[si][pi]
			t.Run(st.name+" with "+pr.name, func(t *testing.T) {
				s, _, log := newLoggedStore(t, "n1", 1, 1)
				stale := st.build(t, s)
				before := s.Record("k")
				p := pr.make(before.ETag(), stale)
				if p.Holds(before) != (w == nil || errors.Is(w, nf)) {
					t.Fatalf("Holds = %v disagrees with the expected outcome %v", p.Holds(before), w)
				}
				var v occ.Version
				var err error
				if del {
					v, err = s.Delete("k", p)
				} else {
					v, err = s.Put("k", []byte("new"), p)
				}
				after := s.Record("k")
				if w != nil {
					if !errors.Is(err, w) {
						t.Fatalf("err = %v, want %v", err, w)
					}
					if !after.Equal(before) || after.ETag() != before.ETag() {
						t.Fatal("a refused write changed the record")
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				got, ok := after.Version()
				if !ok || got.ID() != v.ID() || v.Tombstone() != del || after.ETag() != v.ETag() {
					t.Fatal("the new version is not the record's one version")
				}
				if old, ok := before.Version(); ok {
					if !v.Supersedes(old) || !lwwLess(old, v) || v.Compare(old) <= 0 {
						t.Fatal("a write does not causally supersede and beat the version it replaced")
					}
				}
				if s.LWWDiscards() != 0 || len(log.lines) != 0 {
					t.Fatal("a local write was recorded as a discard")
				}
			})
		}
	}
}

// TestConditionalPutTable proves every precondition x record-state row
// (absent, live, tombstone) for Store.Put, checking error codes, no-op refusal,
// causal succession, and zero discards.
func TestConditionalPutTable(t *testing.T) {
	runCASTable(t, false, [][]error{
		{okW, okW, pf, pf, pf},
		{okW, pf, okW, pf, pf},
		{okW, okW, okW, pf, pf},
	})
}

// TestConditionalDeleteTable proves every precondition x record-state row
// for Store.Delete, checking precondition-before-liveness ordering and no-op refusal.
func TestConditionalDeleteTable(t *testing.T) {
	runCASTable(t, true, [][]error{
		{nf, nf, pf, pf, pf},
		{okW, pf, okW, pf, pf},
		{nf, nf, nf, pf, pf},
	})
}

// TestETagDeterministic proves that ETags are a pure function of the single
// version across merge orders, differ for distinct versions with equal values,
// cover tombstones, are zero when absent, and round-trip through ParseETag.
func TestETagDeterministic(t *testing.T) {
	a, _ := newTestStore(t, "a", 1, 1)
	b, _ := newTestStore(t, "b", 1, 2)
	c, _ := newTestStore(t, "c", 1, 3)
	va := mustPut(t, a, "k", "same", occ.Unconditional())
	vb := mustPut(t, b, "k", "same", occ.Unconditional())
	vc := mustPut(t, c, "k", "same", occ.Unconditional())
	if va.ETag() == vb.ETag() || vb.ETag() == vc.ETag() || va.ETag() == vc.ETag() {
		t.Fatal("different versions with the same value share an etag")
	}
	x, _ := newTestStore(t, "x", 1, 4)
	y, _ := newTestStore(t, "y", 1, 5)
	for _, d := range []*occ.Doc{a.Doc(), b.Doc(), c.Doc()} {
		mustMerge(t, x, d)
	}
	for _, d := range []*occ.Doc{c.Doc(), a.Doc(), b.Doc(), a.Doc()} {
		mustMerge(t, y, d)
	}
	if x.Record("k").ETag() != y.Record("k").ETag() || !x.Doc().Equal(y.Doc()) {
		t.Fatal("the same version reached in different orders has different etags")
	}
	w, _ := x.Record("k").Version()
	if w.ETag() != x.Record("k").ETag() || occ.NewRecord(w).ETag() != w.ETag() {
		t.Fatal("the etag is not a function of the one version")
	}
	if (occ.Record{}).ETag() != (occ.ETag{}) || !(occ.Record{}).ETag().IsZero() || (occ.Record{}).Present() {
		t.Fatal("an absent record must have the zero etag")
	}
	if got, _ := (occ.Record{}).Merge(occ.NewRecord(w)).Version(); got.ID() != w.ID() || !occ.NewRecord(w).Merge(occ.Record{}).Equal(occ.NewRecord(w)) {
		t.Fatal("merging with an absent record must keep the present version")
	}
	if !w.Descends(w) || w.Supersedes(w) || w.Context().Len() != 0 || !slices.Equal(x.Keys(), []string{"k"}) {
		t.Fatal("version accessors")
	}
	before := a.Record("k").ETag()
	mustPut(t, a, "k", "same", occ.IfMatch(before))
	if a.Record("k").ETag() == before {
		t.Fatal("a new version with the same value kept the etag")
	}
	tomb, err := a.Delete("k", occ.IfMatch(a.Record("k").ETag()))
	if err != nil || tomb.ETag().IsZero() || a.Record("k").ETag() != tomb.ETag() {
		t.Fatalf("a tombstone must carry its own etag: %v", err)
	}
	e := a.Record("k").ETag()
	p, err := occ.ParseETag(e.String())
	if err != nil || p != e {
		t.Fatalf("parse etag round trip: %v", err)
	}
	for _, bad := range []string{"", "zz", e.String()[:10], e.String() + "00"} {
		if _, err := occ.ParseETag(bad); !errors.Is(err, occ.ErrMalformed) {
			t.Fatalf("ParseETag(%q) err = %v", bad, err)
		}
	}
}

func version(node string, counter uint64, ctx occ.VersionVector, tomb bool, value string, s occ.Stamp) occ.Version {
	return occ.NewVersion(occ.Dot{Actor: occ.Actor{Node: env.NodeID(node), Epoch: 1}, Counter: counter}, ctx, tomb, []byte(value), s)
}

// TestLWWWinnerTable proves the LWW total order (Wall, Logical, Node, VersionID)
// across values, tombstones, forged equal stamps, and causally-below higher stamps
// consistently in Compare, Record.Merge, and Doc.Merge in both orders.
func TestLWWWinnerTable(t *testing.T) {
	forgedA := version("z", 1, occ.VersionVector{}, false, "p", st(5, 1, "z"))
	forgedB := version("z", 1, occ.VersionVector{}, false, "q", st(5, 1, "z"))
	hiID, loID := forgedA, forgedB
	if lwwLess(forgedA, forgedB) {
		hiID, loID = forgedB, forgedA
	}
	rows := []struct {
		name        string
		loser, want occ.Version
	}{
		{"the higher wall time wins", version("z", 1, occ.VersionVector{}, false, "old", st(1, 9, "z")), version("a", 1, occ.VersionVector{}, false, "new", st(2, 0, "a"))},
		{"on equal wall time the higher logical counter wins", version("z", 1, occ.VersionVector{}, false, "x", st(5, 1, "z")), version("a", 1, occ.VersionVector{}, false, "y", st(5, 2, "a"))},
		{"on an equal (wall, logical) the higher node id wins", version("a", 1, occ.VersionVector{}, false, "from-a", st(5, 1, "a")), version("b", 1, occ.VersionVector{}, false, "from-b", st(5, 1, "b"))},
		{"forged equal stamps fall back to the version id bytes", loID, hiID},
		{"a later tombstone beats a value", version("a", 1, occ.VersionVector{}, false, "v", st(5, 0, "a")), version("b", 1, occ.VersionVector{}, true, "", st(6, 0, "b"))},
		{"a later value beats a tombstone", version("a", 1, occ.VersionVector{}, true, "", st(5, 0, "a")), version("b", 1, occ.VersionVector{}, false, "v", st(6, 0, "b"))},
		{"the higher stamp wins even when it is causally below the other", version("b", 1, version("a", 1, occ.VersionVector{}, false, "", st(1, 0, "a")).Clock(), false, "child", st(1, 0, "b")), version("a", 1, occ.VersionVector{}, false, "parent", st(9, 0, "a"))},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if r.want.Compare(r.loser) <= 0 || r.loser.Compare(r.want) >= 0 || r.want.Compare(r.want) != 0 || !lwwLess(r.loser, r.want) {
				t.Fatal("Compare disagrees with the table")
			}
			for _, pair := range [][2]occ.Version{{r.loser, r.want}, {r.want, r.loser}} {
				if got, _ := occ.NewRecord(pair[0]).Merge(occ.NewRecord(pair[1])).Version(); got.ID() != r.want.ID() {
					t.Fatal("record merge picked the loser")
				}
				d := occ.NewDoc()
				d.MergeVersion("k", pair[0])
				d.MergeVersion("k", pair[1])
				if got, _ := d.Record("k").Version(); got.ID() != r.want.ID() || d.Len() != 1 {
					t.Fatal("document merge picked the loser")
				}
			}
		})
	}
}

func concurrentPair(t *testing.T) (a, b *occ.Store, la, lb *discardLog, ca, cb *fakeClock) {
	a, ca, la = newLoggedStore(t, "a", 1, 1)
	b, cb, lb = newLoggedStore(t, "b", 1, 2)
	mustPut(t, a, "k", "base", occ.IfNoneMatch())
	mustMerge(t, b, a.Doc())
	ca.now += 10
	cb.now += 20
	return
}

// TestLWWDiscardCountedAndLogged proves that concurrent replacements and
// rejected incoming losers are counted, formatted, tailed, and reported once,
// covering puts vs. deletes, counter saturation, and DiscardTailLen bounds.
func TestLWWDiscardCountedAndLogged(t *testing.T) {
	a, b, la, lb, _, _ := concurrentPair(t)
	et := a.Record("k").ETag()
	left := mustPut(t, a, "k", "left", occ.IfMatch(et))
	right := mustPut(t, b, "k", "right", occ.IfMatch(et))
	if !lwwLess(left, right) {
		t.Fatal("setup: b's later write must win")
	}
	if mustMerge(t, b, a.Doc()); b.LWWDiscards() != 1 || len(lb.lines) != 1 || lb.discards[0].Loser.ID() != left.ID() {
		t.Fatal("a node that rejects an incoming concurrent loser must record it")
	}
	mustMerge(t, a, b.Doc())
	if got, _ := a.Record("k").Version(); got.ID() != right.ID() {
		t.Fatal("the LWW winner did not replace the loser")
	}
	if a.LWWDiscards() != 1 || len(la.lines) != 1 || len(la.discards) != 1 || len(a.DiscardTail()) != 1 {
		t.Fatalf("discards %d, log lines %d, hook calls %d", a.LWWDiscards(), len(la.lines), len(la.discards))
	}
	d := la.discards[0]
	if d.Key != "k" || d.Loser.ID() != left.ID() || d.Winner.ID() != right.ID() || a.DiscardTail()[0].Loser.ID() != left.ID() {
		t.Fatal("the hook named the wrong versions")
	}
	line := la.lines[0]
	for _, want := range []string{`key="k"`, "loser=" + left.ID().String(), "loser_stamp=" + left.Stamp().String(), "loser_writer=" + left.Dot().Actor.String(), "winner=" + right.ID().String(), "winner_stamp=" + right.Stamp().String()} {
		if !strings.Contains(line, want) {
			t.Fatalf("log line %q lacks %q", line, want)
		}
	}
	if line != d.String() {
		t.Fatal("the log line is not the discard's string")
	}
	if !a.Doc().Equal(b.Doc()) {
		t.Fatal("replicas holding the same version differ")
	}
	mustMerge(t, a, b.Doc())
	mustMerge(t, b, a.Doc())
	if a.LWWDiscards() != 1 || b.LWWDiscards() != 1 {
		t.Fatal("a repeated merge counted the discard again")
	}
	t.Run("a delete discards a concurrent put and the reverse", func(t *testing.T) {
		for _, delWins := range []bool{true, false} {
			a, b, la, _, ca, cb := concurrentPair(t)
			if delWins {
				ca.now, cb.now = cb.now, ca.now
			}
			et := a.Record("k").ETag()
			tomb, err := a.Delete("k", occ.IfMatch(et))
			if err != nil {
				t.Fatal(err)
			}
			put := mustPut(t, b, "k", "put", occ.IfMatch(et))
			mustMerge(t, a, b.Doc())
			mustMerge(t, b, a.Doc())
			got, _ := a.Record("k").Version()
			loser := tomb
			if delWins {
				loser = put
				if got.ID() != tomb.ID() {
					t.Fatal("the later delete did not win")
				}
				if b.LWWDiscards() != 1 {
					t.Fatal("the put lost to a delete without a record")
				}
				continue
			}
			if got.ID() != put.ID() || len(la.discards) != 1 || la.discards[0].Loser.ID() != loser.ID() {
				t.Fatal("the delete lost to a put without a record")
			}
		}
	})
	t.Run("the counter saturates", func(t *testing.T) {
		c := &fakeClock{now: 1_000_000}
		cfg := storeConfig("a", 1, 1, c)
		cfg.Discards = math.MaxUint64
		a, err := occ.NewStore(cfg)
		if err != nil {
			t.Fatal(err)
		}
		mustPut(t, a, "k", "mine", occ.IfNoneMatch())
		other := occ.NewDoc()
		other.MergeVersion("k", version("z", 1, occ.VersionVector{}, false, "theirs", st(int64(c.now)+5, 0, "z")))
		mustMerge(t, a, other)
		if a.LWWDiscards() != math.MaxUint64 || len(a.DiscardTail()) != 1 {
			t.Fatal("the counter must saturate and the record must still be kept")
		}
	})
	t.Run("the in-memory tail is bounded", func(t *testing.T) {
		a, c, l := newLoggedStore(t, "a", 1, 1)
		n := occ.DiscardTailLen + 10
		for i := range n {
			key := string(rune('A'+i%26)) + string(rune('a'+i/26))
			mustPut(t, a, key, "mine", occ.IfNoneMatch())
			other := occ.NewDoc()
			other.MergeVersion(key, version("z", uint64(i+1), occ.VersionVector{}, false, "theirs", st(int64(c.now)+int64(i+1)*10_000, 0, "z")))
			mustMerge(t, a, other)
		}
		tail := a.DiscardTail()
		if a.LWWDiscards() != uint64(n) || len(l.lines) != n || len(tail) != occ.DiscardTailLen || tail[len(tail)-1].Key != l.discards[n-1].Key || tail[0].Key != l.discards[10].Key {
			t.Fatalf("discards %d, tail %d", a.LWWDiscards(), len(tail))
		}
	})
}

// TestCausalOverwriteIsNotADiscard proves that causally ordered conditional
// and unconditional overwrites across replicas record zero LWW discards.
func TestCausalOverwriteIsNotADiscard(t *testing.T) {
	a, _, la := newLoggedStore(t, "a", 1, 1)
	b, _, lb := newLoggedStore(t, "b", 1, 2)
	c, _, lc := newLoggedStore(t, "c", 1, 3)
	mustPut(t, a, "k", "one", occ.IfNoneMatch())
	mustMerge(t, b, a.Doc())
	mustPut(t, b, "k", "two", occ.IfMatch(b.Record("k").ETag()))
	mustMerge(t, c, b.Doc())
	mustPut(t, c, "k", "three", occ.Unconditional())
	mustMerge(t, a, c.Doc())
	mustMerge(t, b, c.Doc())
	if v := value(a.Record("k")); v != "three" {
		t.Fatalf("a holds %q", v)
	}
	for _, s := range []*occ.Store{a, b, c} {
		if s.LWWDiscards() != 0 {
			t.Fatal("an ordinary overwrite was counted as a discard")
		}
	}
	if len(la.lines)+len(lb.lines)+len(lc.lines) != 0 {
		t.Fatal("an ordinary overwrite was logged")
	}
}

// TestEpochRotatesAtCounterOverflow proves that a hostile context carrying
// MaxUint64 for the local actor triggers a single epoch rotation before writing.
func TestEpochRotatesAtCounterOverflow(t *testing.T) {
	a, _ := newTestStore(t, "a", 7, 1)
	hostile := occ.NewDoc()
	other := occ.Actor{Node: "z", Epoch: 1}
	v := occ.NewVersion(occ.Dot{Actor: other, Counter: 1}, occ.NewVersionVector(occ.Entry{Actor: a.Actor(), Counter: math.MaxUint64}), false, []byte("x"), occ.Stamp{})
	hostile.MergeVersion("k", v)
	mustMerge(t, a, hostile)
	before := a.Actor()
	w := mustPut(t, a, "k", "y", occ.IfMatch(a.Record("k").ETag()))
	if a.Actor() == before || a.Actor().Node != "a" || a.Rotations() != 1 {
		t.Fatalf("actor did not rotate: %s -> %s", before.String(), a.Actor().String())
	}
	if w.Dot().Actor != a.Actor() || w.Dot().Counter != 1 || !w.Supersedes(v) || a.Counter() != 1 {
		t.Fatal("the write after rotation is not a fresh dot that supersedes what it read")
	}
	mustPut(t, a, "other", "z", occ.IfNoneMatch())
	if a.Rotations() != 1 || a.Counter() != 2 {
		t.Fatal("rotated without overflow")
	}
}

// TestNewStoreValidates proves that NewStore rejects missing required config
// fields and draws a fresh non-zero epoch when Epoch is 0.
func TestNewStoreValidates(t *testing.T) {
	if _, err := occ.NewStore(occ.StoreConfig{}); !errors.Is(err, env.ErrInvalidArgument) {
		t.Fatalf("err = %v", err)
	}
	s, _ := newTestStore(t, "a", 0, 1)
	if s.Actor().Epoch == 0 {
		t.Fatal("epoch 0 must draw a fresh epoch")
	}
	if s.HLC() == nil || s.AheadStamps() != 0 || s.LWWDiscards() != 0 || s.DiscardTail() != nil {
		t.Fatal("a new store")
	}
}

// TestPreconditionStrings proves String formatting for all three precondition kinds.
func TestPreconditionStrings(t *testing.T) {
	e := occ.ETag{1}
	for p, want := range map[string]string{
		occ.Unconditional().String(): "unconditional",
		occ.IfNoneMatch().String():   "If-None-Match: *",
		occ.IfMatch(e).String():      "If-Match: " + e.String(),
	} {
		if p != want {
			t.Fatalf("%q != %q", p, want)
		}
	}
}

// TestFarFutureStampPinsOnlyItsKey proves that a far-future stamp is merged
// and counted without moving the node HLC, that a local write steps past it
// (including logical carry), and that (MaxInt64, MaxUint32) refuses writes cleanly.
func TestFarFutureStampPinsOnlyItsKey(t *testing.T) {
	s, c := newTestStore(t, "a", 1, 1)
	far := st(int64(c.now)+int64(2*occ.MaxClockLead), 5, "rogue")
	forged := version("rogue", 1, occ.VersionVector{}, false, "future", far)
	d := occ.NewDoc()
	d.MergeVersion("k", forged)
	if !mustMerge(t, s, d) || s.AheadStamps() != 1 {
		t.Fatal("a far-future stamp must merge its data and be counted")
	}
	if s.HLC().Last().Wall >= far.Wall {
		t.Fatal("the node clock adopted a far-future stamp")
	}
	w := mustPut(t, s, "k", "mine", occ.IfMatch(forged.ETag()))
	if !forged.Stamp().After(occ.Stamp{}) || !w.Stamp().After(forged.Stamp()) || w.Compare(forged) <= 0 || w.Stamp().Node != "a" {
		t.Fatalf("the acknowledged write %s does not beat the version %s it replaced", w.Stamp().String(), far.String())
	}
	if got := value(s.Record("k")); got != "mine" {
		t.Fatal("the write lost to the version it replaced")
	}
	o := mustPut(t, s, "other", "x", occ.IfNoneMatch())
	if o.Stamp().Wall >= far.Wall || s.HLC().Last().Wall >= far.Wall {
		t.Fatal("a far-future stamp on one key moved the stamps of other keys")
	}
	carry := occ.NewDoc()
	carry.MergeVersion("c", version("rogue", 3, occ.VersionVector{}, false, "carry", st(far.Wall, math.MaxUint32, "rogue")))
	mustMerge(t, s, carry)
	if cw := mustPut(t, s, "c", "mine", occ.Unconditional()); cw.Stamp() != st(far.Wall+1, 0, "a") {
		t.Fatalf("the successor of a full logical counter carries into the wall: %s", cw.Stamp().String())
	}
	end := occ.NewDoc()
	end.MergeVersion("e", version("rogue", 2, occ.VersionVector{}, false, "end", st(math.MaxInt64, math.MaxUint32, "rogue")))
	mustMerge(t, s, end)
	before := s.Doc().Encode()
	if _, err := s.Put("e", []byte("x"), occ.Unconditional()); !errors.Is(err, occ.ErrClockExhausted) {
		t.Fatalf("a write over the last stamp: %v", err)
	}
	if !slices.Equal(before, s.Doc().Encode()) {
		t.Fatal("a refused write changed state")
	}
}

// TestStoreClockEdges proves initial stamp assignment, ahead-stamp counting
// when loading a Doc, and ErrClockExhausted on Put and SetScalar.
func TestStoreClockEdges(t *testing.T) {
	s, c := newTestStore(t, "a", 1, 1)
	v := mustPut(t, s, "k", "v", occ.IfNoneMatch())
	if v.Stamp().Node != "a" || v.Stamp().Wall != int64(c.now) {
		t.Fatalf("stamp %s", v.Stamp().String())
	}
	far := occ.NewDoc()
	far.MergeVersion("s", version("z", 1, occ.VersionVector{}, false, "future", st(int64(c.now)+int64(2*occ.MaxClockLead), 0, "z")))
	s2, err := occ.NewStore(occ.StoreConfig{Node: "b", Epoch: 1, Clock: c, Rand: rand.New(rand.NewPCG(1, 1)), Doc: far})
	if err != nil || s2.AheadStamps() != 1 {
		t.Fatal("a loaded far-future stamp must be counted")
	}
	c.now = math.MaxInt64
	if err := s.HLC().Observe(occ.Stamp{Wall: math.MaxInt64, Logical: math.MaxUint32}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Put("k", nil, occ.Unconditional()); !errors.Is(err, occ.ErrClockExhausted) {
		t.Fatalf("put on an exhausted clock: %v", err)
	}
	if _, err := s.SetScalar("s", nil); !errors.Is(err, occ.ErrClockExhausted) {
		t.Fatalf("set on an exhausted clock: %v", err)
	}
}

// TestHLCObservesEveryStamp proves that a far-future stamp on one key does not
// block observing valid stamps on other keys, that negative-skew nodes start at
// their own clock on empty docs, and that MaxStamp handles empty and negative docs.
func TestHLCObservesEveryStamp(t *testing.T) {
	t.Run("a far-future stamp on one key does not hide in-range stamps on others", func(t *testing.T) {
		fast, fc := newTestStore(t, "fast", 1, 1)
		slow, sc := newTestStore(t, "slow", 1, 2)
		fc.now = env.Time(10 * time.Second)
		sc.now = env.Time(9 * time.Second)
		route, err := fast.SetRoute("b1", "region-a")
		if err != nil {
			t.Fatal(err)
		}
		poison := occ.NewDoc()
		poison.MergeVersion("unrelated", version("rogue", 1, occ.VersionVector{}, false, "", st(int64(fc.now)+int64(2*occ.MaxClockLead), 0, "rogue")))
		mustMerge(t, fast, poison)
		mustMerge(t, slow, fast.Doc())
		if slow.AheadStamps() != 1 || !slow.HLC().Last().After(occ.Stamp{Wall: route.Stamp().Wall - 1}) || slow.HLC().Last().Compare(route.Stamp()) < 0 {
			t.Fatalf("the in-range stamp %s was not observed (clock %s)", route.Stamp().String(), slow.HLC().Last().String())
		}
		sc.now = env.Time(9500 * time.Millisecond)
		other, err := slow.SetRoute("b2", "region-c")
		if err != nil || !other.Stamp().After(route.Stamp()) {
			t.Fatal("a write on another key is not after the observed stamp")
		}
	})
	t.Run("a node with negative skew starts at its own clock", func(t *testing.T) {
		for _, skew := range []time.Duration{-2 * time.Hour, -400 * time.Millisecond} {
			c := &fakeClock{now: env.Time(skew)}
			s, err := occ.NewStore(occ.StoreConfig{Node: "a", Epoch: 1, Clock: c, Rand: rand.New(rand.NewPCG(1, 1))})
			if err != nil {
				t.Fatal(err)
			}
			mustMerge(t, s, occ.NewDoc())
			if s.AheadStamps() != 0 {
				t.Fatalf("skew %s: an empty document counted %d ahead stamps", skew, s.AheadStamps())
			}
			now, err := s.HLC().Now()
			if err != nil || now != st(int64(skew), 0, "a") {
				t.Fatalf("skew %s: first stamp %s", skew, now.String())
			}
		}
	})
	t.Run("MaxStamp reports absence and negative maxima", func(t *testing.T) {
		if _, ok := occ.NewDoc().MaxStamp(); ok {
			t.Fatal("an empty document has no maximum stamp")
		}
		d := occ.NewDoc()
		d.MergeVersion("a", version("x", 1, occ.VersionVector{}, false, "", st(-50, 0, "x")))
		d.MergeVersion("b", version("x", 2, occ.VersionVector{}, false, "", st(-20, 3, "x")))
		if m, ok := d.MaxStamp(); !ok || m != st(-20, 3, "x") || len(d.Stamps()) != 2 {
			t.Fatalf("max stamp %s %v", m.String(), ok)
		}
	})
}

type flakyPersist struct {
	fail  bool
	calls int
}

func (f *flakyPersist) persist([]byte) error {
	f.calls++
	if f.fail {
		return env.ErrIO
	}
	return nil
}

type storeSnapshot struct {
	doc      []byte
	actor    occ.Actor
	counter  uint64
	discards uint64
	tail     int
	state    []byte
	clock    occ.Stamp
	ahead    int
	logs     int
}

func snap(s *occ.Store, l *discardLog) storeSnapshot {
	return storeSnapshot{s.Doc().Encode(), s.Actor(), s.Counter(), s.LWWDiscards(), len(s.DiscardTail()), s.EncodeState(), s.HLC().Last(), s.AheadStamps(), len(l.lines) + len(l.discards)}
}

// TestFailedPersistLeavesNoTrace proves that a Persist error rolls back every
// store field (doc, actor, counter, rotations, HLC, ahead, discards, tail,
// outbox, dedup window, and callbacks) across writes, merges, and epoch rotation.
func TestFailedPersistLeavesNoTrace(t *testing.T) {
	ops := []struct {
		name  string
		setup func(t *testing.T, s *occ.Store)
		do    func(s *occ.Store, c *fakeClock) error
	}{
		{"put", nil, func(s *occ.Store, _ *fakeClock) error {
			_, err := s.Put("k", []byte("new"), occ.IfMatch(s.Record("k").ETag()))
			return err
		}},
		{"delete", nil, func(s *occ.Store, _ *fakeClock) error {
			_, err := s.Delete("k", occ.Unconditional())
			return err
		}},
		{"set scalar", nil, func(s *occ.Store, _ *fakeClock) error {
			_, err := s.SetRoute("b", "r")
			return err
		}},
		{"update quota", nil, func(s *occ.Store, _ *fakeClock) error {
			_, err := s.UpdateQuota(occ.QuotaKey("b"), 2, occ.AddUsage(1, 1, occ.Usage{}))
			return err
		}},
		{"merge with a discard and a far-future stamp", nil, func(s *occ.Store, c *fakeClock) error {
			d := occ.NewDoc()
			d.MergeVersion("k", version("z", 9, occ.VersionVector{}, false, "theirs", st(int64(c.now)+5, 0, "z")))
			d.MergeVersion("far", version("z", 10, occ.VersionVector{}, false, "", st(int64(c.now)+int64(2*occ.MaxClockLead), 0, "z")))
			d.MergeVersion("near", version("z", 11, occ.VersionVector{}, false, "", st(int64(c.now)+int64(time.Minute), 0, "z")))
			_, err := s.Merge(d)
			return err
		}},
		{"merge rejecting a concurrent incoming loser", nil, func(s *occ.Store, c *fakeClock) error {
			d := occ.NewDoc()
			d.MergeVersion("k", version("z", 12, occ.VersionVector{}, false, "older", st(5, 0, "z")))
			_, err := s.Merge(d)
			return err
		}},
		{"a write that rotates the epoch", func(t *testing.T, s *occ.Store) {
			d := occ.NewDoc()
			d.MergeVersion("rot", occ.NewVersion(occ.Dot{Actor: occ.Actor{Node: "z", Epoch: 1}, Counter: 1}, occ.NewVersionVector(occ.Entry{Actor: s.Actor(), Counter: math.MaxUint64}), false, nil, occ.Stamp{}))
			mustMerge(t, s, d)
		}, func(s *occ.Store, _ *fakeClock) error {
			_, err := s.Put("rot", []byte("x"), occ.Unconditional())
			return err
		}},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			f := &flakyPersist{}
			c := &fakeClock{now: 1_000_000}
			l := &discardLog{}
			cfg := storeConfig("a", 1, 1, c)
			cfg.Persist, cfg.Log, cfg.OnDiscard = f.persist, l.sink, l.hook
			s, err := occ.NewStore(cfg)
			if err != nil {
				t.Fatal(err)
			}
			mustPut(t, s, "k", "mine", occ.IfNoneMatch())
			if op.setup != nil {
				op.setup(t, s)
			}
			c.now += 1000
			f.fail = true
			before, rotations := snap(s, l), s.Rotations()
			calls := f.calls
			if err := op.do(s, c); !errors.Is(err, env.ErrIO) {
				t.Fatalf("err = %v, want the disk error", err)
			}
			if f.calls == calls {
				t.Fatal("the operation did not try to persist")
			}
			if got := snap(s, l); !equalSnap(got, before) || s.Rotations() != rotations {
				t.Fatalf("a failed persist left a trace:\nbefore %+v\nafter  %+v", before, got)
			}
			f.fail = false
			if err := op.do(s, c); err != nil {
				t.Fatalf("the same operation after the disk recovered: %v", err)
			}
			if equalSnap(snap(s, l), before) {
				t.Fatal("the operation changed nothing once the disk recovered")
			}
		})
	}
}

func equalSnap(a, b storeSnapshot) bool {
	return slices.Equal(a.doc, b.doc) && a.actor == b.actor && a.counter == b.counter && a.discards == b.discards &&
		a.tail == b.tail && slices.Equal(a.state, b.state) && a.clock == b.clock && a.ahead == b.ahead && a.logs == b.logs
}
