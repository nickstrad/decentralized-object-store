// Phase 1 feasibility probe (not product code): native fuzzing, the race detector and
// testing/synctest virtual time all work on this toolchain.
package toolchain

import (
	"bytes"
	"context"
	"testing"
	"testing/synctest"
	"time"
)

func xorRoundTrip(b []byte, k byte) []byte {
	o := make([]byte, len(b))
	for i := range b {
		o[i] = b[i] ^ k
	}
	return o
}

func FuzzProbeRoundTrip(f *testing.F) {
	f.Add([]byte(""), byte(0))
	f.Add([]byte("a"), byte(7))
	f.Fuzz(func(t *testing.T, b []byte, k byte) {
		if !bytes.Equal(xorRoundTrip(xorRoundTrip(b, k), k), b) {
			t.Fatal("round trip")
		}
	})
}

// A ticker goroutine owned by a context: under synctest an hour of virtual time passes
// instantly, and the bubble fails the test if the goroutine outlives cancellation.
func TestSynctestLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		ticks := 0
		done := make(chan struct{})
		go func() {
			defer close(done)
			tk := time.NewTicker(time.Second)
			defer tk.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tk.C:
					ticks++
				}
			}
		}()
		time.Sleep(time.Hour + time.Millisecond)
		cancel()
		<-done
		if ticks != 3600 {
			t.Fatalf("ticks = %d, want 3600", ticks)
		}
	})
}
