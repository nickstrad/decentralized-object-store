package noderuntime

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
)

// TestFileDiskMeetsTheDiskContract proves Read, Write, Append, Sync, Delete,
// List (hiding .tmp/), invalid-name rejection, and I/O errors on fileDisk.
func TestFileDiskMeetsTheDiskContract(t *testing.T) {
	d, err := openFileDisk(osFS{}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Read("a/b"); !errors.Is(err, env.ErrNotFound) {
		t.Fatalf("read of a missing blob: %v", err)
	}
	if err := d.Write("a/b", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := d.Append("a/b", []byte("+two")); err != nil {
		t.Fatal(err)
	}
	if err := d.Append("a/c", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, err := d.Read("a/b"); err != nil || string(b) != "one+two" {
		t.Fatalf("read %q %v", b, err)
	}
	if err := d.Write("a/b", []byte("three")); err != nil {
		t.Fatal(err)
	}
	if err := d.Sync("a/b"); err != nil {
		t.Fatal(err)
	}
	if err := d.Write("z", nil); err != nil {
		t.Fatal(err)
	}
	names, err := d.List("a/")
	if err != nil || !slices.Equal(names, []string{"a/b", "a/c"}) {
		t.Fatalf("list a/: %v %v", names, err)
	}
	all, err := d.List("")
	if err != nil || !slices.Equal(all, []string{"a/b", "a/c", "z"}) {
		t.Fatalf("list all: %v %v (temporary files must stay hidden)", all, err)
	}
	if err := d.Delete("a/b"); err != nil {
		t.Fatal(err)
	}
	if err := d.Delete("a/b"); !errors.Is(err, env.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if err := d.Sync("a/b"); err != nil {
		t.Fatalf("syncing a deletion: %v", err)
	}
	for _, bad := range []string{"", "/abs", "a/../b", "a//b", ".tmp/x", "a\\b", "a/./b", "x\x00"} {
		if _, err := d.Read(bad); !errors.Is(err, env.ErrInvalidArgument) {
			t.Errorf("name %q: %v", bad, err)
		}
		if err := d.Write(bad, nil); !errors.Is(err, env.ErrInvalidArgument) {
			t.Errorf("write %q: %v", bad, err)
		}
	}
	if err := os.WriteFile(filepath.Join(d.dir, "dir"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := d.Write("dir/x", []byte("y")); !errors.Is(err, env.ErrIO) {
		t.Fatalf("write under a file: %v", err)
	}
	if err := d.Append("dir/x", []byte("y")); !errors.Is(err, env.ErrIO) {
		t.Fatalf("append under a file: %v", err)
	}
}

func newLoopRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt, err := NewRuntime(RuntimeConfig{ID: "solo", Region: "r0", DataDir: t.TempDir(), NoNetwork: true, Node: node.Config{Layout: soloLayout()}})
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

// TestRealTimersFireStopAndResetOnTheLoop proves realTimer callbacks run on
// the node loop, honor Stop before firing, handle non-positive durations, and
// can be Reset after firing.
func TestRealTimersFireStopAndResetOnTheLoop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newLoopRuntime(t)
		rt.wg.Go(rt.loop)
		fired := make(chan string, 8)
		var a, b, c env.Timer
		rt.post(func() {
			clock := realClock{rt: rt}
			a = clock.AfterFunc(time.Second, func() { fired <- "a" })
			b = clock.AfterFunc(time.Second, func() { fired <- "b" })
			c = clock.AfterFunc(-time.Second, func() { fired <- "c" })
		})
		synctest.Wait()
		stopped := make(chan bool, 1)
		rt.post(func() { stopped <- b.Stop() })
		if !<-stopped {
			t.Fatal("stopping a pending timer reported it was not pending")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		var got []string
		for len(fired) > 0 {
			got = append(got, <-fired)
		}
		if !slices.Equal(got, []string{"c", "a"}) {
			t.Fatalf("fired %v, want c then a", got)
		}
		rt.post(func() { stopped <- a.Stop(); a.Reset(time.Second); _ = c })
		if <-stopped {
			t.Fatal("a fired timer reported pending")
		}
		time.Sleep(2 * time.Second)
		synctest.Wait()
		if got := <-fired; got != "a" {
			t.Fatalf("reset timer fired %q", got)
		}
		rt.cancel()
		rt.wg.Wait()
	})
}
