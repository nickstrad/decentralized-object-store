package occ

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type StoreConfig struct {
	Node  env.NodeID
	Epoch uint64
	Clock env.Clock
	Rand  *rand.Rand
	Doc   *Doc
}

type Store struct {
	actor     Actor
	hlc       *HLC
	rand      *rand.Rand
	doc       *Doc
	rotations int
	ahead     int
}

func NewStore(cfg StoreConfig) (*Store, error) {
	if cfg.Node == "" || cfg.Clock == nil || cfg.Rand == nil {
		return nil, fmt.Errorf("occ: new store: node, clock and rand are required: %w", env.ErrInvalidArgument)
	}
	return newStore(cfg.Node, cfg.Epoch, cfg.Clock, cfg.Rand, cfg.Doc), nil
}

func newStore(node env.NodeID, epoch uint64, c env.Clock, r *rand.Rand, doc *Doc) *Store {
	s := &Store{actor: Actor{Node: node, Epoch: epoch}, hlc: NewHLC(c, node), rand: r, doc: doc}
	if s.doc == nil {
		s.doc = NewDoc()
	}
	if s.actor.Epoch == 0 {
		s.actor.Epoch = s.freshEpoch()
	}
	if err := s.hlc.Observe(s.doc.MaxStamp()); err != nil {
		s.ahead++
	}
	return s
}

func (s *Store) freshEpoch() uint64 {
	for {
		e := s.rand.Uint64()
		if e != 0 && e != s.actor.Epoch {
			return e
		}
	}
}

func (s *Store) Actor() Actor     { return s.actor }
func (s *Store) Doc() *Doc        { return s.doc }
func (s *Store) HLC() *HLC        { return s.hlc }
func (s *Store) Rotations() int   { return s.rotations }
func (s *Store) AheadStamps() int { return s.ahead }

func (s *Store) Record(key string) Record { return s.doc.Record(key) }

func (s *Store) Get(key string) ([]byte, ETag, error) {
	r := s.doc.Record(key)
	live := r.Live()
	switch {
	case len(live) == 0:
		return nil, r.ETag(), fmt.Errorf("occ: get %q: %w", key, ErrNotFound)
	case r.Len() > 1:
		return nil, r.ETag(), &ConflictError{Key: key, ETag: r.ETag(), Siblings: r.Siblings()}
	}
	return live[0].Value(), r.ETag(), nil
}

func (s *Store) Put(key string, value []byte, p Precondition) (Version, error) {
	v, err := s.write(key, false, value, p)
	if err != nil {
		return Version{}, fmt.Errorf("occ: put %q: %w", key, err)
	}
	return v, nil
}

func (s *Store) Delete(key string, p Precondition) (Version, error) {
	v, err := s.write(key, true, nil, p)
	if err != nil {
		return Version{}, fmt.Errorf("occ: delete %q: %w", key, err)
	}
	return v, nil
}

func (s *Store) write(key string, tomb bool, value []byte, p Precondition) (Version, error) {
	r := s.doc.Record(key)
	if !p.Holds(r) {
		return Version{}, fmt.Errorf("%s against etag %s: %w", p.String(), r.ETag().String(), ErrPreconditionFailed)
	}
	if tomb && !r.hasLive() {
		return Version{}, ErrNotFound
	}
	var ctx VersionVector
	var keep []Version
	if p.IsUnconditional() {
		keep = r.siblings
	} else {
		ctx = r.clock()
	}
	stamp, err := s.hlc.Now()
	if err != nil {
		return Version{}, err
	}
	if r.maxCounter(s.actor) == math.MaxUint64 {
		s.actor.Epoch = s.freshEpoch()
		s.rotations++
	}
	dot := Dot{Actor: s.actor, Counter: r.maxCounter(s.actor) + 1}
	v := NewVersion(dot, ctx, tomb, value, stamp)
	s.doc.setRecord(key, NewRecord(append(slices.Clone(keep), v)...))
	return v, nil
}

func (s *Store) SetScalar(key string, value []byte) (Scalar, error) {
	stamp, err := s.hlc.Now()
	if err != nil {
		return Scalar{}, fmt.Errorf("occ: set scalar %q: %w", key, err)
	}
	sc := Scalar{Value: slices.Clone(value), Stamp: stamp}
	s.doc.MergeScalar(key, sc)
	return sc, nil
}

func (s *Store) Scalar(key string) ([]byte, Stamp, error) {
	sc, ok := s.doc.Scalar(key)
	if !ok {
		return nil, Stamp{}, fmt.Errorf("occ: scalar %q: %w", key, ErrNotFound)
	}
	return sc.Value, sc.Stamp, nil
}

func (s *Store) Merge(o *Doc) bool {
	if err := s.hlc.Observe(o.MaxStamp()); err != nil {
		s.ahead++
	}
	return s.doc.Merge(o)
}

func IsConflict(err error) (*ConflictError, bool) {
	var ce *ConflictError
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}
