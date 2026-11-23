package e2e

import (
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
)

var simBounds = bounds{
	converge:  30 * time.Second,
	repair:    30 * time.Second,
	migrate:   40 * time.Second,
	catchUp:   30 * time.Second,
	refill:    60 * time.Second,
	readBatch: 20,
}

func runSim(t *testing.T, seed int64, opts simOptions, body func(c *simCluster) error) *sim.Sim {
	t.Helper()
	return simtest.RequireDeterministic(t, seed, func(seed int64) *sim.Sim {
		c := newSimCluster(seed, opts)
		if c.s.Err() != nil {
			return c.s
		}
		if err := c.boot(); err != nil {
			c.fail(err)
			return c.s
		}
		if err := body(c); err != nil {
			c.fail(err)
		}
		return c.s
	})
}

const sweepShards = 4

func shardOf(seed int64) int {
	return int(uint64(seed) % sweepShards)
}

func sweep(t *testing.T, seeds []int64, fn func(t *testing.T, seed int64)) {
	t.Helper()
	var ran atomic.Int64
	t.Cleanup(func() {
		if explicit, ok, err := simtest.Seed(); err == nil && ok && !t.Failed() && ran.Load() == 0 {
			t.Fatalf("explicit -seed=%d ran 0 seed subtests (check -run pattern)", explicit)
		}
	})
	for sh := range sweepShards {
		var mine []int64
		for _, s := range seeds {
			if shardOf(s) == sh {
				mine = append(mine, s)
			}
		}
		if len(mine) == 0 {
			continue
		}
		t.Run(fmt.Sprintf("shard=%d", sh), func(t *testing.T) {
			t.Parallel()
			for _, seed := range mine {
				t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
					ran.Add(1)
					fn(t, seed)
				})
			}
		})
	}
}

const fullSeedsEnv = "E2E_FULL_SEEDS"

func seedsFor(t *testing.T, num, den int) []int64 {
	t.Helper()
	all := simtest.Seeds(t)
	if _, ok, _ := simtest.Seed(); ok || os.Getenv(fullSeedsEnv) == "1" {
		return all
	}
	pinned, err := simtest.PinnedSeeds("testdata")
	if err != nil {
		t.Fatal(err)
	}
	var out, rest []int64
	for _, s := range all {
		if slices.Contains(pinned, s) {
			out = append(out, s)
		} else {
			rest = append(rest, s)
		}
	}
	n := max(1, len(rest)*num/max(den, 1))
	if raceEnabled {
		n = 1
	}
	return append(out, rest[:min(n, len(rest))]...)
}

type measure struct {
	mu   sync.Mutex
	vals map[string][]int64
}

func (m *measure) add(name string, v int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.vals == nil {
		m.vals = map[string][]int64{}
	}
	m.vals[name] = append(m.vals[name], v)
}

func (m *measure) ms(name string, d time.Duration) { m.add(name+"_ms", d.Milliseconds()) }

func (m *measure) report(t *testing.T) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k := range m.vals {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v := slices.Clone(m.vals[k])
		slices.Sort(v)
		t.Logf("MEASURE %s n=%d min=%d p50=%d p99=%d max=%d", k, len(v), v[0], v[len(v)/2], v[min(len(v)-1, len(v)*99/100)], v[len(v)-1])
	}
}
