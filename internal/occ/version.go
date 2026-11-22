package occ

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"slices"
)

// Dot uniquely tags one write by an Actor using a strictly positive per-actor
// counter.
type Dot struct {
	Actor   Actor
	Counter uint64
}

// VersionID is the 32-byte SHA-256 identity of a Version.
type VersionID [32]byte

// String returns the lowercase hex representation of id.
func (id VersionID) String() string { return hex.EncodeToString(id[:]) }

// Compare orders VersionIDs lexicographically by byte value.
func (id VersionID) Compare(o VersionID) int { return bytes.Compare(id[:], o[:]) }

// Version is an immutable value or tombstone for one metadata key, carrying
// its writer dot, causal context, HLC stamp, and cached VersionID.
type Version struct {
	dot   Dot
	ctx   VersionVector
	tomb  bool
	value []byte
	stamp Stamp
	id    VersionID
}

// NewVersion builds an immutable Version and computes its deterministic ID.
// Tombstones always store an empty value.
func NewVersion(dot Dot, ctx VersionVector, tombstone bool, value []byte, stamp Stamp) Version {
	v := Version{dot: dot, ctx: ctx, tomb: tombstone, stamp: stamp}
	if !tombstone && len(value) > 0 {
		v.value = slices.Clone(value)
	}
	v.id = v.computeID()
	return v
}

// Accessors on Version:
//   - Dot: writer actor and counter that created v.
//   - Context: causal context observed before v was written.
//   - Tombstone: whether v represents a deletion.
//   - Value: a copy of v's payload bytes (nil for a tombstone or empty value).
//   - Stamp: v's HLC timestamp.
//   - ID: v's deterministic SHA-256 VersionID.
//   - ETag: v's ETag, equal to its VersionID.
//   - Clock: v's causal context joined with v's own dot.
func (v Version) Dot() Dot               { return v.dot }
func (v Version) Context() VersionVector { return v.ctx }
func (v Version) Tombstone() bool        { return v.tomb }
func (v Version) Value() []byte          { return slices.Clone(v.value) }
func (v Version) Stamp() Stamp           { return v.stamp }
func (v Version) ID() VersionID          { return v.id }
func (v Version) ETag() ETag             { return ETag(v.id) }
func (v Version) Clock() VersionVector   { return v.ctx.Join(NewVersionVector(Entry(v.dot))) }

// clockAt returns actor a's counter in v.Clock() without allocating a vector.
func (v Version) clockAt(a Actor) uint64 {
	c := v.ctx.Get(a)
	if v.dot.Actor == a {
		c = max(c, v.dot.Counter)
	}
	return c
}

// Compare defines the last-writer-wins total order on versions: HLC stamp
// (Wall, Logical, Node), with VersionID bytes breaking any remaining tie.
func (v Version) Compare(w Version) int {
	if c := v.stamp.Compare(w.stamp); c != 0 {
		return c
	}
	return v.id.Compare(w.id)
}

// Descends reports whether v's event set (downset(ctx) union {dot}) includes
// w's event set.
func (v Version) Descends(w Version) bool {
	ok, _ := v.descends(w)
	return ok
}

// Supersedes reports whether v's event set strictly includes w's event set,
// indicating that v's writer causally saw w.
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

// descends checks events(w) <= events(v) in a single linear two-pointer scan
// over the sorted contexts, returning the result and counted operations.
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
		// v's own dot closes the gap when w's context ends exactly at v.dot
		// and v's context already holds 1..(v.dot.Counter-1).
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
