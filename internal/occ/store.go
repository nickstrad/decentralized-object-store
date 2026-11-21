package occ

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

const DiscardTailLen = 64

var canaryUnrecordedDiscard bool

type StoreConfig struct {
	Node      env.NodeID
	Epoch     uint64
	Counter   uint64
	Discards  uint64
	Clock     env.Clock
	Rand      *rand.Rand
	Doc       *Doc
	Persist   func(state []byte) error
	Log       func(line string)
	OnDiscard func(key string, winner, loser Version)
}

type Discard struct {
	Key    string
	Winner Version
	Loser  Version
}

func (d Discard) String() string {
	return "occ: lww discard key=" + strconv.Quote(d.Key) +
		" loser=" + d.Loser.id.String() +
		" loser_stamp=" + d.Loser.stamp.String() +
		" loser_writer=" + d.Loser.dot.Actor.String() +
		" winner=" + d.Winner.id.String() +
		" winner_stamp=" + d.Winner.stamp.String()
}

type Store struct {
	Reader
	actor     Actor
	counter   uint64
	hlc       *HLC
	rand      *rand.Rand
	rotations int
	ahead     int
	discards  uint64
	tail      []Discard
	head      int
	work      int
	persist   func([]byte) error
	log       func(string)
	onDiscard func(string, Version, Version)
}

type undo struct {
	actor     Actor
	counter   uint64
	rotations int
	ahead     int
	discards  uint64
	clock     hlcState
	keys      []string
	prev      []Record
	pending   []Discard
	changed   bool
}

func NewStore(cfg StoreConfig) (*Store, error) {
	if cfg.Node == "" || cfg.Clock == nil || cfg.Rand == nil {
		return nil, fmt.Errorf("occ: new store: node, clock and rand are required: %w", env.ErrInvalidArgument)
	}
	return newStore(cfg), nil
}

func newStore(cfg StoreConfig) *Store {
	s := &Store{
		actor:     Actor{Node: cfg.Node, Epoch: cfg.Epoch},
		counter:   cfg.Counter,
		discards:  cfg.Discards,
		hlc:       NewHLC(cfg.Clock, cfg.Node),
		rand:      cfg.Rand,
		persist:   cfg.Persist,
		log:       cfg.Log,
		onDiscard: cfg.OnDiscard,
	}
	s.doc = cfg.Doc
	if s.doc == nil {
		s.doc = NewDoc()
	}
	if s.actor.Epoch == 0 {
		s.actor.Epoch = s.freshEpoch()
		s.counter = 0
	}
	s.observe(s.doc)
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

func (s *Store) Actor() Actor        { return s.actor }
func (s *Store) Counter() uint64     { return s.counter }
func (s *Store) HLC() *HLC           { return s.hlc }
func (s *Store) Rotations() int      { return s.rotations }
func (s *Store) AheadStamps() int    { return s.ahead }
func (s *Store) LWWDiscards() uint64 { return s.discards }
func (s *Store) DiscardTail() []Discard {
	if len(s.tail) == 0 {
		return nil
	}
	return append(slices.Clone(s.tail[s.head:]), s.tail[:s.head]...)
}

func (s *Store) observe(o *Doc) {
	for _, st := range o.Stamps() {
		if err := s.hlc.Observe(st); err != nil {
			s.ahead++
		}
	}
}

func (s *Store) begin() *undo {
	return &undo{
		actor:     s.actor,
		counter:   s.counter,
		rotations: s.rotations,
		ahead:     s.ahead,
		discards:  s.discards,
		clock:     s.hlc.state(),
	}
}

func (s *Store) touch(u *undo, key string) {
	u.keys = append(u.keys, key)
	u.prev = append(u.prev, s.doc.Record(key))
}

func (s *Store) end(u *undo, err error) error {
	if err == nil && u.changed && s.persist != nil {
		err = s.persist(s.EncodeState())
	}
	if err != nil {
		s.rollback(u)
		return err
	}
	s.publish(u.pending)
	return nil
}

func (s *Store) rollback(u *undo) {
	for i := len(u.keys) - 1; i >= 0; i-- {
		s.doc.set(u.keys[i], u.prev[i])
	}
	s.actor, s.counter, s.rotations, s.ahead, s.discards = u.actor, u.counter, u.rotations, u.ahead, u.discards
	s.hlc.restore(u.clock)
}

func (s *Store) publish(pending []Discard) {
	for _, d := range pending {
		if len(s.tail) < DiscardTailLen {
			s.tail = append(s.tail, d)
		} else {
			s.tail[s.head] = d
			s.head = (s.head + 1) % DiscardTailLen
		}
		if s.log != nil {
			s.log(d.String())
		}
		if s.onDiscard != nil {
			s.onDiscard(d.Key, d.Winner, d.Loser)
		}
	}
}

func (s *Store) classify(u *undo, key string, winner, loser Version) {
	causal, ops := winner.supersedes(loser)
	s.work += ops
	if causal || canaryUnrecordedDiscard {
		return
	}
	if s.discards < math.MaxUint64 {
		s.discards++
	}
	u.pending = append(u.pending, Discard{Key: key, Winner: winner, Loser: loser})
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

func (s *Store) SetScalar(key string, value []byte) (Version, error) {
	return s.Put(key, value, Unconditional())
}

func (s *Store) write(key string, tomb bool, value []byte, p Precondition) (Version, error) {
	held := s.doc.Record(key)
	if !p.Holds(held) {
		return Version{}, fmt.Errorf("%s against etag %s: %w", p.String(), held.ETag().String(), ErrPreconditionFailed)
	}
	if tomb && !held.Live() {
		return Version{}, ErrNotFound
	}
	u := s.begin()
	v, err := s.successor(held, tomb, value)
	if err == nil {
		s.touch(u, key)
		s.doc.set(key, NewRecord(v))
		u.changed = true
	}
	if err := s.end(u, err); err != nil {
		return Version{}, err
	}
	return v, nil
}

func (s *Store) successor(held Record, tomb bool, value []byte) (Version, error) {
	stamp, err := s.hlc.Now()
	if err != nil {
		return Version{}, err
	}
	var ctx VersionVector
	if held.ok {
		ctx = held.v.Clock()
		if !stamp.After(held.v.stamp) {
			if stamp, err = held.v.stamp.successor(s.actor.Node); err != nil {
				return Version{}, err
			}
		}
	}
	next := max(s.counter, ctx.Get(s.actor))
	for next == math.MaxUint64 {
		s.actor.Epoch = s.freshEpoch()
		s.rotations++
		next = ctx.Get(s.actor)
	}
	s.counter = next + 1
	return NewVersion(Dot{Actor: s.actor, Counter: s.counter}, ctx, tomb, value, stamp), nil
}

func (s *Store) Merge(o *Doc) (bool, error) {
	u := s.begin()
	s.observe(o)
	u.changed = s.doc.merge(o, func(key string, held, next Record) {
		s.touch(u, key)
		if held.ok {
			s.classify(u, key, next.v, held.v)
		}
	})
	if err := s.end(u, nil); err != nil {
		return false, fmt.Errorf("occ: merge: %w", err)
	}
	return u.changed, nil
}

var stateMagic = []byte("OCCS")

func (s *Store) EncodeState() []byte {
	return encodeState(s.actor.Epoch, s.counter, s.discards, s.doc.Encode())
}

func encodeState(epoch, counter, discards uint64, doc []byte) []byte {
	var p encoder
	p.uvarint(epoch)
	p.uvarint(counter)
	p.uvarint(discards)
	p.raw(doc)
	sum := sha256.Sum256(p.b)
	out := append(slices.Clone(stateMagic), sum[:]...)
	return append(out, p.b...)
}

type persisted struct {
	epoch    uint64
	counter  uint64
	discards uint64
	doc      *Doc
}

func decodeState(b []byte) (persisted, error) {
	if !bytes.HasPrefix(b, stateMagic) || len(b) < len(stateMagic)+sha256.Size {
		return persisted{}, fmt.Errorf("occ: state blob header: %w", ErrMalformed)
	}
	payload := b[len(stateMagic)+sha256.Size:]
	if sum := sha256.Sum256(payload); !bytes.Equal(sum[:], b[len(stateMagic):len(stateMagic)+sha256.Size]) {
		return persisted{}, fmt.Errorf("occ: state blob checksum: %w", ErrMalformed)
	}
	d := &decoder{b: payload}
	st := persisted{epoch: d.uvarint(), counter: d.uvarint(), discards: d.uvarint()}
	if d.err == nil && st.epoch == 0 {
		d.fail("epoch")
	}
	if d.err != nil {
		return persisted{}, d.err
	}
	doc, err := Decode(payload[d.off:])
	if err != nil {
		return persisted{}, err
	}
	st.doc = doc
	return st, nil
}

type Reader struct {
	doc *Doc
}

func (r Reader) Record(key string) Record { return r.doc.Record(key) }
func (r Reader) Keys() []string           { return r.doc.Keys() }
func (r Reader) Doc() *Doc                { return r.doc.Clone() }
func (r Reader) Digest() [32]byte         { return r.doc.Digest() }

func (r Reader) Get(key string) ([]byte, ETag, error) {
	rec := r.doc.Record(key)
	if !rec.Live() {
		return nil, rec.ETag(), fmt.Errorf("occ: get %q: %w", key, ErrNotFound)
	}
	return rec.v.Value(), rec.ETag(), nil
}

func (r Reader) Scalar(key string) ([]byte, Stamp, error) {
	rec := r.doc.Record(key)
	if !rec.Live() {
		return nil, Stamp{}, fmt.Errorf("occ: scalar %q: %w", key, ErrNotFound)
	}
	return rec.v.Value(), rec.v.stamp, nil
}
