package sim_test

import (
	"hash/fnv"
	"math/rand/v2"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

// TestStreamDerivationMatchesThePlan proves that NewStream and NodeStream match
// NewPCG(uint64(seed), fnv64a(label)) across boundary seeds and labels.
func TestStreamDerivationMatchesThePlan(t *testing.T) {
	labels := []string{sim.StreamNet, sim.StreamDisk, sim.StreamNemesis, sim.StreamWorkload, sim.NodeStream("n007")}
	if sim.NodeStream("n007") != "node/n007" {
		t.Fatalf("NodeStream = %q", sim.NodeStream("n007"))
	}
	for _, seed := range []int64{0, 1, -1, 1 << 62} {
		for _, label := range labels {
			h := fnv.New64a()
			_, _ = h.Write([]byte(label))
			want := rand.New(rand.NewPCG(uint64(seed), h.Sum64()))
			got := sim.NewStream(seed, label)
			for i := 0; i < 8; i++ {
				if a, b := got.Uint64(), want.Uint64(); a != b {
					t.Fatalf("seed %d label %q draw %d: %d != %d", seed, label, i, a, b)
				}
			}
		}
	}
}

// TestSimStreamsAreTheNamedStreams proves that Sim initializes WorkloadRand
// from the named workload stream and exposes its configured seed.
func TestSimStreamsAreTheNamedStreams(t *testing.T) {
	s := sim.New(sim.Config{Seed: 42})
	if s.WorkloadRand().Uint64() != sim.NewStream(42, sim.StreamWorkload).Uint64() {
		t.Fatal("WorkloadRand is not the workload stream")
	}
	if s.Seed() != 42 {
		t.Fatalf("Seed = %d", s.Seed())
	}
}
