package sim

import (
	"fmt"
	"math"

	"github.com/nickstrad/object-store/internal/env"
)

func (s *Sim) upNode(op string, id env.NodeID) (*node, error) {
	n, err := s.node(id)
	if err != nil {
		return nil, fmt.Errorf("sim: %s: %w", op, err)
	}
	if !n.up {
		return nil, fmt.Errorf("sim: %s %s: %w", op, id, ErrNodeDown)
	}
	return n, nil
}

// crash stops node n, discards its volatile state, pending calls, and held
// events, and rolls its disk back to the durable layer (with optional torn tail).
func (s *Sim) crash(n *node, torn bool) {
	n.up = false
	n.paused = false
	n.started = false
	n.proc = nil
	for _, e := range n.held {
		e.dead = true
	}
	n.held = nil
	n.calls = map[uint64]*call{}
	n.disk.crash(torn)
}

// Crash stops node id, closing its handle, dropping volatile state, timers,
// pending requests, held events, and unsynced disk writes while keeping durable
// disk blobs. Returns ErrUnknownNode or ErrNodeDown.
func (s *Sim) Crash(id env.NodeID) error {
	n, err := s.upNode("crash", id)
	if err != nil {
		return err
	}
	s.crash(n, false)
	return nil
}

// CrashTorn crashes node id like Crash, except append-only unsynced disk blobs
// keep a random prefix of their appended bytes drawn from the disk stream.
// Returns ErrUnknownNode or ErrNodeDown.
func (s *Sim) CrashTorn(id env.NodeID) error {
	n, err := s.upNode("crash", id)
	if err != nil {
		return err
	}
	s.crash(n, true)
	return nil
}

// Restart boots a crashed node id into a new incarnation with a fresh Env and
// schedules its Start event while preserving its durable disk and advancing
// PRNG stream. Returns ErrUnknownNode, ErrNodeUp, or env.ErrInvalidArgument if
// the node has already reached incarnation math.MaxUint32.
func (s *Sim) Restart(id env.NodeID) error {
	n, err := s.node(id)
	if err != nil {
		return fmt.Errorf("sim: restart: %w", err)
	}
	if n.up {
		return fmt.Errorf("sim: restart %s: %w", id, ErrNodeUp)
	}
	if n.lastIncarnation() {
		return fmt.Errorf("sim: restart %s: incarnation %d is the last: %w", id, n.inc, env.ErrInvalidArgument)
	}
	s.boot(n)
	return nil
}

// Pause freezes node id so arriving events and timers are held while virtual
// time advances, until Resume or HealAll releases them in order. Returns
// ErrUnknownNode or ErrNodeDown.
func (s *Sim) Pause(id env.NodeID) error {
	n, err := s.upNode("pause", id)
	if err != nil {
		return err
	}
	n.paused = true
	return nil
}

// Resume unpauses node id and schedules all events held during the pause at the
// current instant in FIFO order. Returns ErrUnknownNode, ErrNodeDown, or
// ErrNotPaused.
func (s *Sim) Resume(id env.NodeID) error {
	n, err := s.upNode("resume", id)
	if err != nil {
		return err
	}
	if !n.paused {
		return fmt.Errorf("sim: resume %s: %w", id, ErrNotPaused)
	}
	n.paused = false
	s.releaseNode(n)
	return nil
}

// WipeRejoin crashes node id if it is up, wipes both layers of its disk, and
// restarts it in a new incarnation. Returns ErrUnknownNode or
// env.ErrInvalidArgument if the node is at incarnation math.MaxUint32.
func (s *Sim) WipeRejoin(id env.NodeID) error {
	n, err := s.node(id)
	if err != nil {
		return fmt.Errorf("sim: wipe-rejoin: %w", err)
	}
	if n.lastIncarnation() {
		return fmt.Errorf("sim: wipe-rejoin %s: incarnation %d is the last: %w", id, n.inc, env.ErrInvalidArgument)
	}
	if n.up {
		s.crash(n, false)
	}
	n.disk.wipe()
	s.boot(n)
	return nil
}

// HealAll heals the network, restarts every crashed node (unless it has reached
// incarnation math.MaxUint32), resumes every paused node, and clears pending
// read-error injections. Bit-rot and disk wipes remain for the protocol to repair.
func (s *Sim) HealAll() {
	s.HealNet()
	for _, n := range s.nodes {
		if !n.up {
			if !n.lastIncarnation() {
				s.boot(n)
			}
		} else if n.paused {
			n.paused = false
			s.releaseNode(n)
		}
		n.disk.clearReadFaults()
	}
}

// lastIncarnation prevents n.inc from wrapping to 0, which marks
// incarnation-free events and would allow stale timers to fire.
func (n *node) lastIncarnation() bool { return n.inc == math.MaxUint32 }
