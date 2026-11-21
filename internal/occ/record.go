package occ

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
)

type ETag [32]byte

func (e ETag) String() string { return hex.EncodeToString(e[:]) }

func (e ETag) IsZero() bool { return e == ETag{} }

func ParseETag(s string) (ETag, error) {
	var e ETag
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(e) {
		return ETag{}, fmt.Errorf("occ: parse etag %q: %w", s, ErrMalformed)
	}
	copy(e[:], b)
	return e, nil
}

type Record struct {
	siblings []Version
}

var canaryDropConcurrent bool

func NewRecord(vs ...Version) Record {
	sorted := slices.Clone(vs)
	slices.SortFunc(sorted, func(a, b Version) int { return a.id.Compare(b.id) })
	sorted = slices.CompactFunc(sorted, func(a, b Version) bool { return a.id == b.id })
	return Record{siblings: maximal(sorted)}
}

func maximal(vs []Version) []Version {
	out := make([]Version, 0, len(vs))
	for i, v := range vs {
		dominated := false
		for j, w := range vs {
			if i != j && w.Supersedes(v) {
				dominated = true
				break
			}
		}
		if !dominated {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (r Record) Len() int { return len(r.siblings) }

func (r Record) Siblings() []Version { return slices.Clone(r.siblings) }

func (r Record) Live() []Version {
	var out []Version
	for _, v := range r.siblings {
		if !v.tomb {
			out = append(out, v)
		}
	}
	return out
}

func (r Record) hasLive() bool {
	for _, v := range r.siblings {
		if !v.tomb {
			return true
		}
	}
	return false
}

func (r Record) IDs() []VersionID {
	out := make([]VersionID, len(r.siblings))
	for i, v := range r.siblings {
		out[i] = v.id
	}
	return out
}

func (r Record) ETag() ETag {
	if len(r.siblings) == 0 {
		return ETag{}
	}
	var e encoder
	e.raw([]byte("occ/etag/v1"))
	e.uvarint(uint64(len(r.siblings)))
	for _, v := range r.siblings {
		e.raw(v.id[:])
	}
	return sha256.Sum256(e.b)
}

func (r Record) Equal(o Record) bool {
	if len(r.siblings) != len(o.siblings) {
		return false
	}
	for i := range r.siblings {
		if r.siblings[i].id != o.siblings[i].id {
			return false
		}
	}
	return true
}

func (r Record) Covers(v Version) bool {
	for _, w := range r.siblings {
		if w.id == v.id || w.Descends(v) {
			return true
		}
	}
	return false
}

func (r Record) Merge(o Record) Record {
	union := make([]Version, 0, len(r.siblings)+len(o.siblings))
	i, j := 0, 0
	for i < len(r.siblings) || j < len(o.siblings) {
		switch {
		case j == len(o.siblings) || (i < len(r.siblings) && r.siblings[i].id.Compare(o.siblings[j].id) < 0):
			union = append(union, r.siblings[i])
			i++
		case i == len(r.siblings) || r.siblings[i].id.Compare(o.siblings[j].id) > 0:
			union = append(union, o.siblings[j])
			j++
		default:
			union = append(union, r.siblings[i])
			i++
			j++
		}
	}
	out := maximal(union)
	if canaryDropConcurrent && len(out) > 1 {
		out = out[len(out)-1:]
	}
	return Record{siblings: out}
}

func (r Record) maxCounter(a Actor) uint64 {
	var m uint64
	for _, v := range r.siblings {
		if v.dot.Actor == a {
			m = max(m, v.dot.Counter)
		}
		m = max(m, v.ctx.Get(a))
	}
	return m
}

func (r Record) clock() VersionVector {
	var c VersionVector
	for _, v := range r.siblings {
		c = c.Join(v.Clock())
	}
	return c
}

type preKind uint8

const (
	preNone preKind = iota
	preIfMatch
	preIfNoneMatch
)

type Precondition struct {
	kind preKind
	etag ETag
}

func IfMatch(e ETag) Precondition { return Precondition{kind: preIfMatch, etag: e} }

func IfNoneMatch() Precondition { return Precondition{kind: preIfNoneMatch} }

func Unconditional() Precondition { return Precondition{} }

func (p Precondition) IsUnconditional() bool { return p.kind == preNone }

func (p Precondition) String() string {
	switch p.kind {
	case preIfMatch:
		return "If-Match: " + p.etag.String()
	case preIfNoneMatch:
		return "If-None-Match: *"
	}
	return "unconditional"
}

func (p Precondition) Holds(r Record) bool {
	switch p.kind {
	case preIfMatch:
		return r.Len() > 0 && r.ETag() == p.etag
	case preIfNoneMatch:
		return !r.hasLive()
	}
	return true
}
