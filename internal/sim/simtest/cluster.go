package simtest

import (
	"testing"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/sim"
)

// Cluster adds n nodes distributed round-robin across regions to s using the
// factory built from the generated NodeID slice, failing t on error.
func Cluster(t testing.TB, s *sim.Sim, n, regions int, factory func(ids []env.NodeID) sim.Factory) []env.NodeID {
	t.Helper()
	ids := sim.NodeIDs(n)
	if err := s.AddCluster(ids, regions, factory(ids)); err != nil {
		t.Fatal(err)
	}
	return ids
}
