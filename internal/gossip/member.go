package gossip

import (
	"slices"
	"strconv"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type State uint8

const (
	StateAlive State = iota + 1
	StateSuspect
	StateDead
	StateLeft
)

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

func (s State) Live() bool { return s == StateAlive || s == StateSuspect }

func (s State) valid() bool { return s >= StateAlive && s <= StateLeft }

type Member struct {
	ID          env.NodeID
	Region      env.Region
	Incarnation uint32
	State       State
}

func Supersedes(a, b Member) bool {
	if a.Incarnation != b.Incarnation {
		return a.Incarnation > b.Incarnation
	}
	if a.State != b.State {
		return a.State > b.State
	}
	return a.Region > b.Region
}

type View struct {
	byID   map[env.NodeID]Member
	ids    []env.NodeID
	sorted bool
	live   int
}

func NewView() *View {
	return &View{byID: map[env.NodeID]Member{}, sorted: true}
}

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
	return true
}

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

func (v *View) Get(id env.NodeID) (Member, bool) {
	m, ok := v.byID[id]
	return m, ok
}

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

func (v *View) Len() int { return len(v.byID) }

func (v *View) LiveCount() int { return v.live }

func (v *View) All() []Member {
	ids := v.order()
	out := make([]Member, 0, len(ids))
	for _, id := range ids {
		out = append(out, v.byID[id])
	}
	return out
}

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

func (v *View) IDs() []env.NodeID { return slices.Clone(v.order()) }
