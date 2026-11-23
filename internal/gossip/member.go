package gossip

import (
	"slices"
	"strconv"

	"github.com/nickstrad/object-store/internal/env"
)

// State is a member's lifecycle state in the SWIM membership lattice.
type State uint8

// Member lifecycle states ordered by increasing precedence at equal incarnation:
// StateAlive < StateSuspect < StateDead < StateLeft.
const (
	StateAlive State = iota + 1
	StateSuspect
	StateDead
	StateLeft
)

// String returns the canonical lowercase name of s, or "state(N)" if unknown.
func (s State) String() string {
	switch s {
	case StateAlive:
		return "alive"
	case StateSuspect:
		return "suspect"
	case StateDead:
		return "dead"
	case StateLeft:
		return "left"
	}
	return "state(" + strconv.Itoa(int(s)) + ")"
}

// Live reports whether s represents an active member (StateAlive or StateSuspect).
func (s State) Live() bool { return s == StateAlive || s == StateSuspect }

func (s State) valid() bool { return s >= StateAlive && s <= StateLeft }

// Member is one node's membership record: its identity, region, incarnation
// counter, and lifecycle state.
type Member struct {
	ID          env.NodeID
	Region      env.Region
	Incarnation uint32
	State       State
}

// Supersedes reports whether a strictly outranks b in the SWIM merge lattice.
// Entries are ordered lexicographically by (Incarnation, State, Region), with
// Region as a deterministic tie-break so any two distinct records have a unique
// maximum regardless of arrival order.
func Supersedes(a, b Member) bool {
	if a.Incarnation != b.Incarnation {
		return a.Incarnation > b.Incarnation
	}
	if a.State != b.State {
		return a.State > b.State
	}
	return a.Region > b.Region
}

// View holds a node's membership table and merges updates as a max-lattice
// ordered by Supersedes. Applying the same set of updates in any order with any
// duplication produces the same view. Ordered reads sort lazily by ID.
type View struct {
	byID   map[env.NodeID]Member
	ids    []env.NodeID
	sorted bool
	live   int
}

// NewView returns an empty View.
func NewView() *View {
	return &View{byID: map[env.NodeID]Member{}, sorted: true}
}

// Apply merges u into v and reports whether the view changed. Updates with an
// empty ID or invalid State are ignored. An existing entry is replaced only
// when Supersedes(u, current) is true.
func (v *View) Apply(u Member) bool {
	if u.ID == "" || !u.State.valid() {
		return false
	}
	cur, ok := v.byID[u.ID]
	if ok && !Supersedes(u, cur) {
		return false
	}
	if !ok {
		if v.sorted && len(v.ids) > 0 && v.ids[len(v.ids)-1] >= u.ID {
			v.sorted = false
		}
		v.ids = append(v.ids, u.ID)
	} else if cur.State.Live() {
		v.live--
	}
	if u.State.Live() {
		v.live++
	}
	v.byID[u.ID] = u
	// Compact stale and duplicate IDs left by Remove so alternating Apply and
	// Remove calls keep v.ids bounded even without an intervening ordered read.
	if len(v.ids) > 2*len(v.byID) {
		v.order()
	}
	return true
}

// order returns the sorted slice of present member IDs, compacting stale entries lazily.
func (v *View) order() []env.NodeID {
	if !v.sorted {
		slices.Sort(v.ids)
		v.ids = slices.Compact(v.ids)
		v.ids = slices.DeleteFunc(v.ids, func(id env.NodeID) bool {
			_, ok := v.byID[id]
			return !ok
		})
		v.sorted = true
	}
	return v.ids
}

// Get returns the stored entry for id and reports whether id is present.
func (v *View) Get(id env.NodeID) (Member, bool) {
	m, ok := v.byID[id]
	return m, ok
}

// Remove deletes id from v if present.
func (v *View) Remove(id env.NodeID) {
	cur, ok := v.byID[id]
	if !ok {
		return
	}
	if cur.State.Live() {
		v.live--
	}
	delete(v.byID, id)
	v.sorted = false
}

// Len returns the total number of retained entries in v, including dead and left ones.
func (v *View) Len() int { return len(v.byID) }

// LiveCount returns the number of live (StateAlive or StateSuspect) entries in v.
func (v *View) LiveCount() int { return v.live }

// All returns every entry in v, sorted by ID.
func (v *View) All() []Member {
	ids := v.order()
	out := make([]Member, 0, len(ids))
	for _, id := range ids {
		out = append(out, v.byID[id])
	}
	return out
}

// Live returns all StateAlive and StateSuspect entries in v, sorted by ID.
func (v *View) Live() []Member {
	ids := v.order()
	out := make([]Member, 0, v.live)
	for _, id := range ids {
		if m := v.byID[id]; m.State.Live() {
			out = append(out, m)
		}
	}
	return out
}

// IDs returns a sorted copy of all member IDs currently in v.
func (v *View) IDs() []env.NodeID { return slices.Clone(v.order()) }
