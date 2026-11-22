package occ

import (
	"cmp"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

// MaxClockLead is the maximum distance ahead of a node's local clock that
// HLC.Observe will accept before returning ErrClockAhead.
const MaxClockLead = time.Hour

// Stamp is a hybrid logical clock timestamp ordered by (Wall, Logical, Node).
type Stamp struct {
	Wall    int64
	Logical uint32
	Node    env.NodeID
}

// Compare defines a total order on stamps: Wall, then Logical, then Node.
func (s Stamp) Compare(t Stamp) int {
	if c := cmp.Compare(s.Wall, t.Wall); c != 0 {
		return c
	}
	if c := cmp.Compare(s.Logical, t.Logical); c != 0 {
		return c
	}
	return cmp.Compare(s.Node, t.Node)
}

// IsZero reports whether s is the zero Stamp.
func (s Stamp) IsZero() bool { return s == Stamp{} }

// After reports whether s is strictly greater than t on (Wall, Logical) alone,
// ignoring Node so a successor beats t regardless of node ID tie-breaking.
func (s Stamp) After(t Stamp) bool {
	return s.Wall > t.Wall || (s.Wall == t.Wall && s.Logical > t.Logical)
}

// successor returns the least stamp for node that satisfies After(s), carrying
// a full Logical counter into Wall+1. It returns ErrClockExhausted at
// (MaxInt64, MaxUint32).
func (s Stamp) successor(node env.NodeID) (Stamp, error) {
	switch {
	case s.Logical < math.MaxUint32:
		return Stamp{Wall: s.Wall, Logical: s.Logical + 1, Node: node}, nil
	case s.Wall < math.MaxInt64:
		return Stamp{Wall: s.Wall + 1, Node: node}, nil
	}
	return Stamp{}, fmt.Errorf("occ: no stamp after %s: %w", s.String(), ErrClockExhausted)
}

// String formats s as "<wall>.<logical>@<node>".
func (s Stamp) String() string {
	return strconv.FormatInt(s.Wall, 10) + "." + strconv.FormatUint(uint64(s.Logical), 10) + "@" + string(s.Node)
}

// HLC maintains a monotonic hybrid logical clock over an env.Clock.
type HLC struct {
	clock env.Clock
	node  env.NodeID
	wall  int64
	logic uint32
}

// NewHLC returns an HLC backed by c and tagged with node.
func NewHLC(c env.Clock, node env.NodeID) *HLC {
	// Start at MinInt64 so even negative virtual clock times advance h.wall
	// cleanly on the first Now or Observe call.
	return &HLC{clock: c, node: node, wall: math.MinInt64}
}

// Last returns the most recent stamp issued or observed, or the zero Stamp
// before any call to Now or Observe has advanced the clock.
func (h *HLC) Last() Stamp {
	if h.wall == math.MinInt64 && h.logic == 0 {
		return Stamp{}
	}
	return Stamp{Wall: h.wall, Logical: h.logic, Node: h.node}
}

// Now advances h and returns a stamp strictly greater than the previous one.
// It returns ErrClockExhausted if h is already at (MaxInt64, MaxUint32).
func (h *HLC) Now() (Stamp, error) {
	pt := int64(h.clock.Now())
	if pt > h.wall {
		h.wall, h.logic = pt, 0
		return h.Last(), nil
	}
	if h.logic < math.MaxUint32 {
		h.logic++
		return h.Last(), nil
	}
	if h.wall == math.MaxInt64 {
		return Stamp{}, fmt.Errorf("occ: hlc at wall %d logical %d: %w", h.wall, h.logic, ErrClockExhausted)
	}
	h.wall, h.logic = h.wall+1, 0
	return h.Last(), nil
}

type hlcState struct {
	wall  int64
	logic uint32
}

func (h *HLC) state() hlcState    { return hlcState{wall: h.wall, logic: h.logic} }
func (h *HLC) restore(s hlcState) { h.wall, h.logic = s.wall, s.logic }

// Observe incorporates remote stamp s into h so later local stamps sort after
// it. Stamps more than MaxClockLead ahead of the local clock are rejected with
// ErrClockAhead without modifying h.
func (h *HLC) Observe(s Stamp) error {
	pt := int64(h.clock.Now())
	limit := pt + int64(MaxClockLead)
	if pt > math.MaxInt64-int64(MaxClockLead) {
		limit = math.MaxInt64
	}
	if s.Wall > limit {
		return fmt.Errorf("occ: observe stamp %s at local time %d: %w", s.String(), pt, ErrClockAhead)
	}
	if s.Wall > h.wall || (s.Wall == h.wall && s.Logical > h.logic) {
		h.wall, h.logic = s.Wall, s.Logical
	}
	return nil
}
