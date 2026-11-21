package sim_test

import (
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtoy"
)

func scheduleRun(data []byte) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		s := sim.New(sim.Config{Seed: seed})
		ids := sim.NodeIDs(5)
		if err := s.AddCluster(ids, 3, simtoy.Factory(simtoy.Config{Peers: ids})); err != nil {
			s.Fail(err)
			return s
		}
		simtoy.AddInvariants(s, 1)
		sched := s.ScheduleFromBytes(data, env.Time(time.Second), 50*time.Millisecond)
		quiet := s.InjectSchedule(sched)
		s.InjectAt(quiet, sim.Fault{Kind: sim.FaultHealAll})
		_ = s.RunUntilConverged(quiet.Add(time.Minute), func() bool {
			return s.Now() >= quiet && simtoy.Converged(s)
		})
		return s
	}
}

// FuzzSimSchedule proves that arbitrary fuzz bytes decoded into a fault
// schedule preserve replay determinism, toy invariants, and post-heal convergence.
func FuzzSimSchedule(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte{1, 1, 0, 0})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{255, 255, 255, 255, 255, 255, 255, 255, 255})
	for k := 0; k < 20; k++ {
		f.Add([]byte{3, byte(k), byte(k * 7), byte(k * 13), 9, byte(k + 1), 2, 4})
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			data = data[:4096]
		}
		simtest.RequireDeterministic(t, 1, scheduleRun(data))
	})
}

// TestScheduleFromBytes proves that decoding bytes into TimedFaults is
// deterministic, produces non-decreasing timestamps from start, and handles empty input.
func TestScheduleFromBytes(t *testing.T) {
	s := sim.New(sim.Config{Seed: 1})
	ids := sim.NodeIDs(4)
	mustNil(t, s.AddCluster(ids, 2, simtoy.Factory(simtoy.Config{Peers: ids})))
	data := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23}
	a := s.ScheduleFromBytes(data, 100, time.Millisecond)
	b := s.ScheduleFromBytes(data, 100, time.Millisecond)
	if len(a) == 0 || len(a) != len(b) {
		t.Fatalf("schedules %d %d", len(a), len(b))
	}
	for i := range a {
		if a[i].At != b[i].At || a[i].Fault.String() != b[i].Fault.String() {
			t.Fatalf("decode not deterministic at %d", i)
		}
		if i > 0 && a[i].At < a[i-1].At {
			t.Fatalf("times not ordered: %v", a)
		}
		if a[i].At < 100 {
			t.Fatalf("time before start: %v", a[i])
		}
	}
	if len(s.ScheduleFromBytes(nil, 0, time.Millisecond)) != 0 {
		t.Fatal("empty input gave faults")
	}
}
