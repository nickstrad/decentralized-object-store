package e2e

import (
	"hash/fnv"
	"sync"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

const (
	e7Cycles  = 4
	e7Chaos   = 4 * time.Second
	fuzzWarmM = 2
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
	var mu sync.Mutex
	var totPutsM, totPutsMOK, totMig, totMigShards int
	t.Cleanup(func() {
		m.report(t)
		if t.Failed() {
			return
		}
		if totPutsM >= 4 && totPutsMOK == 0 {
			t.Fatalf("E7 acknowledged 0 of %d puts to routed bucket %s", totPutsM, bucketM)
		}
		if totMig > 0 && totPutsMOK > 0 && totMigShards == 0 {
			t.Fatalf("E7 completed %d migrations of bucket %s with 0 shards moved", totMig, bucketM)
		}
	})
	sweep(t, seedsFor(t, 3, 5), func(t *testing.T, seed int64) {
		var st nemesisStats
		simtest.RequireDeterministic(t, seed, runNemesis(seed, e7Options(), func(n *nemesis) nemesisPlan { return randomPlan(n.r, e7Cycles, e7Chaos) }, &st))
		mu.Lock()
		totPutsM += st.putsM
		totPutsMOK += st.putsMOK
		totMig += st.migrated
		totMigShards += st.migratedShards
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
		m.add("e7/migrated_objects", int64(st.migratedObjects))
		m.add("e7/migrated_shards", int64(st.migratedShards))
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
		if err := s.Err(); err != nil {
			return tot, failf("corpus[%d] (seed %d): %v", i, fuzzSeed(data), err)
		}
		tot.warmM += st.warmM
		tot.warmMOK += st.warmMOK
		tot.putsM += st.putsM
		tot.putsMOK += st.putsMOK
		tot.migrated += st.migrated
		tot.compensated += st.compensated
		tot.migratedObjects += st.migratedObjects
		tot.migratedShards += st.migratedShards
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
	if tot.migratedShards == 0 {
		return tot, failf("fuzz seed corpus completed %d migrations of bucket %s with 0 shards moved", tot.migrated, bucketM)
	}
	return tot, nil
}

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
	t.Logf("MEASURE fuzz_corpus/migrated_objects %d", st.migratedObjects)
	t.Logf("MEASURE fuzz_corpus/migrated_shards %d", st.migratedShards)
}

func FuzzClusterNemesis(f *testing.F) {
	for _, b := range fuzzCorpus() {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			t.Skip("input over 64 KiB")
		}
		s, _ := runFuzzInput(fuzzOptions(), fuzzWarmM, data)
		if err := s.Err(); err != nil {
			simtest.Report(t, s)
			t.Fatalf("seed %d: %v", fuzzSeed(data), err)
		}
	})
}
