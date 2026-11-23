package e2e

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/node"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/saga"
	"github.com/nickstrad/object-store/internal/saga/sagasim"
	"github.com/nickstrad/object-store/internal/sim"
	"github.com/nickstrad/object-store/internal/sim/simtest"
	"github.com/nickstrad/object-store/internal/storage"
)

const (
	e5From    = "r0"
	e5To      = "r1"
	e5Objects = 6
)

// e5Harness adapts the tier S e2e cluster (regional clients over the simulated
// network plus 60 rounds of background traffic) to sagasim.Harness so
// sagasim.Run can drive every cell of the 78-cell migration failure matrix.
type e5Harness struct{}

type e5Cluster struct {
	c     *simCluster
	coord env.NodeID
	data  map[string][]byte
}

func (e5Harness) Build(seed int64, o sagasim.BuildOptions) (*sim.Sim, sagasim.Cluster, error) {
	opts := defaultSimOptions()
	opts.tweak = func(cfg *node.Config) {
		cfg.SagaPolicy = o.Policy
		cfg.WrapMigration = o.Wrap
		cfg.ConfigureSaga = o.Configure
	}
	c := newSimCluster(seed, opts)
	if err := c.s.Err(); err != nil {
		return c.s, nil, err
	}
	if err := c.boot(); err != nil {
		return c.s, nil, err
	}
	if err := createBucket(c, simBounds, storage.BucketSpec{Name: sagasim.Bucket, Region: e5From}); err != nil {
		return c.s, nil, err
	}
	if err := createBucket(c, simBounds, storage.BucketSpec{Name: "bg"}); err != nil {
		return c.s, nil, err
	}
	ec := &e5Cluster{c: c, coord: c.ids[2], data: map[string][]byte{}}
	var puts []*Call
	for i := range e5Objects {
		key := fmt.Sprintf("o%d", i)
		data := randomBytes(c.Rand(), 1+c.Rand().IntN(2*simStripe))
		ec.data[key] = data
		puts = append(puts, c.Put(pick(c, c.nodes), sagasim.Bucket, key, data, storage.Condition{}))
	}
	if err := waitAll(c, puts...); err != nil {
		return c.s, nil, err
	}
	if _, err := c.Await("objects replicated", simBounds.converge, c.Converged); err != nil {
		return c.s, nil, err
	}
	c.s.Workload(c.s.Now(), 60, 500*time.Millisecond, func(i int) {
		c.Put(pick(c, c.nodes), "bg", fmt.Sprintf("bg%03d", i), randomBytes(c.Rand(), 300+i), storage.Condition{})
		c.Get(pick(c, c.nodes), sagasim.Bucket, fmt.Sprintf("o%d", i%e5Objects))
	})
	return c.s, ec, nil
}

func (e *e5Cluster) Coordinator() env.NodeID { return e.coord }

func (e *e5Cluster) Begin(id saga.ID) error {
	n := e.c.node(e.coord)
	if n == nil {
		return env.ErrUnreachable
	}
	return n.BeginMigration(id, saga.MigrationInput{Bucket: sagasim.Bucket, From: e5From, To: e5To})
}

func (e *e5Cluster) Status(id saga.ID) (saga.Status, bool) {
	n := e.c.node(e.coord)
	if n == nil {
		return saga.Status{}, false
	}
	st, err := n.Engine().Status(id)
	return st, err == nil
}

func (e *e5Cluster) LogName(id saga.ID) string { return saga.DefaultPrefix + string(id) }

func e5Check(f string, a ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(f, a...), sagasim.ErrCheck)
}

// check verifies every up node routes sagasim.Bucket to loc, holds no live
// staged entries, decodes every source object byte-for-byte from durable shards
// in loc, leaves zero shards of the bucket in gone, and saw no wrong reads.
func (e *e5Cluster) check(loc, gone string) error {
	c := e.c
	ns := c.upNodes()
	if len(ns) == 0 {
		return e5Check("no node up")
	}
	for _, n := range ns {
		rd, err := n.OCC().Reader()
		if err != nil {
			return e5Check("node %s not ready", n.ID())
		}
		r, err := rd.Route(sagasim.Bucket)
		if err != nil || string(r) != loc {
			return e5Check("node %s routes %s to %q (%v), want %q", n.ID(), sagasim.Bucket, r, err, loc)
		}
		for _, k := range rd.Keys() {
			_, parts, err := occ.ParseKey(k)
			if err == nil && parts[0] == sagasim.Bucket && len(parts) > 2 && strings.HasPrefix(parts[2], "~stage/") && rd.Record(k).Live() {
				return e5Check("node %s still holds a staged entry for %s", n.ID(), parts[1])
			}
		}
	}
	rd, err := ns[0].OCC().Reader()
	if err != nil {
		return err
	}
	if len(e.data) == 0 {
		return e5Check("no objects in migrating bucket %s", sagasim.Bucket)
	}
	keys := make([]string, 0, len(e.data))
	for k := range e.data {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		mk := occ.ManifestKey(sagasim.Bucket, key)
		v, ok := rd.Record(mk).Version()
		man := manifestOf(mk, v)
		if !ok || man == nil {
			return e5Check("object %s has no live manifest", key)
		}
		got, err := e.read(man, loc)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, e.data[key]) {
			return e5Check("object %s in %s reads wrong bytes", key, loc)
		}
	}
	for _, id := range c.ids {
		if string(c.s.Region(id)) != gone {
			continue
		}
		for _, name := range c.s.Disk(id).Names() {
			if strings.HasPrefix(name, "shards/"+sagasim.Bucket+"/") {
				return e5Check("shard %s of %s left on %s in %s", name, sagasim.Bucket, id, gone)
			}
		}
	}
	return c.ledger.Wrong()
}

func (e *e5Cluster) read(man *storage.Manifest, loc string) ([]byte, error) {
	codec, err := man.Layout.Codec()
	if err != nil {
		return nil, err
	}
	out := []byte{}
	for st, ref := range man.Stripes {
		blobs := make([][]byte, len(ref.Shards))
		want := make([][32]byte, len(ref.Shards))
		for i, sh := range ref.Shards {
			if string(e.c.s.Region(sh.Node)) != loc {
				return nil, e5Check("object %s shard %d.%d placed on %s outside %s", man.Key, st, i, sh.Node, loc)
			}
			want[i] = sh.Checksum
			if b, ok := e.c.s.Disk(sh.Node).PeekDurable(man.ShardID(st, i).BlobName()); ok {
				blobs[i] = b
			}
		}
		data, _, err := storage.DecodeStripe(codec, man.Version, uint32(st), man.Layout.StripeLen(man.Size, st), blobs, want)
		if err != nil {
			return nil, e5Check("object %s stripe %d: %v", man.Key, st, err)
		}
		out = append(out, data...)
	}
	return out, nil
}

func (e *e5Cluster) CheckMoved() error       { return e.check(e5To, e5From) }
func (e *e5Cluster) CheckCompensated() error { return e.check(e5From, e5To) }

// e5Seeds assigns all pinned seeds, the first unpinned seed, and a strided
// share of remaining unpinned seeds to cell so every cell runs at least once
// without multiplying sweep cost by all 78 cells.
func e5Seeds(seeds, pinned []int64, cell, cells int) []int64 {
	var out, rest []int64
	for _, s := range seeds {
		if slices.Contains(pinned, s) {
			out = append(out, s)
		} else {
			rest = append(rest, s)
		}
	}
	if len(rest) == 0 {
		return out
	}
	out = append(out, rest[0])
	for j := 1 + cell; j < len(rest); j += cells {
		out = append(out, rest[j])
	}
	return out
}

const e5TrimStride = 8

// e5CellSeeds selects the seeds for one failure-matrix cell, trimming -short
// (unless overridden by -seed/DST_SEED or E2E_FULL_SEEDS=1) to every eighth
// cell on its first seed and -race to every eighth cell.
func e5CellSeeds(cell, cells int, short, race, explicitSeed, fullSeeds bool, seeds, pinned []int64) []int64 {
	trimShort := short && !explicitSeed && !fullSeeds
	if (race || trimShort) && cell%e5TrimStride != 0 {
		return nil
	}
	mine := e5Seeds(seeds, pinned, cell, cells)
	if trimShort && len(mine) > 1 {
		return mine[:1:1]
	}
	return mine
}

// TestE5CellSeeds proves e5CellSeeds preserves full cell/seed coverage outside
// -short, trims -short to 10/78 cells on one seed each, and lets explicit -seed
// or E2E_FULL_SEEDS=1 override -short.
func TestE5CellSeeds(t *testing.T) {
	const cells = 78
	pinned := []int64{166963932986536, 1}
	defaultSeeds := []int64{166963932986536, 1, 2}
	manySeeds := make([]int64, 0, 127)
	manySeeds = append(manySeeds, pinned...)
	for s := int64(2); s < 127; s++ {
		manySeeds = append(manySeeds, s)
	}
	unpinnedSeeds := []int64{10, 11, 12, 13, 14}
	singlePinned := []int64{1}
	singleUnpinned := []int64{42}

	seedSets := []struct {
		name   string
		seeds  []int64
		pinned []int64
	}{
		{"default", defaultSeeds, pinned},
		{"many", manySeeds, pinned},
		{"unpinned", unpinnedSeeds, nil},
		{"single-pinned", singlePinned, pinned},
		{"single-unpinned", singleUnpinned, pinned},
	}

	t.Run("non-short matches e5Seeds and race filter", func(t *testing.T) {
		for _, ss := range seedSets {
			for _, race := range []bool{false, true} {
				for _, explicit := range []bool{false, true} {
					for _, full := range []bool{false, true} {
						for i := range cells {
							got := e5CellSeeds(i, cells, false, race, explicit, full, ss.seeds, ss.pinned)
							if race && i%e5TrimStride != 0 {
								if len(got) != 0 {
									t.Fatalf("%s race=%v cell=%d: got %v, want empty", ss.name, race, i, got)
								}
								continue
							}
							want := e5Seeds(ss.seeds, ss.pinned, i, cells)
							if !slices.Equal(got, want) {
								t.Fatalf("%s race=%v explicit=%v full=%v cell=%d: got %v, want %v", ss.name, race, explicit, full, i, got, want)
							}
							if len(got) == 0 {
								t.Fatalf("%s race=%v cell=%d: kept cell got 0 seeds", ss.name, race, i)
							}
						}
					}
				}
			}
		}
	})

	t.Run("short trims to every eighth cell with first seed", func(t *testing.T) {
		for _, ss := range seedSets {
			for _, race := range []bool{false, true} {
				kept := 0
				for i := range cells {
					got := e5CellSeeds(i, cells, true, race, false, false, ss.seeds, ss.pinned)
					if i%e5TrimStride != 0 {
						if len(got) != 0 {
							t.Fatalf("%s race=%v cell=%d: got %v, want empty", ss.name, race, i, got)
						}
						continue
					}
					kept++
					full := e5Seeds(ss.seeds, ss.pinned, i, cells)
					if len(got) != 1 || got[0] != full[0] {
						t.Fatalf("%s race=%v cell=%d: got %v, want [%d]", ss.name, race, i, got, full[0])
					}
				}
				if kept != 10 {
					t.Fatalf("%s race=%v: kept %d cells, want 10", ss.name, race, kept)
				}
			}
		}
	})

	t.Run("short overridden by explicit or full matches non-short", func(t *testing.T) {
		for _, ss := range seedSets {
			for _, race := range []bool{false, true} {
				for _, override := range []struct {
					name     string
					explicit bool
					full     bool
				}{
					{"explicit", true, false},
					{"full", false, true},
					{"both", true, true},
				} {
					kept := 0
					for i := range cells {
						got := e5CellSeeds(i, cells, true, race, override.explicit, override.full, ss.seeds, ss.pinned)
						want := e5CellSeeds(i, cells, false, race, override.explicit, override.full, ss.seeds, ss.pinned)
						if !slices.Equal(got, want) {
							t.Fatalf("%s %s race=%v cell=%d: got %v, want %v", ss.name, override.name, race, i, got, want)
						}
						if len(got) > 0 {
							kept++
						} else if !race || i%e5TrimStride == 0 {
							t.Fatalf("%s %s race=%v cell=%d: kept cell got 0 seeds", ss.name, override.name, race, i)
						}
					}
					wantKept := cells
					if race {
						wantKept = 10
					}
					if kept != wantKept {
						t.Fatalf("%s %s race=%v: kept %d cells, want %d", ss.name, override.name, race, kept, wantKept)
					}
				}
			}
		}
	})
}

// TestE5MigrationEveryFailurePointS runs the 78-cell saga failure matrix
// against a full 21-node tier S cluster with background client traffic, proving
// every crash, torn-log, and step-failure cell ends Completed or Compensated as
// expected with zero orphan shards and intact data.
func TestE5MigrationEveryFailurePointS(t *testing.T) {
	t.Parallel()
	cells := sagasim.Cells()
	if len(cells) == 0 {
		t.Fatal("sagasim.Cells() is empty")
	}
	var m measure
	var ran atomic.Int64
	t.Cleanup(func() {
		m.report(t)
		if explicit, ok, err := simtest.Seed(); err == nil && ok && !t.Failed() && ran.Load() == 0 {
			t.Fatalf("explicit -seed=%d ran 0 seed subtests (check -run pattern)", explicit)
		}
	})
	seeds := seedsFor(t, 1, 4)
	if len(seeds) == 0 {
		t.Fatal("seedsFor returned 0 seeds")
	}
	pinned, err := simtest.PinnedSeeds("testdata")
	if err != nil {
		t.Fatal(err)
	}
	_, explicitSeed, err := simtest.Seed()
	if err != nil {
		t.Fatal(err)
	}
	fullSeeds := os.Getenv(fullSeedsEnv) == "1"
	short := testing.Short()
	trimShort := short && !explicitSeed && !fullSeeds
	plan := make([][]int64, len(cells))
	ranCells, ranRuns, totalRuns := 0, 0, 0
	for i := range cells {
		totalRuns += len(e5Seeds(seeds, pinned, i, len(cells)))
		mine := e5CellSeeds(i, len(cells), short, raceEnabled, explicitSeed, fullSeeds, seeds, pinned)
		if len(mine) == 0 {
			continue
		}
		plan[i] = mine
		ranCells++
		ranRuns += len(mine)
	}
	if ranCells == 0 {
		t.Fatal("e5 matrix selected 0 cells")
	}
	if !trimShort && !raceEnabled && ranCells != len(cells) {
		t.Fatalf("e5 matrix selected %d/%d cells", ranCells, len(cells))
	}
	if trimShort {
		t.Logf("e5 short smoke: %d/%d cells, %d/%d seed runs (1 seed per kept cell)", ranCells, len(cells), ranRuns, totalRuns)
	}
	for i, cell := range cells {
		mine := plan[i]
		if len(mine) == 0 {
			continue
		}
		t.Run(cell.Name(), func(t *testing.T) {
			t.Parallel()
			for _, seed := range mine {
				t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
					ran.Add(1)
					if msg := os.Getenv("E2E_PLANT_E5_FAIL"); msg != "" {
						simtest.RequireDeterministic(t, seed, func(seed int64) *sim.Sim {
							s := sim.New(sim.Config{Seed: seed})
							s.Fail(failf("%s (cell=%s seed=%d)", msg, cell.Name(), seed))
							return s
						})
						return
					}
					rep := &sagasim.Report{}
					simtest.RequireDeterministic(t, seed, sagasim.Run(e5Harness{}, cell, sagasim.RunOptions{Report: rep, Settle: 8 * time.Second}))
					if rep.State != cell.Want() {
						t.Fatalf("terminal state %v, want %v", rep.State, cell.Want())
					}
					m.ms("e5/begin_to_terminal", rep.Elapsed)
					m.add("e5/crashes", int64(rep.Crashes))
					if rep.State == saga.StateCompensated {
						m.add("e5/compensated", 1)
					} else {
						m.add("e5/completed", 1)
					}
				})
			}
		})
	}
}
