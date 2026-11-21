package sim

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
