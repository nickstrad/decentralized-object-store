package e2e

import (
	"hash/fnv"
	"sync"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

const (
	e7Cycles             = 4
	e7Chaos              = 4 * time.Second
	fuzzWarmM            = 2
	e7AvailFloorMinSeeds = 10
	e7AvailFloorPerSeed  = 10
)

// checkE7AvailabilityFloor fails a multi-seed E7 sweep (>= 10 seeds) if average
// checked availability reads fall below e7AvailFloorPerSeed while allowing
// single-seed runs that legitimately draw network faults on every cycle.
func checkE7AvailabilityFloor(seedsRun, totAvailGets int) error {
	if seedsRun >= e7AvailFloorMinSeeds && totAvailGets < seedsRun*e7AvailFloorPerSeed {
		return failf("E7 checked only %d availability gets across %d seeds (floor %d, %d/seed)", totAvailGets, seedsRun, seedsRun*e7AvailFloorPerSeed, e7AvailFloorPerSeed)
	}
	return nil
}

// e7Options configures the 21-node 4+2 cluster for E7 with static clock skew
// (±3 s), drift (±1000 ppm), 0.5% datagram loss, and shortened grace periods
// that remain longer than any chaos cut E7 applies.
func e7Options() simOptions {
	o := defaultSimOptions()
	o.skew, o.driftP = 3*time.Second, 1000
	o.net.DropPPM = 5_000
	o.cfg.OrphanGrace = 20 * time.Second
	o.cfg.GCGrace = 5 * time.Second
	return o
}

func runNemesis(seed int64, opts simOptions, plan func(n *nemesis) nemesisPlan, st *nemesisStats) func(int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		c := newSimCluster(seed, opts)
		if c.s.Err() != nil {
			return c.s
		}
		if err := c.boot(); err != nil {
			c.fail(err)
			return c.s
		}
		n := newNemesis(c)
		if err := n.run(plan(n)); err != nil {
			c.fail(err)
		}
		*st = n.st
		return c.s
	}
}

// TestE7NemesisHealingCyclesS runs alternating chaos and heal cycles across a
// 3/5 share of sweep seeds, proving continuous safety invariants, availability
// within the fault budget, post-heal convergence and repair, per-migration
// shard movement, zero declared losses, and post-grace orphan cleanup.
func TestE7NemesisHealingCyclesS(t *testing.T) {
	t.Parallel()
	var m measure
	var mu sync.Mutex
	var seedsRun, totPutsM, totPutsMOK, totMovedObjs, totAvailGets int
	t.Cleanup(func() {
		m.report(t)
		if t.Failed() {
			return
		}
		if seedsRun >= 10 && totPutsM >= 4 && totPutsMOK == 0 {
			t.Fatalf("E7 acknowledged 0 of %d puts to routed bucket %s across %d seeds", totPutsM, bucketM, seedsRun)
		}
		if seedsRun >= 20 && totMovedObjs == 0 {
			t.Fatalf("E7 migrated 0 pre-existing objects of bucket %s across %d seeds", bucketM, seedsRun)
		}
		if err := checkE7AvailabilityFloor(seedsRun, totAvailGets); err != nil {
			t.Fatal(err)
		}
	})
	sweep(t, seedsFor(t, 3, 5), func(t *testing.T, seed int64) {
		var st nemesisStats
		simtest.RequireDeterministic(t, seed, runNemesis(seed, e7Options(), func(n *nemesis) nemesisPlan { return randomPlan(n.r, e7Cycles, e7Chaos) }, &st))
		if st.cycles != e7Cycles {
			t.Fatalf("seed %d ran %d cycles, want %d", seed, st.cycles, e7Cycles)
		}
		if st.applied == 0 {
			t.Fatalf("seed %d applied 0 chaos faults", seed)
		}
		if st.putsOK == 0 || st.getsOK == 0 {
			t.Fatalf("seed %d saw putsOK=%d getsOK=%d, want both > 0", seed, st.putsOK, st.getsOK)
		}
		mu.Lock()
		seedsRun++
		totPutsM += st.putsM
		totPutsMOK += st.putsMOK
		totMovedObjs += st.movedObjects
		totAvailGets += st.availabilityGets
		mu.Unlock()
		m.add("e7/cycles", int64(st.cycles))
		m.add("e7/faults_applied", int64(st.applied))
		m.add("e7/faults_skipped_by_budget", int64(st.skipped))
		m.add("e7/puts", int64(st.puts))
		m.add("e7/puts_ok", int64(st.putsOK))
		m.add("e7/puts_m", int64(st.putsM))
		m.add("e7/puts_m_ok", int64(st.putsMOK))
		m.add("e7/gets", int64(st.gets))
		m.add("e7/gets_ok", int64(st.getsOK))
		m.add("e7/availability_gets", int64(st.availabilityGets))
		m.add("e7/discards", int64(st.discards))
		m.add("e7/declared_lost", int64(st.declared))
		m.add("e7/migrations_completed", int64(st.migrated))
		m.add("e7/migrations_compensated", int64(st.compensated))
		m.add("e7/ambiguous_migrations_recovered", int64(st.ambiguousRecovered))
		m.add("e7/dest_objects", int64(st.destObjects))
		m.add("e7/dest_shards", int64(st.destShards))
		m.add("e7/moved_objects", int64(st.movedObjects))
		m.add("e7/moved_shards", int64(st.movedShards))
		for _, d := range st.converge {
			m.ms("e7/converge_after_heal", d)
		}
		for _, d := range st.repair {
			m.ms("e7/repair_after_converge", d)
		}
	})
}

// fuzzOptions uses 12 nodes (4 per region) with a 2+1 layout so routed bucket m
// keeps >= k+m=3 nodes in its region even under a single-node fault while
// keeping per-execution fuzz cost low.
func fuzzOptions() simOptions {
	o := defaultSimOptions()
	o.nodes = 12
	o.cfg.Layout = storage.Layout{K: 2, M: 1, StripeSize: simStripe}
	o.skew, o.driftP = time.Second, 500
	return o
}

func fuzzSeed(data []byte) int64 {
	h := fnv.New64a()
	_, _ = h.Write(data)
	return int64(h.Sum64() >> 1)
}

func fuzzCorpus() [][]byte {
	out := [][]byte{
		{},
		{0},
		{2, 0xff, 0, 0, 0},
	}
	for k := range opKinds {
		out = append(out, []byte{0, byte(k), 1, 2, 64, byte(k), 3, 5, 128})
	}
	out = append(out,
		[]byte{0, byte(opCrashNode), 0, 0, 10, byte(opCrashNode), 1, 0, 20, byte(opCrashNode), 2, 0, 30, byte(opCrashNode), 3, 0, 40},
		[]byte{1, byte(opWipeNode), 4, 0, 0, byte(opCutRegion), 1, 0, 50, 0xff, 0, 0, 0, byte(opRotShard), 5, 7, 10, byte(opRotShard), 6, 9, 20},
		[]byte{2, byte(opMigrate), 0, 1, 0, byte(opCrashNode), 2, 0, 60, byte(opRestartNode), 2, 0, 200, 0xff, 0, 0, 0, byte(opPauseNode), 7, 0, 5, byte(opLossyLink), 1, 8, 9},
	)
	return out
}

func runFuzzInput(opts simOptions, warmM int, data []byte) (*sim.Sim, nemesisStats) {
	var st nemesisStats
	seed := fuzzSeed(data)
	s := runNemesis(seed, opts, func(n *nemesis) nemesisPlan {
		n.noGrace = true
		n.warmM = warmM
		return planFromBytes(data, 3, 8, 4*time.Second)
	}, &st)(seed)
	return s, st
}

func checkFuzzCorpus(opts simOptions, warmM int) (nemesisStats, error) {
	var tot nemesisStats
	for i, data := range fuzzCorpus() {
		s, st := runFuzzInput(opts, warmM, data)
		if err := s.Verify(); err != nil {
			return tot, failf("corpus[%d] (seed %d): %v", i, fuzzSeed(data), err)
		}
		tot.warmM += st.warmM
		tot.warmMOK += st.warmMOK
		tot.putsM += st.putsM
		tot.putsMOK += st.putsMOK
		tot.migrated += st.migrated
		tot.compensated += st.compensated
		tot.destObjects += st.destObjects
		tot.destShards += st.destShards
		tot.movedObjects += st.movedObjects
		tot.movedShards += st.movedShards
	}
	if warmM > 0 && tot.warmMOK == 0 {
		return tot, failf("fuzz seed corpus acknowledged 0 warm-up puts to routed bucket %s", bucketM)
	}
	if tot.putsMOK == 0 {
		return tot, failf("fuzz seed corpus acknowledged 0 traffic puts to routed bucket %s", bucketM)
	}
	if tot.migrated == 0 {
		return tot, failf("fuzz seed corpus completed 0 migrations of bucket %s", bucketM)
	}
	if tot.destShards == 0 {
		return tot, failf("fuzz seed corpus completed %d migrations of bucket %s with 0 destination shards", tot.migrated, bucketM)
	}
	if warmM > 0 && tot.movedShards == 0 {
		return tot, failf("fuzz seed corpus completed %d migrations of bucket %s with 0 pre-existing shards moved", tot.migrated, bucketM)
	}
	return tot, nil
}

// TestFuzzClusterNemesisCorpusMovesShards proves the fuzz seed corpus exercises
// non-empty routed bucket writes and completed migrations moving real shards
// with every simulator invariant verified.
func TestFuzzClusterNemesisCorpusMovesShards(t *testing.T) {
	st, err := checkFuzzCorpus(fuzzOptions(), fuzzWarmM)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MEASURE fuzz_corpus/warm_m_ok %d", st.warmMOK)
	t.Logf("MEASURE fuzz_corpus/puts_m %d", st.putsM)
	t.Logf("MEASURE fuzz_corpus/puts_m_ok %d", st.putsMOK)
	t.Logf("MEASURE fuzz_corpus/migrations_completed %d", st.migrated)
	t.Logf("MEASURE fuzz_corpus/migrations_compensated %d", st.compensated)
	t.Logf("MEASURE fuzz_corpus/dest_objects %d", st.destObjects)
	t.Logf("MEASURE fuzz_corpus/dest_shards %d", st.destShards)
	t.Logf("MEASURE fuzz_corpus/moved_objects %d", st.movedObjects)
	t.Logf("MEASURE fuzz_corpus/moved_shards %d", st.movedShards)
}

func ambiguousMigrationRun(seed int64, cycles int, forget bool, st *nemesisStats) func(int64) *sim.Sim {
	return runNemesis(seed, fuzzOptions(), func(n *nemesis) nemesisPlan {
		n.noGrace = true
		n.warmM = fuzzWarmM
		n.forgetAmbiguousMig = forget
		n.beforeCycle = func(n *nemesis, ci int) error {
			if ci != 0 {
				return nil
			}
			var clients []env.NodeID
			for _, r := range n.c.regions {
				clients = append(clients, env.NodeID("client-"+r))
			}
			n.markNetFault()
			return n.c.apply(sim.Fault{Kind: sim.FaultPartitionOneWay, Side: n.c.ids, Other: clients})
		}
		var plan nemesisPlan
		for range cycles {
			plan.cycles = append(plan.cycles, cycle{
				chaos: e7Chaos,
				ops:   []chaosOp{{kind: opMigrate, a: 0, b: 0, at: 500 * time.Millisecond}},
			})
		}
		return plan
	}, st)
}

func verifyAmbiguousMigrationTracked(seed int64, cycles int, forget bool) (*sim.Sim, nemesisStats, error) {
	var st nemesisStats
	s := ambiguousMigrationRun(seed, cycles, forget, &st)(seed)
	if err := s.Verify(); err != nil {
		return s, st, err
	}
	if st.ambiguousRecovered != 1 {
		return s, st, failf("ambiguous migration start was not recovered: got %d recovered, want 1", st.ambiguousRecovered)
	}
	if st.migrated != cycles {
		return s, st, failf("completed migrations = %d, want %d", st.migrated, cycles)
	}
	wantShards := cycles * fuzzWarmM * (fuzzOptions().cfg.Layout.K + fuzzOptions().cfg.Layout.M)
	if st.movedShards < wantShards {
		return s, st, failf("moved shards = %d, want at least %d", st.movedShards, wantShards)
	}
	wantRegion := "r1"
	if cycles == 2 {
		wantRegion = "r2"
	}
	if st.finalRegion != wantRegion {
		return s, st, failf("final region = %q, want %q", st.finalRegion, wantRegion)
	}
	return s, st, nil
}

type delayStep struct {
	saga.Step
	sim   func() *sim.Sim
	delay time.Duration
}

func (d delayStep) Do(call saga.Call, done func([]byte, error)) {
	s := d.sim()
	if s == nil {
		d.Step.Do(call, done)
		return
	}
	s.At(s.Now().Add(d.delay), "delay-"+d.Step.Name(), func() {
		d.Step.Do(call, done)
	})
}

func slowAmbiguousMigrationRun(seed int64, forget bool, st *nemesisStats) func(int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		var cl *simCluster
		opts := fuzzOptions()
		opts.tweak = func(cfg *node.Config) {
			cfg.SagaPolicy = saga.DefaultPolicy()
			cfg.SagaPolicy.StepTimeout = 35 * time.Second
			cfg.WrapMigration = func(d saga.Definition) saga.Definition {
				steps := make([]saga.Step, len(d.Steps))
				copy(steps, d.Steps)
				for i, step := range steps {
					if step.Name() == saga.StepCopy {
						steps[i] = delayStep{Step: step, sim: func() *sim.Sim {
							if cl == nil {
								return nil
							}
							return cl.s
						}, delay: 25 * time.Second}
					}
				}
				d.Steps = steps
				return d
			}
		}
		cl = newSimCluster(seed, opts)
		if cl.s.Err() != nil {
			return cl.s
		}
		if err := cl.boot(); err != nil {
			cl.fail(err)
			return cl.s
		}
		n := newNemesis(cl)
		n.noGrace = true
		n.warmM = fuzzWarmM
		n.forgetAmbiguousMig = forget
		var coordID env.NodeID
		n.beforeCycle = func(n *nemesis, ci int) error {
			if ci == 0 {
				var clients []env.NodeID
				for _, r := range n.c.regions {
					clients = append(clients, env.NodeID("client-"+r))
				}
				n.markNetFault()
				n.c.s.At(n.c.s.Now().Add(time.Second), "record-mig-coord", func() {
					coordID = env.NodeID(n.migCoord.ID)
				})
				return n.c.apply(sim.Fault{Kind: sim.FaultPartitionOneWay, Side: n.c.ids, Other: clients})
			}
			if n.mig != nil {
				return failf("n.mig still non-nil at start of cycle %d", ci)
			}
			for _, id := range n.c.ids {
				nd := n.c.anyNode(id)
				if nd == nil {
					continue
				}
				eng := nd.Engine()
				for _, sid := range eng.IDs() {
					if s, err := eng.Status(sid); err == nil && !s.State.Terminal() {
						return failf("coordinator %s still holds non-terminal saga %s at start of cycle %d", id, sid, ci)
					}
				}
			}
			if coordID != "" {
				for idx, id := range n.c.ids {
					if id == coordID {
						return n.apply(chaosOp{kind: opWipeNode, a: idx})
					}
				}
			}
			return nil
		}
		plan := nemesisPlan{cycles: []cycle{
			{chaos: e7Chaos, ops: []chaosOp{{kind: opMigrate, a: 0, b: 0, at: 500 * time.Millisecond}}},
			{chaos: e7Chaos},
		}}
		if err := n.run(plan); err != nil {
			cl.fail(err)
		}
		*st = n.st
		return cl.s
	}
}

// TestNemesisAmbiguousMigrationStartIsTracked proves that when StartMigration
// times out after a one-way network cut, finishMigration recovers and awaits
// any saga started on the coordinator before clearing n.mig or allowing a
// subsequent coordinator wipe, and handles dropped requests cleanly.
func TestNemesisAmbiguousMigrationStartIsTracked(t *testing.T) {
	const seed = 702
	for _, cycles := range []int{1, 2} {
		var st nemesisStats
		simtest.RequireDeterministic(t, seed, ambiguousMigrationRun(seed, cycles, false, &st))
		if st.ambiguousRecovered != 1 {
			t.Fatalf("cycles=%d: ambiguousRecovered = %d, want 1", cycles, st.ambiguousRecovered)
		}
		if st.migrated != cycles {
			t.Fatalf("cycles=%d: migrated = %d, want %d", cycles, st.migrated, cycles)
		}
		wantShards := cycles * fuzzWarmM * (fuzzOptions().cfg.Layout.K + fuzzOptions().cfg.Layout.M)
		if st.movedShards < wantShards {
			t.Fatalf("cycles=%d: movedShards = %d, want >= %d", cycles, st.movedShards, wantShards)
		}
		wantRegion := "r1"
		if cycles == 2 {
			wantRegion = "r2"
		}
		if st.finalRegion != wantRegion {
			t.Fatalf("cycles=%d: finalRegion = %q, want %q", cycles, st.finalRegion, wantRegion)
		}
		t.Logf("cycles=%d recovered=%d migrated=%d movedShards=%d destShards=%d finalRegion=%s", cycles, st.ambiguousRecovered, st.migrated, st.movedShards, st.destShards, st.finalRegion)
	}
	t.Run("dropped request timeout starts no saga and next cycle migrates from original region", func(t *testing.T) {
		var st nemesisStats
		simtest.RequireDeterministic(t, seed, runNemesis(seed, fuzzOptions(), func(n *nemesis) nemesisPlan {
			n.noGrace = true
			n.warmM = fuzzWarmM
			n.beforeCycle = func(n *nemesis, ci int) error {
				if ci != 0 {
					return nil
				}
				var clients []env.NodeID
				for _, r := range n.c.regions {
					clients = append(clients, env.NodeID("client-"+r))
				}
				n.markNetFault()
				return n.c.apply(sim.Fault{Kind: sim.FaultPartitionOneWay, Side: clients, Other: n.c.ids})
			}
			return nemesisPlan{cycles: []cycle{
				{chaos: e7Chaos, ops: []chaosOp{{kind: opMigrate, a: 0, b: 0, at: 500 * time.Millisecond}}},
				{chaos: e7Chaos, ops: []chaosOp{{kind: opMigrate, a: 0, b: 0, at: 500 * time.Millisecond}}},
			}}
		}, &st))
		if st.ambiguousRecovered != 0 {
			t.Fatalf("ambiguousRecovered = %d, want 0 when request never reached coordinator", st.ambiguousRecovered)
		}
		if st.migrated != 1 || st.finalRegion != "r1" {
			t.Fatalf("migrated=%d finalRegion=%q, want 1 and r1", st.migrated, st.finalRegion)
		}
	})
	t.Run("ambiguous start finishes to terminal state before next cycle can wipe coordinator", func(t *testing.T) {
		var st nemesisStats
		simtest.RequireDeterministic(t, seed, slowAmbiguousMigrationRun(seed, false, &st))
		if st.ambiguousRecovered != 1 || st.migrated != 1 || st.finalRegion != "r1" {
			t.Fatalf("unexpected stats: recovered=%d migrated=%d finalRegion=%q", st.ambiguousRecovered, st.migrated, st.finalRegion)
		}
		if st.applied < 2 {
			t.Fatalf("expected migration and cycle-1 coordinator wipe to both apply, got applied=%d", st.applied)
		}
	})
}

// TestE7AvailabilityFloorSafeForSingleSeed proves the sweep-level availability
// floor never false-fails single-seed runs and catches under-floor sweeps.
func TestE7AvailabilityFloorSafeForSingleSeed(t *testing.T) {
	cases := []struct {
		name         string
		seedsRun     int
		totAvailGets int
		wantErr      bool
	}{
		{"single seed with zero availability gets passes", 1, 0, false},
		{"below min seeds with zero passes", e7AvailFloorMinSeeds - 1, 0, false},
		{"sweep with zero availability gets fails", e7AvailFloorMinSeeds, 0, true},
		{"sweep below per-seed floor fails", e7AvailFloorMinSeeds, e7AvailFloorMinSeeds*e7AvailFloorPerSeed - 1, true},
		{"sweep at per-seed floor passes", e7AvailFloorMinSeeds, e7AvailFloorMinSeeds * e7AvailFloorPerSeed, false},
		{"301-seed sweep with normal count passes", 301, 6000, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkE7AvailabilityFloor(tc.seedsRun, tc.totAvailGets)
			if tc.wantErr && err == nil {
				t.Fatalf("checkE7AvailabilityFloor(%d, %d) = nil, want error", tc.seedsRun, tc.totAvailGets)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("checkE7AvailabilityFloor(%d, %d) = %v, want nil", tc.seedsRun, tc.totAvailGets, err)
			}
		})
	}
}

// FuzzClusterNemesis decodes fuzz inputs into whole-cluster nemesis schedules
// on a 12-node 2+1 cluster and verifies all safety invariants and post-heal
// convergence via Sim.Verify.
func FuzzClusterNemesis(f *testing.F) {
	for _, b := range fuzzCorpus() {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			t.Skip("input over 64 KiB")
		}
		s, _ := runFuzzInput(fuzzOptions(), fuzzWarmM, data)
		if err := s.Verify(); err != nil {
			simtest.Report(t, s)
			t.Fatalf("seed %d: %v", fuzzSeed(data), err)
		}
	})
}
