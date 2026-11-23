package occ

import (
	"encoding/binary"
	"fmt"

	"github.com/nickstrad/object-store/internal/env"
)

// encoder appends canonical binary primitives for documents, state blobs,
// typed metadata values, and wire messages.
type encoder struct {
	b []byte
}

func (e *encoder) raw(p []byte)     { e.b = append(e.b, p...) }
func (e *encoder) byte(c byte)      { e.b = append(e.b, c) }
func (e *encoder) uvarint(x uint64) { e.b = binary.AppendUvarint(e.b, x) }
func (e *encoder) varint(x int64)   { e.b = binary.AppendVarint(e.b, x) }
func (e *encoder) bytes(p []byte)   { e.uvarint(uint64(len(p))); e.raw(p) }
func (e *encoder) str(s string)     { e.uvarint(uint64(len(s))); e.b = append(e.b, s...) }
func (e *encoder) actor(a Actor)    { e.str(string(a.Node)); e.uvarint(a.Epoch) }
func (e *encoder) stamp(s Stamp) {
	e.varint(s.Wall)
	e.uvarint(uint64(s.Logical))
	e.str(string(s.Node))
}
func (e *encoder) fixed(p *[32]byte) { e.raw(p[:]) }

func (e *encoder) bool(v bool) {
	if v {
		e.byte(1)
		return
	}
	e.byte(0)
}

func (e *encoder) vector(v VersionVector) {
	e.uvarint(uint64(len(v.entries)))
	for _, x := range v.entries {
		e.actor(x.Actor)
		e.uvarint(x.Counter)
	}
}

func (e *encoder) version(v Version) {
	e.actor(v.dot.Actor)
	e.uvarint(v.dot.Counter)
	e.vector(v.ctx)
	e.bool(v.tomb)
	e.stamp(v.stamp)
	e.bytes(v.value)
}

// decoder parses canonical binary inputs with sticky errors and a running
// operation counter (ops) to bound work on hostile inputs.
type decoder struct {
	b   []byte
	off int
	err error
	ops int
}

// fail records the first decode failure as ErrMalformed and keeps it sticky.
func (d *decoder) fail(what string) {
	if d.err == nil {
		d.err = fmt.Errorf("occ: decode %s at offset %d: %w", what, d.off, ErrMalformed)
	}
}

func (d *decoder) rest() int { return len(d.b) - d.off }

func (d *decoder) byte() byte {
	d.ops++
	if d.err != nil {
		return 0
	}
	if d.rest() < 1 {
		d.fail("byte")
		return 0
	}
	c := d.b[d.off]
	d.off++
	return c
}

func (d *decoder) uvarint() uint64 {
	d.ops++
	if d.err != nil {
		return 0
	}
	x, n := binary.Uvarint(d.b[d.off:])
	if n <= 0 {
		d.fail("uvarint")
		return 0
	}
	d.off += n
	return x
}

func (d *decoder) varint() int64 {
	d.ops++
	if d.err != nil {
		return 0
	}
	x, n := binary.Varint(d.b[d.off:])
	if n <= 0 {
		d.fail("varint")
		return 0
	}
	d.off += n
	return x
}

// count reads a declared element count and rejects any count that exceeds
// what can fit in the remaining bytes at minSize bytes per element.
func (d *decoder) count(minSize int) int {
	n := d.uvarint()
	if d.err != nil {
		return 0
	}
	if n > uint64(d.rest()/minSize) {
		d.fail("count")
		return 0
	}
	return int(n)
}

func (d *decoder) bytes() []byte {
	n := d.count(1)
	if d.err != nil {
		return nil
	}
	p := d.b[d.off : d.off+n]
	d.off += n
	d.ops += n
	return p
}

func (d *decoder) str() string { return string(d.bytes()) }

func (d *decoder) fixed(p *[32]byte) {
	if d.err != nil {
		return
	}
	if d.rest() < len(p) {
		d.fail("fixed")
		return
	}
	copy(p[:], d.b[d.off:])
	d.off += len(p)
}

func (d *decoder) bool() bool {
	switch d.byte() {
	case 0:
		return false
	case 1:
		return true
	}
	d.fail("flag")
	return false
}

func (d *decoder) actor() Actor {
	node := d.str()
	return Actor{Node: env.NodeID(node), Epoch: d.uvarint()}
}

func (d *decoder) stamp() Stamp {
	wall := d.varint()
	logical := d.uvarint()
	if logical > 0xffffffff {
		d.fail("logical")
	}
	return Stamp{Wall: wall, Logical: uint32(logical), Node: env.NodeID(d.str())}
}

// vector decodes a canonical VersionVector, checking non-zero counters and
// strictly increasing actor order inline so malformed inputs fail without sorting.
func (d *decoder) vector() VersionVector {
	// An entry needs at least 3 bytes: empty node length (1), epoch (1), counter (1).
	n := d.count(3)
	if d.err != nil || n == 0 {
		return VersionVector{}
	}
	es := make([]Entry, 0, n)
	for i := 0; i < n && d.err == nil; i++ {
		a := d.actor()
		c := d.uvarint()
		if d.err != nil {
			break
		}
		if c == 0 {
			d.fail("context entry with a zero counter")
			break
		}
		if i > 0 && es[i-1].Actor.Compare(a) >= 0 {
			d.fail("context entries out of order")
			break
		}
		es = append(es, Entry{Actor: a, Counter: c})
	}
	if d.err != nil {
		return VersionVector{}
	}
	return VersionVector{entries: es}
}

// version decodes a Version and returns early on any error before hashing
// the VersionID.
func (d *decoder) version() Version {
	a := d.actor()
	c := d.uvarint()
	ctx := d.vector()
	tomb := d.bool()
	st := d.stamp()
	val := d.bytes()
	if d.err == nil && c == 0 {
		d.fail("dot counter")
	}
	if d.err == nil && tomb && len(val) > 0 {
		d.fail("tombstone value")
	}
	if d.err != nil {
		return Version{}
	}
	return NewVersion(Dot{Actor: a, Counter: c}, ctx, tomb, val, st)
}

// end fails the decoder if any unconsumed trailing bytes remain.
func (d *decoder) end() {
	if d.err == nil && d.rest() != 0 {
		d.fail("trailing bytes")
	}
}
