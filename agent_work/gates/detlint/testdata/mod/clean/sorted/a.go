package sorted

import (
	"maps"
	"slices"
	"sort"
)

type byLen []string

func (b byLen) Len() int           { return len(b) }
func (b byLen) Less(i, j int) bool { return len(b[i]) < len(b[j]) }
func (b byLen) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }

func Keys(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func Pairs(m map[string]int) ([]string, []int) {
	var ks []string
	var vs []int
	for k, v := range m {
		ks = append(ks, k)
		vs = append(vs, v)
	}
	slices.Sort(ks)
	slices.Sort(vs)
	return ks, vs
}

func ByLen(m map[string]bool) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Sort(byLen(ks))
	return ks
}

func Iter(m map[string]int) []string {
	return slices.Sorted(maps.Keys(m))
}
