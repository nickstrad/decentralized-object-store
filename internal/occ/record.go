package occ

import (
	"encoding/hex"
	"fmt"
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
	v  Version
	ok bool
}

func NewRecord(v Version) Record { return Record{v: v, ok: true} }

func (r Record) Version() (Version, bool) { return r.v, r.ok }

func (r Record) Present() bool { return r.ok }

func (r Record) Live() bool { return r.ok && !r.v.tomb }

func (r Record) ETag() ETag {
	if !r.ok {
		return ETag{}
	}
	return r.v.ETag()
}

func (r Record) Equal(o Record) bool {
	return r.ok == o.ok && (!r.ok || r.v.id == o.v.id)
}

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
		return r.ok && r.ETag() == p.etag
	case preIfNoneMatch:
		return !r.Live()
	}
	return true
}
