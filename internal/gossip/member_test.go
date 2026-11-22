package gossip_test

import (
	"math"
	"slices"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/gossip"
)

type outcome struct {
	changed bool
	events  []gossip.EventKind
}

var (
	none      = outcome{}
	quiet     = outcome{changed: true}
	joined    = outcome{changed: true, events: []gossip.EventKind{gossip.EventJoined}}
	joinedSus = outcome{changed: true, events: []gossip.EventKind{gossip.EventJoined, gossip.EventSuspected}}
	suspected = outcome{changed: true, events: []gossip.EventKind{gossip.EventSuspected}}
	recovered = outcome{changed: true, events: []gossip.EventKind{gossip.EventRecovered}}
	died      = outcome{changed: true, events: []gossip.EventKind{gossip.EventDied}}
	left      = outcome{changed: true, events: []gossip.EventKind{gossip.EventLeft}}
)

// TestStateTransitions proves the full 16-pair alive/suspect/dead/left
// transition matrix at lower, equal, and higher incarnations—plus unknown
// members and region tie-breaking—against both Node (including emitted events)
// and View.Apply.
func TestStateTransitions(t *testing.T) {
	const base = 5
	type row struct {
		cur   gossip.State
		msg   gossip.State
		lower outcome
		equal outcome
		upper outcome
	}
	a, s, d, l := gossip.StateAlive, gossip.StateSuspect, gossip.StateDead, gossip.StateLeft
	rows := []row{
		{a, a, none, none, quiet},
		{a, s, none, suspected, suspected},
		{a, d, none, died, died},
		{a, l, none, left, left},
		{s, a, none, none, recovered},
		{s, s, none, none, quiet},
		{s, d, none, died, died},
		{s, l, none, left, left},
		{d, a, none, none, joined},
		{d, s, none, none, joinedSus},
		{d, d, none, none, quiet},
		{d, l, none, quiet, quiet},
		{l, a, none, none, joined},
		{l, s, none, none, joinedSus},
		{l, d, none, none, quiet},
		{l, l, none, none, quiet},
	}
	for _, r := range rows {
		for _, step := range []struct {
			name string
			inc  uint32
			want outcome
		}{
			{"lower", base - 1, r.lower},
			{"equal", base, r.equal},
			{"higher", base + 1, r.upper},
		} {
			name := "a " + r.cur.String() + " member receiving " + r.msg.String() + " at a " + step.name + " incarnation"
			t.Run(name, func(t *testing.T) {
				h := newHarness(t, "self", gossip.Config{})
				h.deliver("x", gossipMsg(mem("m", base, r.cur)))
				h.takeEvents()
				before, ok := lookupState(t, h.node, "m")
				if !ok {
					t.Fatalf("setup: member m unknown after %s update", r.cur)
				}
				h.deliver("x", gossipMsg(mem("m", step.inc, r.msg)))
				after, _ := lookupState(t, h.node, "m")
				var kinds []gossip.EventKind
				for _, ev := range h.takeEvents() {
					if ev.Member.ID == "m" {
						kinds = append(kinds, ev.Kind)
					}
				}
				if step.want.changed {
					want := mem("m", step.inc, r.msg)
					if after != want {
						t.Fatalf("node view: got %+v, want %+v", after, want)
					}
				} else if after != before {
					t.Fatalf("node view changed from %+v to %+v", before, after)
				}
				if !slices.Equal(kinds, step.want.events) {
					t.Fatalf("events: got %v, want %v", kinds, step.want.events)
				}

				v := gossip.NewView()
				v.Apply(mem("m", base, r.cur))
				changed := v.Apply(mem("m", step.inc, r.msg))
				if changed != step.want.changed {
					t.Fatalf("View.Apply changed=%v, want %v", changed, step.want.changed)
				}
				got, _ := v.Get("m")
				if got != after {
					t.Fatalf("View and Node disagree: view %+v, node %+v", got, after)
				}
			})
		}
	}
	for _, st := range []struct {
		st   gossip.State
		want outcome
	}{
		{gossip.StateAlive, joined},
		{gossip.StateSuspect, joinedSus},
		{gossip.StateDead, quiet},
		{gossip.StateLeft, quiet},
	} {
		t.Run("an unknown member arriving as "+st.st.String(), func(t *testing.T) {
			h := newHarness(t, "self", gossip.Config{})
			h.deliver("x", gossipMsg(mem("m", base, st.st)))
			got, ok := lookupState(t, h.node, "m")
			if !ok || got != mem("m", base, st.st) {
				t.Fatalf("view: got %+v (known=%v)", got, ok)
			}
			var kinds []gossip.EventKind
			for _, ev := range h.takeEvents() {
				kinds = append(kinds, ev.Kind)
			}
			if !slices.Equal(kinds, st.want.events) {
				t.Fatalf("events: got %v, want %v", kinds, st.want.events)
			}
			live := slices.ContainsFunc(h.node.Members(), func(m gossip.Member) bool { return m.ID == "m" })
			if live != st.st.Live() {
				t.Fatalf("Members contains m = %v, want %v", live, st.st.Live())
			}
		})
	}
	t.Run("equal incarnation and state are ordered by region so every node picks the same entry", func(t *testing.T) {
		v := gossip.NewView()
		lo := gossip.Member{ID: "m", Region: "a", Incarnation: 1, State: gossip.StateAlive}
		hi := lo
		hi.Region = "b"
		v.Apply(hi)
		if v.Apply(lo) {
			t.Fatal("a lower region replaced a higher one")
		}
		w := gossip.NewView()
		w.Apply(lo)
		if !w.Apply(hi) {
			t.Fatal("a higher region did not replace a lower one")
		}
		if !gossip.Supersedes(hi, lo) || gossip.Supersedes(lo, hi) || gossip.Supersedes(lo, lo) {
			t.Fatal("Supersedes is not a strict order on region")
		}
	})
}

// TestIncarnationRules proves self-refutation, monotonic self incarnation,
// no self-declared death, saturation without wrap at MaxUint32, ignoring
// claims after Leave, and overturning death only at a higher incarnation.
func TestIncarnationRules(t *testing.T) {
	selfInc := func(h *harness) uint32 { return h.node.Self().Incarnation }
	refuted := func(t *testing.T, h *harness, want uint32) {
		t.Helper()
		self := h.node.Self()
		if self.Incarnation != want || self.State != gossip.StateAlive {
			t.Fatalf("self: got %+v, want alive at %d", self, want)
		}
		h.node.Start()
		h.clock.advance(2 * gossip.DefaultProbeInterval)
		found := false
		for _, m := range decodeAll(t, h.net.take()) {
			for _, u := range m.Updates {
				if u.ID == "self" && u.State == gossip.StateAlive && u.Incarnation == want {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("the refutation alive(self, %d) was not piggybacked on any packet", want)
		}
	}
	peers := func(h *harness) {
		h.deliver("p1", gossipMsg(mem("p1", 0, gossip.StateAlive), mem("p2", 0, gossip.StateAlive)))
	}
	t.Run("a new node starts at incarnation zero and alive", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		if got := h.node.Self(); got.Incarnation != 0 || got.State != gossip.StateAlive || got.Region != "r0" {
			t.Fatalf("self: %+v", got)
		}
	})
	for _, c := range []struct {
		name  string
		start uint32
		in    gossip.Member
		want  uint32
	}{
		{"a suspicion at the current incarnation is refuted with incarnation+1", 0, mem("self", 0, gossip.StateSuspect), 1},
		{"a death at the current incarnation is refuted with incarnation+1", 0, mem("self", 0, gossip.StateDead), 1},
		{"a death at a higher incarnation is refuted with that incarnation+1", 0, mem("self", 7, gossip.StateDead), 8},
		{"a left entry from a previous life is refuted with its incarnation+1", 0, mem("self", 9, gossip.StateLeft), 10},
		{"an alive claim at a higher incarnation is refuted with that incarnation+1", 0, mem("self", 5, gossip.StateAlive), 6},
		{"an alive claim at the current incarnation is not refuted", 2, mem("self", 2, gossip.StateAlive), 2},
		{"a stale suspicion below the current incarnation is ignored", 4, mem("self", 3, gossip.StateSuspect), 4},
		{"a stale death below the current incarnation is ignored", 4, mem("self", 1, gossip.StateDead), 4},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, "self", gossip.Config{})
			peers(h)
			if c.start > 0 {
				h.deliver("p1", gossipMsg(mem("self", c.start-1, gossip.StateSuspect)))
			}
			if got := selfInc(h); got != c.start {
				t.Fatalf("setup: incarnation %d, want %d", got, c.start)
			}
			h.net.take()
			h.deliver("p1", gossipMsg(c.in))
			refuted(t, h, c.want)
		})
	}
	t.Run("the incarnation never decreases over a sequence of claims", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		last := selfInc(h)
		for _, u := range []gossip.Member{
			mem("self", 3, gossip.StateDead), mem("self", 1, gossip.StateSuspect), mem("self", 0, gossip.StateAlive),
			mem("self", 4, gossip.StateSuspect), mem("self", 2, gossip.StateLeft), mem("self", 10, gossip.StateAlive),
		} {
			h.deliver("p1", gossipMsg(u))
			got := selfInc(h)
			if got < last {
				t.Fatalf("incarnation went from %d to %d after %+v", last, got, u)
			}
			if h.node.Self().State != gossip.StateAlive {
				t.Fatalf("the node declared itself %s", h.node.Self().State)
			}
			last = got
		}
	})
	t.Run("a death at the maximum incarnation cannot be refuted and is counted, never wrapped", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		h.deliver("p1", gossipMsg(mem("self", math.MaxUint32, gossip.StateDead)))
		self := h.node.Self()
		if self.Incarnation != math.MaxUint32 || self.State != gossip.StateAlive {
			t.Fatalf("self: %+v", self)
		}
		if h.node.Stats().RefuteOverflows != 1 {
			t.Fatalf("RefuteOverflows = %d, want 1", h.node.Stats().RefuteOverflows)
		}
		h.deliver("p1", gossipMsg(mem("self", math.MaxUint32, gossip.StateSuspect)))
		if got := h.node.Self(); got.Incarnation != math.MaxUint32 || h.node.Stats().RefuteOverflows != 2 {
			t.Fatalf("second overflow: self %+v, overflows %d", got, h.node.Stats().RefuteOverflows)
		}
	})
	t.Run("claims about this node after it left are ignored", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		peers(h)
		if err := h.node.Leave(); err != nil {
			t.Fatal(err)
		}
		h.deliver("p1", gossipMsg(mem("self", 5, gossip.StateDead)))
		if got := h.node.Self(); got.State != gossip.StateLeft || got.Incarnation != 0 {
			t.Fatalf("self after leave: %+v", got)
		}
	})
	t.Run("an alive entry overturns a death only with a higher incarnation", func(t *testing.T) {
		h := newHarness(t, "self", gossip.Config{})
		h.deliver("x", gossipMsg(mem("m", 3, gossip.StateDead)))
		h.deliver("x", gossipMsg(mem("m", 3, gossip.StateAlive)))
		if got, _ := lookupState(t, h.node, "m"); got.State != gossip.StateDead {
			t.Fatalf("dead verdict overturned at equal incarnation: %+v", got)
		}
		h.deliver("x", gossipMsg(mem("m", 4, gossip.StateAlive)))
		if got, _ := lookupState(t, h.node, "m"); got.State != gossip.StateAlive || got.Incarnation != 4 {
			t.Fatalf("higher incarnation did not overturn the death: %+v", got)
		}
	})
}

// TestViewAccessors proves View sorting, live filtering, removal, invalid
// input rejection, and string formatting for State, EventKind, and Kind.
func TestViewAccessors(t *testing.T) {
	v := gossip.NewView()
	for _, m := range []gossip.Member{
		mem("c", 1, gossip.StateDead), mem("a", 0, gossip.StateAlive), mem("b", 2, gossip.StateSuspect), mem("d", 0, gossip.StateLeft),
	} {
		v.Apply(m)
	}
	if v.Len() != 4 {
		t.Fatalf("Len = %d", v.Len())
	}
	ids := func(ms []gossip.Member) []env.NodeID {
		var out []env.NodeID
		for _, m := range ms {
			out = append(out, m.ID)
		}
		return out
	}
	if got := ids(v.All()); !slices.Equal(got, []env.NodeID{"a", "b", "c", "d"}) {
		t.Fatalf("All order: %v", got)
	}
	if got := ids(v.Live()); !slices.Equal(got, []env.NodeID{"a", "b"}) {
		t.Fatalf("Live: %v", got)
	}
	v.Remove("b")
	v.Remove("zz")
	if _, ok := v.Get("b"); ok || v.Len() != 3 {
		t.Fatal("Remove did not remove")
	}
	if v.Apply(gossip.Member{ID: "", State: gossip.StateAlive}) {
		t.Fatal("an empty id was accepted")
	}
	if v.Apply(gossip.Member{ID: "q", State: 0}) || v.Apply(gossip.Member{ID: "q", State: 9}) {
		t.Fatal("an invalid state was accepted")
	}
	for _, st := range []gossip.State{0, gossip.StateAlive, gossip.StateSuspect, gossip.StateDead, gossip.StateLeft, 9} {
		if st.String() == "" {
			t.Fatalf("State(%d) has no name", st)
		}
	}
	for _, k := range []gossip.EventKind{0, gossip.EventJoined, gossip.EventSuspected, gossip.EventRecovered, gossip.EventDied, gossip.EventLeft, 9} {
		if k.String() == "" {
			t.Fatalf("EventKind(%d) has no name", k)
		}
	}
	for _, k := range []gossip.Kind{0, gossip.KindPing, gossip.KindAck, gossip.KindPingReq, gossip.KindGossip, gossip.KindSyncReq, gossip.KindSyncResp, 9} {
		if k.String() == "" {
			t.Fatalf("Kind(%d) has no name", k)
		}
	}
}

// TestViewApplyRemoveWithoutReadStaysBounded proves that alternating View.Apply
// and View.Remove compacts the internal ID slice without needing an ordered read.
func TestViewApplyRemoveWithoutReadStaysBounded(t *testing.T) {
	v := gossip.NewView()
	v.Apply(gossip.Member{ID: "a", State: gossip.StateAlive})
	v.Apply(gossip.Member{ID: "b", State: gossip.StateAlive})
	for i := 0; i < 1000; i++ {
		v.Apply(gossip.Member{ID: "x", State: gossip.StateAlive})
		v.Remove("x")
		if got, limit := gossip.ViewBacklog(v), 2*v.Len()+2; got > limit {
			t.Fatalf("after %d apply/remove rounds the view holds %d ordered IDs for %d members, want at most %d", i+1, got, v.Len(), limit)
		}
	}
	if got := v.IDs(); !slices.Equal(got, []env.NodeID{"a", "b"}) {
		t.Fatalf("IDs() = %v, want [a b]", got)
	}
	if v.Len() != 2 || v.LiveCount() != 2 {
		t.Fatalf("Len %d LiveCount %d, want 2 and 2", v.Len(), v.LiveCount())
	}
}
