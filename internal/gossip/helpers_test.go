package gossip_test

import (
	"math/rand/v2"
	"slices"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/gossip"
)

type tester interface {
	Helper()
	Fatalf(format string, args ...any)
}

type fakeTimer struct {
	c      *fakeClock
	at     env.Time
	seq    uint64
	fn     func()
	active bool
}

func (t *fakeTimer) Stop() bool {
	was := t.active
	t.active = false
	return was
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	was := t.active
	t.active = true
	t.at = t.c.now.Add(d)
	t.c.seq++
	t.seq = t.c.seq
	return was
}

// fakeClock is a manual virtual clock with FIFO tie-breaking for single-node unit tests.
type fakeClock struct {
	now    env.Time
	seq    uint64
	timers []*fakeTimer
}

func (c *fakeClock) Now() env.Time { return c.now }

func (c *fakeClock) AfterFunc(d time.Duration, fn func()) env.Timer {
	c.seq++
	t := &fakeTimer{c: c, at: c.now.Add(d), seq: c.seq, fn: fn, active: true}
	c.timers = append(c.timers, t)
	return t
}

func (c *fakeClock) next() *fakeTimer {
	var best *fakeTimer
	for _, t := range c.timers {
		if !t.active {
			continue
		}
		if best == nil || t.at < best.at || (t.at == best.at && t.seq < best.seq) {
			best = t
		}
	}
	return best
}

func (c *fakeClock) advance(d time.Duration) {
	end := c.now.Add(d)
	for {
		t := c.next()
		if t == nil || t.at > end {
			break
		}
		t.active = false
		if t.at > c.now {
			c.now = t.at
		}
		t.fn()
	}
	c.now = end
	live := c.timers[:0]
	for _, t := range c.timers {
		if t.active {
			live = append(live, t)
		}
	}
	c.timers = live
}

type sent struct {
	to  env.NodeID
	msg []byte
}

type pendingRequest struct {
	to      env.NodeID
	req     []byte
	timeout time.Duration
	cb      func([]byte, error)
}

// fakeNet records outbound datagrams and pending requests for manual delivery in unit tests.
type fakeNet struct {
	sent     []sent
	requests []pendingRequest
	sendErr  error
}

func (n *fakeNet) Send(to env.NodeID, msg []byte) error {
	if n.sendErr != nil {
		return n.sendErr
	}
	n.sent = append(n.sent, sent{to: to, msg: slices.Clone(msg)})
	return nil
}

func (n *fakeNet) Request(to env.NodeID, req []byte, timeout time.Duration, cb func([]byte, error)) {
	n.requests = append(n.requests, pendingRequest{to: to, req: slices.Clone(req), timeout: timeout, cb: cb})
}

func (n *fakeNet) take() []sent {
	out := n.sent
	n.sent = nil
	return out
}

func (n *fakeNet) takeRequests() []pendingRequest {
	out := n.requests
	n.requests = nil
	return out
}

// harness pairs a single Node with a fakeClock, fakeNet, and recorded events.
type harness struct {
	t      tester
	clock  *fakeClock
	net    *fakeNet
	node   *gossip.Node
	events []gossip.Event
}

func newHarness(t tester, id env.NodeID, cfg gossip.Config) *harness {
	t.Helper()
	h := &harness{t: t, clock: &fakeClock{}, net: &fakeNet{}}
	e := env.Env{
		ID:     id,
		Region: "r0",
		Clock:  h.clock,
		Rand:   rand.New(rand.NewPCG(1, 2)),
		Net:    h.net,
	}
	n, err := gossip.New(e, cfg)
	if err != nil {
		t.Fatalf("gossip.New: %v", err)
	}
	h.node = n
	n.Subscribe(func(ev gossip.Event) { h.events = append(h.events, ev) })
	return h
}

func (h *harness) deliver(from env.NodeID, m gossip.Message) {
	h.t.Helper()
	b, err := m.Encode()
	if err != nil {
		h.t.Fatalf("encode %v: %v", m.Kind, err)
	}
	h.node.OnDatagram(from, b)
}

func (h *harness) takeEvents() []gossip.Event {
	out := h.events
	h.events = nil
	return out
}

func decodeAll(t tester, ss []sent) []gossip.Message {
	t.Helper()
	out := make([]gossip.Message, 0, len(ss))
	for _, s := range ss {
		m, err := gossip.Decode(s.msg)
		if err != nil {
			t.Fatalf("node sent an undecodable packet to %s: %v", s.to, err)
		}
		out = append(out, m)
	}
	return out
}

func mem(id string, inc uint32, st gossip.State) gossip.Member {
	return gossip.Member{ID: env.NodeID(id), Region: "r0", Incarnation: inc, State: st}
}

func gossipMsg(updates ...gossip.Member) gossip.Message {
	return gossip.Message{Kind: gossip.KindGossip, Updates: updates}
}

func lookupState(t tester, n *gossip.Node, id string) (gossip.Member, bool) {
	t.Helper()
	m, err := n.Lookup(env.NodeID(id))
	if err != nil {
		return gossip.Member{}, false
	}
	return m, true
}
