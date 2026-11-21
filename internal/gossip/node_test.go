package gossip_test

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/gossip"
)

func peerIDs(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("p%02d", i+1)
	}
	return out
}

func announce(h *harness, ids ...string) {
	var ups []gossip.Member
	for _, id := range ids {
		ups = append(ups, mem(id, 0, gossip.StateAlive))
	}
	h.deliver(env.NodeID(ids[0]), gossipMsg(ups...))
}

func pings(t tester, ss []sent) []sent {
	t.Helper()
	var out []sent
	for i, m := range decodeAll(t, ss) {
		if m.Kind == gossip.KindPing {
			out = append(out, ss[i])
		}
	}
	return out
}

func probeRound(t *testing.T, h *harness, n int) []env.NodeID {
	t.Helper()
	var targets []env.NodeID
	for len(targets) < n {
		h.clock.advance(gossip.DefaultProbeInterval)
		for _, p := range pings(t, h.net.take()) {
			m, _ := gossip.Decode(p.msg)
			targets = append(targets, p.to)
			h.deliver(p.to, gossip.Message{Kind: gossip.KindAck, Seq: m.Seq})
		}
	}
	return targets
}

func TestProbeTargetRotation(t *testing.T) {
	t.Run("each live member is probed exactly once per round and rounds are reshuffled", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		ids := peerIDs(9)
		announce(h, ids...)
		h.node.Start()
		var rounds [][]env.NodeID
		for range 4 {
			r := probeRound(t, h, len(ids))
			sorted := slices.Clone(r)
			slices.Sort(sorted)
			var want []env.NodeID
			for _, id := range ids {
				want = append(want, env.NodeID(id))
			}
			if !slices.Equal(sorted, want) {
				t.Fatalf("round %v is not a permutation of the members", r)
			}
			rounds = append(rounds, r)
		}
		same := true
		for _, r := range rounds[1:] {
			if !slices.Equal(r, rounds[0]) {
				same = false
			}
		}
		if same {
			t.Fatalf("four rounds in the same order %v: no shuffle", rounds[0])
		}
		if h.node.Stats().Probes < 36 {
			t.Fatalf("Probes = %d, want >= 36", h.node.Stats().Probes)
		}
	})
	t.Run("a member declared dead is never probed again", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		ids := peerIDs(5)
		announce(h, ids...)
		h.node.Start()
		probeRound(t, h, 2)
		h.deliver("p01", gossipMsg(mem("p03", 0, gossip.StateDead)))
		for _, id := range probeRound(t, h, 12) {
			if id == "p03" {
				t.Fatal("dead member p03 was probed")
			}
		}
	})
	t.Run("a member that joins mid-round is probed within the next two rounds", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		announce(h, peerIDs(6)...)
		h.node.Start()
		probeRound(t, h, 3)
		h.deliver("p01", gossipMsg(mem("new", 0, gossip.StateAlive)))
		if !slices.Contains(probeRound(t, h, 14), "new") {
			t.Fatal("the new member was not probed")
		}
	})
	t.Run("a node alone sends no probes", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		h.node.Start()
		h.clock.advance(5 * gossip.DefaultProbeInterval)
		if p := pings(t, h.net.take()); len(p) != 0 {
			t.Fatalf("lonely node sent %d pings", len(p))
		}
	})
}

func TestProbeAndIndirectProbe(t *testing.T) {
	setup := func(t *testing.T, cfg gossip.Config) (*harness, env.NodeID, uint32) {
		h := newHarness(t, "self", cfg)
		announce(h, peerIDs(5)...)
		h.node.Start()
		for {
			h.clock.advance(10 * time.Millisecond)
			if p := pings(t, h.net.take()); len(p) == 1 {
				m, _ := gossip.Decode(p[0].msg)
				return h, p[0].to, m.Seq
			}
		}
	}
	t.Run("a ping is answered with an ack carrying the same sequence number", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		h.deliver("p01", gossip.Message{Kind: gossip.KindPing, Seq: 5})
		ms := decodeAll(t, h.net.sent)
		if len(ms) != 1 || ms[0].Kind != gossip.KindAck || ms[0].Seq != 5 || h.net.sent[0].to != "p01" {
			t.Fatalf("reply: %+v", ms)
		}
	})
	t.Run("an unanswered ping triggers ping-req through k other members", func(t *testing.T) {
		h, target, seq := setup(t, gossip.Config{})
		h.clock.advance(gossip.DefaultProbeTimeout)
		var via []env.NodeID
		for i, m := range decodeAll(t, h.net.sent) {
			if m.Kind != gossip.KindPingReq {
				continue
			}
			if m.Target != target || m.Seq != seq {
				t.Fatalf("ping-req %+v, want target %s seq %d", m, target, seq)
			}
			via = append(via, h.net.sent[i].to)
		}
		slices.Sort(via)
		if len(via) != gossip.DefaultIndirectProbes || slices.Contains(via, target) || len(slices.Compact(via)) != gossip.DefaultIndirectProbes {
			t.Fatalf("intermediaries %v for target %s", via, target)
		}
		if h.node.Stats().IndirectProbes != 1 {
			t.Fatalf("IndirectProbes = %d", h.node.Stats().IndirectProbes)
		}
	})
	t.Run("an indirect ack saves the target from suspicion", func(t *testing.T) {
		h, target, seq := setup(t, gossip.Config{})
		h.clock.advance(gossip.DefaultProbeTimeout)
		h.deliver("p05", gossip.Message{Kind: gossip.KindAck, Seq: seq})
		h.clock.advance(gossip.DefaultProbeInterval)
		if m, _ := lookupState(t, h.node, string(target)); m.State != gossip.StateAlive {
			t.Fatalf("target %s is %s after an indirect ack", target, m.State)
		}
	})
	t.Run("no ack at all makes the target suspect, and dead after the suspicion timeout", func(t *testing.T) {
		h, target, _ := setup(t, gossip.Config{})
		h.takeEvents()
		h.clock.advance(gossip.DefaultProbeInterval)
		if m, _ := lookupState(t, h.node, string(target)); m.State != gossip.StateSuspect {
			t.Fatalf("target is %s, want suspect", m.State)
		}
		evs := h.takeEvents()
		if len(evs) != 1 || evs[0].Kind != gossip.EventSuspected || evs[0].Member.ID != target {
			t.Fatalf("events %+v", evs)
		}
		timeout := gossip.Config{}.WithDefaults().SuspicionTimeout(6)
		h.clock.advance(timeout - gossip.DefaultProbeInterval)
		if m, _ := lookupState(t, h.node, string(target)); m.State != gossip.StateSuspect {
			t.Fatalf("target is %s before the timeout", m.State)
		}
		h.clock.advance(gossip.DefaultProbeInterval)
		if m, _ := lookupState(t, h.node, string(target)); m.State != gossip.StateDead {
			t.Fatalf("target is %s after the timeout, want dead", m.State)
		}
		var died bool
		for _, ev := range h.takeEvents() {
			if ev.Kind == gossip.EventDied && ev.Member.ID == target {
				died = true
			}
		}
		if !died {
			t.Fatal("no died event")
		}
	})
	t.Run("a refutation during the suspicion timeout cancels the death", func(t *testing.T) {
		h, target, _ := setup(t, gossip.Config{})
		h.clock.advance(gossip.DefaultProbeInterval)
		h.deliver(target, gossipMsg(mem(string(target), 1, gossip.StateAlive)))
		h.takeEvents()
		h.clock.advance(gossip.Config{}.WithDefaults().SuspicionTimeout(6) * 2)
		for _, ev := range h.takeEvents() {
			if ev.Kind == gossip.EventDied && ev.Member.ID == target && ev.Member.Incarnation == 0 {
				t.Fatal("the suspicion at incarnation 0 killed the member after it refuted")
			}
		}
	})
	t.Run("the indirect stage can be switched off only by the test switch", func(t *testing.T) {
		cfg := gossip.WithoutIndirectProbes(gossip.Config{})
		h, _, _ := setup(t, cfg)
		h.clock.advance(gossip.DefaultProbeTimeout)
		for _, m := range decodeAll(t, h.net.sent) {
			if m.Kind == gossip.KindPingReq {
				t.Fatal("ping-req sent with indirect probes off")
			}
		}
	})
	t.Run("a ping-req is relayed and the target's ack forwarded with the requester's sequence number", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		announce(h, peerIDs(3)...)
		h.deliver("p01", gossip.Message{Kind: gossip.KindPingReq, Seq: 77, Target: "p03"})
		ss := h.net.take()
		ms := decodeAll(t, ss)
		if len(ms) != 1 || ms[0].Kind != gossip.KindPing || ss[0].to != "p03" {
			t.Fatalf("relay sent %+v", ms)
		}
		h.deliver("p03", gossip.Message{Kind: gossip.KindAck, Seq: ms[0].Seq + 1000})
		if len(h.net.sent) != 0 {
			t.Fatal("an unknown ack was forwarded")
		}
		h.deliver("p03", gossip.Message{Kind: gossip.KindAck, Seq: ms[0].Seq})
		ss = h.net.take()
		ms = decodeAll(t, ss)
		if len(ms) != 1 || ms[0].Kind != gossip.KindAck || ms[0].Seq != 77 || ss[0].to != "p01" {
			t.Fatalf("forwarded %+v to %v", ms, ss)
		}
		h.deliver("p03", gossip.Message{Kind: gossip.KindAck, Seq: ms[0].Seq})
		if len(h.net.sent) != 0 {
			t.Fatal("a duplicate ack was forwarded twice")
		}
	})
	t.Run("a relay expires and a late ack is not forwarded", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		announce(h, peerIDs(3)...)
		h.node.Start()
		h.deliver("p01", gossip.Message{Kind: gossip.KindPingReq, Seq: 9, Target: "p03"})
		var relaySeq uint32
		for i, m := range decodeAll(t, h.net.take()) {
			_ = i
			relaySeq = m.Seq
		}
		h.clock.advance(3 * gossip.DefaultProbeInterval)
		h.net.take()
		h.deliver("p03", gossip.Message{Kind: gossip.KindAck, Seq: relaySeq})
		for _, m := range decodeAll(t, h.net.take()) {
			if m.Kind == gossip.KindAck && m.Seq == 9 {
				t.Fatal("expired relay forwarded")
			}
		}
	})
	t.Run("the suspicion timeout and retransmit limit scale with log2 of the member count", func(t *testing.T) {
		c := gossip.Config{}.WithDefaults()
		for _, r := range []struct {
			n        int
			wantLog  int
			wantSusp time.Duration
		}{{0, 1, 4 * time.Second}, {1, 1, 4 * time.Second}, {2, 1, 4 * time.Second}, {8, 3, 12 * time.Second}, {9, 4, 16 * time.Second}, {100, 7, 28 * time.Second}} {
			if got := c.SuspicionTimeout(r.n); got != r.wantSusp {
				t.Errorf("SuspicionTimeout(%d) = %v, want %v", r.n, got, r.wantSusp)
			}
			if got := c.RetransmitLimit(r.n); got != gossip.DefaultRetransmitMult*r.wantLog {
				t.Errorf("RetransmitLimit(%d) = %d, want %d", r.n, got, gossip.DefaultRetransmitMult*r.wantLog)
			}
		}
	})
}

func countUpdates(t *testing.T, ss []sent) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, m := range decodeAll(t, ss) {
		for _, u := range m.Updates {
			out[fmt.Sprintf("%s/%d/%s", u.ID, u.Incarnation, u.State)]++
		}
	}
	return out
}

func TestPiggybackSelectionAndLimit(t *testing.T) {
	t.Run("each update is piggybacked exactly lambda*log2(N) times and then dropped", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		ids := peerIDs(7)
		announce(h, ids...)
		h.node.Start()
		var all []sent
		for range 40 {
			h.clock.advance(gossip.DefaultProbeInterval)
			ss := h.net.take()
			all = append(all, ss...)
			for _, p := range pings(t, ss) {
				m, _ := gossip.Decode(p.msg)
				h.deliver(p.to, gossip.Message{Kind: gossip.KindAck, Seq: m.Seq})
			}
		}
		limit := gossip.Config{}.WithDefaults().RetransmitLimit(8)
		got := countUpdates(t, all)
		for _, id := range append(ids, "self") {
			if n := got[id+"/0/alive"]; n != limit {
				t.Errorf("update about %s sent %d times, want %d", id, n, limit)
			}
		}
		h.clock.advance(gossip.DefaultProbeInterval)
		for _, m := range decodeAll(t, h.net.take()) {
			if len(m.Updates) != 0 {
				t.Fatalf("exhausted queue still piggybacks %+v", m.Updates)
			}
		}
	})
	t.Run("packets never exceed MaxPacket and the freshest updates go first", func(t *testing.T) {
		cfg := gossip.Config{MaxPacket: gossip.MinPacket}
		h := newHarness(t, "self", cfg)
		var ups []gossip.Member
		for i := range 30 {
			ups = append(ups, mem(fmt.Sprintf("%s%02d", strings.Repeat("m", 90), i), 0, gossip.StateAlive))
		}
		h.deliver("x", gossipMsg(ups[:15]...))
		h.deliver("x", gossipMsg(ups[15:]...))
		h.node.Start()
		h.clock.advance(gossip.DefaultProbeInterval)
		h.net.take()
		h.deliver("x", gossipMsg(mem("fresh", 0, gossip.StateAlive)))
		h.deliver("p01", gossip.Message{Kind: gossip.KindPing, Seq: 1})
		ss := h.net.take()
		if len(ss) != 1 {
			t.Fatalf("sent %d packets", len(ss))
		}
		if len(ss[0].msg) > gossip.MinPacket {
			t.Fatalf("packet of %d bytes over MaxPacket %d", len(ss[0].msg), gossip.MinPacket)
		}
		m, _ := gossip.Decode(ss[0].msg)
		if len(m.Updates) == 0 || m.Updates[0].ID != "fresh" {
			t.Fatalf("first piggybacked update is not the fresh one: %+v", m.Updates)
		}
		if len(m.Updates) >= 31 {
			t.Fatalf("all %d updates fit in a minimum-size packet; the test does not exercise the limit", len(m.Updates))
		}
		for range 60 {
			h.deliver("p01", gossip.Message{Kind: gossip.KindPing, Seq: 2})
			for _, s := range h.net.take() {
				if len(s.msg) > gossip.MinPacket {
					t.Fatalf("packet of %d bytes over MaxPacket", len(s.msg))
				}
			}
		}
	})
	t.Run("a packet to a member we suspect leads with that suspicion, even after the queue is spent", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		announce(h, peerIDs(3)...)
		h.deliver("p02", gossipMsg(mem("p01", 0, gossip.StateSuspect)))
		for range 50 {
			h.deliver("p02", gossip.Message{Kind: gossip.KindPing, Seq: 1})
		}
		h.net.take()
		h.deliver("p01", gossip.Message{Kind: gossip.KindPing, Seq: 2})
		ms := decodeAll(t, h.net.take())
		if len(ms) != 1 || len(ms[0].Updates) == 0 || ms[0].Updates[0] != mem("p01", 0, gossip.StateSuspect) {
			t.Fatalf("ack to the suspect: %+v", ms)
		}
	})
	t.Run("a newer update about a member replaces the queued older one", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		h.deliver("x", gossipMsg(mem("m", 0, gossip.StateAlive)))
		h.deliver("x", gossipMsg(mem("m", 0, gossip.StateDead)))
		h.deliver("p01", gossip.Message{Kind: gossip.KindPing, Seq: 1})
		got := countUpdates(t, h.net.take())
		if got["m/0/alive"] != 0 || got["m/0/dead"] != 1 {
			t.Fatalf("piggyback %v", got)
		}
	})
	t.Run("a stale update is not queued again", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		h.deliver("x", gossipMsg(mem("m", 3, gossip.StateAlive)))
		for range 30 {
			h.deliver("p01", gossip.Message{Kind: gossip.KindPing, Seq: 1})
		}
		h.net.take()
		h.deliver("x", gossipMsg(mem("m", 2, gossip.StateAlive)))
		h.deliver("p01", gossip.Message{Kind: gossip.KindPing, Seq: 1})
		if got := countUpdates(t, h.net.take()); len(got) != 0 {
			t.Fatalf("stale update re-gossiped: %v", got)
		}
	})
}

func TestJoin(t *testing.T) {
	resp := func(t *testing.T, ms ...gossip.Member) []byte {
		b, err := gossip.Message{Kind: gossip.KindSyncResp, Updates: ms}.Encode()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	t.Run("join with no reachable seed fails with ErrJoinFailed and keeps every cause", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		var got []error
		if err := h.node.Join([]env.NodeID{"s1", "self", "s2"}, func(err error) { got = append(got, err) }); err != nil {
			t.Fatal(err)
		}
		reqs := h.net.takeRequests()
		if len(reqs) != 2 || reqs[0].to != "s1" || reqs[1].to != "s2" {
			t.Fatalf("join requests %+v", reqs)
		}
		m, err := gossip.Decode(reqs[0].req)
		if err != nil || m.Kind != gossip.KindSyncReq || !slices.Contains(m.Updates, h.node.Self()) {
			t.Fatalf("join request %+v (%v) does not carry this node's own entry", m, err)
		}
		reqs[0].cb(nil, env.ErrUnreachable)
		if len(got) != 0 {
			t.Fatal("join reported before every seed answered")
		}
		reqs[1].cb(nil, env.ErrTimeout)
		if len(got) != 1 {
			t.Fatalf("done called %d times", len(got))
		}
		for _, want := range []error{gossip.ErrJoinFailed, env.ErrUnreachable, env.ErrTimeout} {
			if !errors.Is(got[0], want) {
				t.Errorf("join error %v does not match %v", got[0], want)
			}
		}
		if len(h.node.Members()) != 1 {
			t.Fatalf("members after a failed join: %+v", h.node.Members())
		}
	})
	t.Run("join succeeds through any one seed and merges its view", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		var got []error
		if err := h.node.Join([]env.NodeID{"s1", "s2"}, func(err error) { got = append(got, err) }); err != nil {
			t.Fatal(err)
		}
		reqs := h.net.takeRequests()
		reqs[0].cb(nil, env.ErrUnreachable)
		reqs[1].cb(resp(t, mem("s2", 0, gossip.StateAlive), mem("s3", 4, gossip.StateAlive)), nil)
		if len(got) != 1 || got[0] != nil {
			t.Fatalf("done: %v", got)
		}
		if n := len(h.node.Members()); n != 3 {
			t.Fatalf("members %+v", h.node.Members())
		}
		if h.node.Stats().Syncs != 1 || h.node.Stats().SyncErrors != 1 {
			t.Fatalf("stats %+v", h.node.Stats())
		}
	})
	t.Run("a malformed join reply is a failure that names ErrMalformed", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		var got error
		if err := h.node.Join([]env.NodeID{"s1"}, func(err error) { got = err }); err != nil {
			t.Fatal(err)
		}
		h.net.takeRequests()[0].cb([]byte{1, 2, 3}, nil)
		if !errors.Is(got, gossip.ErrJoinFailed) || !errors.Is(got, gossip.ErrMalformed) {
			t.Fatalf("join error %v", got)
		}
	})
	t.Run("a reply of the wrong kind is malformed", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		var got error
		if err := h.node.Join([]env.NodeID{"s1"}, func(err error) { got = err }); err != nil {
			t.Fatal(err)
		}
		b, _ := gossip.Message{Kind: gossip.KindPing}.Encode()
		h.net.takeRequests()[0].cb(b, nil)
		if !errors.Is(got, gossip.ErrMalformed) {
			t.Fatalf("join error %v", got)
		}
	})
	t.Run("join with no other seed bootstraps a cluster of one and reports success", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		called := 0
		var got error
		if err := h.node.Join([]env.NodeID{"self"}, func(err error) { called++; got = err }); err != nil {
			t.Fatal(err)
		}
		h.clock.advance(0)
		if called != 1 || got != nil {
			t.Fatalf("done called %d times with %v", called, got)
		}
		if err := h.node.Join(nil, nil); err != nil {
			t.Fatal(err)
		}
		h.clock.advance(0)
	})
	t.Run("Start joins the configured seeds", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{Seeds: []env.NodeID{"s1", "s2"}})
		h.node.Start()
		reqs := h.net.takeRequests()
		if len(reqs) != 2 {
			t.Fatalf("Start sent %d join requests", len(reqs))
		}
		reqs[0].cb(resp(t, mem("s1", 0, gossip.StateAlive)), nil)
		reqs[1].cb(nil, env.ErrTimeout)
		if len(h.node.Members()) != 2 {
			t.Fatalf("members %+v", h.node.Members())
		}
	})
	t.Run("a node that knows no live member retries its seeds every probe period until one answers", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{Seeds: []env.NodeID{"s1"}})
		h.node.Start()
		attempts := 0
		for range 5 {
			for _, r := range h.net.takeRequests() {
				if r.to == "s1" {
					attempts++
					r.cb(nil, env.ErrTimeout)
				}
			}
			h.clock.advance(gossip.DefaultProbeInterval)
		}
		if attempts < 5 || h.node.Stats().Rejoins < 4 {
			t.Fatalf("%d join attempts, %d rejoins in 5 periods", attempts, h.node.Stats().Rejoins)
		}
		for _, r := range h.net.takeRequests() {
			r.cb(resp(t, mem("s1", 0, gossip.StateAlive)), nil)
		}
		before := h.node.Stats().Rejoins
		h.clock.advance(5 * gossip.DefaultProbeInterval)
		for _, r := range h.net.takeRequests() {
			r.cb(nil, env.ErrTimeout)
		}
		if h.node.Stats().Rejoins != before {
			t.Fatal("rejoined although a live member is known")
		}
	})
	t.Run("no second join starts while one is in flight", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{Seeds: []env.NodeID{"s1"}})
		h.node.Start()
		h.clock.advance(5 * gossip.DefaultProbeInterval)
		n := 0
		for _, r := range h.net.takeRequests() {
			if r.to == "s1" {
				n++
			}
		}
		if n > 2 {
			t.Fatalf("%d concurrent joins to the seed", n)
		}
	})
	t.Run("a restarted node learns its old entry from the seed and rejoins above it", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		if err := h.node.Join([]env.NodeID{"s1"}, nil); err != nil {
			t.Fatal(err)
		}
		h.net.takeRequests()[0].cb(resp(t, mem("s1", 0, gossip.StateAlive), mem("self", 41, gossip.StateDead)), nil)
		if got := h.node.Self(); got.Incarnation != 42 || got.State != gossip.StateAlive {
			t.Fatalf("self after rejoin %+v", got)
		}
	})
}

func TestSyncRequests(t *testing.T) {
	t.Run("a sync request is merged and answered with the merged view", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		announce(h, "p01")
		req, _ := gossip.Message{Kind: gossip.KindSyncReq, Updates: []gossip.Member{mem("p09", 2, gossip.StateAlive)}}.Encode()
		var resp []byte
		var rerr error
		h.node.OnRequest("p09", req, func(b []byte, err error) { resp, rerr = b, err })
		if rerr != nil {
			t.Fatal(rerr)
		}
		m, err := gossip.Decode(resp)
		if err != nil || m.Kind != gossip.KindSyncResp {
			t.Fatalf("reply %+v %v", m, err)
		}
		if !slices.Equal(m.Updates, h.node.All()) || len(m.Updates) != 3 {
			t.Fatalf("reply carries %+v, view %+v", m.Updates, h.node.All())
		}
	})
	t.Run("a malformed request is answered with ErrMalformed", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		ping, _ := gossip.Message{Kind: gossip.KindPing}.Encode()
		for _, req := range [][]byte{nil, {9, 9}, ping} {
			var rerr error
			h.node.OnRequest("p", req, func(_ []byte, err error) { rerr = err })
			if !errors.Is(rerr, gossip.ErrMalformed) {
				t.Fatalf("request %x answered with %v", req, rerr)
			}
		}
	})
	t.Run("periodic sync contacts a live member and a dead member or seed", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{Seeds: []env.NodeID{"s9"}})
		h.node.Start()
		for _, r := range h.net.takeRequests() {
			r.cb(nil, env.ErrUnreachable)
		}
		announce(h, "p01")
		h.deliver("p01", gossipMsg(mem("p02", 0, gossip.StateDead)))
		contacted := map[env.NodeID]int{}
		for range 40 {
			h.clock.advance(gossip.DefaultSyncInterval)
			for _, r := range h.net.takeRequests() {
				contacted[r.to]++
				m, err := gossip.Decode(r.req)
				if err != nil || m.Kind != gossip.KindSyncReq {
					t.Fatalf("sync request %+v %v", m, err)
				}
				r.cb(nil, env.ErrTimeout)
			}
		}
		for _, id := range []env.NodeID{"p01", "p02", "s9"} {
			if contacted[id] == 0 {
				t.Errorf("%s never contacted: %v", id, contacted)
			}
		}
		if h.node.Stats().SyncErrors == 0 {
			t.Fatal("failed syncs not counted")
		}
	})
	t.Run("dead members are purged after the retention period", func(t *testing.T) {
		cfg := gossip.Config{DeadRetention: 10 * time.Second}
		h := newHarness(t, "self", cfg)
		h.node.Start()
		h.deliver("x", gossipMsg(mem("gone", 0, gossip.StateDead), mem("bye", 0, gossip.StateLeft)))
		h.clock.advance(5 * time.Second)
		if _, err := h.node.Lookup("gone"); err != nil {
			t.Fatalf("purged too early: %v", err)
		}
		h.clock.advance(7 * time.Second)
		for _, id := range []env.NodeID{"gone", "bye"} {
			if _, err := h.node.Lookup(id); !errors.Is(err, gossip.ErrNotMember) {
				t.Fatalf("Lookup(%s) after retention: %v", id, err)
			}
		}
		if h.node.Stats().Purged != 2 {
			t.Fatalf("Purged = %d", h.node.Stats().Purged)
		}
	})
}

func TestOperationsAfterLeave(t *testing.T) {
	h := newHarness(t, "self", gossip.Config{})
	announce(h, peerIDs(3)...)
	h.deliver("p01", gossipMsg(mem("dead", 0, gossip.StateDead)))
	h.node.Start()
	h.net.take()
	h.net.takeRequests()
	if err := h.node.Leave(); err != nil {
		t.Fatal(err)
	}
	var to []env.NodeID
	for _, s := range h.net.take() {
		m, err := gossip.Decode(s.msg)
		if err != nil || !slices.Contains(m.Updates, mem("self", 0, gossip.StateLeft)) {
			t.Fatalf("leave packet %+v %v", m, err)
		}
		to = append(to, s.to)
	}
	slices.Sort(to)
	if !slices.Equal(to, []env.NodeID{"p01", "p02", "p03"}) {
		t.Fatalf("leave sent to %v", to)
	}
	if h.node.Self().State != gossip.StateLeft || !h.node.Closed() {
		t.Fatalf("self after leave %+v", h.node.Self())
	}
	t.Run("leave twice is ErrClosed", func(t *testing.T) {
		if err := h.node.Leave(); !errors.Is(err, gossip.ErrClosed) {
			t.Fatalf("second Leave: %v", err)
		}
	})
	t.Run("join after leave is ErrClosed", func(t *testing.T) {
		if err := h.node.Join([]env.NodeID{"p01"}, nil); !errors.Is(err, gossip.ErrClosed) {
			t.Fatalf("Join after Leave: %v", err)
		}
	})
	t.Run("datagrams after leave are dropped and counted", func(t *testing.T) {
		h.deliver("p01", gossip.Message{Kind: gossip.KindPing, Seq: 3})
		if len(h.net.sent) != 0 || h.node.Stats().DroppedClosed != 1 {
			t.Fatalf("sent %d, dropped %d", len(h.net.sent), h.node.Stats().DroppedClosed)
		}
	})
	t.Run("requests after leave are answered with ErrClosed, which crosses the wire as its own code", func(t *testing.T) {
		req, _ := gossip.Message{Kind: gossip.KindSyncReq}.Encode()
		var rerr error
		h.node.OnRequest("p01", req, func(_ []byte, err error) { rerr = err })
		if !errors.Is(rerr, gossip.ErrClosed) {
			t.Fatalf("reply error %v", rerr)
		}
		tbl, err := gossip.CodeTable()
		if err != nil {
			t.Fatal(err)
		}
		remote := tbl.Decode(tbl.Encode(rerr))
		if !errors.Is(remote, gossip.ErrClosed) || !errors.Is(remote, env.ErrRemote) {
			t.Fatalf("remote error %v", remote)
		}
	})
	t.Run("timers stop after leave", func(t *testing.T) {
		h.clock.advance(time.Minute)
		if len(h.net.sent) != 0 || len(h.net.requests) != 0 {
			t.Fatalf("closed node sent %d packets and %d requests", len(h.net.sent), len(h.net.requests))
		}
	})
}

func TestErrors(t *testing.T) {
	t.Run("an inbound packet over MaxPacket is dropped as malformed", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		big := make([]byte, gossip.DefaultMaxPacket+1)
		big[0] = gossip.Version
		h.node.OnDatagram("p01", big)
		if h.node.Stats().Malformed != 1 || !errors.Is(h.node.LastError(), gossip.ErrMalformed) {
			t.Fatalf("stats %+v last %v", h.node.Stats(), h.node.LastError())
		}
	})
	t.Run("an outbound packet the transport refuses as too large is counted with its cause", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		h.net.sendErr = fmt.Errorf("sim: send: %w", env.ErrTooLarge)
		h.deliver("p01", gossip.Message{Kind: gossip.KindPing, Seq: 1})
		if h.node.Stats().SendErrors != 1 || !errors.Is(h.node.LastError(), env.ErrTooLarge) {
			t.Fatalf("stats %+v last %v", h.node.Stats(), h.node.LastError())
		}
	})
	t.Run("garbage and request-only kinds as datagrams are malformed", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		sync, _ := gossip.Message{Kind: gossip.KindSyncReq}.Encode()
		for _, b := range [][]byte{nil, {1}, sync} {
			h.node.OnDatagram("p01", b)
		}
		if h.node.Stats().Malformed != 3 || !errors.Is(h.node.LastError(), gossip.ErrMalformed) {
			t.Fatalf("stats %+v", h.node.Stats())
		}
		if len(h.net.sent) != 0 {
			t.Fatal("a malformed packet was answered")
		}
	})
	t.Run("a datagram from this node's own id is ignored", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		h.deliver("self", gossip.Message{Kind: gossip.KindPing, Seq: 1})
		if len(h.net.sent) != 0 {
			t.Fatal("answered itself")
		}
	})
	t.Run("Lookup of an unknown member is ErrNotMember", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		if _, err := h.node.Lookup("nobody"); !errors.Is(err, gossip.ErrNotMember) {
			t.Fatalf("Lookup: %v", err)
		}
		if m, err := h.node.Lookup("self"); err != nil || m != h.node.Self() {
			t.Fatalf("Lookup(self) = %+v %v", m, err)
		}
	})
	t.Run("every sentinel survives the wire code table", func(t *testing.T) {
		tbl, err := gossip.CodeTable()
		if err != nil {
			t.Fatal(err)
		}
		entries := gossip.Codes()
		if len(entries) != 4 {
			t.Fatalf("%d code entries", len(entries))
		}
		for _, e := range entries {
			if e.Code < env.CodeAppMin {
				t.Fatalf("code %d below CodeAppMin", e.Code)
			}
			wrapped := fmt.Errorf("gossip: op: %w", e.Err)
			code, msg := tbl.Encode(wrapped)
			if code != e.Code {
				t.Fatalf("%v encoded as %d, want %d", e.Err, code, e.Code)
			}
			if back := tbl.Decode(code, msg); !errors.Is(back, e.Err) {
				t.Fatalf("code %d decoded to %v", code, back)
			}
		}
		for _, s := range []error{gossip.ErrMalformed, gossip.ErrNotMember, gossip.ErrClosed, gossip.ErrJoinFailed} {
			if !slices.ContainsFunc(entries, func(e env.CodeEntry) bool { return e.Err == s }) {
				t.Fatalf("%v has no code", s)
			}
		}
	})
	t.Run("invalid configuration is refused", func(t *testing.T) {
		rows := []struct {
			name string
			cfg  gossip.Config
		}{
			{"negative probe interval", gossip.Config{ProbeInterval: -1}},
			{"probe timeout not below the interval", gossip.Config{ProbeInterval: time.Second, ProbeTimeout: time.Second}},
			{"negative probe timeout", gossip.Config{ProbeTimeout: -1}},
			{"negative indirect fan-out", gossip.Config{IndirectProbes: -1}},
			{"negative suspicion multiplier", gossip.Config{SuspicionMult: -1}},
			{"negative retransmit multiplier", gossip.Config{RetransmitMult: -1}},
			{"negative sync interval", gossip.Config{SyncInterval: -1}},
			{"negative sync timeout", gossip.Config{SyncTimeout: -1}},
			{"negative dead retention", gossip.Config{DeadRetention: -1}},
			{"a packet limit below MinPacket", gossip.Config{MaxPacket: gossip.MinPacket - 1}},
			{"an empty seed id", gossip.Config{Seeds: []env.NodeID{""}}},
		}
		for _, r := range rows {
			if err := r.cfg.WithDefaults().Validate(); !errors.Is(err, env.ErrInvalidArgument) {
				t.Errorf("%s: Validate = %v", r.name, err)
			}
			e := env.Env{ID: "a", Clock: &fakeClock{}, Net: &fakeNet{}, Rand: rand.New(rand.NewPCG(1, 1))}
			if _, err := gossip.New(e, r.cfg); !errors.Is(err, env.ErrInvalidArgument) {
				t.Errorf("%s: New = %v", r.name, err)
			}
		}
		if err := (gossip.Config{}).WithDefaults().Validate(); err != nil {
			t.Fatalf("defaults invalid: %v", err)
		}
	})
	t.Run("an empty, over-long or unusable node identity is refused", func(t *testing.T) {
		good := env.Env{ID: "a", Clock: &fakeClock{}, Net: &fakeNet{}, Rand: rand.New(rand.NewPCG(1, 1))}
		bad := []env.Env{good, good, good, good, good, good}
		bad[0].ID = ""
		bad[1].ID = env.NodeID(strings.Repeat("x", gossip.MaxIDLen+1))
		bad[2].Region = env.Region(strings.Repeat("x", gossip.MaxIDLen+1))
		bad[3].Clock = nil
		bad[4].Net = nil
		bad[5].Rand = nil
		for i, e := range bad {
			if _, err := gossip.New(e, gossip.Config{}); !errors.Is(err, env.ErrInvalidArgument) {
				t.Errorf("env %d: New = %v", i, err)
			}
		}
	})
}

func TestEncodeStateIsCanonical(t *testing.T) {
	run := func() *harness {
		h := newHarness(t, "self", gossip.Config{})
		announce(h, peerIDs(4)...)
		h.node.Start()
		h.clock.advance(3 * gossip.DefaultProbeInterval)
		h.deliver("p01", gossipMsg(mem("p02", 0, gossip.StateSuspect)))
		return h
	}
	a, b := run(), run()
	if !bytes.Equal(a.node.EncodeState(), b.node.EncodeState()) {
		t.Fatal("same history, different state encodings")
	}
	b.deliver("p01", gossipMsg(mem("p03", 1, gossip.StateAlive)))
	if bytes.Equal(a.node.EncodeState(), b.node.EncodeState()) {
		t.Fatal("different views, same state encoding")
	}
	var _ gossip.Membership = a.node
}
