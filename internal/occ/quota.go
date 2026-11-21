package occ

import (
	"errors"
	"fmt"
	"math"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type Usage struct {
	Bytes   uint64
	Objects uint64
}

func (u Usage) Encode() []byte {
	var e encoder
	e.uvarint(u.Bytes)
	e.uvarint(u.Objects)
	return e.b
}

func DecodeUsage(b []byte) (Usage, error) {
	d := &decoder{b: b}
	u := Usage{Bytes: d.uvarint(), Objects: d.uvarint()}
	d.end()
	if d.err == nil {
		var e encoder
		e.uvarint(u.Bytes)
		e.uvarint(u.Objects)
		if string(e.b) != string(b) {
			d.fail("non-canonical usage")
		}
	}
	if d.err != nil {
		return Usage{}, d.err
	}
	return u, nil
}

type Recount func(key string) (Usage, error)

func (s *Store) ReadQuota(key string) (Usage, Precondition, error) {
	r := s.doc.Record(key)
	live := r.Live()
	switch {
	case len(live) == 0:
		return Usage{}, IfNoneMatch(), nil
	case r.Len() > 1:
		return Usage{}, IfMatch(r.ETag()), &ConflictError{Key: key, ETag: r.ETag(), Siblings: r.Siblings()}
	}
	u, err := DecodeUsage(live[0].value)
	if err != nil {
		return Usage{}, IfMatch(r.ETag()), fmt.Errorf("occ: read quota %q: %w", key, err)
	}
	return u, IfMatch(r.ETag()), nil
}

func (s *Store) WriteQuota(key string, u Usage, p Precondition) (Version, error) {
	if p.IsUnconditional() {
		return Version{}, fmt.Errorf("occ: write quota %q: an unconditional quota write: %w", key, env.ErrInvalidArgument)
	}
	return s.Put(key, u.Encode(), p)
}

func (s *Store) UpdateQuota(key string, attempts int, recount Recount, fn func(Usage) (Usage, error)) (Usage, error) {
	if attempts < 1 {
		return Usage{}, fmt.Errorf("occ: update quota %q: %d attempts: %w", key, attempts, env.ErrInvalidArgument)
	}
	var last error
	for i := 0; i < attempts; i++ {
		u, p, err := s.ReadQuota(key)
		if _, ok := IsConflict(err); ok && recount != nil {
			u, err = recount(key)
			if err != nil {
				return Usage{}, fmt.Errorf("occ: update quota %q: recount: %w", key, err)
			}
		} else if err != nil {
			return Usage{}, fmt.Errorf("occ: update quota %q: %w", key, err)
		}
		next, err := fn(u)
		if err != nil {
			return Usage{}, fmt.Errorf("occ: update quota %q: %w", key, err)
		}
		if _, err := s.WriteQuota(key, next, p); err != nil {
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
