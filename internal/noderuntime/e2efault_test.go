//go:build e2efault

package noderuntime_test

import (
	"os"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/noderuntime"
)

// TestBlocklistPathSitsBesideTheDataDirectory proves BlocklistPath strips any
// trailing slash so the file sits beside dataDir rather than inside it.
func TestBlocklistPathSitsBesideTheDataDirectory(t *testing.T) {
	for _, dir := range []string{"/x/data", "/x/data/"} {
		if got := noderuntime.BlocklistPath(dir); got != "/x/data"+noderuntime.BlocklistSuffix {
			t.Fatalf("BlocklistPath(%q) = %q", dir, got)
		}
	}
}

// TestBlocklistSplitsTwoNodesUntilCleared proves mutual blocklist files split
// two loopback runtimes into single-node views and removing the files lets them
// rejoin.
func TestBlocklistSplitsTwoNodesUntilCleared(t *testing.T) {
	c := startReal(t, 2, realConfig())
	eventually(t, 10*time.Second, "two nodes join", func() bool { return c.joined(2) })
	dirs := []string{c.dirs[0], c.dirs[1]}
	ids := []string{string(c.rts[1].ID()), string(c.rts[0].ID())}
	for i, d := range dirs {
		if err := os.WriteFile(noderuntime.BlocklistPath(d), []byte(ids[i]+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 15*time.Second, "each node sees only itself alive", func() bool { return c.joined(1) })
	for _, d := range dirs {
		if err := os.Remove(noderuntime.BlocklistPath(d)); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, 15*time.Second, "the two nodes rejoin", func() bool { return c.joined(2) })
}
