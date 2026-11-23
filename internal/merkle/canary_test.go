package merkle_test

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/merkle"
	"github.com/nickstrad/object-store/internal/sim"
	"github.com/nickstrad/object-store/internal/sim/simtest"
)

var canarySeeds = []int64{1, 2, 3}

// TestCanarySkippedBranch plants the bug SetSkipBranch(true) (ignoring child 15
// mismatches in Session) and proves that both the completeness check and the
// three-node DST convergence scenario catch it (stalling with sim.ErrDeadline)
// while passing cleanly without the bug.
func TestCanarySkippedBranch(t *testing.T) {
	local := map[string]merkle.Hash{}
	remote := map[string]merkle.Hash{}
	for i, k := range keysInLeaf(t, 1, 15, 8) {
		remote[k] = sum(fmt.Sprint(i))
	}
	for i := 0; i < 50; i++ {
		k := fmt.Sprintf("shared-%d", i)
		local[k], remote[k] = sum(k), sum(k)
	}
	compare := func() error {
		res, err := merkle.Compare(treeOf(t, 2, local), treeOf(t, 2, remote), merkle.Limits{})
		if err != nil {
			return err
		}
		return checkComplete(local, remote, res)
	}
	if err := compare(); err != nil {
		t.Fatalf("the completeness check fails without the planted bug: %v", err)
	}
	t.Run("the completeness check catches the skipped branch", func(t *testing.T) {
		defer merkle.SetSkipBranch(true)()
		if err := compare(); err == nil {
			t.Fatal("a diff that skips branch f passed the completeness check")
		}
	})
	for _, seed := range canarySeeds {
		s, err := simtest.CheckDeterministic(seed, canaryRun)
		if w := take(s); err != nil || w.convergeAt == 0 {
			t.Fatalf("seed %d: the scenario does not converge without the planted bug: %v", seed, err)
		}
	}
	t.Run("the missed-write scenario fails to converge", func(t *testing.T) {
		defer merkle.SetSkipBranch(true)()
		for _, seed := range canarySeeds {
			s, err := simtest.CheckDeterministic(seed, canaryRun)
			w := take(s)
			if !errors.Is(err, sim.ErrDeadline) || w.convergeAt != 0 {
				t.Fatalf("seed %d: want a convergence deadline stall, got err=%v convergeAt=%v", seed, err, w.convergeAt)
			}
		}
	})
}

func canaryRun(seed int64) *sim.Sim {
	w := newWorld(newSim(seed), merkle.DepthFor(dstChunks))
	if err := w.cluster(3, false, nil); err != nil {
		w.s.Fail(err)
		return remember(w)
	}
	w.writes(dstChunks, dstWriteStart, dstWriteWindow)
	w.quietAt = w.writesAt
	if err := w.s.RunUntil(w.quietAt); err != nil {
		return remember(w)
	}
	w.finish(w.quietAt.Add(MissedWriteBound))
	return remember(w)
}

// TestMeasureDiffCost is an opt-in benchmark table (enabled via
// MERKLE_MEASURE=1) that prints exact round trips, hash counts, bounds, leaf
// load, and wire bytes across N = 1e3..1e5 and d = 1..100.
func TestMeasureDiffCost(t *testing.T) {
	if os.Getenv(measureEnv) == "" {
		t.Skip("set " + measureEnv + "=1 to print the cost table")
	}
	for _, n := range []int{1_000, 10_000, 100_000} {
		depth := merkle.DepthFor(n)
		entries := make([]merkle.Entry, 0, n)
		for i := 0; i < n; i++ {
			k := fmt.Sprintf("object-%07d", i)
			entries = append(entries, merkle.Entry{Key: k, Hash: sum(k)})
		}
		base, err := merkle.Build(depth, entries)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range []int{1, 10, 100} {
			other := base.Snapshot()
			for i := 0; i < d; i++ {
				k := fmt.Sprintf("object-%07d", (i*7919)%n)
				switch i % 3 {
				case 0:
					err = other.Set(k, sum("changed"+k))
				case 1:
					other.Delete(k)
				default:
					err = other.Set(fmt.Sprintf("new-%d", i), sum(k))
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			start := time.Now()
			res, err := merkle.Compare(base, other, merkle.Limits{})
			if err != nil {
				t.Fatal(err)
			}
			el := time.Since(start)
			fmt.Printf("MEASURE N=%d depth=%d d=%d found=%d roundtrips=%d hashes=%d bound16d(depth+1)=%d exactbound=%d leafload=%d bytes=%d+%d time=%v\n",
				n, depth, d, len(res.Diffs), res.Cost.RoundTrips, res.Cost.Hashes, 16*d*(depth+1), costBound(d, depth, other.LeafLoad()),
				other.LeafLoad(), res.Cost.BytesSent, res.Cost.BytesReceived, el)
		}
	}
}
