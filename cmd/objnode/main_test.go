package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/noderuntime"
	"github.com/nickstrad/decentralized-object-store/internal/testutil/leakcheck"
)

func TestMain(m *testing.M) { os.Exit(leakcheck.Main(m, 5*time.Second)) }

// TestParseFlagsAndConfigFile proves flag defaults, full-flag parsing, JSON
// config loading with CLI flag overrides, and rejection of unknown fields,
// missing files, stray arguments, non-positive shutdown timeouts, and bad flags.
func TestParseFlagsAndConfigFile(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "c.json")
	if err := os.WriteFile(cfgPath, []byte(`{"id":"h:1","region":"a","data":"/d","k":2,"m":1,"seeds":"h:1,h:2"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte(`{"nope":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		name    string
		args    []string
		wantErr error
		check   func(options) bool
	}{
		{"defaults", []string{"-id", "h:1", "-data", "/d"}, nil, func(o options) bool { return o.K == 4 && o.M == 2 && o.HTTP == ":8080" }},
		{"every flag", []string{"-id", "h:1", "-region", "b", "-listen", ":1", "-http", ":2", "-data", "/x", "-seeds", "a:1,b:2", "-k", "3", "-m", "1", "-stripe", "4096", "-max-object", "99", "-shutdown-timeout", "3s"}, nil,
			func(o options) bool {
				return o.Region == "b" && o.K == 3 && o.Stripe == 4096 && o.MaxObject == 99 && o.ShutdownTimeout == 3*time.Second
			}},
		{"a config file", []string{"-config", cfgPath}, nil, func(o options) bool { return o.ID == "h:1" && o.K == 2 && o.Seeds == "h:1,h:2" }},
		{"a flag overrides the file", []string{"-config", cfgPath, "-k", "3"}, nil, func(o options) bool { return o.K == 3 && o.M == 1 }},
		{"an unknown field in the file", []string{"-config", bad}, errUsage, nil},
		{"a missing config file", []string{"-config", filepath.Join(dir, "none")}, os.ErrNotExist, nil},
		{"a stray argument", []string{"x"}, errUsage, nil},
		{"a zero shutdown timeout", []string{"-shutdown-timeout", "0s"}, errUsage, nil},
		{"an unknown flag", []string{"-bogus"}, nil, nil},
		{"help", []string{"-h"}, flag.ErrHelp, nil},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			var stderr bytes.Buffer
			o, err := parse(r.args, &stderr)
			switch {
			case r.name == "an unknown flag":
				if err == nil {
					t.Fatal("an unknown flag parsed")
				}
			case r.wantErr != nil:
				if !errors.Is(err, r.wantErr) {
					t.Fatalf("got %v, want %v", err, r.wantErr)
				}
			case err != nil:
				t.Fatal(err)
			case !r.check(o):
				t.Fatalf("options %+v", o)
			}
		})
	}
}

// TestHelpListsEveryFlag proves -h exits 0 and prints embedded help text that
// names every supported flag without duplicate tokens.
func TestHelpListsEveryFlag(t *testing.T) {
	var stderr bytes.Buffer
	if code := run(context.Background(), []string{"-h"}, &stderr, nil); code != 0 {
		t.Fatalf("help exit %d", code)
	}
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	for _, name := range strings.Fields(help) {
		fs.Bool(name, false, "")
	}
	for _, want := range []string{"id", "region", "listen", "http", "data", "seeds", "k", "m", "stripe", "max-object", "shutdown-timeout", "config"} {
		if fs.Lookup(want) == nil {
			t.Errorf("help.txt does not name -%s", want)
		}
	}
	if !strings.Contains(stderr.String(), "shutdown-timeout") {
		t.Fatal("help was not printed")
	}
}

// TestInvalidConfigurationsExitTwo proves invalid addresses, missing data dirs,
// invalid erasure layouts, empty HTTP endpoints, and unknown flags exit 2.
func TestInvalidConfigurationsExitTwo(t *testing.T) {
	for _, args := range [][]string{
		{"-id", "nohostport", "-data", "/tmp/x"},
		{"-id", "h:1"},
		{"-id", "h:1", "-data", "/d", "-k", "0"},
		{"-id", "h:1", "-data", "/d", "-http", ""},
		{"-bogus"},
	} {
		var stderr bytes.Buffer
		if code := run(context.Background(), args, &stderr, nil); code != 2 {
			t.Errorf("%v: exit %d (%s)", args, code, stderr.String())
		}
	}
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// TestServeAndShutdownOnCancel proves objnode starts serving /_health, shuts
// down cleanly with exit 0 when its context is canceled, and exits 1 when the
// HTTP listener cannot bind.
func TestServeAndShutdownOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	id := freePort(t)
	ready := make(chan string, 1)
	done := make(chan int, 1)
	var stderr safeBuffer
	go func() {
		done <- run(ctx, []string{"-id", id, "-region", "a", "-data", t.TempDir(), "-http", "127.0.0.1:0", "-k", "1", "-m", "0", "-stripe", "1024"}, &stderr, func(a string, _ *noderuntime.Runtime) { ready <- a })
	}()
	var addr string
	select {
	case addr = <-ready:
	case code := <-done:
		t.Fatalf("objnode exited %d: %s", code, stderr.String())
	}
	resp, err := http.Get("http://" + addr + "/_health")
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil || resp.StatusCode != 200 {
		t.Fatalf("health %d %v", resp.StatusCode, err)
	}
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("exit %d after shutdown: %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "shutting down") {
		t.Fatalf("no shutdown log: %s", stderr.String())
	}
	var e2 safeBuffer
	if code := run(context.Background(), []string{"-id", id, "-data", t.TempDir(), "-http", "256.1.1.1:1"}, &e2, nil); code != 1 {
		t.Fatalf("an unusable http address exited %d", code)
	}
}

// TestServeExitsNonZeroWhenTheNodeLoopPanics proves an injected panic on the
// node loop logs the failure and terminates objnode with exit code 1.
func TestServeExitsNonZeroWhenTheNodeLoopPanics(t *testing.T) {
	var stderr safeBuffer
	done := make(chan int, 1)
	go func() {
		done <- run(context.Background(), []string{"-id", freePort(t), "-region", "a", "-data", t.TempDir(), "-http", "127.0.0.1:0", "-k", "1", "-m", "0", "-stripe", "1024"}, &stderr,
			func(_ string, rt *noderuntime.Runtime) {
				go func() { _ = rt.Inspect(context.Background(), func(*node.Node) { panic("injected") }) }()
			})
	}()
	select {
	case code := <-done:
		if code != 1 {
			t.Fatalf("objnode exited %d after its node loop panicked, want 1: %s", code, stderr.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("objnode kept running after its node loop panicked: %s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "panic") {
		t.Fatalf("no panic in the log: %s", stderr.String())
	}
}

type safeBuffer struct {
	b  bytes.Buffer
	mu sync.Mutex
}

func (s *safeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *safeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestLeakReportCountsAGoroutineUntilItEnds(t *testing.T) {
	base := leakReport(io.Discard, 0)
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-release
	}()
	var b strings.Builder
	n := leakReport(&b, 50*time.Millisecond)
	if n != base+1 || !strings.Contains(b.String(), fmt.Sprintf("goroutines left at exit: %d", base+1)) || !strings.Contains(b.String(), "TestLeakReportCountsAGoroutineUntilItEnds") {
		t.Fatalf("leakReport = %d with one blocked goroutine over a base of %d: %q", n, base, b.String())
	}
	close(release)
	<-done
	if n := leakReport(io.Discard, time.Second); n != base {
		t.Fatalf("leakReport = %d after the goroutine ended, base %d", n, base)
	}
}
