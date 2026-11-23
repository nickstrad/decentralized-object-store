package leakcheck_test

import (
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/testutil/leakcheck"
)

func TestLeakedReportsAGoroutineStartedAfterTheSnapshot(t *testing.T) {
	before := leakcheck.Take()
	release := make(chan struct{})
	go func() { <-release }()
	err := before.Leaked(50 * time.Millisecond)
	if !errors.Is(err, leakcheck.ErrLeak) {
		t.Fatalf("a blocked goroutine went unreported: %v", err)
	}
	close(release)
	if err := before.Leaked(2 * time.Second); err != nil {
		t.Fatalf("an exited goroutine is still reported: %v", err)
	}
}

func TestLeakedWaitsForAGoroutineThatIsExiting(t *testing.T) {
	before := leakcheck.Take()
	go func() { time.Sleep(30 * time.Millisecond) }()
	if err := before.Leaked(2 * time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestMain(m *testing.M) { os.Exit(leakcheck.Main(m, time.Second)) }
