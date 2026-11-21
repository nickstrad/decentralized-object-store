package simtest

import (
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

func Cluster(t testing.TB, s *sim.Sim, n, regions int, factory func(ids []env.NodeID) sim.Factory) []env.NodeID {
	t.Helper()
	ids := sim.NodeIDs(n)
	if err := s.AddCluster(ids, regions, factory(ids)); err != nil {
		t.Fatal(err)
	}
	return ids
}
