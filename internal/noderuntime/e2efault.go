//go:build e2efault

package noderuntime

import (
	"errors"
	"io/fs"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

const (
	// BlocklistSuffix is appended to a node's data directory path to locate the
	// peer blocklist file used by real-process partition tests.
	BlocklistSuffix = ".e2e-blocklist"
	blocklistReload = 50 * time.Millisecond
)

type blocklist struct {
	checked time.Time
	peers   map[env.NodeID]bool
}

var (
	blocklistMu   sync.Mutex
	blocklists    = map[string]*blocklist{}
	blocklistLogf = log.Printf
)

// Install the outbound peer filter only in e2efault builds; default binaries
// leave outboundFilter nil and omit this file completely.
func init() { outboundFilter = blockedPeer }

// BlocklistPath returns the blocklist file path sitting alongside dataDir.
func BlocklistPath(dataDir string) string { return strings.TrimRight(dataDir, "/") + BlocklistSuffix }

// blockedPeer reports whether outbound traffic from dataDir to peer to is
// blocked, reloading the file at most once per blocklistReload.
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
		if err := b.reload(BlocklistPath(dataDir)); err != nil && blocklistLogf != nil {
			blocklistLogf("noderuntime: e2efault: read %s: %v", BlocklistPath(dataDir), err)
		}
	}
	return b.peers[to]
}

// reload reads the blocklist directly on every throttled check rather than
// caching stat mtime and size, so rapid equal-length rewrites within one
// filesystem mtime tick are never missed. A missing file clears the set; any
// other read error preserves the last good peer set and returns the error.
func (b *blocklist) reload(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			b.peers = map[env.NodeID]bool{}
			return nil
		}
		if b.peers == nil {
			b.peers = map[env.NodeID]bool{}
		}
		return err
	}
	peers := map[env.NodeID]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			peers[env.NodeID(line)] = true
		}
	}
	b.peers = peers
	return nil
}
