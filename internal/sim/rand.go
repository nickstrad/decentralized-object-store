package sim

import (
	"github.com/nickstrad/decentralized-object-store/internal/env"
)

const (
	StreamNet      = "net"
	StreamDisk     = "disk"
	StreamNemesis  = "nemesis"
	StreamWorkload = "workload"
)

func NodeStream(id env.NodeID) string { return "node/" + string(id) }
