package occ

import (
	"cmp"
	"slices"
	"strconv"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type Actor struct {
	Node  env.NodeID
	Epoch uint64
}

func (a Actor) Compare(b Actor) int {
	if c := cmp.Compare(a.Node, b.Node); c != 0 {
		return c
	}
	return cmp.Compare(a.Epoch, b.Epoch)
}

func (a Actor) String() string {
	return string(a.Node) + "@" + strconv.FormatUint(a.Epoch, 16)
}

type Entry struct {
	Actor   Actor
	Counter uint64
}

type VersionVector struct {
	entries []Entry
}

type Ordering uint8

const (
	Equal Ordering = iota + 1
	Before
	After
	Concurrent
)

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

func NewVersionVector(entries ...Entry) VersionVector {
	es := make([]Entry, 0, len(entries))
	for _, e := range entries {
		if e.Counter != 0 {
			es = append(es, e)
		}
	}
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

func (v VersionVector) Len() int { return len(v.entries) }

func (v VersionVector) Entries() []Entry {
	return slices.Clone(v.entries)
}

func (v VersionVector) Get(a Actor) uint64 {
	i, ok := slices.BinarySearchFunc(v.entries, a, func(e Entry, t Actor) int { return e.Actor.Compare(t) })
	if !ok {
		return 0
	}
	return v.entries[i].Counter
}

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

func (v VersionVector) Descends(w VersionVector) bool {
	o := v.Compare(w)
	return o == Equal || o == After
}

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
