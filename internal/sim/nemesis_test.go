package sim_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/sim"
	"github.com/nickstrad/object-store/internal/sim/simtoy"
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

// TestNemesisAlternatesChaosAndHeal proves that chaos actions occur only within
// chaos windows, HealAll runs at each window end, QuietAt marks the final heal,
// and the cluster converges afterward.
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

// TestNemesisIsSeeded proves that equal seeds produce identical nemesis
// histories and different seeds diverge.
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

// TestNemesisBudget proves MinLive enforcement, Lifted override, and zero
// MaxDamagedDisks blocking all disk faults.
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

func stripeOf(_ env.NodeID, blob string) string {
	if strings.HasPrefix(blob, "shard/") {
		return "stripe"
	}
	return ""
}

func shardCluster(t *testing.T) *harness {
	h := newHarness(t, sim.Config{Seed: 1}, spec("a", "r"), spec("b", "r"), spec("c", "r"))
	for _, id := range []env.NodeID{"a", "b", "c"} {
		d := h.rec(id).e.Disk
		for _, name := range []string{"epoch", "meta/1", "saga/1", "shard/" + string(id)} {
			mustNil(t, d.Write(name, []byte("bytes of "+name)))
			mustNil(t, d.Sync(name))
		}
	}
	return h
}

// TestBudgetGroupLimit proves per-group limits across bit-rot, wipes, named and
// whole-node read errors, ungrouped blobs (GroupOf == ""), unnamed bit-rot, and
// nemesis read errors under dynamically written shards.
func TestBudgetGroupLimit(t *testing.T) {
	t.Run("a second rot or a wipe in a full group is refused", func(t *testing.T) {
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
	})
	t.Run("blobs outside any group never count against a group", func(t *testing.T) {
		h := shardCluster(t)
		b := sim.Budget{MaxDamagedDisks: 2, MaxDamagedPerGroup: 1, GroupOf: stripeOf}
		for _, f := range []sim.Fault{
			{Kind: sim.FaultWipeDisk, Node: "a"},
			{Kind: sim.FaultBitRot, Node: "a", Blob: "meta/1"},
			{Kind: sim.FaultReadError, Node: "a", Blob: "saga/1", Count: 1},
		} {
			if !b.Allows(h.s, f) {
				t.Fatalf("%v refused", f)
			}
		}
		mustNil(t, h.s.WipeDisk("a"))
		mustNil(t, b.Check(h.s))
		mustNil(t, h.s.BitRot("b", "meta/1"))
		mustNil(t, h.s.BitRot("b", "saga/1"))
		mustNil(t, b.Check(h.s))
		if b.Allows(h.s, sim.Fault{Kind: sim.FaultBitRot, Node: "c", Blob: "shard/c"}) {
			t.Fatal("rot of a second shard allowed after a wipe took the first")
		}
	})
	t.Run("a read error on a blob counts against its group", func(t *testing.T) {
		h := shardCluster(t)
		b := sim.Budget{MaxDamagedDisks: 3, MaxDamagedPerGroup: 1, GroupOf: stripeOf}
		mustNil(t, h.s.BitRot("a", "shard/a"))
		mustNil(t, b.Check(h.s))
		if b.Allows(h.s, sim.Fault{Kind: sim.FaultReadError, Node: "b", Blob: "shard/b", Count: 2}) {
			t.Fatal("read error on a second shard allowed")
		}
		if !b.Allows(h.s, sim.Fault{Kind: sim.FaultReadError, Node: "b", Blob: "meta/1", Count: 2}) {
			t.Fatal("read error on an ungrouped blob refused")
		}
		mustNil(t, h.s.FailReads("b", "shard/b", 2))
		if err := b.Check(h.s); !errors.Is(err, sim.ErrBudget) {
			t.Fatalf("Check = %v", err)
		}
		h.s.HealAll()
		mustNil(t, b.Check(h.s))
	})
	t.Run("a read error on a whole node counts against every group it holds", func(t *testing.T) {
		h := shardCluster(t)
		b := sim.Budget{MaxDamagedDisks: 3, MaxDamagedPerGroup: 1, GroupOf: stripeOf}
		mustNil(t, h.s.BitRot("a", "shard/a"))
		if b.Allows(h.s, sim.Fault{Kind: sim.FaultReadError, Node: "b", Count: 2}) {
			t.Fatal("whole-node read error on a node holding a second shard allowed")
		}
		mustNil(t, h.s.FailReads("b", "", 2))
		if err := b.Check(h.s); !errors.Is(err, sim.ErrBudget) {
			t.Fatalf("Check = %v", err)
		}
	})
	t.Run("an unnamed bit-rot is judged on every blob it could hit", func(t *testing.T) {
		h := shardCluster(t)
		b := sim.Budget{MaxDamagedDisks: 3, MaxDamagedPerGroup: 1, GroupOf: stripeOf}
		mustNil(t, h.s.BitRot("a", "shard/a"))
		if b.Allows(h.s, sim.Fault{Kind: sim.FaultBitRot, Node: "b"}) {
			t.Fatal("unnamed rot on a node holding a second shard allowed")
		}
		d := h.rec("c").e.Disk
		mustNil(t, d.Delete("shard/c"))
		mustNil(t, d.Sync("shard/c"))
		if !b.Allows(h.s, sim.Fault{Kind: sim.FaultBitRot, Node: "c"}) {
			t.Fatal("unnamed rot on a node with only ungrouped blobs refused")
		}
	})
	t.Run("a nemesis read error never ambushes a blob written after it", func(t *testing.T) {
		for seed := int64(1); seed <= 30; seed++ {
			h := newHarness(t, sim.Config{Seed: seed, Net: fixedNet()}, spec("a", "r"), spec("b", "r"), spec("c", "r"))
			b := sim.Budget{MaxDamagedDisks: 3, MaxDamagedPerGroup: 1, GroupOf: stripeOf}
			n, err := h.s.StartNemesis(sim.NemesisConfig{Start: 0, Phases: 1, Chaos: 100 * time.Millisecond, MaxGap: 10 * time.Millisecond, Kinds: []sim.FaultKind{sim.FaultReadError}, Budget: b})
			mustNil(t, err)
			h.s.AddInvariant("budget", 1, func() error { return b.Check(h.s) })
			for i, id := range []env.NodeID{"a", "b", "c"} {
				id := id
				h.s.At(env.Time(time.Duration(20*(i+1))*time.Millisecond), "put "+string(id), func() {
					d := h.rec(id).e.Disk
					mustNil(t, d.Write("shard/"+string(id), []byte("s")))
					mustNil(t, d.Sync("shard/"+string(id)))
				})
			}
			if err := h.s.RunUntil(n.QuietAt()); err != nil {
				t.Fatalf("seed %d: %v\nhistory %v", seed, err, n.History())
			}
			for _, st := range n.History() {
				if st.Fault.Kind == sim.FaultReadError && st.Outcome == sim.OutcomeApplied && st.Fault.Blob == "" {
					t.Fatalf("seed %d: whole-node read error applied with a group budget: %v", seed, st)
				}
			}
		}
	})
}

// TestNemesisConfigValidation proves that StartNemesis rejects invalid phase
// counts, durations, gaps, and unknown fault kinds with ErrInvalidArgument.
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

// TestApplyEveryFaultKind proves that every FaultKind can be applied, encoded,
// formatted, and scheduled with InjectAt, and that unknown kinds fail.
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
