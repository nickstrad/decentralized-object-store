package occ_test

import (
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"pgregory.net/rapid"
)

type classReplica struct {
	s        *occ.Store
	clock    *fakeClock
	log      *discardLog
	recorded map[string]bool
	node     string
	gen      uint64
}

func (c *classReplica) open(t *rapid.T, epoch uint64) {
	c.log = &discardLog{}
	c.recorded = map[string]bool{}
	cfg := occ.StoreConfig{Node: env.NodeID(c.node), Epoch: epoch, Clock: c.clock, Rand: rand.New(rand.NewPCG(c.gen, 3)), OnDiscard: c.log.hook}
	s, err := occ.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	c.s = s
}

var classKeys = []string{"a", "b"}

func lossKey(key string, id occ.VersionID) string { return key + "\x00" + id.String() }

func propClassificationMatchesOracle(t *rapid.T) {
	or := newOracle()
	reps := make([]*classReplica, rapid.IntRange(3, 4).Draw(t, "replicas"))
	for i := range reps {
		reps[i] = &classReplica{clock: &fakeClock{now: env.Time(1_000_000 + rapid.Int64Range(-3_000, 3_000).Draw(t, "skew"))}, node: fmt.Sprintf("r%d", i), gen: uint64(i)}
		reps[i].open(t, uint64(i+1))
	}
	for range rapid.IntRange(80, 240).Draw(t, "steps") {
		r := reps[rapid.IntRange(0, len(reps)-1).Draw(t, "replica")]
		switch rapid.IntRange(0, 9).Draw(t, "action") {
		case 0, 1, 2, 3:
			key := rapid.SampledFrom(classKeys).Draw(t, "key")
			rec := r.s.Record(key)
			p := occ.IfMatch(rec.ETag())
			if !rec.Present() || rapid.Bool().Draw(t, "blind") {
				p = occ.Unconditional()
			}
			var v occ.Version
			var err error
			if rec.Live() && rapid.IntRange(0, 5).Draw(t, "del") == 0 {
				v, err = r.s.Delete(key, p)
			} else {
				v, err = r.s.Put(key, []byte(key), p)
			}
			if err != nil {
				t.Fatal(err)
			}
			or.record(key, v, rec)
		case 4, 5, 6, 7:
			src := reps[rapid.IntRange(0, len(reps)-1).Draw(t, "from")].s.Doc()
			if rapid.Bool().Draw(t, "one key") {
				src = src.Subset(rapid.SampledFrom(classKeys).Draw(t, "subset"))
			}
			before := r.s.Doc()
			hooks := len(r.log.discards)
			if _, err := r.s.Merge(src); err != nil {
				t.Fatal(err)
			}
			var want []occ.VersionID
			for _, k := range src.Keys() {
				x, _ := src.Record(k).Version()
				h, ok := before.Record(k).Version()
				if !ok || h.ID() == x.ID() {
					continue
				}
				after, _ := r.s.Record(k).Version()
				var loser occ.Version
				var causal bool
				switch after.ID() {
				case x.ID():
					loser, causal = h, or.seen[x.ID()][h.ID()]
					if x.Supersedes(h) != causal {
						t.Fatalf("%s replaced %s on %s: product says causal=%v, the oracle %v", short(x.ID()), short(h.ID()), r.node, !causal, causal)
					}
				case h.ID():
					loser, causal = x, or.seen[h.ID()][x.ID()]
					if h.Supersedes(x) != causal {
						t.Fatalf("%s rejected %s on %s: product says below=%v, the oracle %v", short(h.ID()), short(x.ID()), r.node, !causal, causal)
					}
				default:
					t.Fatal("a merge produced a version that was neither held nor incoming")
				}
				if causal || r.recorded[lossKey(k, loser.ID())] {
					continue
				}
				r.recorded[lossKey(k, loser.ID())] = true
				want = append(want, loser.ID())
			}
			got := r.log.discards[hooks:]
			if len(got) != len(want) {
				t.Fatalf("merge on %s: the oracle expects %d discard records, the product made %d", r.node, len(want), len(got))
			}
			for i := range got {
				if got[i].Loser.ID() != want[i] {
					t.Fatalf("merge on %s: record %d names %s, the oracle %s", r.node, i, short(got[i].Loser.ID()), short(want[i]))
				}
			}
		case 8:
			r.clock.now += env.Time(rapid.Int64Range(-200, 2_000).Draw(t, "tick"))
		default:
			or.diskLost(r.node, r.s.Doc())
			r.gen += 100
			r.open(t, 0)
		}
	}
}

// TestPropClassificationMatchesOracle proves across random multi-replica runs
// (writes, deletes, full/subset merges, clock moves, and wipes) that product
// Supersedes classification and emitted discard records match the independent
// ancestry oracle for both replaced held versions and rejected incoming ones.
func TestPropClassificationMatchesOracle(t *testing.T) {
	rapid.Check(t, propClassificationMatchesOracle)
}
