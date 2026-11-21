package occ_test

import (
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
)

type state struct {
	name  string
	build func(t *testing.T, s *occ.Store) occ.ETag
}

var states = []state{
	{"absent", func(t *testing.T, s *occ.Store) occ.ETag {
		return occ.ETag(mustPut(t, s, "other", "x", occ.IfNoneMatch()).ID())
	}},
	{"one live version", func(t *testing.T, s *occ.Store) occ.ETag {
		mustPut(t, s, "k", "v0", occ.IfNoneMatch())
		stale := s.Record("k").ETag()
		mustPut(t, s, "k", "v1", occ.IfMatch(stale))
		return stale
	}},
	{"one tombstone", func(t *testing.T, s *occ.Store) occ.ETag {
		mustPut(t, s, "k", "v0", occ.IfNoneMatch())
		stale := s.Record("k").ETag()
		if _, err := s.Delete("k", occ.IfMatch(stale)); err != nil {
			t.Fatal(err)
		}
		return stale
	}},
	{"two live siblings", func(t *testing.T, s *occ.Store) occ.ETag {
		mustPut(t, s, "k", "x", occ.Unconditional())
		stale := s.Record("k").ETag()
		mustPut(t, s, "k", "y", occ.Unconditional())
		return stale
	}},
	{"a live sibling and a tombstone sibling", func(t *testing.T, s *occ.Store) occ.ETag {
		mustPut(t, s, "k", "x", occ.IfNoneMatch())
		stale := s.Record("k").ETag()
		if _, err := s.Delete("k", occ.Unconditional()); err != nil {
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

type outcome struct {
	err      error
	siblings int
}

func ok(n int) outcome { return outcome{siblings: n} }

var (
	pf = outcome{err: occ.ErrPreconditionFailed}
	nf = outcome{err: occ.ErrNotFound}
)

func runCASTable(t *testing.T, del bool, want [][]outcome) {
	for si, st := range states {
		for pi, pr := range preRows {
			w := want[si][pi]
			t.Run(st.name+" with "+pr.name, func(t *testing.T) {
				s, _ := newTestStore(t, "n1", 1, 1)
				stale := st.build(t, s)
				before := s.Record("k")
				p := pr.make(before.ETag(), stale)
				if p.Holds(before) != (w.err == nil || errors.Is(w.err, occ.ErrNotFound)) {
					t.Fatalf("Holds = %v disagrees with the expected outcome", p.Holds(before))
				}
				var v occ.Version
				var err error
				if del {
					v, err = s.Delete("k", p)
				} else {
					v, err = s.Put("k", []byte("new"), p)
				}
				after := s.Record("k")
				if w.err != nil {
					if !errors.Is(err, w.err) {
						t.Fatalf("err = %v, want %v", err, w.err)
					}
					if !after.Equal(before) || after.ETag() != before.ETag() {
						t.Fatal("a refused write changed the record")
					}
					return
				}
				if err != nil {
					t.Fatalf("unexpected error %v", err)
				}
				if after.Len() != w.siblings {
					t.Fatalf("record has %d siblings %v, want %d", after.Len(), values(after), w.siblings)
				}
				if !slices.Contains(after.IDs(), v.ID()) || v.Tombstone() != del {
					t.Fatal("the new version is not in the record")
				}
				if p.IsUnconditional() {
					for _, old := range before.Siblings() {
						if !slices.Contains(after.IDs(), old.ID()) {
							t.Fatal("an unconditional write overwrote a version it did not read")
						}
					}
				} else {
					for _, old := range before.Siblings() {
						if !v.Supersedes(old) {
							t.Fatal("a conditional write does not supersede what its etag covered")
						}
					}
				}
			})
		}
	}
}

func TestConditionalPutTable(t *testing.T) {
	runCASTable(t, false, [][]outcome{
		{ok(1), ok(1), pf, pf, pf},
		{ok(2), pf, ok(1), pf, pf},
		{ok(2), ok(1), ok(1), pf, pf},
		{ok(3), pf, ok(1), pf, pf},
		{ok(3), pf, ok(1), pf, pf},
	})
}

func TestConditionalDeleteTable(t *testing.T) {
	runCASTable(t, true, [][]outcome{
		{nf, nf, pf, pf, pf},
		{ok(2), pf, ok(1), pf, pf},
		{nf, nf, nf, pf, pf},
		{ok(3), pf, ok(1), pf, pf},
		{ok(3), pf, ok(1), pf, pf},
	})
}

func TestETagDeterministic(t *testing.T) {
	a, _ := newTestStore(t, "a", 1, 1)
	b, _ := newTestStore(t, "b", 1, 2)
	c, _ := newTestStore(t, "c", 1, 3)
	mustPut(t, a, "k", "from-a", occ.Unconditional())
	mustPut(t, b, "k", "from-b", occ.Unconditional())
	mustPut(t, c, "k", "from-c", occ.Unconditional())
	x, _ := newTestStore(t, "x", 1, 4)
	y, _ := newTestStore(t, "y", 1, 5)
	x.Merge(a.Doc())
	x.Merge(b.Doc())
	x.Merge(c.Doc())
	y.Merge(c.Doc())
	y.Merge(a.Doc())
	y.Merge(b.Doc())
	y.Merge(a.Doc())
	if x.Record("k").ETag() != y.Record("k").ETag() || x.Record("k").Len() != 3 {
		t.Fatal("equal sibling sets merged in different orders have different etags")
	}
	tags := map[occ.ETag]bool{x.Record("k").ETag(): true, a.Record("k").ETag(): true, b.Record("k").ETag(): true, c.Record("k").ETag(): true}
	if len(tags) != 4 {
		t.Fatal("different sibling sets share an etag")
	}
	if (occ.Record{}).ETag() != (occ.ETag{}) || !(occ.Record{}).ETag().IsZero() {
		t.Fatal("an empty record must have the zero etag")
	}
	before := a.Record("k").ETag()
	mustPut(t, a, "k", "from-a", occ.IfMatch(before))
	if a.Record("k").ETag() == before {
		t.Fatal("a new version with the same value kept the etag")
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

func TestResolveCollapsesSiblings(t *testing.T) {
	a, _ := newTestStore(t, "a", 1, 1)
	b, _ := newTestStore(t, "b", 1, 2)
	base := mustPut(t, a, "k", "base", occ.IfNoneMatch())
	b.Merge(a.Doc())
	et := a.Record("k").ETag()
	va := mustPut(t, a, "k", "left", occ.IfMatch(et))
	vb := mustPut(t, b, "k", "right", occ.IfMatch(et))
	a.Merge(b.Doc())
	r := a.Record("k")
	if r.Len() != 2 || !slices.Equal(values(r), []string{"left", "right"}) {
		t.Fatalf("concurrent writes did not become siblings: %v", values(r))
	}
	_, _, err := a.Get("k")
	ce, isConflict := occ.IsConflict(err)
	if !isConflict || !errors.Is(err, occ.ErrConflict) {
		t.Fatalf("get on siblings: %v", err)
	}
	if ce.Key != "k" || ce.ETag != r.ETag() || len(ce.Siblings) != 2 || ce.Error() == "" {
		t.Fatalf("conflict error fields: %+v", ce.Key)
	}
	if _, err := a.Put("k", []byte("stale"), occ.IfMatch(et)); !errors.Is(err, occ.ErrPreconditionFailed) {
		t.Fatalf("stale resolve: %v", err)
	}
	res := mustPut(t, a, "k", "merged", occ.IfMatch(ce.ETag))
	r = a.Record("k")
	if r.Len() != 1 || r.IDs()[0] != res.ID() {
		t.Fatal("a resolving write naming the etag did not collapse the siblings")
	}
	for _, old := range []occ.Version{base, va, vb} {
		if !res.Supersedes(old) || old.Supersedes(res) || !r.Covers(old) {
			t.Fatal("the resolution does not supersede every sibling it named")
		}
	}
	b.Merge(a.Doc())
	if !b.Doc().Equal(a.Doc()) {
		t.Fatal("the resolution did not replace the siblings on the other replica")
	}
	got, _, err := b.Get("k")
	if err != nil || string(got) != "merged" {
		t.Fatalf("get after resolve: %q %v", got, err)
	}
	if _, _, err := b.Get("missing"); !errors.Is(err, occ.ErrNotFound) {
		t.Fatalf("missing key: %v", err)
	}
}

func TestUnconditionalWriteStaysConcurrentWithItsOwnEarlierWrite(t *testing.T) {
	a, _ := newTestStore(t, "a", 1, 1)
	b, _ := newTestStore(t, "b", 1, 2)
	first := mustPut(t, a, "k", "one", occ.Unconditional())
	b.Merge(a.Doc())
	second := mustPut(t, a, "k", "two", occ.Unconditional())
	if first.Supersedes(second) || second.Supersedes(first) {
		t.Fatal("blind writes must be concurrent")
	}
	b.Merge(a.Doc())
	if r := b.Record("k"); r.Len() != 2 {
		t.Fatalf("siblings after merge: %v", values(r))
	}
}

func TestEpochRotatesAtCounterOverflow(t *testing.T) {
	a, _ := newTestStore(t, "a", 7, 1)
	hostile := occ.NewDoc()
	other := occ.Actor{Node: "z", Epoch: 1}
	v := occ.NewVersion(occ.Dot{Actor: other, Counter: 1}, occ.NewVersionVector(occ.Entry{Actor: a.Actor(), Counter: math.MaxUint64}), false, []byte("x"), occ.Stamp{})
	hostile.MergeRecord("k", occ.NewRecord(v))
	a.Merge(hostile)
	before := a.Actor()
	w := mustPut(t, a, "k", "y", occ.IfMatch(a.Record("k").ETag()))
	if a.Actor() == before || a.Actor().Node != "a" || a.Rotations() != 1 {
		t.Fatalf("actor did not rotate: %s -> %s", before.String(), a.Actor().String())
	}
	if w.Dot().Actor != a.Actor() || w.Dot().Counter != 1 || !w.Supersedes(v) {
		t.Fatal("the write after rotation is not a fresh dot that supersedes what it read")
	}
	mustPut(t, a, "other", "z", occ.IfNoneMatch())
	if a.Rotations() != 1 {
		t.Fatal("rotated without overflow")
	}
}

func TestNewStoreValidates(t *testing.T) {
	if _, err := occ.NewStore(occ.StoreConfig{}); !errors.Is(err, env.ErrInvalidArgument) {
		t.Fatalf("err = %v", err)
	}
	s, _ := newTestStore(t, "a", 0, 1)
	if s.Actor().Epoch == 0 {
		t.Fatal("epoch 0 must draw a fresh epoch")
	}
	if s.HLC() == nil || s.AheadStamps() != 0 {
		t.Fatal("hlc")
	}
}

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
