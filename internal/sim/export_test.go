package sim

import "github.com/nickstrad/decentralized-object-store/internal/env"

func SetIncarnation(s *Sim, id env.NodeID, inc uint32) {
	n := s.nodes[s.byID[id]]
	n.inc = inc
	n.handle.inc = inc
}

func PlantDroppedInvariants(s *Sim) { s.dropInvariants = true }

func ReadFaults(d *Disk) ([]string, int) {
	var names []string
	for _, name := range sortedKeys(d.failReads) {
		if d.failReads[name] > 0 {
			names = append(names, name)
		}
	}
	return names, d.failAny
}
