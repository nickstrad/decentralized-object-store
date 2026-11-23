package reportfixture

import (
	"github.com/nickstrad/object-store/internal/sim"
	"github.com/nickstrad/object-store/internal/sim/simtest"
	"testing"
)

func TestFailure(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		simtest.RequireDeterministic(t, seed, func(seed int64) *sim.Sim {
			s := sim.New(sim.Config{Seed: seed, LogCap: 5})
			for i := range 9 {
				s.At(0, "tick", func() {})
				_ = i
			}
			s.At(1, "intentional-failure", func() { panic("replay-fixture") })
			_ = s.RunUntilQuiet()
			return s
		})
	})
}
