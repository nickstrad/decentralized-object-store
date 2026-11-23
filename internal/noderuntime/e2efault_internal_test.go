//go:build e2efault

package noderuntime

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

// TestBlocklistReloadSeesEqualSizeRewrite proves reload observes consecutive
// equal-length peer rewrites even when the file's mtime does not advance.
func TestBlocklistReloadSeesEqualSizeRewrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "node")
	path := BlocklistPath(dir)
	write := func(peer string, mt time.Time) time.Time {
		t.Helper()
		if err := os.WriteFile(path, []byte(peer+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if !mt.IsZero() {
			if err := os.Chtimes(path, mt, mt); err != nil {
				t.Fatal(err)
			}
			return mt
		}
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return fi.ModTime()
	}
	mt := write("peer-a", time.Time{})
	if !blockedPeer(dir, env.NodeID("peer-a")) || blockedPeer(dir, env.NodeID("peer-b")) {
		t.Fatal("initial blocklist did not block peer-a only")
	}
	write("peer-b", mt)
	time.Sleep(blocklistReload + 10*time.Millisecond)
	if !blockedPeer(dir, env.NodeID("peer-b")) || blockedPeer(dir, env.NodeID("peer-a")) {
		t.Fatal("equal-size rewrite 1 (peer-a -> peer-b with identical mtime) was missed")
	}
	write("peer-c", mt)
	time.Sleep(blocklistReload + 10*time.Millisecond)
	if !blockedPeer(dir, env.NodeID("peer-c")) || blockedPeer(dir, env.NodeID("peer-b")) {
		t.Fatal("equal-size rewrite 2 (peer-b -> peer-c with identical mtime) was missed")
	}
}

// TestBlocklistReloadMissingClearsAndReadErrorKeepsLastGood proves a missing
// blocklist clears blocked peers without logging while a non-ENOENT read error
// keeps the last good peer set and reports the error through blocklistLogf.
func TestBlocklistReloadMissingClearsAndReadErrorKeepsLastGood(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "node")
	path := BlocklistPath(dir)
	var logged []string
	blocklistMu.Lock()
	orig := blocklistLogf
	blocklistLogf = func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	}
	blocklistMu.Unlock()
	t.Cleanup(func() {
		blocklistMu.Lock()
		blocklistLogf = orig
		blocklistMu.Unlock()
	})
	if blockedPeer(dir, env.NodeID("peer-a")) {
		t.Fatal("missing blocklist blocked peer-a")
	}
	if len(logged) != 0 {
		t.Fatalf("missing blocklist logged unexpected errors: %v", logged)
	}
	if err := os.WriteFile(path, []byte("peer-a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(blocklistReload + 10*time.Millisecond)
	if !blockedPeer(dir, env.NodeID("peer-a")) {
		t.Fatal("blocklist did not block peer-a")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	time.Sleep(blocklistReload + 10*time.Millisecond)
	if !blockedPeer(dir, env.NodeID("peer-a")) {
		t.Fatal("read error dropped last good peer set instead of keeping peer-a blocked")
	}
	if len(logged) == 0 || !strings.Contains(logged[len(logged)-1], path) {
		t.Fatalf("read error was not surfaced via blocklistLogf: %v", logged)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	time.Sleep(blocklistReload + 10*time.Millisecond)
	if blockedPeer(dir, env.NodeID("peer-a")) {
		t.Fatal("missing blocklist after removal did not clear peer-a")
	}
}
