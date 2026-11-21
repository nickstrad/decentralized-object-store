package occ_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"pgregory.net/rapid"
)

const maxEnumerated = 64

func events(v occ.Version) (map[occ.Dot]bool, bool) {
	m := map[occ.Dot]bool{v.Dot(): true}
	for _, e := range v.Context().Entries() {
		if e.Counter > maxEnumerated {
			return nil, false
		}
		for c := uint64(1); c <= e.Counter; c++ {
			m[occ.Dot{Actor: e.Actor, Counter: c}] = true
		}
	}
	return m, true
}

func strictlyBelow(s, t occ.Version) bool {
	es, ok1 := events(s)
	et, ok2 := events(t)
	if !ok1 || !ok2 {
		return t.Supersedes(s)
	}
	if len(es) >= len(et) {
		return false
	}
	for d := range es {
		if !et[d] {
			return false
		}
	}
	return true
}

func mergeDocs(x, y *occ.Doc) *occ.Doc {
	z := x.Clone()
	z.Merge(y)
	return z
}

func checkLaws(a, b, c *occ.Doc) error {
	ab, ba := mergeDocs(a, b), mergeDocs(b, a)
	if !ab.Equal(ba) {
		return errors.New("merge is not commutative")
	}
	if !mergeDocs(ab, c).Equal(mergeDocs(a, mergeDocs(b, c))) {
		return errors.New("merge is not associative")
	}
	if !mergeDocs(a, a).Equal(a) {
		return errors.New("merge is not idempotent")
	}
	keys := append(a.Keys(), b.Keys()...)
	for _, k := range keys {
		union := append(a.Record(k).Siblings(), b.Record(k).Siblings()...)
		got := ab.Record(k)
		for _, s := range union {
			dominated := false
			for _, t := range union {
				if strictlyBelow(s, t) {
					dominated = true
					break
				}
			}
			if !dominated && !slices.Contains(got.IDs(), s.ID()) {
				return fmt.Errorf("merge dropped non-dominated sibling %s of key %q", s.ID().String()[:12], k)
			}
		}
		for _, s := range got.Siblings() {
			if !slices.ContainsFunc(union, func(u occ.Version) bool { return u.ID() == s.ID() }) {
				return fmt.Errorf("merge invented sibling %s", s.ID().String()[:12])
			}
			for _, t := range got.Siblings() {
				if strictlyBelow(s, t) {
					return fmt.Errorf("merge kept dominated sibling %s", s.ID().String()[:12])
				}
			}
		}
	}
	for _, k := range append(a.ScalarKeys(), b.ScalarKeys()...) {
		sa, _ := a.Scalar(k)
		sb, _ := b.Scalar(k)
		want := sa.Merge(sb)
		got, _ := ab.Scalar(k)
		if got.Compare(want) != 0 {
			return fmt.Errorf("scalar %q is not the last writer", k)
		}
	}
	return nil
}

type replica struct {
	s     *occ.Store
	clock *fakeClock
}

func newReplicas(t *rapid.T, n int) []replica {
	out := make([]replica, n)
	for i := range out {
		c := &fakeClock{now: env.Time(rapid.Int64Range(0, 5_000).Draw(t, "skew"))}
		s, err := occ.NewStore(occ.StoreConfig{Node: env.NodeID(fmt.Sprintf("r%d", i)), Epoch: uint64(i + 1), Clock: c, Rand: rand.New(rand.NewPCG(uint64(i), 1))})
		if err != nil {
			t.Fatal(err)
		}
		out[i] = replica{s: s, clock: c}
	}
	return out
}

var propKeys = []string{"a", "b", "c"}

type preChoice int

const (
	preNone preChoice = iota
	preINM
	preCurrent
	preStale
	preRandom
)

func choosePre(t *rapid.T, rec occ.Record, history []occ.ETag, allowBlind bool) (occ.Precondition, preChoice) {
	hi := int(preRandom)
	k := preChoice(rapid.IntRange(0, hi).Draw(t, "pre"))
	if !allowBlind && k == preNone {
		k = preCurrent
	}
	switch k {
	case preNone:
		return occ.Unconditional(), k
	case preINM:
		return occ.IfNoneMatch(), k
	case preCurrent:
		return occ.IfMatch(rec.ETag()), k
	case preStale:
		if len(history) > 0 {
			return occ.IfMatch(history[rapid.IntRange(0, len(history)-1).Draw(t, "stale")]), k
		}
		return occ.IfMatch(rec.ETag()), preCurrent
	}
	var e occ.ETag
	e[0] = byte(rapid.IntRange(1, 255).Draw(t, "etag"))
	return occ.IfMatch(e), k
}

func expectedOutcome(p occ.Precondition, rec occ.Record, del bool) error {
	if !p.Holds(rec) {
		return occ.ErrPreconditionFailed
	}
	if del && len(rec.Live()) == 0 {
		return occ.ErrNotFound
	}
	return nil
}

func modelHolds(k preChoice, rec occ.Record, e occ.ETag) bool {
	switch k {
	case preNone:
		return true
	case preINM:
		return len(rec.Live()) == 0
	}
	return rec.Len() > 0 && e == rec.ETag()
}

func propConditionalWrite(t *rapid.T) {
	reps := newReplicas(t, rapid.IntRange(2, 4).Draw(t, "replicas"))
	allowBlind := rapid.Bool().Draw(t, "blind")
	or := newOracle()
	history := map[string][]occ.ETag{}
	blind := map[string]bool{}
	writers := map[string]map[occ.Actor]bool{}
	var scalars []occ.Scalar
	steps := rapid.IntRange(1, 80).Draw(t, "steps")
	for range steps {
		i := rapid.IntRange(0, len(reps)-1).Draw(t, "replica")
		r := reps[i]
		switch rapid.IntRange(0, 9).Draw(t, "action") {
		case 0, 1, 2, 3, 4:
			key := rapid.SampledFrom(propKeys).Draw(t, "key")
			del := rapid.IntRange(0, 4).Draw(t, "del") == 0
			rec := r.s.Record(key)
			p, kind := choosePre(t, rec, history[key], allowBlind)
			want := expectedOutcome(p, rec, del)
			if modelHolds(kind, rec, etagOf(p)) != (want == nil || errors.Is(want, occ.ErrNotFound)) {
				t.Fatalf("Holds disagrees with the model for %s", p.String())
			}
			var v occ.Version
			var err error
			if del {
				v, err = r.s.Delete(key, p)
			} else {
				v, err = r.s.Put(key, []byte(fmt.Sprintf("%s-%d", key, rapid.IntRange(0, 1000).Draw(t, "val"))), p)
			}
			after := r.s.Record(key)
			if want != nil {
				if !errors.Is(err, want) {
					t.Fatalf("%s on %q: err %v, want %v", p.String(), key, err, want)
				}
				if !after.Equal(rec) {
					t.Fatal("a refused write changed state")
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s on %q: unexpected %v", p.String(), key, err)
			}
			history[key] = append(history[key], rec.ETag())
			var covered []occ.Version
			if p.IsUnconditional() {
				blind[key] = true
				if after.Len() != rec.Len()+1 {
					t.Fatal("an unconditional write must not replace what it did not read")
				}
			} else {
				covered = rec.Siblings()
				if after.Len() != 1 {
					t.Fatal("a conditional write must leave exactly its own version")
				}
			}
			if writers[key] == nil {
				writers[key] = map[occ.Actor]bool{}
			}
			writers[key][v.Dot().Actor] = true
			or.record(key, v, covered)
		case 5, 6, 7:
			j := rapid.IntRange(0, len(reps)-1).Draw(t, "from")
			r.s.Merge(reps[j].s.Doc().Clone())
		case 8:
			r.clock.now += env.Time(rapid.Int64Range(-100, 1_000).Draw(t, "tick"))
		default:
			sc, err := r.s.SetScalar("s", []byte(fmt.Sprintf("v%d", rapid.IntRange(0, 9).Draw(t, "sval"))))
			if err != nil {
				t.Fatal(err)
			}
			scalars = append(scalars, sc)
		}
		if err := or.checkUnion(reps); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		for i := range reps {
			for j := range reps {
				reps[i].s.Merge(reps[j].s.Doc())
			}
		}
	}
	final := reps[0].s.Doc()
	for _, r := range reps[1:] {
		if !r.s.Doc().Equal(final) {
			t.Fatal("replicas that exchanged everything differ")
		}
		for _, k := range propKeys {
			if r.s.Record(k).ETag() != final.Record(k).ETag() {
				t.Fatal("etags differ after convergence")
			}
		}
	}
	if err := or.check(final); err != nil {
		t.Fatal(err)
	}
	for _, k := range propKeys {
		if n := final.Record(k).Len(); !blind[k] && n > len(writers[k]) {
			t.Fatalf("key %q has %d siblings from %d conditional writers", k, n, len(writers[k]))
		}
	}
	if len(scalars) > 0 {
		want := scalars[0]
		for _, s := range scalars[1:] {
			want = want.Merge(s)
		}
		got, _ := final.Scalar("s")
		if got.Compare(want) != 0 {
			t.Fatal("scalar did not settle on the highest stamp")
		}
	}
}

func etagOf(p occ.Precondition) occ.ETag {
	s := p.String()
	const pfx = "If-Match: "
	if len(s) <= len(pfx) || s[:len(pfx)] != pfx {
		return occ.ETag{}
	}
	e, _ := occ.ParseETag(s[len(pfx):])
	return e
}

func (o *oracle) checkUnion(reps []replica) error {
	for _, a := range o.acks {
		found := false
		for _, r := range reps {
			for _, w := range r.s.Record(a.key).Siblings() {
				if w.ID() == a.v.ID() || o.seen[w.ID()][a.v.ID()] {
					found = true
				}
			}
		}
		if !found {
			return fmt.Errorf("acknowledged version %s of %q vanished from every replica", a.v.ID().String()[:12], a.key)
		}
	}
	return nil
}

func TestPropNoSilentLoss(t *testing.T) {
	rapid.Check(t, propConditionalWrite)
}

func FuzzOCCConditionalWrite(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add(make([]byte, 64))
	f.Add(slices.Repeat([]byte{0xff}, 64))
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	f.Fuzz(rapid.MakeFuzz(propConditionalWrite))
}

func TestPropCheckAndSetModel(t *testing.T) {
	type msib struct {
		value string
		tomb  bool
	}
	rapid.Check(t, func(t *rapid.T) {
		s, _ := newTestStore(t, "solo", 1, 1)
		model := map[string]map[occ.VersionID]msib{}
		etagOfSet := map[string]occ.ETag{}
		setOfEtag := map[occ.ETag]string{}
		var history []occ.ETag
		setKey := func(m map[occ.VersionID]msib) string {
			var ks []string
			for id := range m {
				ks = append(ks, id.String())
			}
			slices.Sort(ks)
			return fmt.Sprint(ks)
		}
		for range rapid.IntRange(1, 60).Draw(t, "steps") {
			key := rapid.SampledFrom(propKeys).Draw(t, "key")
			del := rapid.Bool().Draw(t, "del")
			m := model[key]
			rec := s.Record(key)
			var p occ.Precondition
			switch rapid.IntRange(0, 4).Draw(t, "pre") {
			case 0:
				p = occ.Unconditional()
			case 1:
				p = occ.IfNoneMatch()
			case 2:
				p = occ.IfMatch(rec.ETag())
			case 3:
				if len(history) == 0 {
					p = occ.IfMatch(occ.ETag{9})
				} else {
					p = occ.IfMatch(history[rapid.IntRange(0, len(history)-1).Draw(t, "h")])
				}
			default:
				p = occ.IfMatch(occ.ETag{})
			}
			live := 0
			for _, x := range m {
				if !x.tomb {
					live++
				}
			}
			holds := true
			switch {
			case p.String() == "If-None-Match: *":
				holds = live == 0
			case !p.IsUnconditional():
				holds = len(m) > 0 && etagOfSet[setKey(m)] == etagOf(p)
			}
			value := fmt.Sprintf("v%d", rapid.IntRange(0, 99).Draw(t, "v"))
			var v occ.Version
			var err error
			if del {
				v, err = s.Delete(key, p)
			} else {
				v, err = s.Put(key, []byte(value), p)
			}
			switch {
			case !holds:
				if !errors.Is(err, occ.ErrPreconditionFailed) {
					t.Fatalf("model refuses %s, store says %v", p.String(), err)
				}
			case del && live == 0:
				if !errors.Is(err, occ.ErrNotFound) {
					t.Fatalf("model says not found, store says %v", err)
				}
			default:
				if err != nil {
					t.Fatalf("model accepts %s, store says %v", p.String(), err)
				}
				history = append(history, rec.ETag())
				if !p.IsUnconditional() || m == nil {
					m = map[occ.VersionID]msib{}
				}
				m[v.ID()] = msib{value: value, tomb: del}
				if del {
					m[v.ID()] = msib{tomb: true}
				}
				model[key] = m
			}
			got := s.Record(key)
			if len(got.Siblings()) != len(m) {
				t.Fatalf("store has %d siblings, model %d", got.Len(), len(m))
			}
			for _, w := range got.Siblings() {
				x, ok := m[w.ID()]
				if !ok || x.tomb != w.Tombstone() || (!x.tomb && x.value != string(w.Value())) {
					t.Fatal("store sibling differs from the model")
				}
			}
			if len(m) > 0 {
				sk := setKey(m)
				if e, ok := etagOfSet[sk]; ok && e != got.ETag() {
					t.Fatal("one sibling set, two etags")
				}
				if prev, ok := setOfEtag[got.ETag()]; ok && prev != sk {
					t.Fatal("one etag, two sibling sets")
				}
				etagOfSet[sk] = got.ETag()
				setOfEtag[got.ETag()] = sk
			}
		}
	})
}

func genHostileDoc(t *rapid.T, label string) *occ.Doc {
	actors := []occ.Actor{{Node: "x", Epoch: 1}, {Node: "x", Epoch: 2}, {Node: "y", Epoch: 1}}
	d := occ.NewDoc()
	for i := range rapid.IntRange(0, 3).Draw(t, label+"keys") {
		_ = i
		key := rapid.SampledFrom(propKeys).Draw(t, label+"key")
		var vs []occ.Version
		for range rapid.IntRange(1, 3).Draw(t, label+"n") {
			dot := occ.Dot{Actor: rapid.SampledFrom(actors).Draw(t, label+"actor"), Counter: uint64(rapid.IntRange(1, 4).Draw(t, label+"c"))}
			var es []occ.Entry
			for _, a := range actors {
				es = append(es, occ.Entry{Actor: a, Counter: uint64(rapid.IntRange(0, 4).Draw(t, label+"ctx"))})
			}
			tomb := rapid.IntRange(0, 3).Draw(t, label+"tomb") == 0
			vs = append(vs, occ.NewVersion(dot, occ.NewVersionVector(es...), tomb, []byte{byte(rapid.IntRange(0, 2).Draw(t, label+"val"))}, occ.Stamp{Wall: int64(rapid.IntRange(0, 3).Draw(t, label+"w"))}))
		}
		d.MergeRecord(key, occ.NewRecord(vs...))
	}
	if rapid.Bool().Draw(t, label+"scalar") {
		d.MergeScalar("s", occ.Scalar{Value: []byte{byte(rapid.IntRange(0, 2).Draw(t, label+"sv"))}, Stamp: occ.Stamp{Wall: int64(rapid.IntRange(0, 2).Draw(t, label+"sw")), Node: env.NodeID(rapid.SampledFrom([]string{"x", "y"}).Draw(t, label+"sn"))}})
	}
	return d
}

func genHonestDocs(t *rapid.T) []*occ.Doc {
	reps := newReplicas(t, rapid.IntRange(1, 3).Draw(t, "hreps"))
	var snaps []*occ.Doc
	for range rapid.IntRange(1, 30).Draw(t, "hsteps") {
		r := reps[rapid.IntRange(0, len(reps)-1).Draw(t, "hr")]
		key := rapid.SampledFrom(propKeys).Draw(t, "hkey")
		switch rapid.IntRange(0, 4).Draw(t, "hact") {
		case 0:
			_, _ = r.s.Put(key, []byte("u"), occ.Unconditional())
		case 1:
			_, _ = r.s.Put(key, []byte("c"), occ.IfMatch(r.s.Record(key).ETag()))
		case 2:
			_, _ = r.s.Delete(key, occ.IfMatch(r.s.Record(key).ETag()))
		case 3:
			r.s.Merge(reps[rapid.IntRange(0, len(reps)-1).Draw(t, "hfrom")].s.Doc())
		default:
			_, _ = r.s.SetScalar("s", []byte(key))
		}
		snaps = append(snaps, r.s.Doc().Clone())
	}
	return snaps
}

func pickDoc(t *rapid.T, honest []*occ.Doc, label string) *occ.Doc {
	if rapid.IntRange(0, 2).Draw(t, label+"kind") == 0 {
		return genHostileDoc(t, label)
	}
	return honest[rapid.IntRange(0, len(honest)-1).Draw(t, label+"snap")]
}

func propMergeLaws(t *rapid.T) {
	honest := genHonestDocs(t)
	a, b, c := pickDoc(t, honest, "a"), pickDoc(t, honest, "b"), pickDoc(t, honest, "c")
	for _, d := range []*occ.Doc{a, b, c} {
		back, err := occ.Decode(d.Encode())
		if err != nil || !back.Equal(d) {
			t.Fatalf("a generated state does not round-trip: %v", err)
		}
	}
	if err := checkLaws(a, b, c); err != nil {
		t.Fatal(err)
	}
}

func TestPropMergeLaws(t *testing.T) {
	rapid.Check(t, propMergeLaws)
}

func FuzzOCCMergeLaws(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add(make([]byte, 128))
	f.Add(slices.Repeat([]byte{0xff}, 128))
	f.Fuzz(rapid.MakeFuzz(propMergeLaws))
}

func TestPropConvergence(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		reps := newReplicas(t, rapid.IntRange(2, 3).Draw(t, "writers"))
		var msgs [][]byte
		for range rapid.IntRange(1, 25).Draw(t, "steps") {
			r := reps[rapid.IntRange(0, len(reps)-1).Draw(t, "w")]
			key := rapid.SampledFrom(propKeys).Draw(t, "key")
			switch rapid.IntRange(0, 4).Draw(t, "act") {
			case 0:
				_, _ = r.s.Put(key, []byte("u"), occ.Unconditional())
			case 1:
				_, _ = r.s.Put(key, []byte("c"), occ.IfMatch(r.s.Record(key).ETag()))
			case 2:
				_, _ = r.s.Delete(key, occ.IfMatch(r.s.Record(key).ETag()))
			case 3:
				r.s.Merge(reps[rapid.IntRange(0, len(reps)-1).Draw(t, "from")].s.Doc())
			default:
				_, _ = r.s.SetScalar(key, []byte("s"))
			}
			if rapid.Bool().Draw(t, "delta") {
				msgs = append(msgs, r.s.Doc().Subset(key).Encode())
			} else {
				msgs = append(msgs, r.s.Doc().Encode())
			}
		}
		ref := occ.NewDoc()
		for _, m := range msgs {
			d, err := occ.Decode(m)
			if err != nil {
				t.Fatal(err)
			}
			ref.Merge(d)
		}
		for rcv := range 3 {
			order := rapid.Permutation(func() []int {
				out := make([]int, len(msgs))
				for i := range out {
					out[i] = i
				}
				return out
			}()).Draw(t, fmt.Sprintf("order%d", rcv))
			for range rapid.IntRange(0, len(msgs)).Draw(t, "dups") {
				pos := rapid.IntRange(0, len(order)).Draw(t, "pos")
				order = slices.Insert(order, pos, rapid.IntRange(0, len(msgs)-1).Draw(t, "dup"))
			}
			got := occ.NewDoc()
			for _, i := range order {
				d, err := occ.Decode(msgs[i])
				if err != nil {
					t.Fatal(err)
				}
				got.Merge(d)
			}
			if !got.Equal(ref) || got.Digest() != ref.Digest() {
				t.Fatalf("receiver %d differs after delivery order %v", rcv, order)
			}
			for _, k := range ref.Keys() {
				if got.Record(k).ETag() != ref.Record(k).ETag() {
					t.Fatal("etags differ")
				}
			}
		}
	})
}

func TestPropSiblingsOnlyWhenConcurrent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		reps := newReplicas(t, rapid.IntRange(2, 4).Draw(t, "writers"))
		for range rapid.IntRange(1, 40).Draw(t, "steps") {
			w := reps[rapid.IntRange(0, len(reps)-1).Draw(t, "w")]
			for _, o := range reps {
				w.s.Merge(o.s.Doc())
			}
			key := rapid.SampledFrom(propKeys).Draw(t, "key")
			rec := w.s.Record(key)
			var err error
			switch {
			case len(rec.Live()) == 0:
				_, err = w.s.Put(key, []byte("new"), occ.IfNoneMatch())
			case rapid.IntRange(0, 3).Draw(t, "del") == 0:
				_, err = w.s.Delete(key, occ.IfMatch(rec.ETag()))
			default:
				_, err = w.s.Put(key, []byte("next"), occ.IfMatch(rec.ETag()))
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range reps {
				for _, k := range propKeys {
					if n := o.s.Record(k).Len(); n > 1 {
						t.Fatalf("causally ordered writes left %d siblings on %q", n, k)
					}
				}
			}
			o := reps[rapid.IntRange(0, len(reps)-1).Draw(t, "gossip")]
			o.s.Merge(w.s.Doc())
		}
	})
}

func TestCanaryMergeDropsSibling(t *testing.T) {
	scenario := func() error {
		a, _ := newTestStore(t, "a", 1, 1)
		b, _ := newTestStore(t, "b", 1, 2)
		or := newOracle()
		base := mustPut(t, a, "k", "base", occ.IfNoneMatch())
		or.record("k", base, nil)
		b.Merge(a.Doc())
		et := a.Record("k").ETag()
		prev := a.Record("k").Siblings()
		or.record("k", mustPut(t, a, "k", "left", occ.IfMatch(et)), prev)
		or.record("k", mustPut(t, b, "k", "right", occ.IfMatch(et)), prev)
		a.Merge(b.Doc())
		return or.check(a.Doc())
	}
	if err := scenario(); err != nil {
		t.Fatalf("the honest merge must keep both siblings: %v", err)
	}
	restore := occ.SetCanaryDropConcurrent(true)
	defer restore()
	if err := scenario(); err == nil {
		t.Fatal("the no-silent-loss check did not catch a merge that drops a concurrent sibling")
	}
	failed := 0
	for seed := int64(1); seed <= canarySeeds; seed++ {
		s := multiRegionRun(multiRegionOptions{})(seed)
		if s.Err() != nil {
			failed++
		}
	}
	if failed == 0 {
		t.Fatalf("the convergence DST passed on all %d seeds with a merge that drops siblings", canarySeeds)
	}
	t.Logf("canary: convergence DST failed on %d of %d seeds with the sibling-dropping merge", failed, canarySeeds)
}
