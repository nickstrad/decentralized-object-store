package occ_test

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"pgregory.net/rapid"
)

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
	keys := slices.Compact(slices.Sorted(slices.Values(append(a.Keys(), b.Keys()...))))
	if !slices.Equal(keys, ab.Keys()) {
		return errors.New("merge invented or dropped a key")
	}
	for _, k := range keys {
		va, okA := a.Record(k).Version()
		vb, okB := b.Record(k).Version()
		want := va
		if !okA || (okB && lwwLess(va, vb)) {
			want = vb
		}
		got, _ := ab.Record(k).Version()
		if got.ID() != want.ID() {
			return fmt.Errorf("key %q holds %s, not the LWW maximum %s", k, short(got.ID()), short(want.ID()))
		}
	}
	return nil
}

type replica struct {
	s     *occ.Store
	clock *fakeClock
}

func newReplicas(t *rapid.T, n int, or *oracle, maxSkew int64) []replica {
	out := make([]replica, n)
	for i := range out {
		c := &fakeClock{now: env.Time(1_000_000 + rapid.Int64Range(-maxSkew, maxSkew).Draw(t, "skew"))}
		cfg := occ.StoreConfig{Node: env.NodeID(fmt.Sprintf("r%d", i)), Epoch: uint64(i + 1), Clock: c, Rand: rand.New(rand.NewPCG(uint64(i), 1))}
		if or != nil {
			cfg.OnDiscard = or.discard
		}
		s, err := occ.NewStore(cfg)
		if err != nil {
			t.Fatal(err)
		}
		out[i] = replica{s: s, clock: c}
	}
	return out
}

func docsOf(reps []replica) []*occ.Doc {
	out := make([]*occ.Doc, len(reps))
	for i, r := range reps {
		out[i] = r.s.Doc()
	}
	return out
}

func exchangeAll(t *rapid.T, reps []replica) {
	for range 2 {
		for i := range reps {
			for j := range reps {
				if _, err := reps[i].s.Merge(reps[j].s.Doc()); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

var propKeys = []string{"a", "b", "c"}

func choosePre(t *rapid.T, rec occ.Record, history []occ.ETag) occ.Precondition {
	switch rapid.IntRange(0, 4).Draw(t, "pre") {
	case 0:
		return occ.Unconditional()
	case 1:
		return occ.IfNoneMatch()
	case 2:
		return occ.IfMatch(rec.ETag())
	case 3:
		if len(history) > 0 {
			return occ.IfMatch(history[rapid.IntRange(0, len(history)-1).Draw(t, "stale")])
		}
		return occ.IfMatch(rec.ETag())
	}
	var e occ.ETag
	e[0] = byte(rapid.IntRange(1, 255).Draw(t, "etag"))
	return occ.IfMatch(e)
}

func expectedOutcome(p occ.Precondition, rec occ.Record, del bool) error {
	if !p.Holds(rec) {
		return occ.ErrPreconditionFailed
	}
	if del && !rec.Live() {
		return occ.ErrNotFound
	}
	return nil
}

func propEveryLossRecorded(t *rapid.T) {
	or := newOracle()
	reps := newReplicas(t, rapid.IntRange(2, 4).Draw(t, "replicas"), or, 5_000)
	history := map[string][]occ.ETag{}
	for range rapid.IntRange(1, 80).Draw(t, "steps") {
		r := reps[rapid.IntRange(0, len(reps)-1).Draw(t, "replica")]
		switch rapid.IntRange(0, 9).Draw(t, "action") {
		case 0, 1, 2, 3, 4, 5:
			key := rapid.SampledFrom(propKeys).Draw(t, "key")
			del := rapid.IntRange(0, 4).Draw(t, "del") == 0
			rec := r.s.Record(key)
			p := choosePre(t, rec, history[key])
			want := expectedOutcome(p, rec, del)
			var v occ.Version
			var err error
			if del {
				v, err = r.s.Delete(key, p)
			} else {
				v, err = r.s.Put(key, fmt.Appendf(nil, "%s-%d", key, rapid.IntRange(0, 1000).Draw(t, "val")), p)
			}
			if want != nil {
				if !errors.Is(err, want) || !r.s.Record(key).Equal(rec) {
					t.Fatalf("%s on %q: err %v, want %v and no change", p.String(), key, err, want)
				}
				continue
			}
			if err != nil {
				t.Fatalf("%s on %q: unexpected %v", p.String(), key, err)
			}
			history[key] = append(history[key], rec.ETag())
			or.record(key, v, rec)
		case 6, 7, 8:
			j := rapid.IntRange(0, len(reps)-1).Draw(t, "from")
			if _, err := r.s.Merge(reps[j].s.Doc()); err != nil {
				t.Fatal(err)
			}
		default:
			r.clock.now += env.Time(rapid.Int64Range(-100, 1_000).Draw(t, "tick"))
		}
		if err := or.check(docsOf(reps), false); err != nil {
			t.Fatal(err)
		}
	}
	exchangeAll(t, reps)
	final := reps[0].s.Doc()
	var total uint64
	for _, r := range reps {
		if !r.s.Doc().Equal(final) {
			t.Fatal("replicas that exchanged everything differ")
		}
		for _, k := range propKeys {
			if r.s.Record(k).ETag() != final.Record(k).ETag() {
				t.Fatal("etags differ after convergence")
			}
		}
		total += r.s.LWWDiscards()
	}
	if err := or.check(docsOf(reps), true); err != nil {
		t.Fatal(err)
	}
	if total != uint64(or.logged) {
		t.Fatalf("the counters say %d discards, the logs hold %d", total, or.logged)
	}
}

func TestPropEveryLossRecorded(t *testing.T) {
	rapid.Check(t, propEveryLossRecorded)
}

func fuzzSeeds(f *testing.F) {
	f.Add(make([]byte, 32<<10))
	for seed := range uint64(3) {
		r := rand.New(rand.NewPCG(seed, 99))
		b := make([]byte, 32<<10)
		for i := range b {
			b[i] = byte(r.Uint32())
		}
		f.Add(b)
	}
}

func FuzzOCCConditionalWrite(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(rapid.MakeFuzz(propEveryLossRecorded))
}

func TestPropCheckAndSetModel(t *testing.T) {
	type mver struct {
		present, tomb bool
		value         string
		etag          occ.ETag
	}
	rapid.Check(t, func(t *rapid.T) {
		s, _ := newTestStore(t, "solo", 1, 1)
		model := map[string]mver{}
		var history []occ.ETag
		etags := map[occ.ETag]occ.VersionID{}
		for range rapid.IntRange(1, 60).Draw(t, "steps") {
			key := rapid.SampledFrom(propKeys).Draw(t, "key")
			del := rapid.Bool().Draw(t, "del")
			m := model[key]
			var p occ.Precondition
			var holds bool
			switch rapid.IntRange(0, 4).Draw(t, "pre") {
			case 0:
				p, holds = occ.Unconditional(), true
			case 1:
				p, holds = occ.IfNoneMatch(), !m.present || m.tomb
			case 2:
				p, holds = occ.IfMatch(m.etag), m.present
			case 3:
				e := occ.ETag{9}
				if len(history) > 0 {
					e = history[rapid.IntRange(0, len(history)-1).Draw(t, "h")]
				}
				p, holds = occ.IfMatch(e), m.present && e == m.etag
			default:
				p, holds = occ.IfMatch(occ.ETag{}), false
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
			case del && (!m.present || m.tomb):
				if !errors.Is(err, occ.ErrNotFound) {
					t.Fatalf("model says not found, store says %v", err)
				}
			default:
				if err != nil {
					t.Fatalf("model accepts %s, store says %v", p.String(), err)
				}
				if m.present && v.ETag() == m.etag {
					t.Fatal("a new version kept the etag")
				}
				if prev, ok := etags[v.ETag()]; ok && prev != v.ID() {
					t.Fatal("one etag, two versions")
				}
				etags[v.ETag()] = v.ID()
				history = append(history, m.etag)
				m = mver{present: true, tomb: del, value: value, etag: v.ETag()}
				if del {
					m.value = ""
				}
				model[key] = m
			}
			got := s.Record(key)
			gv, ok := got.Version()
			if ok != m.present || got.ETag() != m.etag || (ok && (gv.Tombstone() != m.tomb || string(gv.Value()) != m.value)) {
				t.Fatal("the store's version differs from the model")
			}
		}
		if s.LWWDiscards() != 0 {
			t.Fatal("a single replica discarded")
		}
	})
}

func genHostileDoc(t *rapid.T, label string) *occ.Doc {
	actors := []occ.Actor{{Node: "x", Epoch: 1}, {Node: "x", Epoch: 2}, {Node: "y", Epoch: 1}}
	d := occ.NewDoc()
	for range rapid.IntRange(0, 4).Draw(t, label+"keys") {
		key := rapid.SampledFrom(propKeys).Draw(t, label+"key")
		dot := occ.Dot{Actor: rapid.SampledFrom(actors).Draw(t, label+"actor"), Counter: uint64(rapid.IntRange(1, 4).Draw(t, label+"c"))}
		var es []occ.Entry
		for _, a := range actors {
			es = append(es, occ.Entry{Actor: a, Counter: uint64(rapid.IntRange(0, 4).Draw(t, label+"ctx"))})
		}
		tomb := rapid.IntRange(0, 3).Draw(t, label+"tomb") == 0
		var val []byte
		if !tomb {
			val = []byte{byte(rapid.IntRange(0, 2).Draw(t, label+"val"))}
		}
		walls := []int64{math.MinInt64, -1, 0, 1, 2, math.MaxInt64}
		stamp := occ.Stamp{Wall: rapid.SampledFrom(walls).Draw(t, label+"w"), Logical: uint32(rapid.IntRange(0, 1).Draw(t, label+"l")), Node: env.NodeID(rapid.SampledFrom([]string{"", "x", "y"}).Draw(t, label+"n"))}
		d.MergeVersion(key, occ.NewVersion(dot, occ.NewVersionVector(es...), tomb, val, stamp))
	}
	return d
}

func genHonestDocs(t *rapid.T) []*occ.Doc {
	reps := newReplicas(t, rapid.IntRange(1, 3).Draw(t, "hreps"), nil, 5_000)
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
			_, _ = r.s.Merge(reps[rapid.IntRange(0, len(reps)-1).Draw(t, "hfrom")].s.Doc())
		default:
			r.clock.now += env.Time(rapid.Int64Range(-100, 1_000).Draw(t, "htick"))
		}
		snaps = append(snaps, r.s.Doc())
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
	fuzzSeeds(f)
	f.Fuzz(rapid.MakeFuzz(propMergeLaws))
}

func TestPropConvergence(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		reps := newReplicas(t, rapid.IntRange(2, 3).Draw(t, "writers"), nil, 5_000)
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
				_, _ = r.s.Merge(reps[rapid.IntRange(0, len(reps)-1).Draw(t, "from")].s.Doc())
			default:
				r.clock.now += env.Time(rapid.Int64Range(-100, 1_000).Draw(t, "tick"))
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
			got, _ := newTestStore(t, fmt.Sprintf("rcv%d", rcv), 1, uint64(rcv))
			for _, i := range order {
				d, err := occ.Decode(msgs[i])
				if err != nil {
					t.Fatal(err)
				}
				if _, err := got.Merge(d); err != nil {
					t.Fatal(err)
				}
			}
			if !got.Doc().Equal(ref) || got.Digest() != ref.Digest() {
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

func TestPropCausalWriteAlwaysWins(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		or := newOracle()
		reps := newReplicas(t, rapid.IntRange(2, 4).Draw(t, "replicas"), or, int64(sim.MaxSkew))
		versions := map[occ.VersionID]occ.Version{}
		for range rapid.IntRange(1, 60).Draw(t, "steps") {
			r := reps[rapid.IntRange(0, len(reps)-1).Draw(t, "replica")]
			key := rapid.SampledFrom(propKeys).Draw(t, "key")
			switch rapid.IntRange(0, 6).Draw(t, "action") {
			case 0, 1, 2:
				held := r.s.Record(key)
				var v occ.Version
				var err error
				if held.Live() && rapid.IntRange(0, 3).Draw(t, "del") == 0 {
					v, err = r.s.Delete(key, occ.IfMatch(held.ETag()))
				} else {
					v, err = r.s.Put(key, []byte("w"), occ.Unconditional())
				}
				if errors.Is(err, occ.ErrClockExhausted) {
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if hv, ok := held.Version(); ok && (!lwwLess(hv, v) || !v.Supersedes(hv)) {
					t.Fatalf("write %s (stamp %s) does not beat the version %s (stamp %s) it replaced", short(v.ID()), v.Stamp().String(), short(hv.ID()), hv.Stamp().String())
				}
				or.record(key, v, held)
				versions[v.ID()] = v
				for id := range or.seen[v.ID()] {
					x, acked := versions[id]
					if acked && (!lwwLess(x, v) || !v.Supersedes(x)) {
						t.Fatalf("write %s (stamp %s) saw %s (stamp %s) and does not beat it", short(v.ID()), v.Stamp().String(), short(id), x.Stamp().String())
					}
				}
			case 3, 4:
				if _, err := r.s.Merge(reps[rapid.IntRange(0, len(reps)-1).Draw(t, "from")].s.Doc()); err != nil {
					t.Fatal(err)
				}
			case 5:
				r.clock.now += env.Time(rapid.Int64Range(-int64(sim.MaxSkew)/64, int64(sim.MaxSkew)/64).Draw(t, "jump"))
			default:
				forged := occ.NewDoc()
				wall := int64(r.clock.now) + rapid.Int64Range(-int64(sim.MaxSkew), int64(sim.MaxSkew)).Draw(t, "fwall")
				forged.MergeVersion(key, occ.NewVersion(occ.Dot{Actor: occ.Actor{Node: "rogue", Epoch: 1}, Counter: uint64(rapid.IntRange(1, 1000).Draw(t, "fc"))}, occ.VersionVector{}, false, []byte("f"), occ.Stamp{Wall: wall, Logical: rapid.Uint32().Draw(t, "flog"), Node: "rogue"}))
				if _, err := r.s.Merge(forged); err != nil {
					t.Fatal(err)
				}
			}
		}
	})
}

func TestPropNoDiscardWithoutConcurrency(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		or := newOracle()
		reps := newReplicas(t, rapid.IntRange(2, 4).Draw(t, "writers"), or, 5_000)
		for range rapid.IntRange(1, 40).Draw(t, "steps") {
			w := reps[rapid.IntRange(0, len(reps)-1).Draw(t, "w")]
			for _, o := range reps {
				if _, err := w.s.Merge(o.s.Doc()); err != nil {
					t.Fatal(err)
				}
			}
			key := rapid.SampledFrom(propKeys).Draw(t, "key")
			rec := w.s.Record(key)
			var err error
			switch {
			case !rec.Live():
				_, err = w.s.Put(key, []byte("new"), occ.IfNoneMatch())
			case rapid.IntRange(0, 3).Draw(t, "del") == 0:
				_, err = w.s.Delete(key, occ.IfMatch(rec.ETag()))
			case rapid.Bool().Draw(t, "blind"):
				_, err = w.s.Put(key, []byte("blind"), occ.Unconditional())
			default:
				_, err = w.s.Put(key, []byte("next"), occ.IfMatch(rec.ETag()))
			}
			if err != nil {
				t.Fatal(err)
			}
			w.clock.now += env.Time(rapid.Int64Range(-100, 1_000).Draw(t, "tick"))
			o := reps[rapid.IntRange(0, len(reps)-1).Draw(t, "gossip")]
			if _, err := o.s.Merge(w.s.Doc()); err != nil {
				t.Fatal(err)
			}
			if or.logged != 0 {
				t.Fatal("causally ordered writes recorded a discard")
			}
		}
		exchangeAll(t, reps)
		for _, r := range reps {
			if r.s.LWWDiscards() != 0 {
				t.Fatal("causally ordered writes counted a discard")
			}
		}
	})
}
