package e2e

import (
	"hash/fnv"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
)

const (
	e7Cycles = 4
	e7Chaos  = 4 * time.Second
)

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

func TestE7NemesisHealingCyclesS(t *testing.T) {
	t.Parallel()
	var m measure
	t.Cleanup(func() { m.report(t) })
	sweep(t, seedsFor(t, 3, 5), func(t *testing.T, seed int64) {
		var st nemesisStats
		simtest.RequireDeterministic(t, seed, runNemesis(seed, e7Options(), func(n *nemesis) nemesisPlan { return randomPlan(n.r, e7Cycles, e7Chaos) }, &st))
		m.add("e7/cycles", int64(st.cycles))
		m.add("e7/faults_applied", int64(st.applied))
		m.add("e7/faults_skipped_by_budget", int64(st.skipped))
		m.add("e7/puts", int64(st.puts))
		m.add("e7/puts_ok", int64(st.putsOK))
		m.add("e7/gets", int64(st.gets))
		m.add("e7/gets_ok", int64(st.getsOK))
		m.add("e7/availability_gets", int64(st.availabilityGets))
		m.add("e7/discards", int64(st.discards))
		m.add("e7/declared_lost", int64(st.declared))
		m.add("e7/migrations_completed", int64(st.migrated))
		m.add("e7/migrations_compensated", int64(st.compensated))
		for _, d := range st.converge {
			m.ms("e7/converge_after_heal", d)
		}
		for _, d := range st.repair {
			m.ms("e7/repair_after_converge", d)
		}
	})
}

func fuzzOptions() simOptions {
	o := defaultSimOptions()
	o.nodes = 9
	o.skew, o.driftP = time.Second, 500
	return o
}

func fuzzSeed(data []byte) int64 {
	h := fnv.New64a()
	_, _ = h.Write(data)
	return int64(h.Sum64() >> 1)
}

func FuzzClusterNemesis(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add([]byte{2, 0xff, 0, 0, 0})
	for k := range opKinds {
		f.Add([]byte{0, byte(k), 1, 2, 64, byte(k), 3, 5, 128})
	}
	f.Add([]byte{0, byte(opCrashNode), 0, 0, 10, byte(opCrashNode), 1, 0, 20, byte(opCrashNode), 2, 0, 30, byte(opCrashNode), 3, 0, 40})
	f.Add([]byte{1, byte(opWipeNode), 4, 0, 0, byte(opCutRegion), 1, 0, 50, 0xff, 0, 0, 0, byte(opRotShard), 5, 7, 10, byte(opRotShard), 6, 9, 20})
	f.Add([]byte{2, byte(opMigrate), 0, 1, 0, byte(opCrashNode), 2, 0, 60, byte(opRestartNode), 2, 0, 200, 0xff, 0, 0, 0, byte(opPauseNode), 7, 0, 5, byte(opLossyLink), 1, 8, 9})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			t.Skip("input over 64 KiB")
		}
		var st nemesisStats
		seed := fuzzSeed(data)
		s := runNemesis(seed, fuzzOptions(), func(n *nemesis) nemesisPlan {
			n.noGrace = true
			return planFromBytes(data, 3, 8, 4*time.Second)
		}, &st)(seed)
		if err := s.Err(); err != nil {
			simtest.Report(t, s)
			t.Fatalf("seed %d: %v", seed, err)
		}
	})
}
