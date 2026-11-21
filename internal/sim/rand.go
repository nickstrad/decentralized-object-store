package sim

import (
	"github.com/nickstrad/decentralized-object-store/internal/env"
)

// Named PRNG stream labels forked from Config.Seed so draws in one subsystem
// do not perturb another.
const (
	StreamNet      = "net"
	StreamDisk     = "disk"
	StreamNemesis  = "nemesis"
	StreamWorkload = "workload"
)

// NodeStream returns the PRNG stream label for node id ("node/<id>").
func NodeStream(id env.NodeID) string { return "node/" + string(id) }
