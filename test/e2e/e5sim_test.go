package e2e

import (
	"bytes"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/saga/sagasim"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

const (
	e5From    = "r0"
	e5To      = "r1"
	e5Objects = 6
)

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

func TestE5MigrationEveryFailurePointS(t *testing.T) {
	t.Parallel()
	cells := sagasim.Cells()
	var m measure
	t.Cleanup(func() { m.report(t) })
	seeds := simtest.Seeds(t)
	pinned, err := simtest.PinnedSeeds("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for i, cell := range cells {
		mine := e5Seeds(seeds, pinned, i, len(cells))
		t.Run(cell.Name(), func(t *testing.T) {
			t.Parallel()
			for _, seed := range mine {
				t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
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
