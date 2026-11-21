package occ

import (
	"cmp"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

const MaxClockLead = time.Hour

type Stamp struct {
	Wall    int64
	Logical uint32
	Node    env.NodeID
}

func (s Stamp) Compare(t Stamp) int {
	if c := cmp.Compare(s.Wall, t.Wall); c != 0 {
		return c
	}
	if c := cmp.Compare(s.Logical, t.Logical); c != 0 {
		return c
	}
	return cmp.Compare(s.Node, t.Node)
}

func (s Stamp) IsZero() bool { return s == Stamp{} }

func (s Stamp) String() string {
	return strconv.FormatInt(s.Wall, 10) + "." + strconv.FormatUint(uint64(s.Logical), 10) + "@" + string(s.Node)
}

type HLC struct {
	clock env.Clock
	node  env.NodeID
	wall  int64
	logic uint32
}

func NewHLC(c env.Clock, node env.NodeID) *HLC {
	return &HLC{clock: c, node: node, wall: math.MinInt64}
}

func (h *HLC) Last() Stamp {
	if h.wall == math.MinInt64 && h.logic == 0 {
		return Stamp{}
	}
	return Stamp{Wall: h.wall, Logical: h.logic, Node: h.node}
}

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
