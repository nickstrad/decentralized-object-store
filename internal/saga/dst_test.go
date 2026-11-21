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
	nemesisConverge = 20 * time.Second
	nemesisWrites   = 24
	nemesisWriteGap = 600 * time.Millisecond
	nemesisTick     = 100 * time.Millisecond
	nemesisMinSagas = 1
	duelConverge    = 90 * time.Second
	duelAfterHeal   = 10 * time.Second
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
				t.Logf("MATRIX %s seed=%d state=%v elapsed=%v crashes=%d torn=%d calls=%v", c.Name(), seed, rep.State, rep.Elapsed, rep.Crashes, rep.Torn, rep.Calls)
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

func TestMigrationUnderNemesisDST(t *testing.T) {
	p := sagasim.NemesisParams{
		Start: nemesisStart, Phases: nemesisPhases, Chaos: nemesisChaos, Heal: nemesisHeal, MaxGap: nemesisMaxGap,
		Converge: nemesisConverge, Writes: nemesisWrites, WriteGap: nemesisWriteGap, Tick: nemesisTick,
	}
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		res := &sagasim.NemesisReport{}
		simtest.RequireDeterministic(t, seed, sagasim.NemesisRun(p, res))
		if res.Sagas < nemesisMinSagas {
			t.Fatalf("only %d migrations ran under the nemesis", res.Sagas)
		}
		if res.Settled > nemesisConverge {
			t.Fatalf("settled %v after the last heal, bound %v", res.Settled, nemesisConverge)
		}
		t.Logf("NEMESIS seed=%d sagas=%d completed=%d compensated=%d settled=%v acked=%d final=%s attempts=%v", seed, res.Sagas, res.Completed, res.Compensated, res.Settled, res.Acked, res.Final, res.Attempts)
	})
}

func TestDuelingMigrationsDST(t *testing.T) {
	p := sagasim.DuelParams{Converge: duelConverge, MaxStagger: duelMaxStagger, MinPartition: duelMinPart, MaxPartition: duelMaxPart}
	winners := map[string]int{}
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		res := &sagasim.DuelReport{}
		simtest.RequireDeterministic(t, seed, sagasim.DuelRun(p, res))
		winners[res.Pointer]++
		if after := res.Finished.Sub(res.Healed); after > duelAfterHeal {
			t.Fatalf("finished %v after the heal, bound %v", after, duelAfterHeal)
		}
		t.Logf("DUEL seed=%d pointer=%s states=%v outcomes=%q afterheal=%v", seed, res.Pointer, res.States, res.Outcomes, res.Finished.Sub(res.Healed))
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
