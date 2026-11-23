package sim

import (
	"fmt"
	"time"

	"github.com/nickstrad/object-store/internal/env"
)

// Network size defaults and rate denominator:
//   - DefaultMaxDatagram: maximum datagram payload bytes (UDP-sized, 65,507).
//   - DefaultMaxMessage: maximum request or reply payload bytes (16 MiB).
//   - PPM: parts-per-million denominator (1,000,000) for drop, duplicate, and
//     per-link loss rates.
const (
	DefaultMaxDatagram = 65507
	DefaultMaxMessage  = 16 << 20
	PPM                = 1_000_000
)

// Range is an inclusive [Min, Max] duration interval with 0 <= Min <= Max.
type Range struct {
	Min time.Duration
	Max time.Duration
}

// NetConfig sets network latencies, datagram drop/duplicate rates, and payload
// size limits. DropPPM and DuplicatePPM apply to datagrams only; per-link loss
// and partitions apply to both datagrams and requests/responses.
type NetConfig struct {
	Latency            Range
	InterRegionLatency Range
	DropPPM            uint32
	DuplicatePPM       uint32
	MaxDatagram        int
	MaxMessage         int
}

// DefaultNetConfig returns the default latency ranges (1–5 ms intra-region,
// 20–80 ms inter-region), zero drop/duplicate rates, and default size limits.
func DefaultNetConfig() NetConfig {
	return NetConfig{
		Latency:            Range{Min: time.Millisecond, Max: 5 * time.Millisecond},
		InterRegionLatency: Range{Min: 20 * time.Millisecond, Max: 80 * time.Millisecond},
		MaxDatagram:        DefaultMaxDatagram,
		MaxMessage:         DefaultMaxMessage,
	}
}

func (c NetConfig) validate() error {
	for _, r := range []Range{c.Latency, c.InterRegionLatency} {
		if r.Min < 0 || r.Max < r.Min {
			return fmt.Errorf("sim: latency range [%v, %v]: %w", r.Min, r.Max, env.ErrInvalidArgument)
		}
	}
	if c.DropPPM > PPM || c.DuplicatePPM > PPM {
		return fmt.Errorf("sim: drop %d / duplicate %d ppm over %d: %w", c.DropPPM, c.DuplicatePPM, PPM, env.ErrInvalidArgument)
	}
	return nil
}

type link struct {
	blocked    bool
	lossPPM    uint32
	clogged    bool
	held       []*event
	relHeld    int
	lastStream env.Time
}

func (s *Sim) growLinks() {
	n := len(s.nodes)
	for i := range s.links {
		s.links[i] = append(s.links[i], link{})
	}
	s.links = append(s.links, make([]link, n))
}

func (s *Sim) roll(ppm uint32) bool {
	if ppm == 0 {
		return false
	}
	return s.netRand.Uint32N(PPM) < ppm
}

// latency draws a one-way delay from the net stream without signed overflow,
// even when r.Max - r.Min == math.MaxInt64.
func (s *Sim) latency(from, to int) time.Duration {
	r := s.cfg.Net.Latency
	if s.nodes[from].region != s.nodes[to].region {
		r = s.cfg.Net.InterRegionLatency
	}
	if r.Max <= r.Min {
		return r.Min
	}
	return r.Min + time.Duration(s.netRand.Uint64N(uint64(r.Max-r.Min)+1))
}

// sendDatagram decides partition, loss, and duplication at send time and draws
// an independent latency per copy so reordering occurs naturally.
func (s *Sim) sendDatagram(from, to int, msg []byte) {
	l := &s.links[from][to]
	if l.blocked || s.roll(l.lossPPM) || s.roll(s.cfg.Net.DropPPM) {
		return
	}
	copies := 1
	if s.roll(s.cfg.Net.DuplicatePPM) {
		copies = 2
	}
	for i := 0; i < copies; i++ {
		s.schedule(&event{at: later(s.now, s.latency(from, to)), kind: kindDatagram, node: to, peer: from, payload: clone(msg)})
	}
}

// sendStream delivers requests, responses, and refusals FIFO per directed link
// without duplication. If a draw pushes lastStream past Horizon, subsequent
// stream events on that link also land past Horizon and are never delivered.
func (s *Sim) sendStream(e *event) {
	l := &s.links[e.peer][e.node]
	if l.blocked || s.roll(l.lossPPM) {
		return
	}
	at := later(s.now, s.latency(e.peer, e.node))
	if at < l.lastStream {
		at = l.lastStream
	}
	l.lastStream = at
	e.at = at
	s.schedule(e)
}

func (s *Sim) indices(ids []env.NodeID) ([]int, error) {
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		n, err := s.node(id)
		if err != nil {
			return nil, err
		}
		out = append(out, n.idx)
	}
	return out, nil
}

func (s *Sim) complement(side []int) []int {
	in := make([]bool, len(s.nodes))
	for _, i := range side {
		in[i] = true
	}
	var out []int
	for i := range s.nodes {
		if !in[i] {
			out = append(out, i)
		}
	}
	return out
}

func (s *Sim) block(from, to []int, both bool) {
	for _, a := range from {
		for _, b := range to {
			if a == b {
				continue
			}
			s.links[a][b].blocked = true
			if both {
				s.links[b][a].blocked = true
			}
		}
	}
}

func (s *Sim) partition(side, other []env.NodeID, both bool) error {
	a, err := s.indices(side)
	if err != nil {
		return fmt.Errorf("sim: partition: %w", err)
	}
	b, err := s.indices(other)
	if err != nil {
		return fmt.Errorf("sim: partition: %w", err)
	}
	if len(other) == 0 {
		b = s.complement(a)
	}
	s.block(a, b, both)
	return nil
}

// Partition blocks traffic in both directions between side and other. When
// other is empty, side is partitioned from all remaining nodes. Returns
// ErrUnknownNode if any listed ID is unregistered.
func (s *Sim) Partition(side, other []env.NodeID) error { return s.partition(side, other, true) }

// PartitionOneWay blocks traffic only from from to to (leaving the reverse
// direction open). When to is empty, traffic from from to all other nodes is
// blocked. Returns ErrUnknownNode if any listed ID is unregistered.
func (s *Sim) PartitionOneWay(from, to []env.NodeID) error { return s.partition(from, to, false) }

// Isolate blocks all links to and from node id in both directions.
func (s *Sim) Isolate(id env.NodeID) error {
	n, err := s.node(id)
	if err != nil {
		return fmt.Errorf("sim: isolate: %w", err)
	}
	s.block([]int{n.idx}, s.complement([]int{n.idx}), true)
	return nil
}

// PartitionRegion blocks traffic in both directions between all nodes in region
// r and all nodes outside r. Returns ErrUnknownRegion if no node has region r.
func (s *Sim) PartitionRegion(r env.Region) error {
	var side []int
	for _, n := range s.nodes {
		if n.region == r {
			side = append(side, n.idx)
		}
	}
	if len(side) == 0 {
		return fmt.Errorf("sim: partition region %q: %w", r, ErrUnknownRegion)
	}
	s.block(side, s.complement(side), true)
	return nil
}

func (s *Sim) linkOf(from, to env.NodeID) (*link, int, int, error) {
	a, err := s.node(from)
	if err != nil {
		return nil, 0, 0, err
	}
	b, err := s.node(to)
	if err != nil {
		return nil, 0, 0, err
	}
	return &s.links[a.idx][b.idx], a.idx, b.idx, nil
}

// SetLinkLoss sets the packet loss rate in parts per million on directed link
// from->to for both datagrams and streams. Returns env.ErrInvalidArgument if
// ppm > PPM or ErrUnknownNode if either endpoint is unknown.
func (s *Sim) SetLinkLoss(from, to env.NodeID, ppm uint32) error {
	if ppm > PPM {
		return fmt.Errorf("sim: link loss %s->%s: %d ppm over %d: %w", from, to, ppm, PPM, env.ErrInvalidArgument)
	}
	l, _, _, err := s.linkOf(from, to)
	if err != nil {
		return fmt.Errorf("sim: link loss: %w", err)
	}
	l.lossPPM = ppm
	return nil
}

// Clog holds arriving network events on directed link from->to until Unclog or
// HealNet releases them in order.
func (s *Sim) Clog(from, to env.NodeID) error {
	l, _, _, err := s.linkOf(from, to)
	if err != nil {
		return fmt.Errorf("sim: clog: %w", err)
	}
	l.clogged = true
	return nil
}

// Unclog clears a clog on directed link from->to and schedules its held events
// at the current virtual time in FIFO order.
func (s *Sim) Unclog(from, to env.NodeID) error {
	_, a, b, err := s.linkOf(from, to)
	if err != nil {
		return fmt.Errorf("sim: unclog: %w", err)
	}
	s.unclog(a, b)
	return nil
}

func (s *Sim) unclog(a, b int) {
	l := &s.links[a][b]
	l.clogged = false
	held := l.held
	l.held = nil
	for _, e := range held {
		if e.dead {
			continue
		}
		e.released = true
		e.relLink = true
		l.relHeld++
		s.schedule(e)
	}
	// Advance lastStream so new stream sends cannot overtake held events
	// released at s.now.
	if l.lastStream < s.now {
		l.lastStream = s.now
	}
}

// HealNet removes all partitions and per-link loss rates and unclogs every link.
func (s *Sim) HealNet() {
	for a := range s.links {
		for b := range s.links[a] {
			l := &s.links[a][b]
			l.blocked = false
			l.lossPPM = 0
			if l.clogged {
				s.unclog(a, b)
			}
		}
	}
}

func (s *Sim) clogged() [][2]int {
	var out [][2]int
	for a := range s.links {
		for b := range s.links[a] {
			if s.links[a][b].clogged {
				out = append(out, [2]int{a, b})
			}
		}
	}
	return out
}
