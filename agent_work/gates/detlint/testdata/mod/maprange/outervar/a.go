package outervar

import "slices"

func Last(m map[string]int) string {
	var last string
	var ks []string
	for last = range m {
		ks = append(ks, last)
	}
	slices.Sort(ks)
	return last
}
