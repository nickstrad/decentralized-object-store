package occ

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"

	"github.com/nickstrad/object-store/internal/env"
)

// Bounds on per-store persisted discard records:
//   - DiscardTailLen: maximum recent discards retained in DiscardTail.
//   - DiscardDedupLen: FIFO capacity of (key, loser ID) hashes deduplicated on this node.
//   - DiscardOutboxLen: cap on already-emitted records retained if the post-emit drain write fails.
const (
	DiscardTailLen   = 64
	DiscardDedupLen  = 256
	DiscardOutboxLen = 1024
)

// Test-only switches used by canary tests to plant unrecorded-discard bugs.
var (
	canaryUnrecordedDiscard   bool
	canaryIncomingLoserSilent bool
)

// StoreConfig configures a Store created with NewStore or opened with OpenStore.
// Node, Clock, and Rand are required. OpenStore ignores Epoch, Counter, Discards,
// and Doc because the state blob supplies them. Persist, Log, and OnDiscard are
// optional hooks.
type StoreConfig struct {
	MetricsEnabled bool
	Node           env.NodeID
	Epoch          uint64
	Counter        uint64
	Discards       uint64
	Clock          env.Clock
	Rand           *rand.Rand
	Doc            *Doc
	Persist        func(state []byte) error
	Log            func(line string)
	OnDiscard      func(key string, winner, loser Version)
}

// Discard records a single last-writer-wins loss observed by a node when
// Winner beat concurrent Loser on Key without causally superseding it.
type Discard struct {
	Key    string
	Winner Version
	Loser  Version
}

// String formats d as a deterministic log line naming the key, losing version,
// loser stamp and writer, winning version, and winner stamp.
func (d Discard) String() string {
	return "occ: lww discard key=" + strconv.Quote(d.Key) +
		" loser=" + d.Loser.id.String() +
		" loser_stamp=" + d.Loser.stamp.String() +
		" loser_writer=" + d.Loser.dot.Actor.String() +
		" winner=" + d.Winner.id.String() +
		" winner_stamp=" + d.Winner.stamp.String()
}

type lossID [16]byte

// lossOf hashes (key, loser ID) into a compact 16-byte deduplication key.
func lossOf(key string, loser VersionID) lossID {
	var e encoder
	e.raw([]byte("occ/loss/v1"))
	e.str(key)
	e.raw(loser[:])
	sum := sha256.Sum256(e.b)
	var id lossID
	copy(id[:], sum[:])
	return id
}

// Store is a single-replica OCC metadata store. It wraps a Doc with a durable
// writer actor, per-actor dot counter, HLC, transactional persistence, and
// local LWW discard tracking. Every mutating method rolls back completely if
// Persist returns an error.
type Store struct {
	metric *metricState

	Reader
	actor     Actor
	counter   uint64
	hlc       *HLC
	rand      *rand.Rand
	rotations int
	ahead     int
	discards  uint64
	tail      []Discard
	outbox    []Discard
	seen      []lossID
	work      int
	persist   func([]byte) error
	log       func(string)
	onDiscard func(string, Version, Version)
}

// undo captures the pre-mutation state of a Store transaction so a failed
// write or persist restores the store without leaving any visible trace.
type undo struct {
	actor      Actor
	counter    uint64
	rotations  int
	ahead      int
	discards   uint64
	clock      hlcState
	keys       []string
	prev       []Record
	pending    []Discard
	changed    bool
	docChanged bool
	saved      bool
	tail       []Discard
	outbox     []Discard
	seen       []lossID
}

// NewStore creates a Store from cfg. If cfg.Epoch is zero, it draws a fresh
// non-zero epoch from cfg.Rand and starts the dot counter at 0. It returns
// env.ErrInvalidArgument if Node, Clock, or Rand is missing.
func NewStore(cfg StoreConfig) (*Store, error) {
	if cfg.Node == "" || cfg.Clock == nil || cfg.Rand == nil {
		return nil, fmt.Errorf("occ: new store: node, clock and rand are required: %w", env.ErrInvalidArgument)
	}
	return newStore(cfg), nil
}

func newStore(cfg StoreConfig) *Store {
	s := &Store{metric: newMetricState(cfg.MetricsEnabled),
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

// OpenStore restores a Store from a state blob produced by EncodeState and
// replays any un-drained outbox discards to cfg.Log and cfg.OnDiscard before
// draining the outbox. It returns env.ErrInvalidArgument if Node, Clock, or
// Rand is missing, or ErrMalformed if state fails checksum or format validation.
func OpenStore(cfg StoreConfig, state []byte) (*Store, error) {
	if cfg.Node == "" || cfg.Clock == nil || cfg.Rand == nil {
		return nil, fmt.Errorf("occ: open store: node, clock and rand are required: %w", env.ErrInvalidArgument)
	}
	st, err := decodeState(state)
	if err != nil {
		return nil, fmt.Errorf("occ: open store %s: %w", cfg.Node, err)
	}
	s := openStore(cfg, st)
	s.replay()
	return s, nil
}

func openStore(cfg StoreConfig, st persisted) *Store {
	cfg.Epoch, cfg.Counter, cfg.Discards, cfg.Doc = st.epoch, st.counter, st.discards, st.doc
	s := newStore(cfg)
	s.tail, s.outbox, s.seen = st.tail, st.outbox, st.seen
	return s
}

// replay emits and drains any discard records persisted in the outbox from an
// earlier run that crashed or failed to drain before restarting.
func (s *Store) replay() {
	if len(s.outbox) > 0 {
		s.emit(s.outbox)
		s.drain()
	}
}

func (s *Store) freshEpoch() uint64 {
	for {
		e := s.rand.Uint64()
		if e != 0 && e != s.actor.Epoch {
			return e
		}
	}
}

// Inspection accessors on Store:
//   - Actor: current writer identity (Node, Epoch).
//   - Counter: last dot counter issued under the current actor epoch.
//   - HLC: the store's hybrid logical clock.
//   - Rotations: number of actor epoch rotations performed by this store instance.
//   - AheadStamps: count of incoming or loaded stamps refused by HLC.Observe.
//   - LWWDiscards: persisted saturating count of discard events observed by this node.
func (s *Store) Actor() Actor        { return s.actor }
func (s *Store) Counter() uint64     { return s.counter }
func (s *Store) HLC() *HLC           { return s.hlc }
func (s *Store) Rotations() int      { return s.rotations }
func (s *Store) AheadStamps() int    { return s.ahead }
func (s *Store) LWWDiscards() uint64 { return s.discards }

// DiscardTail returns a copy of up to DiscardTailLen most recent LWW discards
// recorded by this store, oldest first, or nil if none have been recorded.
func (s *Store) DiscardTail() []Discard {
	if len(s.tail) == 0 {
		return nil
	}
	return slices.Clone(s.tail)
}

// observe feeds every stamp in o to the HLC in sorted key order so a single
// far-future stamp on one key does not prevent observing valid stamps on others.
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

// end commits a transaction by persisting the updated state blob (including any
// new outbox records) before emitting discards and draining the outbox. Any
// error rolls back every touched field.
func (s *Store) end(u *undo, err error) error {
	if err == nil && u.changed && s.persist != nil {
		err = s.persist(s.EncodeState())
		if err != nil {
			s.metric.add("occ_persist_errors_total", 1)
		}
	}
	if err != nil {
		s.rollback(u)
		return err
	}
	if s.discards >= u.discards {
		s.metric.add("occ_lww_discards_total", s.discards-u.discards)
	}
	if len(u.pending) > 0 {
		s.emit(u.pending)
		s.drain()
	}
	return nil
}

func (s *Store) rollback(u *undo) {
	for i := len(u.keys) - 1; i >= 0; i-- {
		s.doc.set(u.keys[i], u.prev[i])
	}
	s.actor, s.counter, s.rotations, s.ahead, s.discards = u.actor, u.counter, u.rotations, u.ahead, u.discards
	if u.saved {
		s.tail, s.outbox, s.seen = u.tail, u.outbox, u.seen
	}
	s.hlc.restore(u.clock)
}

// save lazily snapshots tail, outbox, and seen only when a transaction records
// a discard, avoiding slice clones on the common non-discard path.
func (s *Store) save(u *undo) {
	if u.saved {
		return
	}
	u.saved = true
	u.tail, u.outbox, u.seen = s.tail, s.outbox, s.seen
	s.tail, s.outbox, s.seen = slices.Clone(s.tail), slices.Clone(s.outbox), slices.Clone(s.seen)
}

func (s *Store) emit(ds []Discard) {
	for _, d := range ds {
		if s.log != nil {
			s.log(d.String())
		}
		if s.onDiscard != nil {
			s.onDiscard(d.Key, d.Winner, d.Loser)
		}
	}
}

// drain clears the persisted outbox after emission. If the drain write fails,
// up to DiscardOutboxLen already-emitted records remain in the outbox for
// at-least-once replay on the next open; the already-durable mutation is not failed.
func (s *Store) drain() {
	kept := s.outbox
	s.outbox = nil
	if s.persist == nil || len(kept) == 0 {
		return
	}
	if s.persist(s.EncodeState()) != nil {
		s.metric.add("occ_persist_errors_total", 1)
		s.outbox = kept[max(0, len(kept)-DiscardOutboxLen):]
	}
}

// bounded appends x to xs, evicting the oldest element when len(xs) >= n.
func bounded[T any](xs []T, x T, n int) []T {
	if len(xs) >= n {
		xs = append(xs[:0], xs[len(xs)-n+1:]...)
	}
	return append(xs, x)
}

// record logs a concurrent LWW loss into the counter, dedup window, tail, and
// outbox unless (key, loser ID) is already inside the DiscardDedupLen window.
func (s *Store) record(u *undo, key string, winner, loser Version) {
	id := lossOf(key, loser.id)
	if slices.Contains(s.seen, id) {
		return
	}
	s.save(u)
	if s.discards < math.MaxUint64 {
		s.discards++
	}
	d := Discard{Key: key, Winner: winner, Loser: loser}
	s.seen = bounded(s.seen, id, DiscardDedupLen)
	s.tail = bounded(s.tail, d, DiscardTailLen)
	s.outbox = append(s.outbox, d)
	u.pending = append(u.pending, d)
	u.changed = true
}

// replaced classifies a held version overwritten during Merge. If winner did
// not causally supersede loser, it records the discard and retires the current
// actor epoch whenever the loss leaves a gap in this actor's own dot sequence.
func (s *Store) replaced(u *undo, key string, winner, loser Version) {
	causal, ops := winner.supersedes(loser)
	s.work += ops
	if causal {
		return
	}
	// Retiring the actor epoch prevents a later local write from issuing a higher
	// counter whose downset would falsely claim the discarded dot.
	if loser.clockAt(s.actor) > winner.clockAt(s.actor) {
		s.actor.Epoch = s.freshEpoch()
		s.counter = 0
		s.rotations++
	}
	if canaryUnrecordedDiscard {
		return
	}
	s.record(u, key, winner, loser)
}

// rejected classifies an incoming version that lost to or tied the held version
// during Merge, recording a discard unless the held version is identical to or
// causally supersedes incoming.
func (s *Store) rejected(u *undo, key string, held, incoming Version) {
	if held.id == incoming.id {
		return
	}
	below, ops := held.supersedes(incoming)
	s.work += ops
	if below || canaryUnrecordedDiscard || canaryIncomingLoserSilent {
		return
	}
	s.record(u, key, held, incoming)
}

// Put writes value at key if precondition p holds against the current record.
// On failure it changes nothing and returns ErrPreconditionFailed,
// ErrClockExhausted, or the Persist hook's error.
func (s *Store) Put(key string, value []byte, p Precondition) (Version, error) {
	v, err := s.write(key, false, value, p)
	if err != nil {
		return Version{}, fmt.Errorf("occ: put %q: %w", key, err)
	}
	return v, nil
}

// Delete writes a tombstone at key if precondition p holds and the key is live.
// It checks p before checking liveness, returning ErrPreconditionFailed,
// ErrNotFound, ErrClockExhausted, or the Persist hook's error.
func (s *Store) Delete(key string, p Precondition) (Version, error) {
	v, err := s.write(key, true, nil, p)
	if err != nil {
		return Version{}, fmt.Errorf("occ: delete %q: %w", key, err)
	}
	return v, nil
}

// SetScalar writes value unconditionally at key using the same LWW record
// semantics and discard recording as Put.
func (s *Store) SetScalar(key string, value []byte) (Version, error) {
	return s.Put(key, value, Unconditional())
}

func (s *Store) write(key string, tomb bool, value []byte, p Precondition) (vout Version, eout error) {
	defer func() { s.writeMetric(eout) }()
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

// successor constructs a new Version that causally supersedes held and carries
// a stamp strictly above held's without advancing the store's HLC to a
// far-future held stamp.
func (s *Store) successor(held Record, tomb bool, value []byte) (Version, error) {
	stamp, err := s.hlc.Now()
	if err != nil {
		return Version{}, err
	}
	var ctx VersionVector
	if held.ok {
		ctx = held.v.Clock()
		// When held carries a far-future stamp refused by Observe, step just
		// past held on this key alone so the write still wins without poisoning
		// the node's HLC for other keys.
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

// Merge observes every stamp in o, merges o into the store's document under
// last-writer-wins, records any concurrent replacements or rejected incoming
// losers, and persists the updated state. The returned bool reports whether
// the document itself changed (a merge that only records a rejected loser
// persists the discard and returns false, nil).
func (s *Store) Merge(o *Doc) (changed bool, eout error) {
	defer func() {
		outcome := "unchanged"
		if eout != nil {
			outcome = "error"
		} else if changed {
			outcome = "changed"
		}
		s.metric.add("occ_merges_total", 1, outcome)
	}()
	u := s.begin()
	s.observe(o)
	u.docChanged = s.doc.merge(o, func(key string, held, next Record) {
		s.touch(u, key)
		if held.ok {
			s.replaced(u, key, next.v, held.v)
		}
	}, func(key string, held, incoming Version) {
		s.rejected(u, key, held, incoming)
	})
	u.changed = u.changed || u.docChanged
	if err := s.end(u, nil); err != nil {
		return false, fmt.Errorf("occ: merge: %w", err)
	}
	return u.docChanged, nil
}

// stateMagic identifies the checksummed local state blob format (OCS2).
var stateMagic = []byte("OCS2")

// minDiscardSize is the smallest encoded Discard: empty key (1) plus two
// minimal Versions (minRecordSize-1 each).
const minDiscardSize = 2*minRecordSize - 1

// EncodeState serializes the store's durable state—epoch, dot counter, discard
// count, tail, outbox, dedup window, and document—with a leading SHA-256 checksum.
func (s *Store) EncodeState() []byte {
	return encodeState(persisted{epoch: s.actor.Epoch, counter: s.counter, discards: s.discards, tail: s.tail, outbox: s.outbox, seen: s.seen}, s.doc.Encode())
}

func (e *encoder) discards(ds []Discard) {
	e.uvarint(uint64(len(ds)))
	for _, d := range ds {
		e.str(d.Key)
		e.version(d.Winner)
		e.version(d.Loser)
	}
}

func (d *decoder) discards(limit int) []Discard {
	n := d.count(minDiscardSize)
	if d.err == nil && n > limit {
		d.fail("discard list length")
	}
	var out []Discard
	for i := 0; i < n && d.err == nil; i++ {
		x := Discard{Key: d.str(), Winner: d.version(), Loser: d.version()}
		if d.err == nil {
			out = append(out, x)
		}
	}
	return out
}

func encodeState(st persisted, doc []byte) []byte {
	var p encoder
	p.uvarint(st.epoch)
	p.uvarint(st.counter)
	p.uvarint(st.discards)
	p.discards(st.tail)
	p.discards(st.outbox)
	p.uvarint(uint64(len(st.seen)))
	for _, id := range st.seen {
		p.raw(id[:])
	}
	p.raw(doc)
	sum := sha256.Sum256(p.b)
	out := append(slices.Clone(stateMagic), sum[:]...)
	return append(out, p.b...)
}

type persisted struct {
	epoch    uint64
	counter  uint64
	discards uint64
	tail     []Discard
	outbox   []Discard
	seen     []lossID
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
	st.tail = d.discards(DiscardTailLen)
	// The outbox has no fixed count cap here because a single large merge can
	// legitimately emit one discard per key; d.count still bounds it by input size.
	st.outbox = d.discards(math.MaxInt)
	n := d.count(len(lossID{}))
	if d.err == nil && n > DiscardDedupLen {
		d.fail("loss window length")
	}
	for i := 0; i < n && d.err == nil; i++ {
		var id lossID
		copy(id[:], payload[d.off:])
		d.off += len(id)
		st.seen = append(st.seen, id)
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

// Reader provides read-only access to a Store's underlying Doc.
type Reader struct {
	doc *Doc
}

// Read-only document accessors on Reader:
//   - Record: returns the single Record (present or absent) at key.
//   - Keys: returns a sorted copy of all keys in the document, including tombstones.
//   - Doc: returns an independent clone of the underlying Doc.
//   - Digest: returns the SHA-256 digest of the canonical document encoding.
func (r Reader) Record(key string) Record { return r.doc.Record(key) }
func (r Reader) Keys() []string           { return r.doc.Keys() }
func (r Reader) Doc() *Doc                { return r.doc.Clone() }
func (r Reader) Digest() [32]byte         { return r.doc.Digest() }

// Get returns a copy of the live value at key and its ETag. If the key is
// absent or a tombstone, it returns ErrNotFound together with the record's
// ETag (non-zero for a tombstone so callers can recreate with If-Match).
func (r Reader) Get(key string) ([]byte, ETag, error) {
	rec := r.doc.Record(key)
	if !rec.Live() {
		return nil, rec.ETag(), fmt.Errorf("occ: get %q: %w", key, ErrNotFound)
	}
	return rec.v.Value(), rec.ETag(), nil
}

// Scalar returns a copy of the live value at key and its HLC stamp, or
// ErrNotFound if the key is absent or deleted.
func (r Reader) Scalar(key string) ([]byte, Stamp, error) {
	rec := r.doc.Record(key)
	if !rec.Live() {
		return nil, Stamp{}, fmt.Errorf("occ: scalar %q: %w", key, ErrNotFound)
	}
	return rec.v.Value(), rec.v.stamp, nil
}
