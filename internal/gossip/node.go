// Package gossip implements SWIM cluster membership and failure detection as a
// single-threaded env.Process.
//
// Each Node probes peers in shuffled round-robin order, uses indirect ping-req
// probes on direct timeout, scales suspicion timeouts and piggyback retransmit
// limits with ceil(log2 N), refutes suspicion or death by raising its volatile
// incarnation number, and runs periodic push-pull sync against live members,
// dead entries/tombstones, and remembered seeds so healed partitions re-merge.
//
// Rules for callers:
//   - A Node acts only through env.Clock, env.Rand, and env.Transport (env.Disk
//     is unused) and must be driven by a single serialized event loop.
//   - Subscribe callbacks run synchronously inside the handler that changed the
//     view; Join completion callbacks never run inside Join.
//   - Plain SWIM is unauthenticated: a forged dead claim at math.MaxUint32
//     saturates incarnation and cannot be refuted. Expired dead/left entries
//     move to a bounded tombstone table (MaxTombstones) so monotonicity and
//     post-retention partition healing hold while tombstones are not evicted.
package gossip

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

// EventKind identifies the membership transition reported in an Event.
type EventKind uint8

// Membership transition event kinds emitted to subscribers:
//   - EventJoined: a previously non-live or unknown member became live.
//   - EventSuspected: a member entered StateSuspect (follows EventJoined if first seen suspect).
//   - EventRecovered: a suspected member refuted and returned to StateAlive.
//   - EventDied: a live member transitioned to StateDead.
//   - EventLeft: a live member transitioned to StateLeft.
const (
	EventJoined EventKind = iota + 1
	EventSuspected
	EventRecovered
	EventDied
	EventLeft
)

// String returns the canonical lowercase name of k, or "event(N)" if unknown.
func (k EventKind) String() string {
	switch k {
	case EventJoined:
		return "joined"
	case EventSuspected:
		return "suspected"
	case EventRecovered:
		return "recovered"
	case EventDied:
		return "died"
	case EventLeft:
		return "left"
	}
	return "event(" + strconv.Itoa(int(k)) + ")"
}

// Event describes one membership state transition observed at virtual time At.
type Event struct {
	Kind   EventKind
	Member Member
	At     env.Time
}

// Membership is the read and subscription surface exposed to other cluster
// components. All methods return plain values.
type Membership interface {
	// Members returns all live (StateAlive or StateSuspect) members, including
	// self, sorted by ID.
	Members() []Member
	// Lookup returns any currently retained entry for id, or ErrNotMember if
	// unknown or buried.
	Lookup(id env.NodeID) (Member, error)
	// Subscribe registers fn to be called synchronously whenever a member
	// transitions between membership states.
	Subscribe(fn func(Event))
}

// Stats holds monotonic diagnostic counters for a Node's datagram traffic,
// probing, suspicion, refutation, sync exchanges, and resource bounds.
type Stats struct {
	Sent              uint64
	SendErrors        uint64
	Received          uint64
	Malformed         uint64
	DroppedClosed     uint64
	Probes            uint64
	IndirectProbes    uint64
	Suspicions        uint64
	Refutations       uint64
	RefuteOverflows   uint64
	Syncs             uint64
	SyncErrors        uint64
	Purged            uint64
	Rejoins           uint64
	RelaysDropped     uint64
	ViewFull          uint64
	TombstonesEvicted uint64
	Reinstated        uint64
}

type memberLocal struct {
	changed env.Time
	suspect env.Timer
}

type queued struct {
	m         Member
	transmits int
	order     uint64
}

type relay struct {
	origin    env.NodeID
	originSeq uint32
	expires   env.Time
}

type relayRef struct {
	seq     uint32
	expires env.Time
}

type tomb struct {
	m     Member
	order uint64
}

type tombRef struct {
	id    env.NodeID
	order uint64
}

type probeState struct {
	active bool
	acked  bool
	target env.NodeID
	seq    uint32
	timer  env.Timer
}

// Node is a single-threaded SWIM gossip member implementing env.Process and
// Membership. A Node is not safe for concurrent use.
type Node struct {
	e     env.Env
	cfg   Config
	self  Member
	view  *View
	local map[env.NodeID]*memberLocal

	queue  []*queued
	qindex map[env.NodeID]*queued
	qorder uint64

	tombs    map[env.NodeID]tomb
	tombFIFO []tombRef
	tombSeq  uint64

	nextPurge env.Time

	probe     probeState
	rotation  []env.NodeID
	rotIdx    int
	rotating  map[env.NodeID]bool
	seq       uint32
	relays    map[uint32]relay
	relayFIFO []relayRef

	rx        []Member
	tx        []Member
	seeds     []env.NodeID
	subs      []func(Event)
	tick      env.Timer
	syncTimer env.Timer
	joins     int
	started   bool
	closed    bool
	stats     Stats
	lastErr   error
}

var _ env.Process = (*Node)(nil)
var _ Membership = (*Node)(nil)

// New constructs a Node for e with cfg (applying defaults before validation).
// It returns env.ErrInvalidArgument if cfg is invalid, e.ID is empty or longer
// than MaxIDLen, e.Region exceeds MaxIDLen, or Clock, Net, or Rand is nil.
func New(e env.Env, cfg Config) (*Node, error) {
	cfg = cfg.WithDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	switch {
	case e.ID == "" || len(e.ID) > MaxIDLen:
		return nil, fmt.Errorf("gossip: new node: id of %d bytes: %w", len(e.ID), env.ErrInvalidArgument)
	case len(e.Region) > MaxIDLen:
		return nil, fmt.Errorf("gossip: new node %s: region of %d bytes: %w", e.ID, len(e.Region), env.ErrInvalidArgument)
	case e.Clock == nil || e.Net == nil || e.Rand == nil:
		return nil, fmt.Errorf("gossip: new node %s: environment lacks a clock, transport or random stream: %w", e.ID, env.ErrInvalidArgument)
	}
	n := &Node{
		e:        e,
		cfg:      cfg,
		self:     Member{ID: e.ID, Region: e.Region, State: StateAlive},
		view:     NewView(),
		local:    map[env.NodeID]*memberLocal{},
		qindex:   map[env.NodeID]*queued{},
		tombs:    map[env.NodeID]tomb{},
		rotating: map[env.NodeID]bool{},
		relays:   map[uint32]relay{},
	}
	n.remember(cfg.Seeds)
	n.view.Apply(n.self)
	return n, nil
}

// remember records distinct non-self, non-empty seed IDs up to MaxSeeds for
// lonely rejoin retries and periodic partition-heal syncs.
func (n *Node) remember(seeds []env.NodeID) {
	for _, s := range seeds {
		if len(n.seeds) >= MaxSeeds {
			return
		}
		if s != n.self.ID && s != "" && !slices.Contains(n.seeds, s) {
			n.seeds = append(n.seeds, s)
		}
	}
}

// Start arms the periodic probe and push-pull sync timers and initiates a join
// to any configured seeds. Calling Start more than once or after Leave is a no-op.
func (n *Node) Start() {
	if n.started || n.closed {
		return
	}
	n.started = true
	n.enqueue(n.self)
	// Randomize initial probe and sync offsets so nodes started together do not
	// fire probe or sync bursts in lockstep.
	n.tick = n.e.Clock.AfterFunc(randDuration(n, n.cfg.ProbeInterval), n.onTick)
	n.syncTimer = n.e.Clock.AfterFunc(n.cfg.SyncInterval/2+randDuration(n, n.cfg.SyncInterval), n.onSync)
	if len(n.seeds) > 0 {
		n.join(n.seeds, nil)
	}
}

func randDuration(n *Node, d time.Duration) time.Duration {
	return time.Duration(n.e.Rand.Int64N(int64(d)))
}

// Self returns this node's own membership record.
func (n *Node) Self() Member { return n.self }

// Members returns all live (StateAlive or StateSuspect) members, including
// self, sorted by ID.
func (n *Node) Members() []Member { return n.view.Live() }

// All returns every entry currently retained in the view, including dead and
// left entries within DeadRetention, sorted by ID.
func (n *Node) All() []Member { return n.view.All() }

// Tombstones returns the buried dead and left verdicts currently remembered
// after DeadRetention expiry, sorted by ID.
func (n *Node) Tombstones() []Member {
	out := make([]Member, 0, len(n.tombs))
	for _, r := range n.tombFIFO {
		if t, ok := n.tombs[r.id]; ok && t.order == r.order {
			out = append(out, t.m)
		}
	}
	slices.SortFunc(out, func(a, b Member) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return out
}

// LiveCount returns the number of live (StateAlive or StateSuspect) members,
// including self.
func (n *Node) LiveCount() int { return n.view.LiveCount() }

// Lookup returns the retained view entry for id, or an error wrapping
// ErrNotMember if id is unknown or has been buried as a tombstone.
func (n *Node) Lookup(id env.NodeID) (Member, error) {
	m, ok := n.view.Get(id)
	if !ok {
		return Member{}, fmt.Errorf("gossip: lookup %s: %w", id, ErrNotMember)
	}
	return m, nil
}

// Subscribe appends fn to the list of listeners called synchronously on every
// membership state transition. Nil callbacks are ignored.
func (n *Node) Subscribe(fn func(Event)) {
	if fn != nil {
		n.subs = append(n.subs, fn)
	}
}

// Stats returns a snapshot of the node's diagnostic counters.
func (n *Node) Stats() Stats { return n.stats }

// LastError returns the most recent non-fatal datagram, sync, refutation, or
// view-bound error recorded by the node, or nil if none has occurred.
func (n *Node) LastError() error { return n.lastErr }

// Closed reports whether Leave has been called on this node.
func (n *Node) Closed() bool { return n.closed }

func (n *Node) record(counter *uint64, err error) {
	*counter++
	n.lastErr = err
}

func (n *Node) emit(kind EventKind, m Member) {
	ev := Event{Kind: kind, Member: m, At: n.e.Clock.Now()}
	for _, fn := range n.subs {
		fn(ev)
	}
}

// OnDatagram processes an inbound SWIM datagram (ping, ack, ping-req, or
// gossip). Datagrams from self are ignored; datagrams arriving after Leave or
// failing size, decode, or kind validation are dropped and counted in Stats.
func (n *Node) OnDatagram(from env.NodeID, msg []byte) {
	if from == n.self.ID {
		return
	}
	if n.closed {
		n.stats.DroppedClosed++
		return
	}
	n.stats.Received++
	if len(msg) > n.cfg.MaxPacket {
		n.record(&n.stats.Malformed, fmt.Errorf("gossip: datagram from %s: %d bytes over the %d-byte limit: %w", from, len(msg), n.cfg.MaxPacket, ErrMalformed))
		return
	}
	// Take ownership of the scratch decode buffer before invoking handlers so
	// any nested call cannot alias the same backing array.
	rx := n.rx
	n.rx = nil
	m, err := decodeInto(msg, rx, n.known)
	if err != nil {
		n.rx = rx
		n.record(&n.stats.Malformed, fmt.Errorf("gossip: datagram from %s: %w", from, err))
		return
	}
	defer func() {
		if m.Updates != nil {
			rx = m.Updates[:0]
		}
		n.rx = rx
	}()
	if !m.Kind.datagram() {
		n.record(&n.stats.Malformed, fmt.Errorf("gossip: datagram from %s: %s is not a datagram kind: %w", from, m.Kind.String(), ErrMalformed))
		return
	}
	for _, u := range m.Updates {
		n.receive(u)
	}
	switch m.Kind {
	case KindPing:
		n.send(from, Message{Kind: KindAck, Seq: m.Seq})
	case KindAck:
		n.onAck(m.Seq)
	case KindPingReq:
		n.onPingReq(from, m)
	}
}

func (n *Node) known(id []byte) (Member, bool) {
	m, ok := n.view.byID[env.NodeID(id)]
	return m, ok
}

// pruneRelays expires relays past their deadline in FIFO order and compacts
// stale references left by already-answered relays once the FIFO exceeds 2*MaxRelays.
func (n *Node) pruneRelays(now env.Time) {
	i := 0
	for i < len(n.relayFIFO) && now > n.relayFIFO[i].expires {
		ref := n.relayFIFO[i]
		if r, ok := n.relays[ref.seq]; ok && r.expires == ref.expires {
			delete(n.relays, ref.seq)
		}
		i++
	}
	n.relayFIFO = n.relayFIFO[i:]
	if len(n.relayFIFO) > 2*MaxRelays {
		n.relayFIFO = slices.DeleteFunc(n.relayFIFO, func(ref relayRef) bool {
			r, ok := n.relays[ref.seq]
			return !ok || r.expires != ref.expires
		})
	}
}

func (n *Node) onPingReq(from env.NodeID, m Message) {
	if m.Target == n.self.ID {
		n.send(from, Message{Kind: KindAck, Seq: m.Seq})
		return
	}
	now := n.e.Clock.Now()
	// Prune on arrival so expired relays free slots even before Start or under
	// long probe intervals, then enforce the hard MaxRelays cap.
	n.pruneRelays(now)
	if len(n.relays) >= MaxRelays {
		n.stats.RelaysDropped++
		return
	}
	n.seq++
	seq := n.seq
	// Record the relay only if the ping was actually accepted by the transport.
	if !n.sendWith(m.Target, Message{Kind: KindPing, Seq: seq}, nil) {
		return
	}
	expires := now.Add(n.cfg.ProbeInterval)
	n.relays[seq] = relay{origin: from, originSeq: m.Seq, expires: expires}
	n.relayFIFO = append(n.relayFIFO, relayRef{seq: seq, expires: expires})
}

func (n *Node) onAck(seq uint32) {
	if n.probe.active && !n.probe.acked && n.probe.seq == seq {
		n.probe.acked = true
		if n.probe.timer != nil {
			n.probe.timer.Stop()
		}
		return
	}
	r, ok := n.relays[seq]
	if !ok {
		return
	}
	delete(n.relays, seq)
	if n.e.Clock.Now() <= r.expires {
		n.send(r.origin, Message{Kind: KindAck, Seq: r.originSeq})
	}
}

// receive routes updates about self to refutation and all other updates to the
// membership view so a node never applies a non-alive state to itself.
func (n *Node) receive(u Member) {
	if u.ID == n.self.ID {
		n.refute(u)
		return
	}
	n.apply(u)
}

func (n *Node) refute(u Member) {
	if n.closed {
		return
	}
	// Ignore stale claims and exact matches; a region mismatch at equal
	// incarnation must still be refuted because Supersedes breaks ties by region.
	if u.Incarnation < n.self.Incarnation || (u.Incarnation == n.self.Incarnation && u.State == StateAlive && u.Region == n.self.Region) {
		return
	}
	if u.Incarnation == math.MaxUint32 {
		// Incarnation cannot increment past MaxUint32; saturate without wrap.
		n.stats.RefuteOverflows++
		n.lastErr = fmt.Errorf("gossip: %s claims %s at the maximum incarnation; cannot refute: %w", u.ID, u.State.String(), env.ErrInvalidArgument)
		if n.self.Incarnation == math.MaxUint32 {
			return
		}
		n.self.Incarnation = math.MaxUint32
	} else {
		n.self.Incarnation = u.Incarnation + 1
		n.stats.Refutations++
	}
	n.view.Apply(n.self)
	n.enqueue(n.self)
}

func (n *Node) localOf(id env.NodeID) *memberLocal {
	l, ok := n.local[id]
	if !ok {
		l = &memberLocal{}
		n.local[id] = l
	}
	return l
}

func (n *Node) admit(id env.NodeID) bool {
	if n.view.Len() < n.cfg.MaxMembers {
		return true
	}
	n.record(&n.stats.ViewFull, fmt.Errorf("gossip: member %s: view holds %d members: %w", id, n.view.Len(), ErrViewFull))
	return false
}

func (n *Node) markChanged(id env.NodeID, m Member) *memberLocal {
	l := n.localOf(id)
	l.changed = n.e.Clock.Now()
	if !m.State.Live() {
		n.nextPurge = min(n.nextPurge, l.changed.Add(n.cfg.DeadRetention))
	}
	return l
}

func (n *Node) apply(u Member) bool {
	if u.ID == "" || !u.State.valid() {
		return false
	}
	cur, had := n.view.Get(u.ID)
	if had && !Supersedes(u, cur) {
		return false
	}
	if !had {
		// Invariant: n.view and n.tombs are disjoint. A non-superseding dead/left
		// verdict for a buried ID is dropped so peers purging at different times
		// never resurrect it; a non-superseding live claim reinstates the buried
		// verdict for one DeadRetention (quietly, not gossiped) so a wiped or
		// lagging node learns the verdict and refutes above it.
		if t, ok := n.tombs[u.ID]; ok && !Supersedes(u, t.m) {
			if u.State.Live() && n.admit(u.ID) {
				delete(n.tombs, u.ID)
				n.view.Apply(t.m)
				n.markChanged(u.ID, t.m)
				n.stats.Reinstated++
			}
			return false
		}
		if !n.admit(u.ID) {
			return false
		}
		delete(n.tombs, u.ID)
	}
	if !n.view.Apply(u) {
		return false
	}
	l := n.markChanged(u.ID, u)
	wasLive, isLive := had && cur.State.Live(), u.State.Live()
	if u.State == StateSuspect {
		if !(had && cur.State == StateSuspect && cur.Incarnation == u.Incarnation) {
			n.startSuspicion(l, u)
		}
	} else if l.suspect != nil {
		l.suspect.Stop()
		l.suspect = nil
	}
	switch {
	case !wasLive && isLive:
		n.addToRotation(u.ID)
		n.emit(EventJoined, u)
		if u.State == StateSuspect {
			n.emit(EventSuspected, u)
		}
	case wasLive && isLive:
		if cur.State == StateAlive && u.State == StateSuspect {
			n.emit(EventSuspected, u)
		} else if cur.State == StateSuspect && u.State == StateAlive {
			n.emit(EventRecovered, u)
		}
	case wasLive && !isLive:
		if u.State == StateDead {
			n.emit(EventDied, u)
		} else {
			n.emit(EventLeft, u)
		}
	}
	// Do not re-gossip second-hand dead/left verdicts about members this node
	// never held; sync replies still carry them for DeadRetention.
	if had || isLive {
		n.enqueue(u)
	}
	return true
}

func (n *Node) startSuspicion(l *memberLocal, u Member) {
	if l.suspect != nil {
		l.suspect.Stop()
	}
	id, inc := u.ID, u.Incarnation
	l.suspect = n.e.Clock.AfterFunc(n.cfg.SuspicionTimeout(n.view.LiveCount()), func() {
		if n.closed {
			return
		}
		m, ok := n.view.Get(id)
		if !ok || m.State != StateSuspect || m.Incarnation != inc {
			return
		}
		m.State = StateDead
		n.apply(m)
	})
}

// addToRotation inserts a newly live peer into a random slot in the unvisited
// remainder of the current probe round in O(1) time.
func (n *Node) addToRotation(id env.NodeID) {
	if n.rotating[id] {
		return
	}
	n.rotation = append(n.rotation, id)
	last := len(n.rotation) - 1
	at := n.rotIdx + n.e.Rand.IntN(last-n.rotIdx+1)
	n.rotation[at], n.rotation[last] = n.rotation[last], n.rotation[at]
	n.rotating[id] = true
}

// nextTarget returns the next live non-self peer in the shuffled round-robin
// schedule, rebuilding and reshuffling the round when exhausted.
func (n *Node) nextTarget() (env.NodeID, bool) {
	for range 2 {
		for n.rotIdx < len(n.rotation) {
			id := n.rotation[n.rotIdx]
			n.rotIdx++
			delete(n.rotating, id)
			if m, ok := n.view.Get(id); ok && m.State.Live() && id != n.self.ID {
				return id, true
			}
		}
		n.rotation = n.rotation[:0]
		for _, m := range n.view.Live() {
			if m.ID != n.self.ID {
				n.rotation = append(n.rotation, m.ID)
				n.rotating[m.ID] = true
			}
		}
		n.e.Rand.Shuffle(len(n.rotation), func(i, j int) { n.rotation[i], n.rotation[j] = n.rotation[j], n.rotation[i] })
		n.rotIdx = 0
		if len(n.rotation) == 0 {
			return "", false
		}
	}
	return "", false
}

func (n *Node) onTick() {
	if n.closed {
		return
	}
	if n.probe.active && !n.probe.acked {
		n.suspectLocally(n.probe.target)
	}
	if n.probe.timer != nil {
		n.probe.timer.Stop()
	}
	n.probe = probeState{}
	now := n.e.Clock.Now()
	n.pruneRelays(now)
	n.purge(now)
	// If this node knows no live peer other than itself, retry remembered seeds
	// once per probe period (one join in flight at a time) so early join packet
	// loss does not leave it isolated until the first periodic sync.
	if n.view.LiveCount() <= 1 && len(n.seeds) > 0 && n.joins == 0 {
		n.stats.Rejoins++
		n.join(n.seeds, nil)
	}
	if target, ok := n.nextTarget(); ok {
		n.seq++
		seq := n.seq
		n.probe = probeState{active: true, target: target, seq: seq}
		n.stats.Probes++
		n.send(target, Message{Kind: KindPing, Seq: seq})
		n.probe.timer = n.e.Clock.AfterFunc(n.cfg.ProbeTimeout, func() { n.onProbeTimeout(seq) })
	}
	n.tick = n.e.Clock.AfterFunc(n.cfg.ProbeInterval, n.onTick)
}

func (n *Node) suspectLocally(id env.NodeID) {
	m, ok := n.view.Get(id)
	if !ok || m.State != StateAlive {
		return
	}
	m.State = StateSuspect
	if n.apply(m) {
		n.stats.Suspicions++
	}
}

func (n *Node) onProbeTimeout(seq uint32) {
	if n.closed || !n.probe.active || n.probe.acked || n.probe.seq != seq || n.cfg.noIndirect {
		return
	}
	cands := make([]env.NodeID, 0, n.view.LiveCount())
	for _, id := range n.view.order() {
		if m := n.view.byID[id]; m.State.Live() && id != n.self.ID && id != n.probe.target {
			cands = append(cands, id)
		}
	}
	k := min(n.cfg.IndirectProbes, len(cands))
	if k == 0 {
		return
	}
	for i := 0; i < k; i++ {
		j := i + n.e.Rand.IntN(len(cands)-i)
		cands[i], cands[j] = cands[j], cands[i]
	}
	n.stats.IndirectProbes++
	for _, via := range cands[:k] {
		n.send(via, Message{Kind: KindPingReq, Seq: seq, Target: n.probe.target})
	}
}

func (n *Node) purge(now env.Time) {
	if n.view.Len() == n.view.LiveCount() || now < n.nextPurge {
		return
	}
	var expired []Member
	next := env.Time(math.MaxInt64)
	for _, m := range n.view.All() {
		if m.ID == n.self.ID || m.State.Live() {
			continue
		}
		due := n.localOf(m.ID).changed.Add(n.cfg.DeadRetention)
		if now < due {
			next = min(next, due)
			continue
		}
		expired = append(expired, m)
	}
	n.nextPurge = next
	if len(expired) == 0 {
		return
	}
	for _, m := range expired {
		n.view.Remove(m.ID)
		delete(n.local, m.ID)
		delete(n.qindex, m.ID)
		n.bury(m)
		n.stats.Purged++
	}
	n.queue = slices.DeleteFunc(n.queue, func(q *queued) bool { return n.qindex[q.m.ID] != q })
}

// bury records an expired dead/left verdict as a tombstone, evicting the
// oldest-buried tombstone when at MaxTombstones and compacting stale FIFO
// entries left by reinstatements or rejoins past 2*MaxTombstones.
func (n *Node) bury(m Member) {
	for len(n.tombs) >= MaxTombstones {
		ref := n.tombFIFO[0]
		n.tombFIFO = n.tombFIFO[1:]
		if t, ok := n.tombs[ref.id]; ok && t.order == ref.order {
			delete(n.tombs, ref.id)
			n.stats.TombstonesEvicted++
		}
	}
	n.tombSeq++
	n.tombs[m.ID] = tomb{m: m, order: n.tombSeq}
	n.tombFIFO = append(n.tombFIFO, tombRef{id: m.ID, order: n.tombSeq})
	if len(n.tombFIFO) > 2*MaxTombstones {
		n.tombFIFO = slices.DeleteFunc(n.tombFIFO, func(r tombRef) bool {
			t, ok := n.tombs[r.id]
			return !ok || t.order != r.order
		})
	}
}

func (n *Node) enqueue(m Member) {
	n.qorder++
	if q, ok := n.qindex[m.ID]; ok {
		q.m, q.transmits, q.order = m, 0, n.qorder
		return
	}
	q := &queued{m: m, order: n.qorder}
	n.qindex[m.ID] = q
	n.queue = append(n.queue, q)
}

// piggyback selects updates to attach to an outbound datagram to peer to
// within MaxPacket and MaxUpdates: explicit leads first, then to's own
// non-alive entry (so a suspected or dead peer learns about it even after the
// queue is spent), then queued updates ordered by fewest transmissions and newest first.
func (n *Node) piggyback(to env.NodeID, size int, lead []Member) []Member {
	budget := n.cfg.MaxPacket - size
	// Take ownership of the scratch slice before encoding so nested calls never alias it.
	out := n.tx[:0]
	n.tx = nil
	var first []Member
	take := func(m Member) bool {
		s := UpdateSize(m)
		if s > budget || len(out) >= MaxUpdates {
			return false
		}
		budget -= s
		out = append(out, m)
		return true
	}
	for _, m := range lead {
		if take(m) {
			first = append(first, m)
		}
	}
	if m, ok := n.view.Get(to); ok && to != n.self.ID && m.State != StateAlive && !slices.Contains(first, m) && take(m) {
		first = append(first, m)
	}
	slices.SortFunc(n.queue, func(a, b *queued) int {
		if a.transmits != b.transmits {
			return a.transmits - b.transmits
		}
		if a.order > b.order {
			return -1
		}
		if a.order < b.order {
			return 1
		}
		return 0
	})
	limit := n.cfg.RetransmitLimit(n.view.LiveCount())
	for _, q := range n.queue {
		if len(first) > 0 && slices.Contains(first, q.m) {
			q.transmits++
			continue
		}
		if take(q.m) {
			q.transmits++
		}
	}
	n.queue = slices.DeleteFunc(n.queue, func(q *queued) bool {
		if q.transmits < limit {
			return false
		}
		delete(n.qindex, q.m.ID)
		return true
	})
	return out
}

func (n *Node) send(to env.NodeID, m Message) {
	n.sendWith(to, m, nil)
}

func (n *Node) sendWith(to env.NodeID, m Message, lead []Member) bool {
	m.Updates = n.piggyback(to, m.Size(), lead)
	b, err := m.Encode()
	n.tx = m.Updates[:0]
	if err != nil {
		n.record(&n.stats.SendErrors, fmt.Errorf("gossip: send %s to %s: %w", m.Kind.String(), to, err))
		return false
	}
	if err := n.e.Net.Send(to, b); err != nil {
		n.record(&n.stats.SendErrors, fmt.Errorf("gossip: send %s to %s: %w", m.Kind.String(), to, err))
		return false
	}
	n.stats.Sent++
	return true
}

// Join contacts every non-self, non-empty seed in seeds with a full-state sync
// request and remembers those seeds (up to MaxSeeds) for future heal syncs and
// lonely rejoins. It returns ErrClosed if Leave has already been called.
// Otherwise done (if non-nil) runs asynchronously: with nil on the first seed
// reply, or after all seeds fail with ErrJoinFailed joined with each cause.
// Joining with no non-self seeds bootstraps a single-node cluster and reports
// nil (or ErrClosed if Leave runs before the deferred callback fires).
func (n *Node) Join(seeds []env.NodeID, done func(error)) error {
	if n.closed {
		return fmt.Errorf("gossip: join: %w", ErrClosed)
	}
	n.remember(seeds)
	n.join(seeds, done)
	return nil
}

func (n *Node) join(seeds []env.NodeID, done func(error)) {
	if done == nil {
		done = func(error) {}
	}
	var targets []env.NodeID
	for _, s := range seeds {
		if s != n.self.ID && s != "" && !slices.Contains(targets, s) {
			targets = append(targets, s)
		}
	}
	if len(targets) == 0 {
		// Defer through AfterFunc(0) so done never runs synchronously inside Join.
		n.e.Clock.AfterFunc(0, func() {
			if n.closed {
				done(fmt.Errorf("gossip: join: %w", ErrClosed))
				return
			}
			done(nil)
		})
		return
	}
	remaining, ok := len(targets), false
	var errs []error
	n.joins++
	for _, t := range targets {
		n.syncWith(t, func(err error) {
			remaining--
			if remaining == 0 {
				n.joins--
			}
			if err != nil {
				errs = append(errs, err)
			} else if !ok {
				ok = true
				done(nil)
			}
			if remaining == 0 && !ok {
				done(fmt.Errorf("gossip: join via %d seeds: %w", len(targets), errors.Join(append([]error{ErrJoinFailed}, errs...)...)))
			}
		})
	}
}

func (n *Node) syncWith(peer env.NodeID, cb func(error)) {
	req, err := Message{Kind: KindSyncReq, Updates: n.view.All()}.Encode()
	if err != nil {
		n.record(&n.stats.SyncErrors, fmt.Errorf("gossip: sync with %s: %w", peer, err))
		failed := n.lastErr
		// Report encode failures asynchronously so Join's callback contract holds.
		n.e.Clock.AfterFunc(0, func() { cb(failed) })
		return
	}
	n.e.Net.Request(peer, req, n.cfg.SyncTimeout, func(reply []byte, err error) {
		if n.closed {
			cb(fmt.Errorf("gossip: sync with %s: %w", peer, ErrClosed))
			return
		}
		if err != nil {
			n.record(&n.stats.SyncErrors, fmt.Errorf("gossip: sync with %s: %w", peer, err))
			cb(n.lastErr)
			return
		}
		m, err := Decode(reply)
		if err == nil && m.Kind != KindSyncResp {
			err = fmt.Errorf("gossip: reply of kind %s: %w", m.Kind.String(), ErrMalformed)
		}
		if err != nil {
			n.record(&n.stats.SyncErrors, fmt.Errorf("gossip: sync with %s: %w", peer, err))
			cb(n.lastErr)
			return
		}
		for _, u := range m.Updates {
			n.receive(u)
		}
		n.stats.Syncs++
		cb(nil)
	})
}

func (n *Node) onSync() {
	if n.closed {
		return
	}
	var live, heal []env.NodeID
	for _, id := range n.view.order() {
		m := n.view.byID[id]
		switch {
		case id == n.self.ID:
		case m.State.Live():
			live = append(live, id)
		case m.State == StateDead:
			heal = append(heal, id)
		}
	}
	// Include buried dead tombstones (never StateLeft) and non-live remembered
	// seeds so partitions longer than DeadRetention can still re-merge.
	for _, r := range n.tombFIFO {
		if t, ok := n.tombs[r.id]; ok && t.order == r.order && t.m.State == StateDead {
			heal = append(heal, r.id)
		}
	}
	for _, s := range n.seeds {
		if m, ok := n.view.Get(s); !ok || !m.State.Live() {
			heal = append(heal, s)
		}
	}
	slices.Sort(heal)
	heal = slices.Compact(heal)
	ignore := func(error) {}
	if len(live) > 0 {
		n.syncWith(live[n.e.Rand.IntN(len(live))], ignore)
	}
	if len(heal) > 0 {
		n.syncWith(heal[n.e.Rand.IntN(len(heal))], ignore)
	}
	n.syncTimer = n.e.Clock.AfterFunc(n.cfg.SyncInterval, n.onSync)
}

// OnRequest handles an inbound KindSyncReq by merging the sender's updates and
// replying once with the merged view as KindSyncResp. It replies with ErrClosed
// after Leave or ErrMalformed for invalid or non-sync payloads.
func (n *Node) OnRequest(from env.NodeID, req []byte, reply env.Reply) {
	if n.closed {
		reply(nil, fmt.Errorf("gossip: sync from %s: %w", from, ErrClosed))
		return
	}
	m, err := Decode(req)
	if err == nil && m.Kind != KindSyncReq {
		err = fmt.Errorf("gossip: request of kind %s: %w", m.Kind.String(), ErrMalformed)
	}
	if err != nil {
		n.record(&n.stats.Malformed, fmt.Errorf("gossip: sync from %s: %w", from, err))
		reply(nil, n.lastErr)
		return
	}
	for _, u := range m.Updates {
		n.receive(u)
	}
	resp, err := Message{Kind: KindSyncResp, Updates: n.view.All()}.Encode()
	if err != nil {
		reply(nil, fmt.Errorf("gossip: sync from %s: %w", from, err))
		return
	}
	reply(resp, nil)
}

// Leave marks this node StateLeft at its current incarnation, broadcasts a
// gossip datagram led by its leave entry to every live peer, stops all timers,
// and closes the node. Calling Leave more than once returns ErrClosed.
func (n *Node) Leave() error {
	if n.closed {
		return fmt.Errorf("gossip: leave: %w", ErrClosed)
	}
	n.self.State = StateLeft
	n.view.Apply(n.self)
	// Replace any still-queued StateAlive self entry before sending so leave
	// datagrams carry self once, as StateLeft.
	n.enqueue(n.self)
	for _, m := range n.view.Live() {
		n.sendWith(m.ID, Message{Kind: KindGossip}, []Member{n.self})
	}
	n.closed = true
	for _, t := range []env.Timer{n.tick, n.syncTimer, n.probe.timer} {
		if t != nil {
			t.Stop()
		}
	}
	for _, id := range n.view.IDs() {
		if l, ok := n.local[id]; ok && l.suspect != nil {
			l.suspect.Stop()
		}
	}
	return nil
}

// EncodeState returns a deterministic binary snapshot of the node's volatile
// state (self, view, dissemination queue, tombstones, relay count, seeds, and
// counters) for simulator state-digest comparison.
func (n *Node) EncodeState() []byte {
	b := []byte{Version}
	b = appendMember(b, n.self)
	b = append(b, boolByte(n.started), boolByte(n.closed))
	b = binary.BigEndian.AppendUint32(b, n.seq)
	all := n.view.All()
	b = binary.BigEndian.AppendUint32(b, uint32(len(all)))
	for _, m := range all {
		b = appendMember(b, m)
	}
	b = binary.BigEndian.AppendUint32(b, uint32(len(n.queue)))
	for _, q := range n.queue {
		b = appendMember(b, q.m)
		b = binary.AppendUvarint(b, uint64(q.transmits))
	}
	tombs := n.Tombstones()
	b = binary.BigEndian.AppendUint32(b, uint32(len(tombs)))
	for _, m := range tombs {
		b = appendMember(b, m)
	}
	b = binary.BigEndian.AppendUint32(b, uint32(len(n.relays)))
	b = binary.BigEndian.AppendUint32(b, uint32(len(n.seeds)))
	for _, s := range n.seeds {
		b = appendStr(b, string(s))
	}
	for _, v := range []uint64{
		n.stats.Sent, n.stats.SendErrors, n.stats.Received, n.stats.Malformed, n.stats.DroppedClosed,
		n.stats.Probes, n.stats.IndirectProbes, n.stats.Suspicions, n.stats.Refutations,
		n.stats.RefuteOverflows, n.stats.Syncs, n.stats.SyncErrors, n.stats.Purged, n.stats.Rejoins,
		n.stats.RelaysDropped, n.stats.ViewFull, n.stats.TombstonesEvicted, n.stats.Reinstated,
	} {
		b = binary.AppendUvarint(b, v)
	}
	return b
}

func appendMember(b []byte, m Member) []byte {
	b = append(b, byte(m.State))
	b = binary.BigEndian.AppendUint32(b, m.Incarnation)
	b = appendStr(b, string(m.ID))
	return appendStr(b, string(m.Region))
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}
