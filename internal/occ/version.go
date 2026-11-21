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

func (v Version) Dot() Dot                  { return v.dot }
func (v Version) Context() VersionVector    { return v.ctx }
func (v Version) Tombstone() bool           { return v.tomb }
func (v Version) Value() []byte             { return slices.Clone(v.value) }
func (v Version) Stamp() Stamp              { return v.stamp }
func (v Version) ID() VersionID             { return v.id }
func (v Version) Clock() VersionVector      { return v.ctx.Join(NewVersionVector(Entry(v.dot))) }
func (v Version) hasEvent(d Dot) bool       { return d.Counter <= v.ctx.Get(d.Actor) || d == v.dot }
func (v Version) Supersedes(w Version) bool { return v.Descends(w) && !w.Descends(v) }

func (v Version) Descends(w Version) bool {
	if !v.hasEvent(w.dot) {
		return false
	}
	for _, e := range w.ctx.entries {
		have := v.ctx.Get(e.Actor)
		if e.Counter <= have {
			continue
		}
		if v.dot.Actor == e.Actor && v.dot.Counter == e.Counter && e.Counter-1 == have {
			continue
		}
		return false
	}
	return true
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
