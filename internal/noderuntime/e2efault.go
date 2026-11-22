//go:build e2efault

package noderuntime

import (
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

const (
	BlocklistSuffix = ".e2e-blocklist"
	blocklistReload = 50 * time.Millisecond
)

type blocklist struct {
	checked time.Time
	mod     time.Time
	size    int64
	peers   map[env.NodeID]bool
}

var (
	blocklistMu sync.Mutex
	blocklists  = map[string]*blocklist{}
)

func init() { outboundFilter = blockedPeer }

func BlocklistPath(dataDir string) string { return strings.TrimRight(dataDir, "/") + BlocklistSuffix }

func blockedPeer(dataDir string, to env.NodeID) bool {
	blocklistMu.Lock()
	defer blocklistMu.Unlock()
	b := blocklists[dataDir]
	if b == nil {
		b = &blocklist{}
		blocklists[dataDir] = b
	}
	now := time.Now()
	if b.peers == nil || now.Sub(b.checked) >= blocklistReload {
		b.checked = now
		b.reload(BlocklistPath(dataDir))
	}
	return b.peers[to]
}

func (b *blocklist) reload(path string) {
	fi, err := os.Stat(path)
	if err != nil {
		b.peers, b.mod, b.size = map[env.NodeID]bool{}, time.Time{}, 0
		return
	}
	if b.peers != nil && fi.ModTime().Equal(b.mod) && fi.Size() == b.size {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	peers := map[env.NodeID]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			peers[env.NodeID(line)] = true
		}
	}
	b.peers, b.mod, b.size = peers, fi.ModTime(), fi.Size()
}
