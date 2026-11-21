package sim

import (
	"hash/fnv"
	"math/rand/v2"
)

// NewStream derives a deterministic PCG PRNG stream from seed and label using
// NewPCG(uint64(seed), fnv64a(label)).
func NewStream(seed int64, label string) *rand.Rand {
	return rand.New(rand.NewPCG(uint64(seed), fnv64a([]byte(label))))
}

// fnv64a computes the 64-bit FNV-1a hash of b.
func fnv64a(b []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(b)
	return h.Sum64()
}
