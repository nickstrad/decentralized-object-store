package sim_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtoy"
)

func toySim(t *testing.T, seed int64, n int) (*sim.Sim, []env.NodeID) {
	t.Helper()
	s := sim.New(sim.Config{Seed: seed})
	ids := sim.NodeIDs(n)
	mustNil(t, s.AddCluster(ids, 3, simtoy.Factory(simtoy.Config{Peers: ids})))
	return s, ids
}

func nemesisConfig(b sim.Budget) sim.NemesisConfig {
	return sim.NemesisConfig{
		Start:  env.Time(time.Second),
		Phases: 3,
		Chaos:  5 * time.Second,
		Heal:   3 * time.Second,
		MaxGap: 200 * time.Millisecond,
		Budget: b,
	}
}

func TestNemesisAlternatesChaosAndHeal(t *testing.T) {
	s, _ := toySim(t, 7, 8)
	cfg := nemesisConfig(sim.Budget{Lifted: true})
	n, err := s.StartNemesis(cfg)
	mustNil(t, err)
	want := cfg.Start.Add(2*(cfg.Chaos+cfg.Heal) + cfg.Chaos)
	if n.QuietAt() != want {
		t.Fatalf("QuietAt = %d, want %d", n.QuietAt(), want)
	}
	mustNil(t, s.RunUntil(n.QuietAt().Add(time.Minute)))
	hist := n.History()
	if len(hist) < 20 {
		t.Fatalf("only %d nemesis steps", len(hist))
	}
	heals := 0
	applied := 0
	for _, st := range hist {
		phase := int(st.At.Sub(cfg.Start) / (cfg.Chaos + cfg.Heal))
		offset := st.At.Sub(cfg.Start) % (cfg.Chaos + cfg.Heal)
		if st.Fault.Kind == sim.FaultHealAll {
			heals++
			if offset != cfg.Chaos {
				t.Fatalf("heal at offset %v", offset)
			}
			continue
		}
		if st.At < cfg.Start || offset >= cfg.Chaos || phase >= cfg.Phases {
			t.Fatalf("chaos step outside a chaos window: %+v", st)
		}
		if st.Outcome == sim.OutcomeApplied {
			applied++
		}
	}
	if heals != cfg.Phases || applied == 0 {
		t.Fatalf("heals=%d applied=%d", heals, applied)
	}
	if s.LiveCount() != 8 {
		t.Fatalf("live after final heal = %d", s.LiveCount())
	}
	mustNil(t, s.RunUntilConverged(n.QuietAt().Add(2*time.Minute), func() bool { return simtoy.Converged(s) }))
}

func TestNemesisIsSeeded(t *testing.T) {
	hist := func(seed int64) []sim.NemesisStep {
		s, _ := toySim(t, seed, 6)
		n, err := s.StartNemesis(nemesisConfig(sim.Budget{Lifted: true}))
		mustNil(t, err)
		mustNil(t, s.RunUntil(n.QuietAt()))
		return n.History()
	}
	a, b, c := hist(3), hist(3), hist(4)
	if len(a) != len(b) {
		t.Fatalf("same seed, %d vs %d steps", len(a), len(b))
	}
	for i := range a {
		if a[i].At != b[i].At || a[i].Fault.String() != b[i].Fault.String() || a[i].Outcome != b[i].Outcome {
			t.Fatalf("step %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
	same := len(a) == len(c)
	for i := 0; same && i < len(a); i++ {
		same = a[i].Fault.String() == c[i].Fault.String()
	}
	if same {
		t.Fatal("different seeds gave the same schedule")
	}
}

func TestNemesisBudget(t *testing.T) {
	tests := []struct {
		name   string
		budget sim.Budget
		check  func(t *testing.T, s *sim.Sim, hist []sim.NemesisStep)
	}{
		{"MinLive equal to the cluster size forbids every process fault", sim.Budget{MinLive: 6, MaxDamagedDisks: 6},
			func(t *testing.T, s *sim.Sim, hist []sim.NemesisStep) {
				rejected := 0
				for _, st := range hist {
					switch st.Fault.Kind {
					case sim.FaultCrash, sim.FaultCrashTorn, sim.FaultPause, sim.FaultWipeRejoin:
						if st.Outcome == sim.OutcomeApplied {
							t.Fatalf("applied %v", st.Fault)
						}
						rejected++
					}
				}
				if rejected == 0 {
					t.Fatal("nemesis never tried a process fault")
				}
			}},
		{"a lifted budget lets the nemesis take every node down", sim.Budget{MinLive: 6, Lifted: true},
			func(t *testing.T, s *sim.Sim, hist []sim.NemesisStep) {
				for _, st := range hist {
					if st.Outcome == sim.OutcomeRejected {
						t.Fatalf("lifted budget rejected %v", st.Fault)
					}
				}
			}},
		{"zero damaged disks forbids every disk fault", sim.Budget{MaxDamagedDisks: 0},
			func(t *testing.T, s *sim.Sim, hist []sim.NemesisStep) {
				for _, st := range hist {
					switch st.Fault.Kind {
					case sim.FaultBitRot, sim.FaultWipeDisk, sim.FaultReadError, sim.FaultWipeRejoin:
						if st.Outcome == sim.OutcomeApplied {
							t.Fatalf("applied %v", st.Fault)
						}
					}
				}
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := toySim(t, 11, 6)
			cfg := nemesisConfig(tc.budget)
			cfg.MaxGap = 50 * time.Millisecond
			n, err := s.StartNemesis(cfg)
			mustNil(t, err)
			s.AddInvariant("budget", 1, func() error { return tc.budget.Check(s) })
			mustNil(t, s.RunUntil(n.QuietAt()))
			tc.check(t, s, n.History())
		})
	}
}

func TestBudgetGroupLimit(t *testing.T) {
	s, ids := toySim(t, 5, 6)
	mustNil(t, s.RunUntil(env.Time(time.Second)))
	b := sim.Budget{MaxDamagedDisks: 6, MaxDamagedPerGroup: 1, GroupOf: func(env.NodeID, string) string { return "stripe" }}
	mustNil(t, b.Check(s))
	if !b.Allows(s, sim.Fault{Kind: sim.FaultBitRot, Node: ids[0], Blob: simtoy.Blob}) {
		t.Fatal("first rot refused")
	}
	mustNil(t, s.BitRot(ids[0], simtoy.Blob))
	if b.Allows(s, sim.Fault{Kind: sim.FaultBitRot, Node: ids[1], Blob: simtoy.Blob}) {
		t.Fatal("second rot in the same group allowed")
	}
	if b.Allows(s, sim.Fault{Kind: sim.FaultWipeDisk, Node: ids[1]}) {
		t.Fatal("wipe touching the same group allowed")
	}
	mustNil(t, s.BitRot(ids[1], simtoy.Blob))
	if err := b.Check(s); !errors.Is(err, sim.ErrBudget) {
		t.Fatalf("Check = %v", err)
	}
}

func TestNemesisConfigValidation(t *testing.T) {
	s, _ := toySim(t, 1, 3)
	bad := []sim.NemesisConfig{
		{Phases: 0, Chaos: time.Second, Heal: time.Second, MaxGap: time.Millisecond},
		{Phases: 1, Chaos: 0, Heal: time.Second, MaxGap: time.Millisecond},
		{Phases: 1, Chaos: time.Second, Heal: -1, MaxGap: time.Millisecond},
		{Phases: 1, Chaos: time.Second, Heal: time.Second, MaxGap: 0},
		{Phases: 1, Chaos: time.Second, Heal: time.Second, MaxGap: time.Millisecond, Kinds: []sim.FaultKind{200}},
	}
	for i, cfg := range bad {
		if _, err := s.StartNemesis(cfg); !errors.Is(err, env.ErrInvalidArgument) {
			t.Fatalf("config %d: err = %v", i, err)
		}
	}
}

func TestApplyEveryFaultKind(t *testing.T) {
	s, ids := toySim(t, 2, 4)
	mustNil(t, s.RunUntil(env.Time(time.Second)))
	faults := []sim.Fault{
		{Kind: sim.FaultPartition, Side: ids[:2], Other: ids[2:]},
		{Kind: sim.FaultPartitionOneWay, Side: ids[:1], Other: ids[1:2]},
		{Kind: sim.FaultIsolate, Node: ids[3]},
		{Kind: sim.FaultPartitionRegion, Region: s.Region(ids[0])},
		{Kind: sim.FaultLinkLoss, Node: ids[0], Peer: ids[1], PPM: 10},
		{Kind: sim.FaultClog, Node: ids[0], Peer: ids[1]},
		{Kind: sim.FaultUnclog, Node: ids[0], Peer: ids[1]},
		{Kind: sim.FaultHealNet},
		{Kind: sim.FaultBitRot, Node: ids[0], Blob: simtoy.Blob},
		{Kind: sim.FaultReadError, Node: ids[0], Blob: simtoy.Blob, Count: 1},
		{Kind: sim.FaultWipeDisk, Node: ids[1]},
		{Kind: sim.FaultPause, Node: ids[2]},
		{Kind: sim.FaultResume, Node: ids[2]},
		{Kind: sim.FaultCrash, Node: ids[2]},
		{Kind: sim.FaultRestart, Node: ids[2]},
		{Kind: sim.FaultCrashTorn, Node: ids[2]},
		{Kind: sim.FaultHealAll},
		{Kind: sim.FaultWipeRejoin, Node: ids[3]},
	}
	for _, f := range faults {
		if err := s.Apply(f); err != nil {
			t.Fatalf("Apply(%v): %v", f, err)
		}
		if f.String() == "" || len(f.Encode()) == 0 {
			t.Fatalf("fault %v has no rendering", f.Kind)
		}
	}
	if err := s.Apply(sim.Fault{Kind: 0}); !errors.Is(err, env.ErrInvalidArgument) {
		t.Fatalf("unknown kind: %v", err)
	}
	s.InjectAt(s.Now().Add(time.Second), sim.Fault{Kind: sim.FaultCrash, Node: ids[0]})
	mustNil(t, s.RunUntil(s.Now().Add(2*time.Second)))
	if s.Status(ids[0]).Up {
		t.Fatal("InjectAt did not apply the fault")
	}
}
