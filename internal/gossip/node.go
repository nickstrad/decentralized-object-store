package gossip

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type EventKind uint8

const (
	EventJoined EventKind = iota + 1
	EventSuspected
	EventRecovered
	EventDied
	EventLeft
)

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

type Event struct {
	Kind   EventKind
	Member Member
	At     env.Time
}

type Membership interface {
	Members() []Member
	Lookup(id env.NodeID) (Member, error)
	Subscribe(fn func(Event))
}

type Stats struct {
	Sent            uint64
	SendErrors      uint64
	Received        uint64
	Malformed       uint64
	DroppedClosed   uint64
	Probes          uint64
	IndirectProbes  uint64
	Suspicions      uint64
	Refutations     uint64
	RefuteOverflows uint64
	Syncs           uint64
	SyncErrors      uint64
	Purged          uint64
	Rejoins         uint64
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
	seq       uint32
	origin    env.NodeID
	originSeq uint32
	expires   env.Time
}

type probeState struct {
	active bool
	acked  bool
	target env.NodeID
	seq    uint32
	timer  env.Timer
}

type Node struct {
	e     env.Env
	cfg   Config
	self  Member
	view  *View
	local map[env.NodeID]*memberLocal

	queue  []queued
	qorder uint64

	nextPurge env.Time

	probe    probeState
	rotation []env.NodeID
	rotIdx   int
	seq      uint32
	relays   []relay

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
		e:     e,
		cfg:   cfg,
		self:  Member{ID: e.ID, Region: e.Region, State: StateAlive},
		view:  NewView(),
		local: map[env.NodeID]*memberLocal{},
	}
	n.view.Apply(n.self)
	return n, nil
}

func (n *Node) Start() {
	if n.started || n.closed {
		return
	}
	n.started = true
	n.enqueue(n.self)
	n.tick = n.e.Clock.AfterFunc(randDuration(n, n.cfg.ProbeInterval), n.onTick)
	n.syncTimer = n.e.Clock.AfterFunc(n.cfg.SyncInterval/2+randDuration(n, n.cfg.SyncInterval), n.onSync)
	if len(n.cfg.Seeds) > 0 {
		n.join(n.cfg.Seeds, nil)
	}
}

func randDuration(n *Node, d time.Duration) time.Duration {
	return time.Duration(n.e.Rand.Int64N(int64(d)))
}

func (n *Node) Self() Member { return n.self }

func (n *Node) Members() []Member { return n.view.Live() }

func (n *Node) All() []Member { return n.view.All() }

func (n *Node) LiveCount() int { return n.view.LiveCount() }

func (n *Node) Lookup(id env.NodeID) (Member, error) {
	m, ok := n.view.Get(id)
	if !ok {
		return Member{}, fmt.Errorf("gossip: lookup %s: %w", id, ErrNotMember)
	}
	return m, nil
}

func (n *Node) Subscribe(fn func(Event)) {
	if fn != nil {
		n.subs = append(n.subs, fn)
	}
}

func (n *Node) Stats() Stats { return n.stats }

func (n *Node) LastError() error { return n.lastErr }

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
	m, err := Decode(msg)
	if err != nil {
		n.record(&n.stats.Malformed, fmt.Errorf("gossip: datagram from %s: %w", from, err))
		return
	}
	if !m.Kind.datagram() {
		n.record(&n.stats.Malformed, fmt.Errorf("gossip: datagram from %s: %s is not a datagram kind: %w", from, m.Kind.String(), ErrMalformed))
		return
	}
	for _, u := range m.Updates {
		n.receive(u, "")
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

func (n *Node) onPingReq(from env.NodeID, m Message) {
	if m.Target == n.self.ID {
		n.send(from, Message{Kind: KindAck, Seq: m.Seq})
		return
	}
	n.seq++
	n.relays = append(n.relays, relay{seq: n.seq, origin: from, originSeq: m.Seq, expires: n.e.Clock.Now().Add(n.cfg.ProbeInterval)})
	n.send(m.Target, Message{Kind: KindPing, Seq: n.seq})
}

func (n *Node) onAck(seq uint32) {
	if n.probe.active && !n.probe.acked && n.probe.seq == seq {
		n.probe.acked = true
		if n.probe.timer != nil {
			n.probe.timer.Stop()
		}
		return
	}
	for i, r := range n.relays {
		if r.seq != seq {
			continue
		}
		n.relays = slices.Delete(n.relays, i, i+1)
		if n.e.Clock.Now() <= r.expires {
			n.send(r.origin, Message{Kind: KindAck, Seq: r.originSeq})
		}
		return
	}
}

func (n *Node) receive(u Member, syncPeer env.NodeID) {
	if u.ID == n.self.ID {
		n.refute(u)
		return
	}
	n.apply(u, syncPeer != "" && u.ID != syncPeer)
}

func (n *Node) refute(u Member) {
	if n.closed {
		return
	}
	if u.Incarnation < n.self.Incarnation || (u.Incarnation == n.self.Incarnation && u.State == StateAlive) {
		return
	}
	if u.Incarnation == math.MaxUint32 {
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

func (n *Node) apply(u Member, quietIfNew bool) bool {
	cur, had := n.view.Get(u.ID)
	if !n.view.Apply(u) {
		return false
	}
	l := n.localOf(u.ID)
	l.changed = n.e.Clock.Now()
	if !u.State.Live() {
		n.nextPurge = min(n.nextPurge, l.changed.Add(n.cfg.DeadRetention))
	}
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
	if had || !quietIfNew {
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
		n.apply(m, false)
	})
}

func (n *Node) addToRotation(id env.NodeID) {
	if slices.Contains(n.rotation[n.rotIdx:], id) {
		return
	}
	at := n.rotIdx + n.e.Rand.IntN(len(n.rotation)-n.rotIdx+1)
	n.rotation = slices.Insert(n.rotation, at, id)
}

func (n *Node) nextTarget() (env.NodeID, bool) {
	for range 2 {
		for n.rotIdx < len(n.rotation) {
			id := n.rotation[n.rotIdx]
			n.rotIdx++
			if m, ok := n.view.Get(id); ok && m.State.Live() && id != n.self.ID {
				return id, true
			}
		}
		n.rotation = n.rotation[:0]
		for _, m := range n.view.Live() {
			if m.ID != n.self.ID {
				n.rotation = append(n.rotation, m.ID)
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
	n.relays = slices.DeleteFunc(n.relays, func(r relay) bool { return now > r.expires })
	n.purge(now)
	if n.view.LiveCount() <= 1 && len(n.cfg.Seeds) > 0 && n.joins == 0 {
		n.stats.Rejoins++
		n.join(n.cfg.Seeds, nil)
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
	if n.apply(m, false) {
		n.stats.Suspicions++
	}
}

func (n *Node) onProbeTimeout(seq uint32) {
	if n.closed || !n.probe.active || n.probe.acked || n.probe.seq != seq || n.cfg.noIndirect {
		return
	}
	var cands []env.NodeID
	for _, m := range n.view.Live() {
		if m.ID != n.self.ID && m.ID != n.probe.target {
			cands = append(cands, m.ID)
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
	var expired []env.NodeID
	next := env.Time(math.MaxInt64)
	for _, id := range n.view.ids {
		m := n.view.byID[id]
		if id == n.self.ID || m.State.Live() {
			continue
		}
		due := n.localOf(id).changed.Add(n.cfg.DeadRetention)
		if now < due {
			next = min(next, due)
			continue
		}
		expired = append(expired, id)
	}
	n.nextPurge = next
	for _, id := range expired {
		n.view.Remove(id)
		delete(n.local, id)
		n.queue = slices.DeleteFunc(n.queue, func(q queued) bool { return q.m.ID == id })
		n.stats.Purged++
	}
}

func (n *Node) enqueue(m Member) {
	n.queue = slices.DeleteFunc(n.queue, func(q queued) bool { return q.m.ID == m.ID })
	n.qorder++
	n.queue = append(n.queue, queued{m: m, order: n.qorder})
}

func (n *Node) piggyback(to env.NodeID, size int, lead []Member) []Member {
	budget := n.cfg.MaxPacket - size
	out := make([]Member, 0, min(len(n.queue)+len(lead)+1, budget/minUpdateSize+1))
	var first []Member
	take := func(m Member) bool {
		s := UpdateSize(m)
		if s > budget {
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
	slices.SortFunc(n.queue, func(a, b queued) int {
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
	for i := range n.queue {
		q := &n.queue[i]
		if len(first) > 0 && slices.Contains(first, q.m) {
			q.transmits++
			continue
		}
		if take(q.m) {
			q.transmits++
		}
	}
	n.queue = slices.DeleteFunc(n.queue, func(q queued) bool { return q.transmits >= limit })
	return out
}

func (n *Node) send(to env.NodeID, m Message) {
	n.sendWith(to, m, nil)
}

func (n *Node) sendWith(to env.NodeID, m Message, lead []Member) {
	m.Updates = n.piggyback(to, m.Size(), lead)
	b, err := m.Encode()
	if err != nil {
		n.record(&n.stats.SendErrors, fmt.Errorf("gossip: send %s to %s: %w", m.Kind.String(), to, err))
		return
	}
	if err := n.e.Net.Send(to, b); err != nil {
		n.record(&n.stats.SendErrors, fmt.Errorf("gossip: send %s to %s: %w", m.Kind.String(), to, err))
		return
	}
	n.stats.Sent++
}

func (n *Node) Join(seeds []env.NodeID, done func(error)) error {
	if n.closed {
		return fmt.Errorf("gossip: join: %w", ErrClosed)
	}
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
		n.e.Clock.AfterFunc(0, func() { done(nil) })
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
		cb(n.lastErr)
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
			n.receive(u, peer)
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
	for _, id := range n.view.IDs() {
		m, _ := n.view.Get(id)
		switch {
		case id == n.self.ID:
		case m.State.Live():
			live = append(live, id)
		case m.State == StateDead:
			heal = append(heal, id)
		}
	}
	for _, s := range n.cfg.Seeds {
		if m, ok := n.view.Get(s); s != n.self.ID && (!ok || !m.State.Live()) {
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
		n.receive(u, from)
	}
	resp, err := Message{Kind: KindSyncResp, Updates: n.view.All()}.Encode()
	if err != nil {
		reply(nil, fmt.Errorf("gossip: sync from %s: %w", from, err))
		return
	}
	reply(resp, nil)
}

func (n *Node) Leave() error {
	if n.closed {
		return fmt.Errorf("gossip: leave: %w", ErrClosed)
	}
	n.self.State = StateLeft
	n.view.Apply(n.self)
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
	for _, v := range []uint64{
		n.stats.Sent, n.stats.SendErrors, n.stats.Received, n.stats.Malformed, n.stats.DroppedClosed,
		n.stats.Probes, n.stats.IndirectProbes, n.stats.Suspicions, n.stats.Refutations,
		n.stats.RefuteOverflows, n.stats.Syncs, n.stats.SyncErrors, n.stats.Purged, n.stats.Rejoins,
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
