package sim_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

// TestReadFaultCountsSaturate proves that FailReads counts saturate at
// math.MaxInt instead of wrapping and remain visible to Budget.Check, while
// non-positive counts are rejected.
func TestReadFaultCountsSaturate(t *testing.T) {
	t.Run("a named count added past MaxInt keeps failing reads", func(t *testing.T) {
		h := oneNode(t, 1)
		d := h.rec("a").e.Disk
		mustNil(t, d.Write("x", []byte{1}))
		mustNil(t, h.s.FailReads("a", "x", math.MaxInt))
		mustNil(t, h.s.FailReads("a", "x", 1))
		if _, err := d.Read("x"); !errors.Is(err, env.ErrIO) {
			t.Fatalf("Read = %v, want ErrIO", err)
		}
		names, _ := sim.ReadFaults(h.s.Disk("a"))
		if len(names) != 1 || names[0] != "x" {
			t.Fatalf("pending named read faults = %v", names)
		}
	})
	t.Run("a whole-node count added past MaxInt keeps failing reads and stays visible to the budget", func(t *testing.T) {
		h := oneNode(t, 1)
		d := h.rec("a").e.Disk
		mustNil(t, d.Write("x", []byte{1}))
		mustNil(t, d.Sync("x"))
		mustNil(t, h.s.FailReads("a", "", math.MaxInt))
		mustNil(t, h.s.FailReads("a", "", 1))
		if err := (sim.Budget{}).Check(h.s); !errors.Is(err, sim.ErrBudget) {
			t.Fatalf("Check = %v, want ErrBudget", err)
		}
		if _, any := sim.ReadFaults(h.s.Disk("a")); any != math.MaxInt {
			t.Fatalf("pending whole-node read faults = %d, want MaxInt", any)
		}
		if _, err := d.Read("x"); !errors.Is(err, env.ErrIO) {
			t.Fatalf("Read = %v, want ErrIO", err)
		}
	})
	t.Run("a non-positive count is rejected and changes nothing", func(t *testing.T) {
		h := oneNode(t, 1)
		for _, c := range []int{0, -1, math.MinInt} {
			for _, name := range []string{"", "x"} {
				if err := h.s.FailReads("a", name, c); !errors.Is(err, env.ErrInvalidArgument) {
					t.Fatalf("FailReads(%q, %d) = %v", name, c, err)
				}
			}
		}
		if names, any := sim.ReadFaults(h.s.Disk("a")); len(names) != 0 || any != 0 {
			t.Fatalf("pending read faults %v %d", names, any)
		}
	})
}

// TestIncarnationCounterNeverWraps proves that Restart, WipeRejoin, and HealAll
// refuse to advance a node past incarnation math.MaxUint32.
func TestIncarnationCounterNeverWraps(t *testing.T) {
	for _, op := range []string{"restart", "wipe-rejoin", "heal-all"} {
		t.Run(op, func(t *testing.T) {
			h := oneNode(t, 1)
			sim.SetIncarnation(h.s, "a", math.MaxUint32)
			mustNil(t, h.s.Crash("a"))
			var err error
			switch op {
			case "restart":
				err = h.s.Restart("a")
			case "wipe-rejoin":
				err = h.s.WipeRejoin("a")
			case "heal-all":
				h.s.HealAll()
			}
			if op != "heal-all" && !errors.Is(err, env.ErrInvalidArgument) {
				t.Fatalf("%s at the last incarnation = %v, want ErrInvalidArgument", op, err)
			}
			st := h.s.Status("a")
			if st.Up || st.Incarnation != math.MaxUint32 {
				t.Fatalf("status after %s = %+v", op, st)
			}
			mustNil(t, h.s.RunUntilQuiet())
		})
	}
}

// TestHugeLatencyIsDrawnWithoutOverflow proves that latency ranges up to
// math.MaxInt64 draw cleanly without signed overflow or backward timestamps.
func TestHugeLatencyIsDrawnWithoutOverflow(t *testing.T) {
	for _, r := range []sim.Range{
		{Min: 0, Max: math.MaxInt64},
		{Min: math.MaxInt64 - 1, Max: math.MaxInt64},
		{Min: math.MaxInt64, Max: math.MaxInt64},
		{Min: time.Duration(sim.Horizon), Max: math.MaxInt64},
	} {
		for seed := int64(1); seed <= 20; seed++ {
			nc := fixedNet()
			nc.Latency = r
			h := newHarness(t, sim.Config{Seed: seed, Net: nc}, spec("a", "r"), spec("b", "r"))
			h.s.At(5, "send", func() {
				mustNil(t, h.rec("a").e.Net.Send("b", []byte("x")))
				h.rec("a").request("b", "q", time.Second)
			})
			if err := h.s.RunUntilQuiet(); err != nil {
				t.Fatalf("latency %v seed %d: %v", r, seed, err)
			}
			if now := h.s.Now(); now < 5 || now > sim.Horizon {
				t.Fatalf("latency %v seed %d: virtual time %d outside [5, Horizon]", r, seed, now)
			}
			for _, d := range h.rec("b").datagrams {
				if d.at < 5 {
					t.Fatalf("latency %v seed %d: datagram delivered at %d before it was sent", r, seed, d.at)
				}
			}
		}
	}
}

// TestLatencyBeyondTheHorizonIsNeverDelivered proves that a message whose
// delivery time lands past Horizon is dropped rather than delivered early.
func TestLatencyBeyondTheHorizonIsNeverDelivered(t *testing.T) {
	nc := fixedNet()
	nc.Latency = sim.Range{Min: math.MaxInt64, Max: math.MaxInt64}
	h := newHarness(t, sim.Config{Seed: 1, Net: nc}, spec("a", "r"), spec("b", "r"))
	h.s.At(5, "send", func() { mustNil(t, h.rec("a").e.Net.Send("b", []byte("x"))) })
	mustNil(t, h.s.RunUntilQuiet())
	mustNil(t, h.s.RunUntil(sim.Horizon))
	if got := h.rec("b").datagrams; len(got) != 0 {
		t.Fatalf("delivered %v", got)
	}
	if h.s.Now() != sim.Horizon {
		t.Fatalf("now %d, want Horizon", h.s.Now())
	}
}

// TestVirtualTimeSaturatesAtTheHorizon proves Horizon clamping and overflow-free
// saturation across RunFor, RunUntil, At, Workload, ScheduleFromBytes, and
// StartNemesis.
func TestVirtualTimeSaturatesAtTheHorizon(t *testing.T) {
	t.Run("RunFor past the horizon stops at the horizon", func(t *testing.T) {
		s := sim.New(sim.Config{Seed: 1})
		mustNil(t, s.RunUntil(10))
		mustNil(t, s.RunFor(math.MaxInt64))
		if s.Now() != sim.Horizon {
			t.Fatalf("now %d, want %d", s.Now(), sim.Horizon)
		}
		mustNil(t, s.RunUntil(math.MaxInt64))
		if s.Now() != sim.Horizon {
			t.Fatalf("now %d, want %d", s.Now(), sim.Horizon)
		}
	})
	t.Run("an action past the horizon never runs", func(t *testing.T) {
		s := sim.New(sim.Config{Seed: 1})
		ran := false
		s.At(sim.Horizon+1, "late", func() { ran = true })
		s.At(sim.Horizon, "last", func() {})
		mustNil(t, s.RunUntilQuiet())
		if ran || s.Now() != sim.Horizon {
			t.Fatalf("ran %v now %d", ran, s.Now())
		}
	})
	t.Run("a workload gap past the horizon never runs early", func(t *testing.T) {
		s := sim.New(sim.Config{Seed: 1})
		var at []env.Time
		s.Workload(sim.Horizon-1, 3, math.MaxInt64, func(int) { at = append(at, s.Now()) })
		mustNil(t, s.RunUntilQuiet())
		for i := 1; i < len(at); i++ {
			if at[i] <= at[i-1] {
				t.Fatalf("workload times %v are not increasing", at)
			}
		}
		if len(at) == 0 || at[0] != sim.Horizon-1 {
			t.Fatalf("workload times %v", at)
		}
	})
	t.Run("ScheduleFromBytes with a huge step never wraps", func(t *testing.T) {
		s := sim.New(sim.Config{Seed: 1})
		mustNil(t, s.AddNode(sim.NodeSpec{ID: "a"}, newPanicky))
		sched := s.ScheduleFromBytes([]byte{15, 0, 0, 0, 15, 0, 0, 0}, 1, math.MaxInt64)
		for i, tf := range sched {
			if tf.At < 1 || i > 0 && tf.At < sched[i-1].At {
				t.Fatalf("schedule times wrapped: %v", sched)
			}
		}
	})
	t.Run("a nemesis window past the horizon is rejected", func(t *testing.T) {
		s := sim.New(sim.Config{Seed: 1})
		for _, cfg := range []sim.NemesisConfig{
			{Start: sim.Horizon, Phases: 1, Chaos: 1, MaxGap: 1},
			{Start: 0, Phases: 2, Chaos: math.MaxInt64, MaxGap: 1},
			{Start: 0, Phases: math.MaxInt, Chaos: time.Second, Heal: time.Second, MaxGap: 1},
			{Start: 0, Phases: 1, Chaos: time.Second, Heal: math.MaxInt64, MaxGap: 1},
		} {
			if _, err := s.StartNemesis(cfg); !errors.Is(err, env.ErrInvalidArgument) {
				t.Fatalf("%+v: err = %v", cfg, err)
			}
		}
		n, err := s.StartNemesis(sim.NemesisConfig{Start: sim.Horizon - 2, Phases: 1, Chaos: 1, Heal: 1, MaxGap: math.MaxInt64})
		mustNil(t, err)
		mustNil(t, s.RunUntilQuiet())
		if n.QuietAt() != sim.Horizon-1 {
			t.Fatalf("QuietAt %d", n.QuietAt())
		}
	})
}

// TestSkewIsBoundedSoLocalTimeNeverOverflows proves that AddNode rejects skew
// outside ±MaxSkew and that local time stays monotonic across [0, Horizon] at
// all four (±MaxSkew, ±MaxDriftPPM) corners.
func TestSkewIsBoundedSoLocalTimeNeverOverflows(t *testing.T) {
	s := sim.New(sim.Config{Seed: 1})
	for _, sk := range []time.Duration{sim.MaxSkew + 1, -sim.MaxSkew - 1, math.MaxInt64, math.MinInt64} {
		if err := s.AddNode(sim.NodeSpec{ID: "bad", Skew: sk}, newPanicky); !errors.Is(err, env.ErrInvalidArgument) {
			t.Fatalf("skew %d: err = %v", sk, err)
		}
	}
	type extreme struct {
		id    env.NodeID
		skew  time.Duration
		drift int64
	}
	nodes := []extreme{
		{"fast", sim.MaxSkew, sim.MaxDriftPPM},
		{"slow", -sim.MaxSkew, -sim.MaxDriftPPM},
		{"ahead-slow", sim.MaxSkew, -sim.MaxDriftPPM},
		{"behind-fast", -sim.MaxSkew, sim.MaxDriftPPM},
	}
	for _, x := range nodes {
		mustNil(t, s.AddNode(sim.NodeSpec{ID: x.id, Skew: x.skew, DriftPPM: x.drift}, newPanicky))
	}
	last := map[env.NodeID]env.Time{}
	for _, x := range nodes {
		last[x.id] = math.MinInt64
	}
	for _, g := range []env.Time{0, 1, 999_999, 1_000_000, sim.Horizon / 3, sim.Horizon - 1, sim.Horizon} {
		mustNil(t, s.RunUntil(g))
		for _, x := range nodes {
			got := s.LocalTime(x.id)
			if got < last[x.id] {
				t.Fatalf("%s: local time went from %d to %d at global %d", x.id, last[x.id], got, g)
			}
			last[x.id] = got
		}
	}
	if got := s.LocalTime("fast"); got <= sim.Horizon {
		t.Fatalf("fast node at the horizon reads %d", got)
	}
}

// TestTimersBeyondTheHorizonNeverFire proves that timers and request timeouts
// due past Horizon never fire early, report pending on Stop, and fire normally
// when due exactly at Horizon.
func TestTimersBeyondTheHorizonNeverFire(t *testing.T) {
	for _, drift := range []int64{0, -sim.MaxDriftPPM, sim.MaxDriftPPM} {
		h := newHarness(t, sim.Config{Seed: 1, Net: fixedNet()}, sim.NodeSpec{ID: "a", DriftPPM: drift}, spec("b", "r"))
		c := h.rec("a").e.Clock
		fired := map[string]env.Time{}
		arm := func(name string, d time.Duration) env.Timer {
			return c.AfterFunc(d, func() { fired[name] = h.s.Now() })
		}
		arm("horizon+1", time.Duration(sim.Horizon)+1)
		arm("max", math.MaxInt64)
		stopped := arm("stopped", math.MaxInt64)
		mustNil(t, h.s.Partition([]env.NodeID{"a"}, nil))
		h.rec("a").request("b", "q", math.MaxInt64)
		mustNil(t, h.s.RunUntil(sim.Horizon))
		mustNil(t, h.s.RunUntilQuiet())
		if drift == 0 {
			if len(fired) != 0 {
				t.Fatalf("drift %d: timers beyond the horizon fired: %v", drift, fired)
			}
		} else if _, ok := fired["max"]; ok {
			t.Fatalf("drift %d: a MaxInt64 timer fired at %d", drift, fired["max"])
		}
		if got := h.rec("a").replies; len(got) != 0 {
			t.Fatalf("drift %d: request with a timeout beyond the horizon completed: %v", drift, got)
		}
		if !stopped.Stop() {
			t.Fatalf("drift %d: Stop of a timer beyond the horizon reported nothing pending", drift)
		}
		if h.s.Now() > sim.Horizon {
			t.Fatalf("drift %d: now %d past the horizon", drift, h.s.Now())
		}
	}
	h := newHarness(t, sim.Config{Seed: 1}, spec("a", "r"))
	var at env.Time = -1
	h.rec("a").e.Clock.AfterFunc(time.Duration(sim.Horizon), func() { at = h.s.Now() })
	mustNil(t, h.s.RunUntilQuiet())
	if at != sim.Horizon {
		t.Fatalf("a timer due exactly at the horizon fired at %d", at)
	}
}

// TestScaledHandlesEveryBoundary proves saturating multiplication in scaled
// across zero, ±1, ±2, MaxInt64, and MinInt64 boundaries.
func TestScaledHandlesEveryBoundary(t *testing.T) {
	tests := []struct {
		name string
		k    int64
		d    time.Duration
		want time.Duration
	}{
		{"zero k", 0, math.MinInt64, 0},
		{"zero d", math.MaxInt64, 0, 0},
		{"one times one", 1, 1, 1},
		{"max k times one", math.MaxInt64, 1, math.MaxInt64},
		{"positive saturates", math.MaxInt64/2 + 1, 2, math.MaxInt64},
		{"positive fits", math.MaxInt64 / 2, 2, math.MaxInt64 - 1},
		{"one times minus one", 1, -1, -1},
		{"fifteen times minus one", 15, -1, -15},
		{"max k times minus one", math.MaxInt64, -1, -math.MaxInt64},
		{"minus two fits", math.MaxInt64 / 2, -2, -(math.MaxInt64 - 1)},
		{"minus two reaches min", math.MaxInt64/2 + 1, -2, math.MinInt64},
		{"minus two saturates", math.MaxInt64/2 + 2, -2, math.MinInt64},
		{"one times min", 1, math.MinInt64, math.MinInt64},
		{"two times min saturates", 2, math.MinInt64, math.MinInt64},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sim.Scaled(tc.k, tc.d); got != tc.want {
				t.Fatalf("scaled(%d, %d) = %d, want %d", tc.k, tc.d, got, tc.want)
			}
		})
	}
}

// TestScheduleFromBytesWithStepMinusOne proves that a negative step of -1
// subtracts cleanly instead of saturating to math.MinInt64.
func TestScheduleFromBytesWithStepMinusOne(t *testing.T) {
	s := sim.New(sim.Config{Seed: 1})
	mustNil(t, s.AddNode(sim.NodeSpec{ID: "a"}, newPanicky))
	sched := s.ScheduleFromBytes([]byte{1, 0, 0, 0}, 100, -1)
	if len(sched) != 1 {
		t.Fatalf("schedule has %d faults, want 1", len(sched))
	}
	if sched[0].At != 99 {
		t.Fatalf("first fault at %d, want 99", sched[0].At)
	}
}
