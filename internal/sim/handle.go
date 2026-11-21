package sim

import (
	"fmt"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type handle struct {
	s   *Sim
	n   *node
	inc uint32
}

func (h *handle) closed() bool { return h.inc != h.n.inc || !h.n.up }

type call struct {
	id      uint64
	to      int
	cb      func([]byte, error)
	timeout *event
}

func (h *handle) Send(to env.NodeID, msg []byte) error {
	s := h.s
	if h.closed() {
		return fmt.Errorf("sim: send %s->%s: %w", h.n.id, to, env.ErrClosed)
	}
	if len(msg) > s.cfg.Net.MaxDatagram {
		return fmt.Errorf("sim: send %s->%s: %d bytes over %d: %w", h.n.id, to, len(msg), s.cfg.Net.MaxDatagram, env.ErrTooLarge)
	}
	ti, ok := s.byID[to]
	if !ok {
		return fmt.Errorf("sim: send %s->%s: %w", h.n.id, to, env.ErrUnreachable)
	}
	s.sendDatagram(h.n.idx, ti, msg)
	return nil
}

func (h *handle) Request(to env.NodeID, req []byte, timeout time.Duration, cb func([]byte, error)) {
	s := h.s
	if h.closed() || cb == nil {
		return
	}
	from := h.n
	fail := func(err error) {
		s.schedule(&event{at: s.now, kind: kindLocal, node: from.idx, peer: -1, inc: h.inc, fn: func() { cb(nil, err) }})
	}
	if timeout <= 0 {
		fail(fmt.Errorf("sim: request %s->%s: timeout %v: %w", from.id, to, timeout, env.ErrInvalidArgument))
		return
	}
	if len(req) > s.cfg.Net.MaxMessage {
		fail(fmt.Errorf("sim: request %s->%s: %d bytes over %d: %w", from.id, to, len(req), s.cfg.Net.MaxMessage, env.ErrTooLarge))
		return
	}
	ti, ok := s.byID[to]
	if !ok {
		fail(fmt.Errorf("sim: request %s->%s: no such node: %w", from.id, to, env.ErrUnreachable))
		return
	}
	s.nextCall++
	c := &call{id: s.nextCall, to: ti, cb: cb}
	from.calls[c.id] = c
	c.timeout = s.schedule(&event{at: s.now.Add(from.toGlobal(timeout)), kind: kindTimeout, node: from.idx, peer: -1, inc: h.inc, call: c.id})
	s.sendStream(&event{kind: kindRequest, node: ti, peer: from.idx, peerInc: h.inc, call: c.id, payload: clone(req)})
}

func clone(b []byte) []byte {
	if b == nil {
		return nil
	}
	out := make([]byte, len(b))
	copy(out, b)
	return out
}

func (s *Sim) serve(n *node, e *event) {
	caller := s.nodes[e.peer]
	inc := n.inc
	done := false
	reply := func(resp []byte, err error) {
		if done {
			panic(fmt.Sprintf("sim: reply called twice for request %d from %s", e.call, caller.id))
		}
		done = true
		if n.inc != inc || !n.up {
			return
		}
		code, msg := s.codes.Encode(err)
		if code == env.CodeOK && len(resp) > s.cfg.Net.MaxMessage {
			code, msg = s.codes.Encode(fmt.Errorf("sim: reply %s->%s: %d bytes over %d: %w", n.id, caller.id, len(resp), s.cfg.Net.MaxMessage, env.ErrTooLarge))
		}
		var payload []byte
		if code == env.CodeOK {
			payload = clone(resp)
		}
		s.sendStream(&event{kind: kindResponse, node: caller.idx, peer: n.idx, inc: e.peerInc, call: e.call, payload: payload, code: code, msg: msg})
	}
	s.guard(n.idx, "request", func() { n.proc.OnRequest(caller.id, e.payload, reply) })
}

func (s *Sim) refuse(e *event) {
	s.sendStream(&event{kind: kindRefused, node: e.peer, peer: e.node, inc: e.peerInc, call: e.call})
}

func (s *Sim) complete(n *node, e *event) disposition {
	c := n.calls[e.call]
	if c == nil {
		return dispStale
	}
	delete(n.calls, e.call)
	var reply []byte
	var err error
	peer := s.nodes[c.to].id
	switch e.kind {
	case kindTimeout:
		err = fmt.Errorf("sim: request %s->%s: %w", n.id, peer, env.ErrTimeout)
	case kindRefused:
		s.cancel(c.timeout)
		err = fmt.Errorf("sim: request %s->%s: connection refused: %w", n.id, peer, env.ErrUnreachable)
	case kindResponse:
		s.cancel(c.timeout)
		err = s.codes.Decode(e.code, e.msg)
		if err == nil {
			reply = e.payload
		}
	}
	s.guard(n.idx, "reply callback", func() { c.cb(reply, err) })
	return dispRan
}

func (s *Sim) cancel(e *event) {
	if e.hi >= 0 {
		s.q.remove(e)
		if e.released {
			e.released = false
			if e.relLink {
				s.links[e.peer][e.node].relHeld--
			} else {
				s.nodes[e.node].relHeld--
			}
		}
	}
	e.dead = true
}
