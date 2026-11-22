package noderuntime

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
	"github.com/nickstrad/decentralized-object-store/internal/testutil/leakcheck"
)

func loopbackAddr(t *testing.T) string {
	t.Helper()
	for range 20 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		u, uerr := net.ListenPacket("udp", addr)
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		if uerr == nil {
			if err := u.Close(); err != nil {
				t.Fatal(err)
			}
			return addr
		}
	}
	t.Fatal("no free loopback port")
	return ""
}

// TestALoopPanicTakesTheWholeNetworkedRuntimeDown proves a panic on the node
// loop closes Done(), records ErrPanicked, logs the panic, closes TCP/UDP
// sockets so all goroutines exit before Close is called, and still lets
// Shutdown return cleanly.
func TestALoopPanicTakesTheWholeNetworkedRuntimeDown(t *testing.T) {
	before := leakcheck.Take()
	var mu sync.Mutex
	var logged []string
	rt, err := NewRuntime(RuntimeConfig{ID: env.NodeID(loopbackAddr(t)), Region: "r0", DataDir: t.TempDir(), Node: node.Config{Layout: soloLayout()},
		Logf: func(f string, a ...any) { mu.Lock(); logged = append(logged, f); mu.Unlock() }})
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	addr := rt.Addr()
	if err := rt.Inspect(context.Background(), func(*node.Node) { panic("injected") }); !errors.Is(err, env.ErrClosed) {
		t.Fatalf("the call whose function panicked returned %v", err)
	}
	select {
	case <-rt.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("the runtime never reported that its loop stopped")
	}
	if !rt.Panicked() || !errors.Is(rt.Err(), ErrPanicked) {
		t.Fatalf("panicked %v, err %v", rt.Panicked(), rt.Err())
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		_ = c.Close()
		t.Fatal("the listener still accepts connections after the loop panicked")
	}
	if err := before.Leaked(5 * time.Second); err != nil {
		t.Fatalf("goroutines left running after the loop panicked, before any Close: %v", err)
	}
	stopped := make(chan error, 1)
	go func() { stopped <- rt.Shutdown(context.Background()) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("shutdown after a panic: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown after a panic never returned")
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(logged, "\n"), "panic") {
		t.Fatalf("the panic was not logged: %q", logged)
	}
}

// TestAStalledNameLookupNeverStallsTheLoop proves a blocked hostname lookup
// drops the first datagram without stalling Health on the loop and caches the
// resolved address once the lookup completes.
func TestAStalledNameLookupNeverStallsTheLoop(t *testing.T) {
	release := make(chan struct{})
	asked := make(chan string, 4)
	rt, err := NewRuntime(RuntimeConfig{ID: env.NodeID(loopbackAddr(t)), Region: "r0", DataDir: t.TempDir(), Node: node.Config{Layout: soloLayout()},
		Lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			asked <- host
			select {
			case <-release:
				return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := rt.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var sendErr error
	if err := rt.Inspect(ctx, func(*node.Node) { sendErr = (&realNet{rt: rt}).Send("stalls.example:7", []byte{1}) }); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(sendErr, env.ErrUnreachable) {
		t.Fatalf("a send to an unresolved host returned %v, want it dropped as unreachable", sendErr)
	}
	if got := <-asked; got != "stalls.example" {
		t.Fatalf("looked up %q", got)
	}
	hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if _, err := rt.Health(hctx); err != nil {
		t.Fatalf("the loop did not answer while a lookup was stalled: %v", err)
	}
	close(release)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var resolved bool
		if err := rt.Inspect(ctx, func(*node.Node) { _, resolved = rt.addrs["stalls.example:7"] }); err != nil {
			t.Fatal(err)
		}
		if resolved {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the lookup result never reached the loop")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := rt.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestCloseWhileStartIsInProgress proves a concurrent Close while Start is
// inside stateStarting aborts Start with env.ErrClosed and leaves the runtime
// closed.
func TestCloseWhileStartIsInProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newLoopRuntime(t)
		entered, resume := make(chan struct{}), make(chan struct{})
		rt.startHook = func() { close(entered); <-resume }
		errc := make(chan error, 1)
		go func() { errc <- rt.Start(context.Background()) }()
		<-entered
		closed := make(chan error, 1)
		go func() { closed <- rt.Close() }()
		synctest.Wait()
		close(resume)
		if err := <-errc; !errors.Is(err, env.ErrClosed) {
			t.Fatalf("start interrupted by a close returned %v", err)
		}
		if err := <-closed; err != nil {
			t.Fatalf("close during start-up: %v", err)
		}
		if _, err := rt.Members(context.Background()); !errors.Is(err, env.ErrClosed) {
			t.Fatalf("use after a close during start-up: %v", err)
		}
	})
}

// TestACallCanceledBeforeItReachesTheLoopNeverApplies proves pre-canceled
// calls and a PUT canceled while queued behind a busy loop return
// context.Canceled without ErrOutcomeUnknown and never reach the node.
func TestACallCanceledBeforeItReachesTheLoopNeverApplies(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt := newLoopRuntime(t)
		bg := context.Background()
		if err := rt.Start(bg); err != nil {
			t.Fatal(err)
		}
		if err := rt.CreateBucket(bg, storage.BucketSpec{Name: "b"}); err != nil {
			t.Fatal(err)
		}
		canceled, cancel := context.WithCancel(bg)
		cancel()
		for range 20 {
			if err := rt.CreateBucket(canceled, storage.BucketSpec{Name: "c"}); !errors.Is(err, context.Canceled) {
				t.Fatalf("create with a canceled context: %v", err)
			}
		}
		hold := make(chan struct{})
		rt.post(func() { <-hold })
		ctx, cancel := context.WithCancel(bg)
		errc := make(chan error, 1)
		go func() {
			_, err := rt.PutObject(ctx, storage.PutRequest{Bucket: "b", Key: "k", Data: make([]byte, 4096)})
			errc <- err
		}()
		synctest.Wait()
		cancel()
		err := <-errc
		close(hold)
		synctest.Wait()
		if !errors.Is(err, context.Canceled) || errors.Is(err, ErrOutcomeUnknown) {
			t.Fatalf("a put canceled while queued returned %v, want canceled and known not applied", err)
		}
		if _, err := rt.HeadObject(bg, "b", "k"); !errors.Is(err, storage.ErrNotFound) && !strings.Contains(err.Error(), "not found") {
			t.Fatalf("head after a put canceled while queued: %v", err)
		}
		if err := rt.Inspect(bg, func(n *node.Node) {
			if s := n.Storage().Stats(); s.PutsDone+s.PutsFail != 0 {
				t.Errorf("the canceled put reached storage: %+v", s)
			}
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := rt.HeadObject(bg, "c", "x"); !errors.Is(err, storage.ErrNoSuchBucket) {
			t.Fatalf("a bucket created with a canceled context exists: %v", err)
		}
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

// TestACallCanceledAfterTheLoopTookItAppliesOnceAndIsReported proves PUT, GET,
// and migration start canceled after the loop dequeues them return
// ErrOutcomeUnknown joined with context.Canceled, execute once, and are counted
// and logged as abandoned calls.
func TestACallCanceledAfterTheLoopTookItAppliesOnceAndIsReported(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var logged []string
		rt, err := NewRuntime(RuntimeConfig{ID: "solo", Region: "r0", DataDir: t.TempDir(), NoNetwork: true, Node: node.Config{Layout: soloLayout()},
			Logf: func(f string, a ...any) { mu.Lock(); logged = append(logged, f); mu.Unlock() }})
		if err != nil {
			t.Fatal(err)
		}
		bg := context.Background()
		if err := rt.Start(bg); err != nil {
			t.Fatal(err)
		}
		if err := rt.CreateBucket(bg, storage.BucketSpec{Name: "b", Region: "r0"}); err != nil {
			t.Fatal(err)
		}
		for _, op := range []string{"put", "get", "migrate"} {
			ctx, cancel := context.WithCancel(bg)
			rt.dequeueHook = func(name string, left <-chan struct{}) {
				if name == op {
					cancel()
					<-left
				}
			}
			var err error
			switch op {
			case "put":
				_, err = rt.PutObject(ctx, storage.PutRequest{Bucket: "b", Key: "k", Data: []byte("v")})
			case "get":
				_, _, err = rt.GetObject(ctx, "b", "k")
			case "migrate":
				_, err = rt.StartMigration(ctx, "b", "r1")
			}
			rt.dequeueHook = nil
			if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("%s canceled after the loop took it returned %v, want canceled with outcome unknown", op, err)
			}
			synctest.Wait()
		}
		if rt.Abandoned() != 3 {
			t.Fatalf("%d calls reported as finished after their callers left, want 3", rt.Abandoned())
		}
		if _, data, err := rt.GetObject(bg, "b", "k"); err != nil || string(data) != "v" {
			t.Fatalf("the abandoned put did not apply exactly once: %q %v", data, err)
		}
		if err := rt.Inspect(bg, func(n *node.Node) {
			if s := n.Stats(); s.Puts != 1 {
				t.Errorf("%d puts applied, want 1", s.Puts)
			}
		}); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		joined := strings.Join(logged, "\n")
		mu.Unlock()
		if strings.Count(joined, "after its caller left") != 3 {
			t.Fatalf("abandoned calls not reported in the log: %q", logged)
		}
		if err := rt.Close(); err != nil {
			t.Fatal(err)
		}
	})
}
