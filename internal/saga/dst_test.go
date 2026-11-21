package saga_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/saga/sagasim"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
)

const (
	nemesisStart    = env.Time(300 * time.Millisecond)
	nemesisPhases   = 3
	nemesisChaos    = 4 * time.Second
	nemesisHeal     = 2 * time.Second
	nemesisMaxGap   = 400 * time.Millisecond
	nemesisConverge = 60 * time.Second
	nemesisWrites   = 24
	nemesisWriteGap = 600 * time.Millisecond
	nemesisTick     = 100 * time.Millisecond
	nemesisMinSagas = 3
	duelConverge    = 90 * time.Second
	duelMaxStagger  = 300 * time.Millisecond
	duelMinPart     = 2 * time.Second
	duelMaxPart     = 6 * time.Second
)

func TestMigrationFailurePointsDST(t *testing.T) {
	h := sagasim.ModelHarness()
	cells := sagasim.Cells()
	if len(cells) != sagasim.MatrixCells {
		t.Fatalf("%d cells, want %d", len(cells), sagasim.MatrixCells)
	}
	for _, c := range cells {
		t.Run(c.Name(), func(t *testing.T) {
			t.Parallel()
			simtest.Sweep(t, func(t *testing.T, seed int64) {
				rep := &sagasim.Report{}
				simtest.RequireDeterministic(t, seed, sagasim.Run(h, c, sagasim.RunOptions{Report: rep}))
				if rep.State != c.Want() {
					t.Fatalf("terminal state %v, want %v", rep.State, c.Want())
				}
				if rep.Elapsed > sagasim.MatrixBound {
					t.Fatalf("took %v, bound %v", rep.Elapsed, sagasim.MatrixBound)
				}
				t.Logf("MATRIX %s seed=%d state=%v elapsed=%v crashes=%d calls=%v", c.Name(), seed, rep.State, rep.Elapsed, rep.Crashes, rep.Calls)
			})
		})
	}
}

func TestMatrixCellsCoverEveryFailurePoint(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range sagasim.Cells() {
		if seen[c.Name()] {
			t.Fatalf("duplicate cell %s", c.Name())
		}
		seen[c.Name()] = true
	}
	for step := 0; step < sagasim.MigrationSteps; step++ {
		for _, k := range []sagasim.FailureKind{sagasim.FailBeforeAction, sagasim.FailAfterAction, sagasim.CrashBeforeAction, sagasim.CrashAfterAction} {
			for _, torn := range []bool{false, true} {
				c := sagasim.Cell{Step: step, Kind: k, Torn: torn}
				if !seen[c.Name()] {
					t.Fatalf("missing cell %s", c.Name())
				}
			}
		}
		if step < saga.MigrationPivot {
			for _, k := range []sagasim.FailureKind{sagasim.FailMidCompensation, sagasim.CrashMidCompensation} {
				c := sagasim.Cell{Step: step, Kind: k, Torn: true}
				if !seen[c.Name()] {
					t.Fatalf("missing cell %s", c.Name())
				}
			}
		}
	}
	for _, k := range []sagasim.FailureKind{0, sagasim.FailBeforeAction, sagasim.CrashMidCompensation, 99} {
		if k.String() == "" {
			t.Fatalf("kind %d has no name", k)
		}
	}
}

func TestCanarySkippedCompensation(t *testing.T) {
	h := sagasim.ModelHarness()
	cell := sagasim.Cell{Step: saga.MigrationPivot, Kind: sagasim.FailBeforeAction}
	if cell.Want() != saga.StateCompensated {
		t.Fatalf("canary cell %s does not compensate", cell.Name())
	}
	plant := func(cfg *saga.Config) { saga.PlantSkippedCompensation(cfg, 1) }
	for _, step := range []int{0, 2} {
		other := func(cfg *saga.Config) { saga.PlantSkippedCompensation(cfg, step) }
		_, err := simtest.CheckDeterministic(1, sagasim.Run(h, cell, sagasim.RunOptions{Configure: other}))
		if step == 0 && !errors.Is(err, sagasim.ErrCheck) {
			t.Fatalf("skipping the allocate compensation went unnoticed: %v", err)
		}
		if step == 2 && err != nil {
			t.Fatalf("skipping the verify compensation, which undoes nothing, failed: %v", err)
		}
	}
	for _, seed := range []int64{1, 2, 3} {
		if _, err := simtest.CheckDeterministic(seed, sagasim.Run(h, cell, sagasim.RunOptions{})); err != nil {
			t.Fatalf("seed %d: the clean cell fails: %v", seed, err)
		}
		_, err := simtest.CheckDeterministic(seed, sagasim.Run(h, cell, sagasim.RunOptions{Configure: plant}))
		if err == nil {
			t.Fatalf("seed %d: skipping the copy compensation went unnoticed", seed)
		}
		if !errors.Is(err, sagasim.ErrCheck) {
			t.Fatalf("seed %d: failed for another reason: %v", seed, err)
		}
	}
}

func nemesisKinds() []sim.FaultKind {
	var out []sim.FaultKind
	for _, k := range sim.ChaosKinds() {
		switch k {
		case sim.FaultBitRot, sim.FaultWipeDisk, sim.FaultWipeRejoin:
			continue
		}
		out = append(out, k)
	}
	return out
}

type nemesisResult struct {
	sagas       int
	completed   int
	compensated int
	settled     time.Duration
	attempts    []int
	acked       int
	final       string
}

func nemesisRun(res *nemesisResult) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		s, m, err := sagasim.Build(seed, sagasim.Options{
			Coordinators: []sim.NodeSpec{{ID: "c1", Region: "ra"}},
			Stores:       []sim.NodeSpec{{ID: "a", Region: "ra"}, {ID: "b", Region: "rb"}, {ID: "c", Region: "rc"}},
			Client:       &sim.NodeSpec{ID: "cl", Region: "rb"},
			Source:       "a",
			Objects:      sagasim.MatrixObjects,
			Policy:       sagasim.MatrixPolicy(),
		})
		if err != nil {
			s.Fail(err)
			return s
		}
		m.AddInvariants(s)
		nem, err := s.StartNemesis(sim.NemesisConfig{
			Start: nemesisStart, Phases: nemesisPhases, Chaos: nemesisChaos, Heal: nemesisHeal, MaxGap: nemesisMaxGap,
			Kinds:  nemesisKinds(),
			Budget: sim.Budget{MinLive: 3, MaxDamagedDisks: 1},
		})
		if err != nil {
			s.Fail(err)
			return s
		}
		stores := m.Stores()
		cur := string(stores[0])
		var last saga.ID
		var lastIn saga.MigrationInput
		next := 0
		var tick func()
		tick = func() {
			if last != "" {
				st, ok := m.Status("c1", last)
				if !ok || !st.State.Terminal() {
					s.At(s.Now().Add(nemesisTick), "migrate", tick)
					return
				}
				if st.State == saga.StateCompleted {
					cur = lastIn.To
					res.completed++
				} else {
					res.compensated++
				}
				last = ""
			}
			if s.Now() >= nem.QuietAt() {
				return
			}
			next++
			in := saga.MigrationInput{Bucket: sagasim.Bucket, From: cur, To: string(stores[(slices.Index(stores, env.NodeID(cur))+1)%len(stores)])}
			id := saga.ID(fmt.Sprintf("m%d", next))
			if err := m.Begin("c1", id, in); err == nil {
				last, lastIn = id, in
				res.sagas++
			}
			s.At(s.Now().Add(nemesisTick), "migrate", tick)
		}
		s.At(env.Time(50*time.Millisecond), "migrate", tick)
		s.Workload(env.Time(100*time.Millisecond), nemesisWrites, nemesisWriteGap, func(i int) { m.Write(i) })
		if err := s.RunUntil(nem.QuietAt()); err != nil {
			return s
		}
		done := func() bool { return last == "" && m.WritesIdle() }
		if err := s.RunUntilConverged(nem.QuietAt().Add(nemesisConverge), done); err != nil {
			return s
		}
		res.settled = s.Now().Sub(nem.QuietAt())
		res.attempts = m.Attempts("c1")
		res.acked = m.Acked()
		res.final = cur
		cerr := m.CheckAt(sagasim.Bucket, cur)
		if cerr == nil {
			cerr = m.CheckWrites(sagasim.Bucket, cur)
		}
		if cerr != nil {
			s.Fail(cerr)
		}
		return s
	}
}

func TestMigrationUnderNemesisDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		res := &nemesisResult{}
		simtest.RequireDeterministic(t, seed, nemesisRun(res))
		if res.sagas < nemesisMinSagas {
			t.Fatalf("only %d migrations ran under the nemesis", res.sagas)
		}
		if res.settled > nemesisConverge {
			t.Fatalf("settled %v after the last heal, bound %v", res.settled, nemesisConverge)
		}
		t.Logf("NEMESIS seed=%d sagas=%d completed=%d compensated=%d settled=%v acked=%d final=%s attempts=%v", seed, res.sagas, res.completed, res.compensated, res.settled, res.acked, res.final, res.attempts)
	})
}

type duelResult struct {
	states   [2]saga.State
	outcomes [2]string
	pointer  string
}

func duelRun(res *duelResult) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		ins := [2]saga.MigrationInput{{Bucket: sagasim.Bucket, From: "a", To: "b"}, {Bucket: sagasim.Bucket, From: "a", To: "c"}}
		coords := [2]env.NodeID{"c1", "c2"}
		ids := [2]saga.ID{"x", "y"}
		s, m, err := sagasim.Build(seed, sagasim.Options{
			Coordinators: []sim.NodeSpec{{ID: "c1", Region: "ra"}, {ID: "c2", Region: "rc"}},
			Stores:       []sim.NodeSpec{{ID: "a", Region: "ra"}, {ID: "b", Region: "rb"}, {ID: "c", Region: "rc"}},
			Source:       "a",
			Objects:      sagasim.MatrixObjects,
			Policy:       sagasim.MatrixPolicy(),
		})
		if err != nil {
			s.Fail(err)
			return s
		}
		m.AddInvariants(s)
		r := s.WorkloadRand()
		part := duelMinPart + time.Duration(r.Int64N(int64(duelMaxPart-duelMinPart)))
		s.At(env.Time(10*time.Millisecond), "partition", func() {
			if err := s.Partition([]env.NodeID{"c1", "b"}, []env.NodeID{"c2", "c"}); err != nil {
				s.Fail(err)
			}
		})
		s.At(env.Time(10*time.Millisecond).Add(part), "heal", s.HealNet)
		for i := range coords {
			at := env.Time(20*time.Millisecond) + env.Time(r.Int64N(int64(duelMaxStagger)))
			s.At(at, "begin", func() {
				if err := m.Begin(coords[i], ids[i], ins[i]); err != nil {
					s.Fail(err)
				}
			})
		}
		done := func() bool {
			for i := range coords {
				st, ok := m.Status(coords[i], ids[i])
				if !ok || !st.State.Terminal() {
					return false
				}
			}
			return true
		}
		if err := s.RunUntilConverged(env.Time(duelConverge), done); err != nil {
			return s
		}
		for i := range coords {
			st, _ := m.Status(coords[i], ids[i])
			res.states[i] = st.State
			if len(st.Outputs) == saga.MigrationSteps {
				res.outcomes[i] = string(st.Outputs[saga.MigrationSteps-1])
			}
		}
		if err := m.CheckDuel(ins[:], res.states[:], res.outcomes[:]); err != nil {
			s.Fail(err)
		}
		res.pointer = m.Pointer(sagasim.Bucket)
		return s
	}
}

func TestDuelingMigrationsDST(t *testing.T) {
	winners := map[string]int{}
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		res := &duelResult{}
		simtest.RequireDeterministic(t, seed, duelRun(res))
		winners[res.pointer]++
		t.Logf("DUEL seed=%d pointer=%s states=%v outcomes=%q", seed, res.pointer, res.states, res.outcomes)
	})
	t.Logf("DUEL winners %v", winners)
}

func TestModelClusterReportsBadInput(t *testing.T) {
	tests := []struct {
		name string
		in   saga.MigrationInput
	}{
		{"a migration to the location it starts from", saga.MigrationInput{Bucket: sagasim.Bucket, From: "a", To: "a"}},
		{"a migration of a bucket the source does not hold", saga.MigrationInput{Bucket: "nope", From: "a", To: "b"}},
		{"a migration to an unknown location", saga.MigrationInput{Bucket: sagasim.Bucket, From: "a", To: "zz"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			run := func(seed int64) *sim.Sim {
				s, m, err := sagasim.Build(seed, sagasim.Options{
					Coordinators: []sim.NodeSpec{{ID: "c1", Region: "ra"}},
					Stores:       []sim.NodeSpec{{ID: "a", Region: "ra"}, {ID: "b", Region: "rb"}},
					Source:       "a", Objects: 3, Policy: sagasim.MatrixPolicy(),
				})
				if err != nil {
					s.Fail(err)
					return s
				}
				s.At(1, "begin", func() {
					if err := m.Begin("c1", "bad", tc.in); err != nil {
						s.Fail(err)
					}
				})
				if err := s.RunUntilConverged(env.Time(time.Minute), func() bool {
					st, ok := m.Status("c1", "bad")
					return ok && st.State.Terminal()
				}); err != nil {
					return s
				}
				st, _ := m.Status("c1", "bad")
				if st.State != saga.StateCompensated {
					s.Fail(fmt.Errorf("state %v", st.State))
				}
				ok := saga.MigrationInput{Bucket: sagasim.Bucket, From: "a", To: "b"}
				if err := m.CheckCompensated(ok); err != nil {
					s.Fail(err)
				}
				return s
			}
			simtest.RequireDeterministic(t, 1, run)
		})
	}
}

func TestMigrationInputCodec(t *testing.T) {
	in := saga.MigrationInput{Bucket: "b1", From: "x", To: "y"}
	out, err := saga.DecodeMigrationInput(in.Encode())
	if err != nil || out != in {
		t.Fatalf("round trip %+v err %v", out, err)
	}
	enc := in.Encode()
	bad := [][]byte{nil, {}, enc[:len(enc)-1], append(slices.Clone(enc), 0), {0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x01}}
	for _, b := range bad {
		if _, err := saga.DecodeMigrationInput(b); !errors.Is(err, env.ErrInvalidArgument) {
			t.Fatalf("%x: err %v", b, err)
		}
	}
	for _, v := range []saga.MigrationInput{{}, {Bucket: "b", From: "x", To: "x"}, {Bucket: "b", From: "", To: "y"}} {
		if err := v.Validate(); !errors.Is(err, env.ErrInvalidArgument) {
			t.Fatalf("%+v: err %v", v, err)
		}
	}
}
