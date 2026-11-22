package noderuntime

import (
	"context"
	"errors"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
)

type callCount struct {
	n    int
	errs []error
}

func countOnLoop(t *testing.T, rt *Runtime, got *callCount) callCount {
	t.Helper()
	read := make(chan callCount, 1)
	if !rt.post(func() { read <- callCount{n: got.n, errs: append([]error(nil), got.errs...)} }) {
		t.Fatal("the loop refused the read")
	}
	return <-read
}

func issueOnLoop(t *testing.T, rt *Runtime, to env.NodeID, req []byte, timeout time.Duration) *callCount {
	t.Helper()
	got := &callCount{}
	issued := make(chan struct{})
	if !rt.post(func() {
		(&realNet{rt: rt}).Request(to, req, timeout, func(_ []byte, err error) {
			got.n++
			got.errs = append(got.errs, err)
		})
		close(issued)
	}) {
		t.Fatal("the loop refused the request")
	}
	<-issued
	return got
}

func TestRequestCallsBackExactlyOnceOnEveryPath(t *testing.T) {
	rows := []struct {
		name    string
		to      env.NodeID
		req     []byte
		timeout time.Duration
		want    error
	}{
		{"zero timeout", "solo", []byte{1}, 0, env.ErrInvalidArgument},
		{"negative timeout", "solo", []byte{1}, -time.Second, env.ErrInvalidArgument},
		{"zero timeout to a peer", "peer:1", []byte{1}, 0, env.ErrInvalidArgument},
		{"oversized request to itself", "solo", make([]byte, node.MaxFrame+1), 50 * time.Millisecond, env.ErrTooLarge},
		{"request to a peer without a network", "peer:1", []byte{1}, 50 * time.Millisecond, env.ErrUnreachable},
		{"request to itself", "solo", []byte{1}, 50 * time.Millisecond, nil},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rt := newLoopRuntime(t)
				if err := rt.Start(context.Background()); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := rt.Close(); err != nil {
						t.Error(err)
					}
				}()
				got := issueOnLoop(t, rt, r.to, r.req, r.timeout)
				time.Sleep(10*max(r.timeout, time.Second) + time.Second)
				synctest.Wait()
				c := countOnLoop(t, rt, got)
				if c.n != 1 {
					t.Fatalf("callback ran %d times (%v), want exactly once", c.n, c.errs)
				}
				if r.want != nil && !errors.Is(c.errs[0], r.want) {
					t.Fatalf("callback error %v, want %v", c.errs[0], r.want)
				}
			})
		})
	}
}

func TestNetworkedRequestCallsBackExactlyOnce(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(RuntimeConfig{ID: env.NodeID(addr), Region: "r0", Listen: addr, DataDir: t.TempDir(), Node: node.Config{Layout: soloLayout()}})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := rt.Close(); err != nil {
			t.Error(err)
		}
	}()
	rows := []struct {
		name string
		req  []byte
		want error
	}{
		{"oversized request to a peer", make([]byte, node.MaxFrame+1), env.ErrTooLarge},
		{"request to a peer that is not listening", []byte{1}, nil},
	}
	for _, r := range rows {
		got := issueOnLoop(t, rt, "127.0.0.1:1", r.req, 100*time.Millisecond)
		time.Sleep(500 * time.Millisecond)
		c := countOnLoop(t, rt, got)
		if c.n != 1 {
			t.Fatalf("%s: callback ran %d times (%v), want exactly once", r.name, c.n, c.errs)
		}
		if c.errs[0] == nil || r.want != nil && !errors.Is(c.errs[0], r.want) {
			t.Fatalf("%s: callback error %v, want %v", r.name, c.errs[0], r.want)
		}
	}
}
