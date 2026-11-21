package occ_test

import (
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"pgregory.net/rapid"
)

var (
	actA  = occ.Actor{Node: "a", Epoch: 1}
	actB  = occ.Actor{Node: "b", Epoch: 1}
	actC  = occ.Actor{Node: "c", Epoch: 1}
	actA2 = occ.Actor{Node: "a", Epoch: 2}
)

func vv(pairs ...any) occ.VersionVector {
	var es []occ.Entry
	for i := 0; i < len(pairs); i += 2 {
		es = append(es, occ.Entry{Actor: pairs[i].(occ.Actor), Counter: uint64(pairs[i+1].(int))})
	}
	return occ.NewVersionVector(es...)
}

func TestVersionVectorCompareTable(t *testing.T) {
	rows := []struct {
		name string
		a, b occ.VersionVector
		want occ.Ordering
	}{
		{"two empty vectors are equal", vv(), vv(), occ.Equal},
		{"identical vectors are equal", vv(actA, 2, actB, 1), vv(actB, 1, actA, 2), occ.Equal},
		{"a zero entry is the same as a missing one", vv(actA, 2, actB, 0), vv(actA, 2), occ.Equal},
		{"empty is before anything", vv(), vv(actA, 1), occ.Before},
		{"anything is after empty", vv(actA, 1), vv(), occ.After},
		{"smaller counter on one actor is before", vv(actA, 1, actB, 2), vv(actA, 2, actB, 2), occ.Before},
		{"an extra actor is after", vv(actA, 1, actB, 2), vv(actA, 1), occ.After},
		{"crossed counters are concurrent", vv(actA, 2, actB, 1), vv(actA, 1, actB, 2), occ.Concurrent},
		{"disjoint actors are concurrent", vv(actA, 1), vv(actB, 1), occ.Concurrent},
		{"a new epoch of the same node is a different actor", vv(actA2, 1), vv(actA, 5), occ.Concurrent},
		{"three actors, one behind and one ahead, are concurrent", vv(actA, 1, actB, 1, actC, 3), vv(actA, 1, actB, 2, actC, 1), occ.Concurrent},
		{"duplicate entries keep the larger counter", vv(actA, 1, actA, 3), vv(actA, 3), occ.Equal},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if got := r.a.Compare(r.b); got != r.want {
				t.Fatalf("%s.Compare(%s) = %s, want %s", r.a.String(), r.b.String(), got.String(), r.want.String())
			}
			wantDesc := r.want == occ.Equal || r.want == occ.After
			if got := r.a.Descends(r.b); got != wantDesc {
				t.Fatalf("Descends = %v, want %v", got, wantDesc)
			}
		})
	}
}

func TestVersionVectorBasics(t *testing.T) {
	v := vv(actB, 3, actA, 2)
	if v.Len() != 2 || v.Get(actA) != 2 || v.Get(actB) != 3 || v.Get(actC) != 0 {
		t.Fatalf("unexpected vector %s", v.String())
	}
	es := v.Entries()
	es[0].Counter = 99
	if v.Get(actA) != 2 {
		t.Fatal("Entries exposed internal storage")
	}
	j := v.Join(vv(actA, 5, actC, 1))
	if j.String() != "{a@1:5 b@1:3 c@1:1}" {
		t.Fatalf("join = %s", j.String())
	}
	if vv().Join(vv()).Len() != 0 || vv().String() != "{}" {
		t.Fatal("empty join or string")
	}
	if actA.String() != "a@1" || actA.Compare(actA2) >= 0 || actB.Compare(actA) <= 0 {
		t.Fatal("actor order or string")
	}
	names := map[occ.Ordering]string{occ.Equal: "equal", occ.Before: "before", occ.After: "after", occ.Concurrent: "concurrent", occ.Ordering(9): "ordering(9)"}
	for o, s := range names {
		if o.String() != s {
			t.Fatalf("%d.String() = %s", o, o.String())
		}
	}
}

func genVector(t *rapid.T, label string) occ.VersionVector {
	actors := []occ.Actor{actA, actB, actC, actA2}
	var es []occ.Entry
	for i, a := range actors {
		c := rapid.IntRange(0, 3).Draw(t, label+string(rune('0'+i)))
		es = append(es, occ.Entry{Actor: a, Counter: uint64(c)})
	}
	return occ.NewVersionVector(es...)
}

func pointwiseLE(a, b occ.VersionVector) bool {
	for _, e := range a.Entries() {
		if e.Counter > b.Get(e.Actor) {
			return false
		}
	}
	return true
}

func TestPropVersionVectorPartialOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a, b, c := genVector(t, "a"), genVector(t, "b"), genVector(t, "c")
		le := func(x, y occ.VersionVector) bool { o := x.Compare(y); return o == occ.Equal || o == occ.Before }
		if a.Compare(a) != occ.Equal {
			t.Fatalf("not reflexive: %s", a.String())
		}
		ab, ba := a.Compare(b), b.Compare(a)
		inverse := map[occ.Ordering]occ.Ordering{occ.Equal: occ.Equal, occ.Before: occ.After, occ.After: occ.Before, occ.Concurrent: occ.Concurrent}
		if inverse[ab] != ba {
			t.Fatalf("Compare(%s,%s)=%s but reverse is %s", a.String(), b.String(), ab.String(), ba.String())
		}
		model := occ.Concurrent
		switch l, g := pointwiseLE(a, b), pointwiseLE(b, a); {
		case l && g:
			model = occ.Equal
		case l:
			model = occ.Before
		case g:
			model = occ.After
		}
		if ab != model {
			t.Fatalf("Compare(%s,%s)=%s, pointwise model says %s", a.String(), b.String(), ab.String(), model.String())
		}
		if le(a, b) && le(b, a) && ab != occ.Equal {
			t.Fatal("not antisymmetric")
		}
		if le(a, b) && le(b, c) && !le(a, c) {
			t.Fatalf("not transitive: %s <= %s <= %s", a.String(), b.String(), c.String())
		}
		j := a.Join(b)
		if !le(a, j) || !le(b, j) {
			t.Fatal("join is not an upper bound")
		}
		if le(a, c) && le(b, c) && !le(j, c) {
			t.Fatal("join is not the least upper bound")
		}
		if a.Join(b).Compare(b.Join(a)) != occ.Equal {
			t.Fatal("join not commutative")
		}
	})
}

var _ = env.NodeID("")
