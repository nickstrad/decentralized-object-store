package occ

import (
	"cmp"
	"slices"
	"strconv"

	"github.com/nickstrad/object-store/internal/env"
)

// Actor identifies a single durable writer incarnation: a node ID paired with
// a non-zero epoch. Taking a fresh epoch on wipe or dot-gap retirement prevents
// counter reuse from making a concurrent discard look causal.
type Actor struct {
	Node  env.NodeID
	Epoch uint64
}

// Compare orders actors lexicographically by Node, then Epoch.
func (a Actor) Compare(b Actor) int {
	if c := cmp.Compare(a.Node, b.Node); c != 0 {
		return c
	}
	return cmp.Compare(a.Epoch, b.Epoch)
}

// String formats a as "<node>@<hex-epoch>".
func (a Actor) String() string {
	return string(a.Node) + "@" + strconv.FormatUint(a.Epoch, 16)
}

// Entry pairs an Actor with its highest observed dot counter in a VersionVector.
type Entry struct {
	Actor   Actor
	Counter uint64
}

// VersionVector is an immutable map from Actor to non-zero dot counter, stored
// in strictly increasing Actor order. An entry (a, N) denotes the contiguous
// downset of dots 1..N issued by actor a.
type VersionVector struct {
	entries []Entry
}

// Ordering is the causal relationship between two VersionVectors.
type Ordering uint8

// Causal orderings returned by VersionVector.Compare.
const (
	Equal Ordering = iota + 1
	Before
	After
	Concurrent
)

// String returns a lowercase name for o.
func (o Ordering) String() string {
	switch o {
	case Equal:
		return "equal"
	case Before:
		return "before"
	case After:
		return "after"
	case Concurrent:
		return "concurrent"
	}
	return "ordering(" + strconv.Itoa(int(o)) + ")"
}

// NewVersionVector constructs a canonical VersionVector from entries.
// Zero counters are omitted, and duplicate actors keep their maximum counter.
func NewVersionVector(entries ...Entry) VersionVector {
	es := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Counter != 0 {
			es = append(es, e)
		}
	}
	// Sort ascending by Actor and descending by Counter so the deduplication
	// pass below keeps the highest counter for each actor.
	slices.SortFunc(es, func(a, b Entry) int {
		if c := a.Actor.Compare(b.Actor); c != 0 {
			return c
		}
		return cmp.Compare(b.Counter, a.Counter)
	})
	out := es[:0]
	for _, e := range es {
		if len(out) > 0 && out[len(out)-1].Actor == e.Actor {
			continue
		}
		out = append(out, e)
	}
	if len(out) == 0 {
		return VersionVector{}
	}
	return VersionVector{entries: out}
}

// Len returns the number of actors with non-zero counters in v.
func (v VersionVector) Len() int { return len(v.entries) }

// Entries returns a sorted copy of v's entries.
func (v VersionVector) Entries() []Entry {
	return slices.Clone(v.entries)
}

// Get returns the counter recorded for actor a, or 0 if a is absent.
func (v VersionVector) Get(a Actor) uint64 {
	i, ok := slices.BinarySearchFunc(v.entries, a, func(e Entry, t Actor) int { return e.Actor.Compare(t) })
	if !ok {
		return 0
	}
	return v.entries[i].Counter
}

// Compare returns the pointwise partial-order relation of v relative to w:
// Equal, Before (v < w), After (v > w), or Concurrent.
func (v VersionVector) Compare(w VersionVector) Ordering {
	less, greater := false, false
	i, j := 0, 0
	for i < len(v.entries) || j < len(w.entries) {
		switch {
		case j == len(w.entries) || (i < len(v.entries) && v.entries[i].Actor.Compare(w.entries[j].Actor) < 0):
			greater = true
			i++
		case i == len(v.entries) || v.entries[i].Actor.Compare(w.entries[j].Actor) > 0:
			less = true
			j++
		default:
			if v.entries[i].Counter > w.entries[j].Counter {
				greater = true
			} else if v.entries[i].Counter < w.entries[j].Counter {
				less = true
			}
			i++
			j++
		}
	}
	switch {
	case less && greater:
		return Concurrent
	case less:
		return Before
	case greater:
		return After
	}
	return Equal
}

// Descends reports whether v is equal to or causally after w (v >= w pointwise).
func (v VersionVector) Descends(w VersionVector) bool {
	o := v.Compare(w)
	return o == Equal || o == After
}

// Join returns the pointwise maximum (least upper bound) of v and w.
func (v VersionVector) Join(w VersionVector) VersionVector {
	out := make([]Entry, 0, len(v.entries)+len(w.entries))
	i, j := 0, 0
	for i < len(v.entries) || j < len(w.entries) {
		switch {
		case j == len(w.entries) || (i < len(v.entries) && v.entries[i].Actor.Compare(w.entries[j].Actor) < 0):
			out = append(out, v.entries[i])
			i++
		case i == len(v.entries) || v.entries[i].Actor.Compare(w.entries[j].Actor) > 0:
			out = append(out, w.entries[j])
			j++
		default:
			out = append(out, Entry{Actor: v.entries[i].Actor, Counter: max(v.entries[i].Counter, w.entries[j].Counter)})
			i++
			j++
		}
	}
	if len(out) == 0 {
		return VersionVector{}
	}
	return VersionVector{entries: out}
}

// String formats v as "{actor:counter ...}" in canonical actor order.
func (v VersionVector) String() string {
	b := []byte{'{'}
	for i, e := range v.entries {
		if i > 0 {
			b = append(b, ' ')
		}
		b = append(b, e.Actor.String()...)
		b = append(b, ':')
		b = strconv.AppendUint(b, e.Counter, 10)
	}
	return string(append(b, '}'))
}
