package sim_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

func TestTimers(t *testing.T) {
	tests := []struct {
		name string
		act  func(c env.Clock, fired *[]env.Time) env.Timer
		then func(tm env.Timer)
		want []env.Time
	}{
		{"a timer fires once after its duration", func(c env.Clock, fired *[]env.Time) env.Timer {
			return c.AfterFunc(50, func() { *fired = append(*fired, c.Now()) })
		}, nil, []env.Time{50}},
		{"a stopped timer never fires", func(c env.Clock, fired *[]env.Time) env.Timer {
			return c.AfterFunc(50, func() { *fired = append(*fired, c.Now()) })
		}, func(tm env.Timer) {
			if !tm.Stop() {
				panic("Stop of a pending timer returned false")
			}
			if tm.Stop() {
				panic("second Stop returned true")
			}
		}, nil},
		{"a reset timer fires at the new time only", func(c env.Clock, fired *[]env.Time) env.Timer {
			return c.AfterFunc(50, func() { *fired = append(*fired, c.Now()) })
		}, func(tm env.Timer) {
			if !tm.Reset(80) {
				panic("Reset of a pending timer returned false")
			}
		}, []env.Time{80}},
		{"a stopped timer can be reset and fires", func(c env.Clock, fired *[]env.Time) env.Timer {
			return c.AfterFunc(50, func() { *fired = append(*fired, c.Now()) })
		}, func(tm env.Timer) {
			tm.Stop()
			if tm.Reset(20) {
				panic("Reset of a stopped timer returned true")
			}
		}, []env.Time{20}},
		{"a zero or negative duration fires at the current time", func(c env.Clock, fired *[]env.Time) env.Timer {
			return c.AfterFunc(-5, func() { *fired = append(*fired, c.Now()) })
		}, nil, []env.Time{0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, sim.Config{Seed: 1}, spec("a", "r"))
			var fired []env.Time
			tm := tc.act(h.rec("a").e.Clock, &fired)
			if tc.then != nil {
				tc.then(tm)
			}
			mustNil(t, h.s.RunUntilQuiet())
			if len(fired) != len(tc.want) {
				t.Fatalf("fired %v, want %v", fired, tc.want)
			}
			for i := range fired {
				if fired[i] != tc.want[i] {
					t.Fatalf("fired %v, want %v", fired, tc.want)
				}
			}
		})
	}
}

func TestTimerResetAfterFiringRearms(t *testing.T) {
	h := newHarness(t, sim.Config{Seed: 1}, spec("a", "r"))
	c := h.rec("a").e.Clock
	n := 0
	var tm env.Timer
	tm = c.AfterFunc(10, func() {
		n++
		if n < 3 {
			tm.Reset(10)
		}
	})
	mustNil(t, h.s.RunUntilQuiet())
	if n != 3 || h.s.Now() != 30 {
		t.Fatalf("fired %d times, now %d", n, h.s.Now())
	}
}

func TestClockSkewAndDrift(t *testing.T) {
	tests := []struct {
		name      string
		skew      time.Duration
		drift     int64
		global    env.Time
		wantLocal env.Time
		timer     time.Duration
		wantFire  env.Time
	}{
		{"no skew and no drift reads global time", 0, 0, 1_000_000, 1_000_000, 1000, 1_001_000},
		{"a positive skew shifts local time", 500, 0, 1_000_000, 1_000_500, 1000, 1_001_000},
		{"a negative skew shifts local time back", -500, 0, 1_000_000, 999_500, 1000, 1_001_000},
		{"a fast clock gains drift ppm", 0, 100_000, 1_000_000, 1_100_000, 1100, 1_001_000},
		{"a slow clock loses drift ppm", 0, -100_000, 1_000_000, 900_000, 900, 1_001_000},
		{"a fast clock rounds timer delays up", 0, 300_000, 0, 0, 10, 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, sim.Config{Seed: 1}, sim.NodeSpec{ID: "a", Skew: tc.skew, DriftPPM: tc.drift})
			h.run(time.Duration(tc.global))
			c := h.rec("a").e.Clock
			if got := c.Now(); got != tc.wantLocal {
				t.Fatalf("local Now = %d, want %d", got, tc.wantLocal)
			}
			if got := h.s.LocalTime("a"); got != tc.wantLocal {
				t.Fatalf("LocalTime = %d, want %d", got, tc.wantLocal)
			}
			var fired env.Time = -1
			c.AfterFunc(tc.timer, func() { fired = h.s.Now() })
			mustNil(t, h.s.RunUntilQuiet())
			if fired != tc.wantFire {
				t.Fatalf("timer fired at global %d, want %d", fired, tc.wantFire)
			}
		})
	}
}

func TestAddNodeValidation(t *testing.T) {
	s := sim.New(sim.Config{Seed: 1})
	mustNil(t, s.AddNode(sim.NodeSpec{ID: "a"}, newPanicky))
	tests := []struct {
		name string
		spec sim.NodeSpec
		f    sim.Factory
		want error
	}{
		{"a duplicate id", sim.NodeSpec{ID: "a"}, newPanicky, sim.ErrDuplicateNode},
		{"an empty id", sim.NodeSpec{ID: ""}, newPanicky, env.ErrInvalidArgument},
		{"a nil factory", sim.NodeSpec{ID: "c"}, nil, env.ErrInvalidArgument},
		{"drift beyond the limit", sim.NodeSpec{ID: "d", DriftPPM: sim.MaxDriftPPM + 1}, newPanicky, env.ErrInvalidArgument},
		{"drift below the limit", sim.NodeSpec{ID: "e", DriftPPM: -sim.MaxDriftPPM - 1}, newPanicky, env.ErrInvalidArgument},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.AddNode(tc.spec, tc.f); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}
