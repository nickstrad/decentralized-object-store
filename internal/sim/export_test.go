package sim

import (
	"time"

	"github.com/nickstrad/object-store/internal/env"
)

// SetIncarnation sets node id's incarnation counter directly for boundary tests
// near math.MaxUint32.
func SetIncarnation(s *Sim, id env.NodeID, inc uint32) {
	n := s.nodes[s.byID[id]]
	n.inc = inc
	n.handle.inc = inc
}

// PlantDroppedInvariants disables scheduled invariant checks in Step so canary
// tests can verify that Verify catches missing invariant runs.
func PlantDroppedInvariants(s *Sim) { s.dropInvariants = true }

// ReadFaults returns the sorted blob names with pending per-blob read errors
// and the pending whole-node read-error count on d.
func ReadFaults(d *Disk) ([]string, int) {
	var names []string
	for _, name := range sortedKeys(d.failReads) {
		if d.failReads[name] > 0 {
			names = append(names, name)
		}
	}
	return names, d.failAny
}

// Scaled exposes the saturating duration-scaling helper for boundary tests.
func Scaled(k int64, d time.Duration) time.Duration { return scaled(k, d) }
