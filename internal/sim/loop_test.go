package sim_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/sim"
)

// TestEventOrdering proves that the queue runs events in ascending timestamp
// order and breaks equal-timestamp ties in FIFO insertion order.
func TestEventOrdering(t *testing.T) {
	tests := []struct {
		name  string
		times []env.Time
		want  []int
	}{
		{"events run in time order", []env.Time{30, 10, 20}, []int{1, 2, 0}},
		{"equal-time events run in insertion order", []env.Time{5, 5, 5, 5}, []int{0, 1, 2, 3}},
		{"ties break FIFO among mixed times", []env.Time{7, 3, 7, 3}, []int{1, 3, 0, 2}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := sim.New(sim.Config{Seed: 1})
			var order []int
			for i, at := range tc.times {
				s.At(at, "act", func() { order = append(order, i) })
			}
			mustNil(t, s.RunUntilQuiet())
			if len(order) != len(tc.want) {
				t.Fatalf("order = %v", order)
			}
			for i := range order {
				if order[i] != tc.want[i] {
					t.Fatalf("order = %v, want %v", order, tc.want)
				}
			}
		})
	}
}

// TestEventScheduledInThePastRunsNow proves that scheduling an event with a
// timestamp before Sim.Now executes it at Sim.Now.
func TestEventScheduledInThePastRunsNow(t *testing.T) {
	s := sim.New(sim.Config{Seed: 1})
	var at env.Time
	s.At(100, "first", func() {
		s.At(50, "late", func() { at = s.Now() })
	})
	mustNil(t, s.RunUntilQuiet())
	if at != 100 {
		t.Fatalf("late event ran at %d, want 100", at)
	}
}

// TestStepAndRunUntil proves Step, RunUntil, RunFor, empty-queue completion,
// and Steps counting.
func TestStepAndRunUntil(t *testing.T) {
	s := sim.New(sim.Config{Seed: 1})
	ran := 0
	for _, at := range []env.Time{10, 20, 30} {
		s.At(at, "tick", func() { ran++ })
	}
	ok, err := s.Step()
	if !ok || err != nil || ran != 1 || s.Now() != 10 {
		t.Fatalf("Step = %v %v, ran=%d now=%d", ok, err, ran, s.Now())
	}
	mustNil(t, s.RunUntil(25))
	if ran != 2 || s.Now() != 25 {
		t.Fatalf("RunUntil(25): ran=%d now=%d", ran, s.Now())
	}
	mustNil(t, s.RunFor(10))
	if ran != 3 || s.Now() != 35 {
		t.Fatalf("RunFor(10): ran=%d now=%d", ran, s.Now())
	}
	ok, err = s.Step()
	if ok || err != nil {
		t.Fatalf("Step on empty queue = %v %v", ok, err)
	}
	if s.Steps() != 3 {
		t.Fatalf("Steps = %d", s.Steps())
	}
}

// TestStallDetection proves that empty queue, step-budget exhaustion, and
// missed deadlines produce sticky *StallErrors carrying the seed and reason.
func TestStallDetection(t *testing.T) {
	tests := []struct {
		name    string
		run     func(s *sim.Sim) error
		wantErr error
	}{
		{"queue empty and not converged", func(s *sim.Sim) error {
			s.At(10, "only", func() {})
			return s.RunUntilConverged(1000, func() bool { return false })
		}, sim.ErrQueueEmpty},
		{"budget exhausted by a periodic event", func(s *sim.Sim) error {
			var tick func()
			tick = func() { s.At(s.Now()+1, "tick", tick) }
			s.At(0, "tick", tick)
			return s.RunUntilQuiet()
		}, sim.ErrBudgetExhausted},
		{"deadline passes before convergence", func(s *sim.Sim) error {
			var tick func()
			tick = func() { s.At(s.Now()+10, "tick", tick) }
			s.At(0, "tick", tick)
			return s.RunUntilConverged(100, func() bool { return false })
		}, sim.ErrDeadline},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := sim.New(sim.Config{Seed: 9, MaxSteps: 500})
			err := tc.run(s)
			if !errors.Is(err, sim.ErrStall) || !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want stall %v", err, tc.wantErr)
			}
			var se *sim.StallError
			if !errors.As(err, &se) || se.Seed != 9 {
				t.Fatalf("not a StallError with the seed: %#v", err)
			}
			if !errors.Is(s.Err(), tc.wantErr) {
				t.Fatalf("stall is not sticky: %v", s.Err())
			}
			if _, err := s.Step(); !errors.Is(err, tc.wantErr) {
				t.Fatalf("Step after stall = %v", err)
			}
		})
	}
}

// TestConvergedReturnsNil proves that RunUntilConverged stops immediately once
// its predicate becomes true without draining later events.
func TestConvergedReturnsNil(t *testing.T) {
	s := sim.New(sim.Config{Seed: 1})
	done := false
	s.At(10, "set", func() { done = true })
	s.At(20, "after", func() {})
	mustNil(t, s.RunUntilConverged(100, func() bool { return done }))
	if s.Now() != 10 {
		t.Fatalf("stopped at %d, want 10", s.Now())
	}
}

type panicky struct{ env.Env }

func (p *panicky) Start()                                  {}
func (p *panicky) OnDatagram(env.NodeID, []byte)           { panic("boom in node") }
func (p *panicky) OnRequest(env.NodeID, []byte, env.Reply) {}
func (p *panicky) EncodeState() []byte                     { return nil }
func newPanicky(e env.Env) env.Process                     { return &panicky{e} }
func (p *panicky) send(to env.NodeID) error                { return p.Net.Send(to, []byte("x")) }
func asPanicky(s *sim.Sim, id env.NodeID) *panicky         { return s.Process(id).(*panicky) }
func addPanicky(t *testing.T, s *sim.Sim, id env.NodeID) {
	mustNil(t, s.AddNode(sim.NodeSpec{ID: id}, newPanicky))
}
func startAll(t *testing.T, s *sim.Sim) { mustNil(t, s.RunUntil(s.Now())) }
func expectPanic(t *testing.T, err error, node env.NodeID) {
	t.Helper()
	var pe *sim.PanicError
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want PanicError", err)
	}
	if pe.Node != node || pe.Seed != 4 || !strings.Contains(pe.Error(), "boom") || len(pe.Stack) == 0 {
		t.Fatalf("PanicError = %+v", pe)
	}
}

// TestNodePanicFailsTheRunWithTheSeed proves that a panic inside a node handler
// is caught and returned as a sticky *PanicError naming the seed, node, and stack.
func TestNodePanicFailsTheRunWithTheSeed(t *testing.T) {
	s := sim.New(sim.Config{Seed: 4, Net: fixedNet()})
	addPanicky(t, s, "a")
	addPanicky(t, s, "b")
	startAll(t, s)
	mustNil(t, asPanicky(s, "a").send("b"))
	err := s.RunUntilQuiet()
	expectPanic(t, err, "b")
	expectPanic(t, s.Err(), "b")
	if !strings.Contains(err.Error(), "seed 4") {
		t.Fatalf("error does not name the seed: %v", err)
	}
}

// TestPanicInActionAndInvariantIsReported proves that panics in At actions and
// invariant functions are caught as *PanicErrors.
func TestPanicInActionAndInvariantIsReported(t *testing.T) {
	s := sim.New(sim.Config{Seed: 4})
	s.At(1, "bad action", func() { panic("boom in action") })
	expectPanic(t, s.RunUntilQuiet(), "")

	s2 := sim.New(sim.Config{Seed: 4})
	s2.AddInvariant("explodes", 1, func() error { panic("boom in invariant") })
	s2.At(1, "x", func() {})
	expectPanic(t, s2.RunUntilQuiet(), "")
}

// TestPanicInABudgetCallbackFailsTheRun proves that a panic inside Budget.GroupOf
// during a nemesis step never escapes the loop and becomes a sticky *PanicError.
func TestPanicInABudgetCallbackFailsTheRun(t *testing.T) {
	for _, kind := range []sim.FaultKind{sim.FaultBitRot, sim.FaultReadError, sim.FaultWipeDisk, sim.FaultCrash} {
		t.Run(kind.String(), func(t *testing.T) {
			h := newHarness(t, sim.Config{Seed: 4, Net: fixedNet()}, spec("a", "r"))
			d := h.rec("a").e.Disk
			mustNil(t, d.Write("x", []byte{1}))
			mustNil(t, d.Sync("x"))
			b := sim.Budget{MaxDamagedDisks: 1, MaxDamagedPerGroup: 1, GroupOf: func(env.NodeID, string) string { panic("boom in GroupOf") }}
			mustNil(t, h.s.BitRot("a", "x"))
			_, err := h.s.StartNemesis(sim.NemesisConfig{Start: 1, Phases: 1, Chaos: 2, Heal: 1, MaxGap: 1, Kinds: []sim.FaultKind{kind}, Budget: b})
			mustNil(t, err)
			var escaped any
			func() {
				defer func() { escaped = recover() }()
				err = h.s.RunUntilQuiet()
			}()
			if escaped != nil {
				t.Fatalf("panic escaped the loop: %v", escaped)
			}
			expectPanic(t, err, "")
			expectPanic(t, h.s.Err(), "")
			if !strings.Contains(err.Error(), "nemesis") {
				t.Fatalf("error does not name the nemesis: %v", err)
			}
		})
	}
}

// TestFailIsSticky proves that calling Sim.Fail stops subsequent events and
// causes both RunUntilQuiet and Verify to return the recorded error.
func TestFailIsSticky(t *testing.T) {
	s := sim.New(sim.Config{Seed: 1})
	want := errors.New("scenario says no")
	s.At(1, "x", func() { s.Fail(want) })
	s.At(2, "y", func() { t.Fatal("ran after Fail") })
	if err := s.RunUntilQuiet(); !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(s.Verify(), want) {
		t.Fatalf("Verify = %v", s.Verify())
	}
}

// TestInvariants proves periodic invariant execution, error stopping,
// InvariantRuns counting, Verify, and immediate CheckInvariants.
func TestInvariants(t *testing.T) {
	t.Run("an invariant runs every N events and its error stops the run", func(t *testing.T) {
		s := sim.New(sim.Config{Seed: 2})
		calls := 0
		s.AddInvariant("count", 3, func() error {
			calls++
			if calls == 2 {
				return errors.New("second check fails")
			}
			return nil
		})
		for i := 1; i <= 10; i++ {
			s.At(env.Time(i), "e", func() {})
		}
		err := s.RunUntilQuiet()
		var ie *sim.InvariantError
		if !errors.As(err, &ie) || ie.Name != "count" || ie.Steps != 6 || !errors.Is(err, sim.ErrInvariant) {
			t.Fatalf("err = %#v", err)
		}
	})
	t.Run("Verify passes when every invariant ran as often as expected", func(t *testing.T) {
		s := sim.New(sim.Config{Seed: 2})
		s.AddInvariant("ok", 2, func() error { return nil })
		for i := 1; i <= 5; i++ {
			s.At(env.Time(i), "e", func() {})
		}
		mustNil(t, s.RunUntilQuiet())
		mustNil(t, s.Verify())
		if s.InvariantRuns("ok") != 2 {
			t.Fatalf("runs = %d", s.InvariantRuns("ok"))
		}
	})
	t.Run("CheckInvariants runs every invariant now", func(t *testing.T) {
		s := sim.New(sim.Config{Seed: 2})
		s.AddInvariant("bad", 1000, func() error { return errors.New("bad") })
		if err := s.CheckInvariants(); !errors.Is(err, sim.ErrInvariant) {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestScheduleHelpers proves that Sim.Workload schedules the requested count
// of actions spaced within [1, maxGap] on the workload stream.
func TestScheduleHelpers(t *testing.T) {
	s := sim.New(sim.Config{Seed: 3})
	var seen []int
	var times []env.Time
	s.Workload(100, 5, 10*time.Millisecond, func(i int) {
		seen = append(seen, i)
		times = append(times, s.Now())
		_ = s.WorkloadRand().IntN(10)
	})
	mustNil(t, s.RunUntilQuiet())
	if len(seen) != 5 {
		t.Fatalf("workload ran %d actions", len(seen))
	}
	for i := 1; i < len(times); i++ {
		if times[i] <= times[i-1] || times[i]-times[i-1] > env.Time(10*time.Millisecond) {
			t.Fatalf("workload times %v", times)
		}
	}
}
