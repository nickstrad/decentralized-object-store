package occ

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"slices"
)

type Dot struct {
	Actor   Actor
	Counter uint64
}

type VersionID [32]byte

func (id VersionID) String() string { return hex.EncodeToString(id[:]) }

func (id VersionID) Compare(o VersionID) int { return bytes.Compare(id[:], o[:]) }

type Version struct {
	dot   Dot
	ctx   VersionVector
	tomb  bool
	value []byte
	stamp Stamp
	id    VersionID
}

func NewVersion(dot Dot, ctx VersionVector, tombstone bool, value []byte, stamp Stamp) Version {
	v := Version{dot: dot, ctx: ctx, tomb: tombstone, stamp: stamp}
	if !tombstone && len(value) > 0 {
		v.value = slices.Clone(value)
	}
	v.id = v.computeID()
	return v
}

func (v Version) Dot() Dot               { return v.dot }
func (v Version) Context() VersionVector { return v.ctx }
func (v Version) Tombstone() bool        { return v.tomb }
func (v Version) Value() []byte          { return slices.Clone(v.value) }
func (v Version) Stamp() Stamp           { return v.stamp }
func (v Version) ID() VersionID          { return v.id }
func (v Version) ETag() ETag             { return ETag(v.id) }
func (v Version) Clock() VersionVector   { return v.ctx.Join(NewVersionVector(Entry(v.dot))) }

func (v Version) Compare(w Version) int {
	if c := v.stamp.Compare(w.stamp); c != 0 {
		return c
	}
	return v.id.Compare(w.id)
}

func (v Version) Descends(w Version) bool {
	ok, _ := v.descends(w)
	return ok
}

func (v Version) Supersedes(w Version) bool {
	ok, _ := v.supersedes(w)
	return ok
}

func (v Version) supersedes(w Version) (bool, int) {
	down, n := v.descends(w)
	if !down {
		return false, n
	}
	up, m := w.descends(v)
	return !up, n + m
}

func (v Version) descends(w Version) (bool, int) {
	ops := 1
	if w.dot != v.dot && w.dot.Counter > v.ctx.Get(w.dot.Actor) {
		return false, ops
	}
	i := 0
	for _, e := range w.ctx.entries {
		for i < len(v.ctx.entries) && v.ctx.entries[i].Actor.Compare(e.Actor) < 0 {
			i++
			ops++
		}
		ops++
		var have uint64
		if i < len(v.ctx.entries) && v.ctx.entries[i].Actor == e.Actor {
			have = v.ctx.entries[i].Counter
		}
		if e.Counter <= have {
			continue
		}
		if v.dot.Actor == e.Actor && v.dot.Counter == e.Counter && e.Counter-1 == have {
			continue
		}
		return false, ops
	}
	return true, ops
}

func (v Version) contentHash() [32]byte {
	var e encoder
	e.vector(v.ctx)
	e.bool(v.tomb)
	e.stamp(v.stamp)
	e.bytes(v.value)
	return sha256.Sum256(e.b)
}

func (v Version) computeID() VersionID {
	var e encoder
	e.raw([]byte("occ/version/v1"))
	e.actor(v.dot.Actor)
	e.uvarint(v.dot.Counter)
	h := v.contentHash()
	e.raw(h[:])
	return sha256.Sum256(e.b)
}
