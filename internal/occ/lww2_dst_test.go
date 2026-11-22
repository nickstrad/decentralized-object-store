package occ_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
)

const (
	writerLossNodes  = 4
	writerLossRounds = 6
	writerLossEvery  = 2 * time.Second
	writerLossSkew   = -3 * time.Second
	writerLossOps    = 120
)

func writerLossRun(seed int64) *sim.Sim {
	s := dstSim(seed, dstNet())
	w := newWorld(s)
	ids := sim.NodeIDs(writerLossNodes)
	f := w.factory(ids)
	for i, id := range ids {
		spec := sim.NodeSpec{ID: id, Region: regions[i%2]}
		if i == 0 {
			spec.Skew = writerLossSkew
		}
		if err := s.AddNode(spec, f); err != nil {
			s.Fail(err)
			return s
		}
	}
	w.ids = ids
	s.AddInvariant("occ/every-loss-recorded", invariantEvery, w.everyLossRecorded)
	doomed, others := ids[0], ids[1:]
	writeAll := func(id env.NodeID, tag string) {
		n := w.live(id)
		if n == nil {
			return
		}
		for _, k := range dstKeys {
			w.write(n, id, k, current(n.Record(k)), false, fmt.Appendf(nil, "%s-%s", tag, k))
		}
	}
	end := chaosStart
	for r := range writerLossRounds {
		t0 := chaosStart.Add(time.Duration(r) * writerLossEvery)
		rival := others[r%len(others)]
		kind := r % lossKinds
		s.At(t0, "isolate the writer's inbound links", func() {
			if err := s.PartitionOneWay(others, []env.NodeID{doomed}); err != nil {
				s.Fail(err)
			}
		})
		s.At(t0.Add(50*time.Millisecond), "rival writes", func() { writeAll(rival, fmt.Sprintf("rival%d", r)) })
		s.At(t0.Add(100*time.Millisecond), "doomed writes", func() { writeAll(doomed, fmt.Sprintf("doomed%d", r)) })
		s.At(t0.Add(300*time.Millisecond), "writer loses its disk", func() { w.loseDisk(doomed, kind) })
		s.At(t0.Add(500*time.Millisecond), "heal", s.HealAll)
		end = t0.Add(500 * time.Millisecond)
	}
	s.Workload(chaosStart, writerLossOps, workloadMaxGap, w.action(true))
	if err := s.RunUntil(end); err != nil {
		return s
	}
	w.finish(end, ConvergeBound)
	if s.Err() == nil && w.diskLosses != writerLossRounds {
		s.Fail(fmt.Errorf("%d of %d disk losses ran", w.diskLosses, writerLossRounds))
	}
	return s
}

// TestWriterLostAfterDeliveryDST proves across 4 nodes in 2 regions that when
// a skewed writer's concurrent deltas reach peers right before the writer loses
// its disk (across all three disk-loss kinds), surviving peers record every
// delivered loss and the cluster converges cleanly.
func TestWriterLostAfterDeliveryDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		simtest.RequireDeterministic(t, seed, writerLossRun)
		if lastWorld.tally["discarded"] == 0 {
			t.Fatal("no delivered version lost to a concurrent write: the scenario did not exercise the hole")
		}
		logWorld(t, lastWorld)
	})
}

// TestCanaryIncomingLoserSilentDST plants the bug of silently dropping rejected
// incoming concurrent losers and proves TestWriterLostAfterDeliveryDST fails.
func TestCanaryIncomingLoserSilentDST(t *testing.T) {
	restore := occ.SetCanaryIncomingLoserSilent(true)
	defer restore()
	failed := 0
	for seed := int64(1); seed <= canarySeeds; seed++ {
		if writerLossRun(seed).Err() != nil {
			failed++
		}
	}
	if failed == 0 {
		t.Fatalf("the writer-loss DST passed on all %d seeds with incoming losers rejected silently", canarySeeds)
	}
	t.Logf("canary: the writer-loss DST failed on %d of %d seeds with incoming losers rejected silently", failed, canarySeeds)
}
