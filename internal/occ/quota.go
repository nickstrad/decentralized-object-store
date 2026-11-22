package occ

import (
	"errors"
	"fmt"
	"math"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

// Usage holds a bucket's tracked byte and object totals.
type Usage struct {
	Bytes   uint64
	Objects uint64
}

// Encode returns the canonical varint encoding of u.
func (u Usage) Encode() []byte {
	var e encoder
	e.uvarint(u.Bytes)
	e.uvarint(u.Objects)
	return e.b
}

// DecodeUsage parses a canonical Usage encoding, returning ErrMalformed on
// truncated, trailing, or non-canonical bytes.
func DecodeUsage(b []byte) (Usage, error) {
	d := &decoder{b: b}
	u := Usage{Bytes: d.uvarint(), Objects: d.uvarint()}
	d.end()
	if d.err == nil && string(u.Encode()) != string(b) {
		d.fail("non-canonical usage")
	}
	if d.err != nil {
		return Usage{}, d.err
	}
	return u, nil
}

// Recount computes the authoritative Usage for a quota key from underlying
// metadata so RecountQuota can repair drift after a partition discard.
type Recount func(key string) (Usage, error)

type putter interface {
	Put(key string, value []byte, p Precondition) (Version, error)
}

// ReadQuota reads the current Usage at key and returns the Precondition needed
// for a conditional follow-up write (IfNoneMatch when absent, IfMatch on the
// current version or tombstone otherwise). On malformed bytes it still returns
// the record's IfMatch precondition so a recount can overwrite it.
func (r Reader) ReadQuota(key string) (Usage, Precondition, error) {
	rec := r.doc.Record(key)
	if !rec.Live() {
		p := IfNoneMatch()
		if rec.ok {
			p = IfMatch(rec.ETag())
		}
		return Usage{}, p, nil
	}
	u, err := DecodeUsage(rec.v.value)
	if err != nil {
		return Usage{}, IfMatch(rec.ETag()), fmt.Errorf("occ: read quota %q: %w", key, err)
	}
	return u, IfMatch(rec.ETag()), nil
}

func writeQuota(w putter, key string, u Usage, p Precondition) (Version, error) {
	if p.IsUnconditional() {
		return Version{}, fmt.Errorf("occ: write quota %q: an unconditional quota write: %w", key, env.ErrInvalidArgument)
	}
	return w.Put(key, u.Encode(), p)
}

// updateQuota runs a bounded check-and-set loop, retrying only on
// ErrPreconditionFailed so a value computed from a stale read is never applied.
func updateQuota(w putter, r Reader, key string, attempts int, fn func(Usage) (Usage, error)) (Usage, error) {
	if attempts < 1 {
		return Usage{}, fmt.Errorf("occ: update quota %q: %d attempts: %w", key, attempts, env.ErrInvalidArgument)
	}
	var last error
	for range attempts {
		u, p, err := r.ReadQuota(key)
		if err != nil {
			return Usage{}, fmt.Errorf("occ: update quota %q: %w", key, err)
		}
		next, err := fn(u)
		if err != nil {
			return Usage{}, fmt.Errorf("occ: update quota %q: %w", key, err)
		}
		if _, err := writeQuota(w, key, next, p); err != nil {
			if errors.Is(err, ErrPreconditionFailed) {
				last = err
				continue
			}
			return Usage{}, err
		}
		return next, nil
	}
	return Usage{}, fmt.Errorf("occ: update quota %q: gave up after %d attempts: %w", key, attempts, last)
}

// recountQuota recomputes usage via recount and writes only when the stored
// usage differs or is unreadable, stopping nodes from writing redundant
// identical versions that would discard each other indefinitely.
func recountQuota(w putter, r Reader, key string, attempts int, recount Recount) (Usage, bool, error) {
	if attempts < 1 || recount == nil {
		return Usage{}, false, fmt.Errorf("occ: recount quota %q: %d attempts, hook set %v: %w", key, attempts, recount != nil, env.ErrInvalidArgument)
	}
	var last error
	for range attempts {
		cur, p, rerr := r.ReadQuota(key)
		want, err := recount(key)
		if err != nil {
			return Usage{}, false, fmt.Errorf("occ: recount quota %q: %w", key, err)
		}
		if rerr == nil && cur == want {
			// Re-check the stored version before skipping the write so a
			// concurrent modification during recount does not slip past as a no-op.
			_, now, nerr := r.ReadQuota(key)
			if nerr == nil && now == p {
				return want, false, nil
			}
			last = fmt.Errorf("occ: recount quota %q: changed while recounting: %w", key, ErrPreconditionFailed)
			continue
		}
		if _, err := writeQuota(w, key, want, p); err != nil {
			if errors.Is(err, ErrPreconditionFailed) {
				last = err
				continue
			}
			return Usage{}, false, err
		}
		return want, true, nil
	}
	return Usage{}, false, fmt.Errorf("occ: recount quota %q: gave up after %d attempts: %w", key, attempts, last)
}

// WriteQuota writes u to key conditionally. An unconditional Precondition is
// rejected with env.ErrInvalidArgument.
func (s *Store) WriteQuota(key string, u Usage, p Precondition) (Version, error) {
	return writeQuota(s, key, u, p)
}

// UpdateQuota performs a read-modify-write on quota key using fn, retrying on
// ErrPreconditionFailed up to attempts times. It returns env.ErrInvalidArgument
// if attempts < 1, fn's error, ErrPreconditionFailed after the final attempt,
// or any underlying read/persist error.
func (s *Store) UpdateQuota(key string, attempts int, fn func(Usage) (Usage, error)) (Usage, error) {
	return updateQuota(s, s.Reader, key, attempts, fn)
}

// RecountQuota recomputes key's Usage via recount and conditionally writes it
// only if the stored figure differs or is malformed. It returns the computed
// Usage, whether a write was performed, and any error.
func (s *Store) RecountQuota(key string, attempts int, recount Recount) (Usage, bool, error) {
	return recountQuota(s, s.Reader, key, attempts, recount)
}

// AddUsage returns an UpdateQuota callback that adds signed byte and object
// deltas to the current Usage with checked arithmetic. Positive growth is
// checked against any non-zero limit fields and fails with ErrQuotaExceeded;
// overflow or underflow fails with env.ErrInvalidArgument.
func AddUsage(bytes, objects int64, limit Usage) func(Usage) (Usage, error) {
	return func(u Usage) (Usage, error) {
		nb, okb := addSigned(u.Bytes, bytes)
		no, oko := addSigned(u.Objects, objects)
		if !okb || !oko {
			return Usage{}, fmt.Errorf("occ: usage %d bytes %d objects plus %d, %d: %w", u.Bytes, u.Objects, bytes, objects, env.ErrInvalidArgument)
		}
		grows := bytes > 0 || objects > 0
		if grows && ((limit.Bytes > 0 && nb > limit.Bytes) || (limit.Objects > 0 && no > limit.Objects)) {
			return Usage{}, fmt.Errorf("occ: usage %d bytes %d objects over limit %d bytes %d objects: %w", nb, no, limit.Bytes, limit.Objects, ErrQuotaExceeded)
		}
		return Usage{Bytes: nb, Objects: no}, nil
	}
}

// addSigned adds signed delta d to unsigned x, reporting false on overflow or
// underflow (including d == math.MinInt64).
func addSigned(x uint64, d int64) (uint64, bool) {
	if d >= 0 {
		if x > math.MaxUint64-uint64(d) {
			return 0, false
		}
		return x + uint64(d), true
	}
	m := uint64(-(d + 1)) + 1
	if m > x {
		return 0, false
	}
	return x - m, true
}
