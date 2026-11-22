package occ

import (
	"encoding/hex"
	"fmt"
)

// ETag is a 32-byte deterministic version tag equal to the record's VersionID.
// The zero ETag represents an absent record and never satisfies IfMatch.
type ETag [32]byte

// String returns the lowercase hex representation of e.
func (e ETag) String() string { return hex.EncodeToString(e[:]) }

// IsZero reports whether e is the zero ETag.
func (e ETag) IsZero() bool { return e == ETag{} }

// ParseETag parses a 64-character hex string into an ETag, returning
// ErrMalformed on invalid hex or wrong length.
func ParseETag(s string) (ETag, error) {
	var e ETag
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(e) {
		return ETag{}, fmt.Errorf("occ: parse etag %q: %w", s, ErrMalformed)
	}
	copy(e[:], b)
	return e, nil
}

// Record holds the optional single Version stored for one metadata key.
// The zero Record represents an absent key.
type Record struct {
	v  Version
	ok bool
}

// NewRecord wraps v in a present Record.
func NewRecord(v Version) Record { return Record{v: v, ok: true} }

// Version returns the stored Version and true, or the zero Version and false
// when r is absent.
func (r Record) Version() (Version, bool) { return r.v, r.ok }

// Present reports whether r holds a version (live or tombstone).
func (r Record) Present() bool { return r.ok }

// Live reports whether r holds a non-tombstone version.
func (r Record) Live() bool { return r.ok && !r.v.tomb }

// ETag returns the stored version's ETag (including for tombstones), or the
// zero ETag when r is absent.
func (r Record) ETag() ETag {
	if !r.ok {
		return ETag{}
	}
	return r.v.ETag()
}

// Equal reports whether r and o are both absent or hold the same VersionID.
func (r Record) Equal(o Record) bool {
	return r.ok == o.ok && (!r.ok || r.v.id == o.v.id)
}

// Merge returns the last-writer-wins maximum of r and o under Version.Compare.
// A present record always beats an absent one.
func (r Record) Merge(o Record) Record {
	switch {
	case !o.ok:
		return r
	case !r.ok:
		return o
	case o.v.Compare(r.v) > 0:
		return o
	}
	return r
}

type preKind uint8

const (
	preNone preKind = iota
	preIfMatch
	preIfNoneMatch
)

// Precondition guards a conditional Put or Delete against a key's current Record.
// The zero Precondition is unconditional.
type Precondition struct {
	kind preKind
	etag ETag
}

// IfMatch requires the key to hold a version (live or tombstone) with ETag e.
func IfMatch(e ETag) Precondition { return Precondition{kind: preIfMatch, etag: e} }

// IfNoneMatch requires the key to be absent or a tombstone (create-only).
func IfNoneMatch() Precondition { return Precondition{kind: preIfNoneMatch} }

// Unconditional returns a Precondition that always holds.
func Unconditional() Precondition { return Precondition{} }

// IsUnconditional reports whether p imposes no precondition check.
func (p Precondition) IsUnconditional() bool { return p.kind == preNone }

// String formats p in HTTP-like precondition notation.
func (p Precondition) String() string {
	switch p.kind {
	case preIfMatch:
		return "If-Match: " + p.etag.String()
	case preIfNoneMatch:
		return "If-None-Match: *"
	}
	return "unconditional"
}

// Holds reports whether p is satisfied by record r.
func (p Precondition) Holds(r Record) bool {
	switch p.kind {
	case preIfMatch:
		return r.ok && r.ETag() == p.etag
	case preIfNoneMatch:
		return !r.Live()
	}
	return true
}
