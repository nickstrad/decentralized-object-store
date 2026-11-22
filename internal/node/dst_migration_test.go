package node_test

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/saga/sagasim"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

const (
	matrixSettle = 8 * time.Second
	migFrom      = "r0"
	migTo        = "r1"
	migObjects   = 6
)

type nodeHarness struct{}

type nodeCluster struct {
	h     *harness
	coord env.NodeID
	data  map[string][]byte
}

func (nodeHarness) Build(seed int64, o sagasim.BuildOptions) (*sim.Sim, sagasim.Cluster, error) {
	cfg := partitionConfig()
	cfg.SagaPolicy = o.Policy
	cfg.WrapMigration = o.Wrap
	cfg.ConfigureSaga = o.Configure
	cfg.ViewSettle = 2 * time.Second
	h, err := newHarness(seed, dstNodes, cfg, sim.DefaultNetConfig())
	if err != nil {
		return h.s, nil, err
	}
	c := &nodeCluster{h: h, coord: h.ids[2], data: map[string][]byte{}}
	s := h.s
	if err := s.RunFor(2 * time.Second); err != nil {
		return s, nil, err
	}
	if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: sagasim.Bucket, Region: migFrom}); err != nil {
		return s, nil, err
	}
	w := s.WorkloadRand()
	acked := 0
	var perr error
	for i := range migObjects {
		key := fmt.Sprintf("o%d", i)
		body := payload(w, 1+w.IntN(2*dstStripe))
		h.node(h.ids[0]).PutObject(storage.PutRequest{Bucket: sagasim.Bucket, Key: key, Data: body}, func(_ storage.ObjectInfo, err error) {
			if err != nil {
				perr = errors.Join(perr, err)
			}
			c.data[key] = body
			acked++
		})
	}
	if err := s.RunUntilConverged(s.Now().Add(20*time.Second), func() bool { return acked == migObjects && h.converged() }); err != nil {
		return s, nil, errors.Join(err, perr)
	}
	return s, c, perr
}

func (c *nodeCluster) Coordinator() env.NodeID { return c.coord }

func (c *nodeCluster) Begin(id saga.ID) error {
	n := c.h.node(c.coord)
	if n == nil {
		return env.ErrUnreachable
	}
	return n.BeginMigration(id, saga.MigrationInput{Bucket: sagasim.Bucket, From: migFrom, To: migTo})
}

func (c *nodeCluster) Status(id saga.ID) (saga.Status, bool) {
	n := c.h.node(c.coord)
	if n == nil {
		return saga.Status{}, false
	}
	st, err := n.Engine().Status(id)
	return st, err == nil
}

func (c *nodeCluster) LogName(id saga.ID) string { return saga.DefaultPrefix + string(id) }

func checkErr(f string, a ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(f, a...), sagasim.ErrCheck)
}

func (c *nodeCluster) checkAt(loc, gone string) error {
	h := c.h
	for _, n := range h.up() {
		rd, err := n.OCC().Reader()
		if err != nil {
			return checkErr("node %s not ready", n.ID())
		}
		r, err := rd.Route(sagasim.Bucket)
		if err != nil || string(r) != loc {
			return checkErr("node %s routes %s to %q (%v), want %q", n.ID(), sagasim.Bucket, r, err, loc)
		}
		for _, k := range rd.Keys() {
			_, parts, err := occ.ParseKey(k)
			if err != nil || parts[0] != sagasim.Bucket || len(parts) < 3 {
				continue
			}
			if strings.HasPrefix(parts[2], "~stage/") && rd.Record(k).Live() {
				return checkErr("node %s still holds staged entry for %s", n.ID(), parts[1])
			}
		}
	}
	ref := h.node(h.ids[0])
	if ref == nil {
		return checkErr("reference node down")
	}
	rd, _ := ref.OCC().Reader()
	for key, want := range c.data {
		val, _, err := rd.Get(occ.ManifestKey(sagasim.Bucket, key))
		if err != nil {
			return checkErr("object %s: %v", key, err)
		}
		man, err := manifestOf(val)
		if err != nil {
			return checkErr("object %s: %v", key, err)
		}
		got, err := c.read(man, loc)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return checkErr("object %s at %s reads wrong bytes", key, loc)
		}
	}
	for _, id := range h.region(gone) {
		for _, name := range h.s.Disk(id).Names() {
			if strings.HasPrefix(name, "shards/"+sagasim.Bucket+"/") {
				return checkErr("shard %s of %s left on %s in %s", name, sagasim.Bucket, id, gone)
			}
		}
	}
	return nil
}

func (c *nodeCluster) read(man *storage.Manifest, loc string) ([]byte, error) {
	codec, err := man.Layout.Codec()
	if err != nil {
		return nil, err
	}
	var out []byte
	for st, ref := range man.Stripes {
		blobs := make([][]byte, len(ref.Shards))
		want := make([][32]byte, len(ref.Shards))
		for i, sh := range ref.Shards {
			if string(c.h.s.Region(sh.Node)) != loc {
				return nil, checkErr("object %s shard %d.%d placed on %s outside %s", man.Key, st, i, sh.Node, loc)
			}
			want[i] = sh.Checksum
			if b, ok := c.h.s.Disk(sh.Node).PeekDurable(man.ShardID(st, i).BlobName()); ok {
				blobs[i] = b
			}
		}
		data, _, err := storage.DecodeStripe(codec, man.Version, uint32(st), man.Layout.StripeLen(man.Size, st), blobs, want)
		if err != nil {
			return nil, checkErr("object %s stripe %d: %v", man.Key, st, err)
		}
		out = append(out, data...)
	}
	if out == nil {
		out = []byte{}
	}
	return out, nil
}

func (c *nodeCluster) CheckMoved() error       { return c.checkAt(migTo, migFrom) }
func (c *nodeCluster) CheckCompensated() error { return c.checkAt(migFrom, migTo) }

func TestClusterMigrationFailurePointsDST(t *testing.T) {
	t.Parallel()
	cells := sagasim.Cells()
	if len(cells) != sagasim.MatrixCells {
		t.Fatalf("%d cells, want %d", len(cells), sagasim.MatrixCells)
	}
	var m measure
	seeds := simtest.Seeds(t)
	pinned, err := simtest.PinnedSeeds("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range cells {
		mine := matrixSeeds(seeds, pinned, i, len(cells))
		t.Run(c.Name(), func(t *testing.T) {
			t.Parallel()
			for _, seed := range mine {
				t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
					rep := &sagasim.Report{}
					simtest.RequireDeterministic(t, seed, sagasim.Run(nodeHarness{}, c, sagasim.RunOptions{Report: rep, Settle: matrixSettle}))
					if rep.State != c.Want() {
						t.Fatalf("terminal state %v, want %v", rep.State, c.Want())
					}
					m.add("matrix/elapsed_ms", rep.Elapsed.Milliseconds())
					m.add("matrix/runs", 1)
				})
			}
		})
	}
	t.Cleanup(func() { m.report(t) })
}

func matrixSeeds(seeds, pinned []int64, cell, cells int) []int64 {
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

func TestMatrixSeedsRunEveryPinnedSeedOnEveryCell(t *testing.T) {
	pinned := []int64{900, 901}
	seeds := []int64{900, 901, 1, 2, 3, 4, 5, 6, 7}
	cover := map[int64]int{}
	for c := range 3 {
		got := matrixSeeds(seeds, pinned, c, 3)
		for _, want := range []int64{900, 901, 1} {
			if !slices.Contains(got, want) {
				t.Fatalf("cell %d runs %v, missing %d", c, got, want)
			}
		}
		for _, s := range got {
			cover[s]++
		}
	}
	for _, s := range seeds {
		if cover[s] == 0 {
			t.Fatalf("seed %d runs on no cell", s)
		}
	}
	if got := matrixSeeds([]int64{42}, nil, 5, 78); !slices.Equal(got, []int64{42}) {
		t.Fatalf("a single replay seed runs as %v on a cell", got)
	}
}
