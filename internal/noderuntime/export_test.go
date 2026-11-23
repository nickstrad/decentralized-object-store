package noderuntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/nickstrad/object-store/internal/env"
)

// SetCanaryUntrackedGoroutine injects a leaked goroutine into Runtime.Start
// for the shutdown leak-check canary test.
func SetCanaryUntrackedGoroutine(release chan struct{}) { canaryUntrackedGoroutine = release }

const maxLoopbackStartAttempts = 5

func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE) || strings.Contains(err.Error(), "address already in use")
}

// FreeLoopbackAddrs reserves n distinct 127.0.0.1 ports that are free on both
// TCP and UDP, holding all sockets open until every port is chosen so two
// nodes in one cluster cannot receive the same port.
func FreeLoopbackAddrs(n int) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	var tcpLs []net.Listener
	var udpCs []net.PacketConn
	defer func() {
		for _, u := range udpCs {
			_ = u.Close()
		}
		for _, l := range tcpLs {
			_ = l.Close()
		}
	}()
	used := map[string]bool{}
	addrs := make([]string, 0, n)
	for attempts := 0; len(addrs) < n && attempts < n*50; attempts++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		addr := l.Addr().String()
		if used[addr] {
			_ = l.Close()
			continue
		}
		u, uerr := net.ListenPacket("udp", addr)
		if uerr != nil {
			_ = l.Close()
			continue
		}
		used[addr] = true
		tcpLs = append(tcpLs, l)
		udpCs = append(udpCs, u)
		addrs = append(addrs, addr)
	}
	if len(addrs) < n {
		return nil, fmt.Errorf("no %d free loopback ports", n)
	}
	return addrs, nil
}

// startLoopbackClusterWith starts n loopback runtimes and retries the whole
// cluster on fresh addresses up to maxLoopbackStartAttempts times if a port is
// taken between reservation release and bind. Already-started runtimes are
// closed on every retry or failure path so no goroutines leak.
func startLoopbackClusterWith(t testing.TB, n int, pick func(int) ([]string, error), makeCfg func(i int, ids []env.NodeID, dir string) RuntimeConfig) ([]*Runtime, []string, error) {
	t.Helper()
	if pick == nil {
		pick = FreeLoopbackAddrs
	}
	var lastErr error
	for range maxLoopbackStartAttempts {
		addrs, err := pick(n)
		if err != nil {
			return nil, nil, err
		}
		if len(addrs) != n {
			return nil, nil, fmt.Errorf("address picker returned %d addresses, want %d", len(addrs), n)
		}
		ids := make([]env.NodeID, n)
		for i, a := range addrs {
			ids[i] = env.NodeID(a)
		}
		rts := make([]*Runtime, 0, n)
		dirs := make([]string, 0, n)
		closeAll := func() error {
			var errs []error
			for _, rt := range rts {
				if cerr := rt.Close(); cerr != nil {
					errs = append(errs, cerr)
				}
			}
			return errors.Join(errs...)
		}
		retry := false
		for i := range n {
			dir := t.TempDir()
			dirs = append(dirs, dir)
			rt, err := NewRuntime(makeCfg(i, ids, dir))
			if err != nil {
				return nil, nil, errors.Join(err, closeAll())
			}
			if err := rt.Start(context.Background()); err != nil {
				cerr := closeAll()
				if isAddrInUse(err) && cerr == nil {
					lastErr = err
					retry = true
					break
				}
				return nil, nil, errors.Join(err, cerr)
			}
			rts = append(rts, rt)
		}
		if !retry {
			return rts, dirs, nil
		}
	}
	return nil, nil, lastErr
}

func startLoopbackRuntime(t *testing.T, makeCfg func(addr, dir string) RuntimeConfig) *Runtime {
	t.Helper()
	rts, _, err := startLoopbackClusterWith(t, 1, nil, func(_ int, ids []env.NodeID, dir string) RuntimeConfig {
		return makeCfg(string(ids[0]), dir)
	})
	if err != nil {
		t.Fatal(err)
	}
	return rts[0]
}

// StartLoopbackCluster starts n loopback runtimes with bounded retry on
// address-in-use bind races, closing any started runtimes before retrying or
// returning an error. A nil pick uses FreeLoopbackAddrs.
func StartLoopbackCluster(t testing.TB, n int, pick func(int) ([]string, error), makeCfg func(i int, ids []env.NodeID, dir string) RuntimeConfig) ([]*Runtime, []string, error) {
	t.Helper()
	return startLoopbackClusterWith(t, n, pick, makeCfg)
}
