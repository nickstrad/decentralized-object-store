package gossip_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/gossip"
)

func exchange(t *testing.T, a *harness, aID env.NodeID, b *harness, bID env.NodeID) {
	t.Helper()
	if err := a.node.Join([]env.NodeID{bID}, nil); err != nil {
		t.Fatal(err)
	}
	for _, r := range a.net.takeRequests() {
		if r.to != bID {
			r.cb(nil, env.ErrTimeout)
			continue
		}
		replied := 0
		b.node.OnRequest(aID, r.req, func(resp []byte, err error) {
			replied++
			r.cb(resp, err)
		})
		if replied != 1 {
			t.Fatalf("%s answered %d times", bID, replied)
		}
	}
}

func drainRequests(hs ...*harness) {
	for _, h := range hs {
		for _, r := range h.net.takeRequests() {
			r.cb(nil, env.ErrTimeout)
		}
	}
}

func tombstone(n *gossip.Node, id env.NodeID) (gossip.Member, bool) {
	for _, m := range n.Tombstones() {
		if m.ID == id {
			return m, true
		}
	}
	return gossip.Member{}, false
}

func carries(t tester, ss []sent, id env.NodeID) bool {
	t.Helper()
	for _, m := range decodeAll(t, ss) {
		for _, u := range m.Updates {
			if u.ID == id {
				return true
			}
		}
	}
	return false
}

func syncReq(t tester, ms ...gossip.Member) []byte {
	t.Helper()
	b, err := gossip.Message{Kind: gossip.KindSyncReq, Updates: ms}.Encode()
	if err != nil {
		t.Fatalf("encode sync request: %v", err)
	}
	return b
}

// TestDeadEntryLifetime proves the full retention and tombstone lifecycle:
// staggered purges never resurrect a dead entry across syncs, stale live claims
// reinstate the buried verdict quietly (allowing a wiped node to refute above
// it), equal/older verdicts are dropped, higher incarnations start a new life,
// stranger verdicts are not re-gossiped, tombstones cap at MaxTombstones with
// oldest-first eviction and bounded FIFO backlog, and heal syncs contact dead
// tombstones but never left ones.
func TestDeadEntryLifetime(t *testing.T) {
	t.Run("two nodes that purge a dead member at different times never resurrect it in each other", func(t *testing.T) {
		cfg := gossip.Config{DeadRetention: 10 * time.Second}
		a, b := newHarness(t, "a", cfg), newHarness(t, "b", cfg)
		a.deliver("b", gossipMsg(mem("b", 0, gossip.StateAlive), mem("c", 0, gossip.StateAlive)))
		b.deliver("a", gossipMsg(mem("a", 0, gossip.StateAlive), mem("c", 0, gossip.StateAlive)))
		a.node.Start()
		b.node.Start()
		a.deliver("b", gossipMsg(mem("c", 0, gossip.StateDead)))
		step := func(d time.Duration) {
			a.clock.advance(d)
			b.clock.advance(d)
			drainRequests(a, b)
			a.net.take()
			b.net.take()
		}
		step(3 * time.Second)
		b.deliver("a", gossipMsg(mem("c", 0, gossip.StateDead)))
		step(8 * time.Second)
		if _, err := a.node.Lookup("c"); !errors.Is(err, gossip.ErrNotMember) {
			t.Fatalf("a did not purge c at 11s: %v", err)
		}
		exchange(t, a, "a", b, "b")
		exchange(t, b, "b", a, "a")
		if _, err := a.node.Lookup("c"); !errors.Is(err, gossip.ErrNotMember) {
			t.Fatalf("a took c back from b's retained entry: %v", err)
		}
		step(3 * time.Second)
		if _, err := b.node.Lookup("c"); !errors.Is(err, gossip.ErrNotMember) {
			t.Fatalf("b did not purge c at 14s: %v", err)
		}
		for range 50 {
			exchange(t, a, "a", b, "b")
			exchange(t, b, "b", a, "a")
			a.deliver("b", gossip.Message{Kind: gossip.KindPing, Seq: 1})
			b.deliver("a", gossip.Message{Kind: gossip.KindPing, Seq: 1})
			step(time.Second)
		}
		for _, h := range []*harness{a, b} {
			if m, err := h.node.Lookup("c"); err == nil {
				t.Fatalf("c came back as %+v", m)
			}
			if h.node.Stats().Purged != 1 {
				t.Fatalf("c purged %d times", h.node.Stats().Purged)
			}
			if m, ok := tombstone(h.node, "c"); !ok || m != mem("c", 0, gossip.StateDead) {
				t.Fatalf("tombstone of c: %+v %v", m, ok)
			}
		}
	})
	t.Run("a stale alive below a purged death brings back the death, never the stale entry", func(t *testing.T) {
		for _, c := range []struct {
			name    string
			history []gossip.Member
			replay  gossip.Member
			want    gossip.Member
		}{
			{"dead at 9, alive at 0 replayed", []gossip.Member{mem("x", 9, gossip.StateDead)}, mem("x", 0, gossip.StateAlive), mem("x", 9, gossip.StateDead)},
			{"alive then dead at 7, alive at 0 replayed", []gossip.Member{mem("x", 7, gossip.StateAlive), mem("x", 7, gossip.StateDead)}, mem("x", 0, gossip.StateAlive), mem("x", 7, gossip.StateDead)},
			{"dead at 7, alive at 7 replayed", []gossip.Member{mem("x", 7, gossip.StateDead)}, mem("x", 7, gossip.StateAlive), mem("x", 7, gossip.StateDead)},
			{"left at 3, suspect at 2 replayed", []gossip.Member{mem("x", 3, gossip.StateLeft)}, mem("x", 2, gossip.StateSuspect), mem("x", 3, gossip.StateLeft)},
		} {
			t.Run(c.name, func(t *testing.T) {
				h := newHarness(t, "self", gossip.Config{DeadRetention: time.Second})
				announce(h, "p01")
				h.node.Start()
				for _, u := range c.history {
					h.deliver("p01", gossipMsg(u))
				}
				h.clock.advance(2 * time.Second)
				if _, err := h.node.Lookup("x"); !errors.Is(err, gossip.ErrNotMember) {
					t.Fatalf("not purged: %v", err)
				}
				h.takeEvents()
				h.net.take()
				h.deliver("p01", gossipMsg(c.replay))
				got, err := h.node.Lookup("x")
				if err != nil || got != c.want {
					t.Fatalf("after replay Lookup = %+v %v, want %+v", got, err, c.want)
				}
				if ev := h.takeEvents(); len(ev) != 0 {
					t.Fatalf("reinstating the verdict emitted %+v", ev)
				}
				if _, ok := tombstone(h.node, "x"); ok {
					t.Fatal("x is both in the view and in the tombstones")
				}
				if h.node.Stats().Reinstated != 1 {
					t.Fatalf("Reinstated = %d", h.node.Stats().Reinstated)
				}
				h.deliver("p01", gossip.Message{Kind: gossip.KindPing, Seq: 5})
				if carries(t, h.net.take(), "x") {
					t.Fatal("a reinstated verdict was queued for gossip")
				}
				h.clock.advance(2 * time.Second)
				if _, err := h.node.Lookup("x"); !errors.Is(err, gossip.ErrNotMember) {
					t.Fatalf("reinstated verdict not purged again: %v", err)
				}
				if m, ok := tombstone(h.node, "x"); !ok || m != c.want {
					t.Fatalf("tombstone after second purge %+v %v", m, ok)
				}
			})
		}
	})
	t.Run("an equal or older verdict about a purged member is dropped", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{DeadRetention: time.Second})
		h.node.Start()
		h.deliver("p01", gossipMsg(mem("x", 7, gossip.StateDead)))
		h.clock.advance(2 * time.Second)
		for _, u := range []gossip.Member{mem("x", 7, gossip.StateDead), mem("x", 3, gossip.StateLeft), mem("x", 6, gossip.StateDead)} {
			h.deliver("p01", gossipMsg(u))
			if m, err := h.node.Lookup("x"); err == nil {
				t.Fatalf("%+v brought back %+v", u, m)
			}
		}
		if h.node.Stats().Reinstated != 0 {
			t.Fatalf("Reinstated = %d", h.node.Stats().Reinstated)
		}
	})
	t.Run("a higher incarnation after the purge is a new life", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{DeadRetention: time.Second})
		h.node.Start()
		h.deliver("p01", gossipMsg(mem("x", 7, gossip.StateDead)))
		h.clock.advance(2 * time.Second)
		h.takeEvents()
		h.deliver("p01", gossipMsg(mem("x", 8, gossip.StateAlive)))
		if m, err := h.node.Lookup("x"); err != nil || m != mem("x", 8, gossip.StateAlive) {
			t.Fatalf("Lookup = %+v %v", m, err)
		}
		if ev := h.takeEvents(); len(ev) != 1 || ev[0].Kind != gossip.EventJoined {
			t.Fatalf("events %+v", ev)
		}
		if _, ok := tombstone(h.node, "x"); ok {
			t.Fatal("tombstone kept after a new life")
		}
	})
	t.Run("a wiped node rejoining at incarnation 0 is told its purged death and refutes above it", func(t *testing.T) {
		seed := newHarness(t, "s", gossip.Config{DeadRetention: time.Second})
		seed.node.Start()
		seed.deliver("p01", gossipMsg(mem("w", 9, gossip.StateDead)))
		seed.clock.advance(2 * time.Second)
		drainRequests(seed)
		w := newHarness(t, "w", gossip.Config{})
		exchange(t, w, "w", seed, "s")
		if self := w.node.Self(); self.Incarnation != 10 || self.State != gossip.StateAlive {
			t.Fatalf("rejoined as %+v", self)
		}
		seed.takeEvents()
		w.deliver("s", gossip.Message{Kind: gossip.KindPing, Seq: 1})
		acks := w.net.take()
		for _, s := range acks {
			seed.node.OnDatagram("w", s.msg)
		}
		if m, err := seed.node.Lookup("w"); err != nil || m.State != gossip.StateAlive || m.Incarnation != 10 {
			t.Fatalf("seed holds %+v %v", m, err)
		}
		if ev := seed.takeEvents(); len(ev) != 1 || ev[0].Kind != gossip.EventJoined {
			t.Fatalf("seed events %+v", ev)
		}
	})
	t.Run("a death learned about an unknown member is kept but not gossiped", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		announce(h, "p01", "p02")
		h.deliver("p01", gossipMsg(mem("m", 3, gossip.StateDead), mem("n", 1, gossip.StateLeft)))
		if m, err := h.node.Lookup("m"); err != nil || m.State != gossip.StateDead {
			t.Fatalf("Lookup(m) = %+v %v", m, err)
		}
		h.net.take()
		for i := range 20 {
			h.deliver("p02", gossip.Message{Kind: gossip.KindPing, Seq: uint32(i)})
		}
		ss := h.net.take()
		if carries(t, ss, "m") || carries(t, ss, "n") {
			t.Fatal("a second-hand verdict about an unknown member was gossiped")
		}
		h.deliver("p01", gossipMsg(mem("p02", 0, gossip.StateDead)))
		h.net.take()
		h.deliver("p01", gossip.Message{Kind: gossip.KindPing, Seq: 99})
		if !carries(t, h.net.take(), "p02") {
			t.Fatal("the death of a known member was not gossiped")
		}
	})
	t.Run("the tombstones are capped and the oldest is evicted first", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{DeadRetention: time.Second})
		h.node.Start()
		var ms []gossip.Member
		for i := range gossip.MaxTombstones + 1 {
			ms = append(ms, mem(fmt.Sprintf("d%05d", i), 4, gossip.StateDead))
		}
		h.node.OnRequest("p01", syncReq(t, ms...), func([]byte, error) {})
		h.clock.advance(2 * time.Second)
		drainRequests(h)
		if n := len(h.node.Tombstones()); n != gossip.MaxTombstones {
			t.Fatalf("%d tombstones, cap %d", n, gossip.MaxTombstones)
		}
		if h.node.Stats().TombstonesEvicted != 1 {
			t.Fatalf("TombstonesEvicted = %d", h.node.Stats().TombstonesEvicted)
		}
		if _, ok := tombstone(h.node, "d00000"); ok {
			t.Fatal("the oldest tombstone was not the one evicted")
		}
		h.takeEvents()
		h.deliver("p01", gossipMsg(mem("d00000", 0, gossip.StateAlive), mem("d00001", 0, gossip.StateAlive)))
		if m, err := h.node.Lookup("d00000"); err != nil || m.State != gossip.StateAlive {
			t.Fatalf("an evicted ID is not accepted as new: %+v %v", m, err)
		}
		if m, err := h.node.Lookup("d00001"); err != nil || m.State != gossip.StateDead {
			t.Fatalf("a retained tombstone did not answer the stale alive: %+v %v", m, err)
		}
	})
	t.Run("one member buried and reinstated many times never grows the tombstone backlog past twice the cap", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{DeadRetention: time.Second})
		h.node.Start()
		h.deliver("p01", gossipMsg(mem("x", 5, gossip.StateDead)))
		for i := range 2*gossip.MaxTombstones + 10 {
			h.clock.advance(2 * time.Second)
			drainRequests(h)
			h.net.take()
			if _, ok := tombstone(h.node, "x"); !ok {
				t.Fatalf("cycle %d: x not buried", i)
			}
			h.deliver("p01", gossipMsg(mem("x", 1, gossip.StateAlive)))
			if n := gossip.TombBacklog(h.node); n > 2*gossip.MaxTombstones+1 {
				t.Fatalf("cycle %d: tombstone backlog %d", i, n)
			}
		}
		if n := len(h.node.Tombstones()); n != 0 {
			t.Fatalf("%d tombstones while x is reinstated", n)
		}
	})
	t.Run("the heal sync contacts a purged dead member but never a purged left one", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{DeadRetention: time.Second})
		announce(h, "p01")
		h.node.Start()
		h.deliver("p01", gossipMsg(mem("gone", 2, gossip.StateDead), mem("bye", 2, gossip.StateLeft)))
		h.clock.advance(2 * time.Second)
		drainRequests(h)
		contacted := map[env.NodeID]int{}
		for range 40 {
			h.clock.advance(gossip.DefaultSyncInterval)
			for _, r := range h.net.takeRequests() {
				contacted[r.to]++
				r.cb(nil, env.ErrTimeout)
			}
		}
		if contacted["gone"] == 0 || contacted["bye"] != 0 {
			t.Fatalf("contacted %v", contacted)
		}
	})
}

// TestJoinSeedsAreRemembered proves that seeds passed to Join are retried while
// alone and used by heal syncs, remembered seeds are capped at MaxSeeds, and a
// self-only seed list triggers no join retries.
func TestJoinSeedsAreRemembered(t *testing.T) {
	t.Run("seeds given to Join are retried while alone and contacted by the heal sync", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		h.node.Start()
		if err := h.node.Join([]env.NodeID{"s1"}, nil); err != nil {
			t.Fatal(err)
		}
		drainRequests(h)
		alone := 0
		for range 5 {
			h.clock.advance(gossip.DefaultProbeInterval)
			for _, r := range h.net.takeRequests() {
				if r.to == "s1" {
					alone++
				}
				r.cb(nil, env.ErrTimeout)
			}
		}
		if alone < 4 {
			t.Fatalf("join seed retried %d times in 5 lonely periods", alone)
		}
		announce(h, "p01")
		healed := 0
		for range 40 {
			h.clock.advance(gossip.DefaultSyncInterval)
			for _, r := range h.net.takeRequests() {
				if r.to == "s1" {
					healed++
				}
				r.cb(nil, env.ErrTimeout)
			}
		}
		if healed == 0 {
			t.Fatal("the heal sync never contacted the join seed")
		}
	})
	t.Run("remembered seeds are capped at MaxSeeds and a longer configured list is refused", func(t *testing.T) {
		var many []env.NodeID
		for i := range gossip.MaxSeeds + 10 {
			many = append(many, env.NodeID(fmt.Sprintf("s%03d", i)))
		}
		h := newHarness(t, "self", gossip.Config{})
		if err := h.node.Join(append(many, many[0], "self"), nil); err != nil {
			t.Fatal(err)
		}
		if n := len(h.net.takeRequests()); n != gossip.MaxSeeds+10 {
			t.Fatalf("join contacted %d seeds", n)
		}
		if n := gossip.SeedCount(h.node); n != gossip.MaxSeeds {
			t.Fatalf("%d seeds remembered", n)
		}
		if err := (gossip.Config{Seeds: many}).WithDefaults().Validate(); !errors.Is(err, env.ErrInvalidArgument) {
			t.Fatalf("%d configured seeds: %v", len(many), err)
		}
	})
	t.Run("a node whose only seed is itself never retries a join", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{Seeds: []env.NodeID{"self"}})
		h.node.Start()
		h.clock.advance(5 * gossip.DefaultProbeInterval)
		if n := h.node.Stats().Rejoins; n != 0 {
			t.Fatalf("Rejoins = %d", n)
		}
		if r := h.net.takeRequests(); len(r) != 0 {
			t.Fatalf("requests %+v", r)
		}
	})
}

// TestRelayBounds proves that ping-req floods stay capped at MaxRelays with
// excess counted in RelaysDropped, expired relays are pruned on arrival without
// a tick, answered relays keep the expiry FIFO bounded at 2*MaxRelays, and
// relays whose outbound ping fails are not retained.
func TestRelayBounds(t *testing.T) {
	const flood = 10_000
	t.Run("a ping-req flood keeps at most MaxRelays relays and counts the rest", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		for i := range flood {
			h.deliver("p01", gossip.Message{Kind: gossip.KindPingReq, Seq: uint32(i), Target: "victim"})
		}
		ss := h.net.take()
		if len(ss) != gossip.MaxRelays {
			t.Fatalf("%d pings relayed, cap %d", len(ss), gossip.MaxRelays)
		}
		if n := gossip.RelayCount(h.node); n != gossip.MaxRelays {
			t.Fatalf("%d relays held", n)
		}
		if n := h.node.Stats().RelaysDropped; n != flood-gossip.MaxRelays {
			t.Fatalf("RelaysDropped = %d", n)
		}
		for i := range flood {
			h.deliver("victim", gossip.Message{Kind: gossip.KindAck, Seq: uint32(1<<31 + i)})
		}
		if len(h.net.sent) != 0 {
			t.Fatal("an unknown ack was forwarded")
		}
		for _, m := range decodeAll(t, ss) {
			h.deliver("victim", gossip.Message{Kind: gossip.KindAck, Seq: m.Seq})
		}
		if n := len(h.net.take()); n != gossip.MaxRelays {
			t.Fatalf("%d acks forwarded", n)
		}
		if n := gossip.RelayCount(h.node); n != 0 {
			t.Fatalf("%d relays left after every ack", n)
		}
	})
	t.Run("expired relays are pruned when a ping-req arrives, without a tick", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		for i := range gossip.MaxRelays {
			h.deliver("p01", gossip.Message{Kind: gossip.KindPingReq, Seq: uint32(i), Target: "victim"})
		}
		h.clock.advance(gossip.DefaultProbeInterval + time.Millisecond)
		h.net.take()
		h.deliver("p01", gossip.Message{Kind: gossip.KindPingReq, Seq: 7, Target: "victim"})
		if n := len(h.net.take()); n != 1 {
			t.Fatalf("%d pings after the old relays expired", n)
		}
		if n := gossip.RelayCount(h.node); n != 1 {
			t.Fatalf("%d relays held", n)
		}
	})
	t.Run("relays answered at once never let the expiry list grow past twice the cap", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		for i := range 3 * gossip.MaxRelays {
			h.deliver("p01", gossip.Message{Kind: gossip.KindPingReq, Seq: uint32(i), Target: "victim"})
			ms := decodeAll(t, h.net.take())
			h.deliver("victim", gossip.Message{Kind: gossip.KindAck, Seq: ms[0].Seq})
			h.net.take()
			if n := gossip.RelayBacklog(h.node); n > 2*gossip.MaxRelays+1 {
				t.Fatalf("expiry list holds %d entries after %d relays", n, i+1)
			}
		}
		if h.node.Stats().RelaysDropped != 0 || gossip.RelayCount(h.node) != 0 {
			t.Fatalf("dropped %d, held %d", h.node.Stats().RelaysDropped, gossip.RelayCount(h.node))
		}
	})
	t.Run("a relay whose ping could not be sent is not kept", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		h.net.sendErr = env.ErrUnreachable
		h.deliver("p01", gossip.Message{Kind: gossip.KindPingReq, Seq: 3, Target: "victim"})
		if n := gossip.RelayCount(h.node); n != 0 {
			t.Fatalf("%d relays kept for an unsent ping", n)
		}
	})
}

// TestViewBounds proves that Config.MaxMembers caps view growth (recording
// ErrViewFull while allowing known members to transition and purges to free
// room), a view filled to MaxUpdates still encodes valid syncs and datagrams,
// and out-of-range MaxMembers values are rejected.
func TestViewBounds(t *testing.T) {
	t.Run("a sync request cannot grow the view past MaxMembers", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{MaxMembers: 10, DeadRetention: time.Second})
		h.node.Start()
		var ms []gossip.Member
		for i := range 20 {
			ms = append(ms, mem(fmt.Sprintf("m%02d", i), 0, gossip.StateAlive))
		}
		var resp []byte
		var rerr error
		h.node.OnRequest("m00", syncReq(t, ms...), func(b []byte, err error) { resp, rerr = b, err })
		if rerr != nil {
			t.Fatal(rerr)
		}
		if n := len(h.node.All()); n != 10 {
			t.Fatalf("view holds %d members, cap 10", n)
		}
		if h.node.Stats().ViewFull != 11 || !errors.Is(h.node.LastError(), gossip.ErrViewFull) {
			t.Fatalf("ViewFull = %d, last %v", h.node.Stats().ViewFull, h.node.LastError())
		}
		if m, err := gossip.Decode(resp); err != nil || len(m.Updates) != 10 {
			t.Fatalf("reply %+v %v", m, err)
		}
		h.deliver("m00", gossipMsg(mem("m01", 0, gossip.StateDead)))
		if m, _ := h.node.Lookup("m01"); m.State != gossip.StateDead {
			t.Fatalf("a known member could not change while the view was full: %+v", m)
		}
		h.clock.advance(2 * time.Second)
		drainRequests(h)
		h.deliver("m00", gossipMsg(mem("m15", 0, gossip.StateAlive)))
		if _, err := h.node.Lookup("m15"); err != nil {
			t.Fatalf("a purge did not free room: %v", err)
		}
	})
	t.Run("a view filled to MaxUpdates still encodes as a sync and its packets stay decodable", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{MaxPacket: gossip.MaxPacketLimit})
		ms := make([]gossip.Member, 0, gossip.MaxUpdates)
		for i := range gossip.MaxUpdates {
			ms = append(ms, gossip.Member{ID: env.NodeID(fmt.Sprintf("%05d", i)), Incarnation: 0, State: gossip.StateAlive})
		}
		var rerr error
		replied := false
		h.node.OnRequest("00000", syncReq(t, ms...), func(_ []byte, err error) { replied, rerr = true, err })
		if !replied || rerr != nil {
			t.Fatalf("reply %v %v", replied, rerr)
		}
		if n := len(h.node.All()); n != gossip.MaxUpdates {
			t.Fatalf("view holds %d, want %d", n, gossip.MaxUpdates)
		}
		if err := h.node.Join([]env.NodeID{"00001"}, nil); err != nil {
			t.Fatal(err)
		}
		reqs := h.net.takeRequests()
		if len(reqs) != 1 {
			t.Fatalf("%d join requests", len(reqs))
		}
		if m, err := gossip.Decode(reqs[0].req); err != nil || len(m.Updates) != gossip.MaxUpdates {
			t.Fatalf("join request does not decode: %v", err)
		}
		h.deliver("00002", gossip.Message{Kind: gossip.KindPing, Seq: 1})
		for _, m := range decodeAll(t, h.net.take()) {
			if len(m.Updates) > gossip.MaxUpdates {
				t.Fatalf("%d updates in one packet", len(m.Updates))
			}
		}
	})
	t.Run("MaxMembers outside 1..MaxUpdates is refused", func(t *testing.T) {
		for _, v := range []int{-1, gossip.MaxUpdates + 1} {
			if err := (gossip.Config{MaxMembers: v}).WithDefaults().Validate(); !errors.Is(err, env.ErrInvalidArgument) {
				t.Fatalf("MaxMembers %d: %v", v, err)
			}
		}
		if got := (gossip.Config{}).WithDefaults().MaxMembers; got != gossip.MaxUpdates {
			t.Fatalf("default MaxMembers %d", got)
		}
	})
}

// TestCallbacksNeverRunInsideTheRegisteringCall proves that Join and Subscribe
// never invoke their callbacks synchronously and that Join callbacks report
// ErrClosed (merging nothing) if Leave happens before completion.
func TestCallbacksNeverRunInsideTheRegisteringCall(t *testing.T) {
	t.Run("Join with no other seed reports later, not inside Join", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		called := 0
		if err := h.node.Join([]env.NodeID{"self"}, func(error) { called++ }); err != nil {
			t.Fatal(err)
		}
		if called != 0 {
			t.Fatal("done ran inside Join")
		}
		h.clock.advance(0)
		if called != 1 {
			t.Fatalf("done called %d times", called)
		}
	})
	t.Run("a Join pending when the node leaves reports ErrClosed once", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		var got []error
		if err := h.node.Join(nil, func(err error) { got = append(got, err) }); err != nil {
			t.Fatal(err)
		}
		if err := h.node.Leave(); err != nil {
			t.Fatal(err)
		}
		h.clock.advance(time.Second)
		if len(got) != 1 || !errors.Is(got[0], gossip.ErrClosed) {
			t.Fatalf("done got %v", got)
		}
	})
	t.Run("a Join to remote seeds reports only from the transport callback", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		called := 0
		if err := h.node.Join([]env.NodeID{"s1", "s2"}, func(error) { called++ }); err != nil {
			t.Fatal(err)
		}
		if called != 0 {
			t.Fatal("done ran inside Join")
		}
		drainRequests(h)
		if called != 1 {
			t.Fatalf("done called %d times", called)
		}
	})
	t.Run("a seed reply arriving after Leave reports ErrClosed and merges nothing", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		var got []error
		if err := h.node.Join([]env.NodeID{"s1"}, func(err error) { got = append(got, err) }); err != nil {
			t.Fatal(err)
		}
		reqs := h.net.takeRequests()
		if err := h.node.Leave(); err != nil {
			t.Fatal(err)
		}
		b, err := gossip.Message{Kind: gossip.KindSyncResp, Updates: []gossip.Member{mem("s1", 0, gossip.StateAlive)}}.Encode()
		if err != nil {
			t.Fatal(err)
		}
		reqs[0].cb(b, nil)
		if len(got) != 1 || !errors.Is(got[0], gossip.ErrJoinFailed) || !errors.Is(got[0], gossip.ErrClosed) {
			t.Fatalf("done got %v", got)
		}
		if _, err := h.node.Lookup("s1"); !errors.Is(err, gossip.ErrNotMember) {
			t.Fatalf("a reply after Leave was merged: %v", err)
		}
	})
	t.Run("Join after Leave returns ErrClosed and never calls done", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		if err := h.node.Leave(); err != nil {
			t.Fatal(err)
		}
		called := 0
		if err := h.node.Join([]env.NodeID{"s1"}, func(error) { called++ }); !errors.Is(err, gossip.ErrClosed) {
			t.Fatalf("Join after Leave: %v", err)
		}
		h.clock.advance(time.Second)
		drainRequests(h)
		if called != 0 {
			t.Fatalf("done called %d times", called)
		}
	})
	t.Run("Subscribe does not call the subscriber", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		announce(h, "p01")
		called := 0
		h.node.Subscribe(func(gossip.Event) { called++ })
		if called != 0 {
			t.Fatal("Subscribe replayed an event")
		}
	})
}

// TestRepairNits proves that a region mismatch at equal incarnation is refuted,
// leave packets carry self once as StateLeft, and every gossip wire code lies
// within the reserved 80..95 block.
func TestRepairNits(t *testing.T) {
	t.Run("a peer holding this node alive at its incarnation in another region is refuted", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		h.deliver("p01", gossipMsg(gossip.Member{ID: "self", Region: "r1", Incarnation: 0, State: gossip.StateAlive}))
		if self := h.node.Self(); self.Incarnation != 1 || self.Region != "r0" {
			t.Fatalf("self %+v", self)
		}
		h.deliver("p01", gossipMsg(gossip.Member{ID: "self", Region: "r0", Incarnation: 1, State: gossip.StateAlive}))
		if self := h.node.Self(); self.Incarnation != 1 {
			t.Fatalf("own entry refuted: %+v", self)
		}
	})
	t.Run("leave packets carry this node's entry once, as left", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		announce(h, peerIDs(3)...)
		h.node.Start()
		h.net.take()
		drainRequests(h)
		if err := h.node.Leave(); err != nil {
			t.Fatal(err)
		}
		ms := decodeAll(t, h.net.take())
		if len(ms) != 3 {
			t.Fatalf("%d leave packets", len(ms))
		}
		for _, m := range ms {
			var selfs []gossip.Member
			for _, u := range m.Updates {
				if u.ID == "self" {
					selfs = append(selfs, u)
				}
			}
			if len(selfs) != 1 || selfs[0].State != gossip.StateLeft {
				t.Fatalf("leave packet carries %+v", selfs)
			}
		}
	})
	t.Run("every gossip wire code lies in the package's block 80 to 95", func(t *testing.T) {
		for _, e := range gossip.Codes() {
			if e.Code < 80 || e.Code > 95 {
				t.Fatalf("code %d for %v is outside 80..95", e.Code, e.Err)
			}
		}
		if !slices.ContainsFunc(gossip.Codes(), func(e env.CodeEntry) bool { return e.Err == gossip.ErrViewFull }) {
			t.Fatal("ErrViewFull has no code")
		}
	})
}
