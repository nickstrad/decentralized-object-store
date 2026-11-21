package sim

import (
	"fmt"

	"github.com/nickstrad/decentralized-object-store/internal/env"
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

func (s *Sim) Crash(id env.NodeID) error {
	n, err := s.upNode("crash", id)
	if err != nil {
		return err
	}
	s.crash(n, false)
	return nil
}

func (s *Sim) CrashTorn(id env.NodeID) error {
	n, err := s.upNode("crash", id)
	if err != nil {
		return err
	}
	s.crash(n, true)
	return nil
}

func (s *Sim) Restart(id env.NodeID) error {
	n, err := s.node(id)
	if err != nil {
		return fmt.Errorf("sim: restart: %w", err)
	}
	if n.up {
		return fmt.Errorf("sim: restart %s: %w", id, ErrNodeUp)
	}
	s.boot(n)
	return nil
}

func (s *Sim) Pause(id env.NodeID) error {
	n, err := s.upNode("pause", id)
	if err != nil {
		return err
	}
	n.paused = true
	return nil
}

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

func (s *Sim) WipeRejoin(id env.NodeID) error {
	n, err := s.node(id)
	if err != nil {
		return fmt.Errorf("sim: wipe-rejoin: %w", err)
	}
	if n.up {
		s.crash(n, false)
	}
	n.disk.wipe()
	s.boot(n)
	return nil
}

func (s *Sim) HealAll() {
	s.HealNet()
	for _, n := range s.nodes {
		if !n.up {
			s.boot(n)
		} else if n.paused {
			n.paused = false
			s.releaseNode(n)
		}
		n.disk.clearReadFaults()
	}
}
