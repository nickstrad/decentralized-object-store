package sim

import (
	"hash/fnv"
	"math/rand/v2"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

const (
	StreamNet      = "net"
	StreamDisk     = "disk"
	StreamNemesis  = "nemesis"
	StreamWorkload = "workload"
)

func NodeStream(id env.NodeID) string { return "node/" + string(id) }

func NewStream(seed int64, label string) *rand.Rand {
	return rand.New(rand.NewPCG(uint64(seed), fnv64a([]byte(label))))
}

func fnv64a(b []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(b)
	return h.Sum64()
}
