package storage_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

const (
	dstNodes      = 20
	dstRegions    = 3
	dstK          = 4
	dstM          = 2
	dstStripeSize = 16 << 10
	dstBucket     = "dst"

	dstPutAt          = env.Time(time.Second)
	dstChaosStart     = env.Time(10 * time.Second)
	dstPhases         = 3
	dstChaos          = 4 * time.Second
	dstHeal           = 2 * time.Second
	dstFaultGap       = 250 * time.Millisecond
	dstGets           = 60
	dstGetGap         = 400 * time.Millisecond
	dstFinalSweep     = time.Second
	dstPutBound       = 2 * time.Second
	dstGetBound       = 3 * time.Second
	dstMaxRequests    = (dstK + dstM) * storage.MaxShardAttempts
	dstInvariantEvery = 20
)

var dstSizes = []int{0, 1, 3, 4, 5, 1021, dstStripeSize - 1, dstStripeSize, dstStripeSize + 1, 3*dstStripeSize + 7, 6*dstStripeSize + 4093}

type dstObject struct {
	key  string
	data []byte
	man  *storage.Manifest
}

type dstOutcome struct {
	putMax, getMax  time.Duration
	gets, ok        int
	unavailable     int
	maxRequests     int
	pending         int
	targetDamaged   bool
	fallbacks       int
	applied         int
	rejected        int
	transientOnly   int
	minDurableAtAck int
}

type dstMode struct {
	lifted bool
}

func stripedRun(mode dstMode, outs *[]*dstOutcome) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		res := &dstOutcome{minDurableAtAck: dstK + dstM}
		*outs = append(*outs, res)
		codes, err := storage.Codes()
		s := sim.New(sim.Config{Seed: seed, Codes: codes})
		if err != nil {
			s.Fail(err)
			return s
		}
		ids := sim.NodeIDs(dstNodes)
		cfg := storage.Config{Layout: storage.Layout{K: dstK, M: dstM, StripeSize: dstStripeSize}, Members: clusterMembers(ids, dstRegions)}
		if err := s.AddCluster(ids, dstRegions, nodeFactory(cfg)); err != nil {
			s.Fail(err)
			return s
		}
		w := s.WorkloadRand()
		objs := make([]*dstObject, len(dstSizes))
		for i, size := range dstSizes {
			data := make([]byte, size)
			for j := range data {
				data[j] = byte(w.Uint32())
			}
			objs[i] = &dstObject{key: fmt.Sprintf("obj-%02d", i), data: data}
		}
		node := func(id env.NodeID) *storage.Node {
			n, _ := s.Process(id).(*storage.Node)
			return n
		}
		s.At(dstPutAt, "put", func() {
			client := node(ids[w.IntN(len(ids))])
			for _, o := range objs {
				start := s.Now()
				client.Put(dstBucket, o.key, o.data, func(man *storage.Manifest, err error) {
					if err != nil {
						s.Fail(fmt.Errorf("put %s before any fault: %w", o.key, err))
						return
					}
					o.man = man
					for st, ref := range man.Stripes {
						durable := 0
						for idx, sh := range ref.Shards {
							blob, ok := s.Disk(sh.Node).PeekDurable(man.ShardID(st, idx).BlobName())
							if ok && storage.BlobChecksum(blob) == sh.Checksum {
								durable++
							}
						}
						if durable < dstK+1 {
							s.Fail(fmt.Errorf("put %s acknowledged with %d durable shards in stripe %d, want at least k+1=%d", o.key, durable, st, dstK+1))
							return
						}
						res.minDurableAtAck = min(res.minDurableAtAck, durable)
					}
					lat := s.Now().Sub(start)
					res.putMax = max(res.putMax, lat)
					if lat > dstPutBound {
						s.Fail(fmt.Errorf("put %s took %v, bound %v", o.key, lat, dstPutBound))
					}
				})
			}
		})
		if err := s.RunUntil(dstChaosStart); err != nil {
			return s
		}
		for _, o := range objs {
			if o.man == nil {
				s.Fail(fmt.Errorf("put %s never acknowledged", o.key))
				return s
			}
			for st, ref := range o.man.Stripes {
				for idx, sh := range ref.Shards {
					if _, ok := s.Disk(sh.Node).PeekDurable(o.man.ShardID(st, idx).BlobName()); !ok {
						s.Fail(fmt.Errorf("%s stripe %d shard %d is not durable on %s before the faults", o.key, st, idx, sh.Node))
						return s
					}
				}
			}
		}
		target := objs[len(objs)-1]
		if mode.lifted {
			for idx := 0; idx <= dstM; idx++ {
				sh := target.man.Stripes[1].Shards[idx*2]
				if err := s.BitRot(sh.Node, target.man.ShardID(1, idx*2).BlobName()); err != nil {
					s.Fail(err)
					return s
				}
			}
			res.targetDamaged = true
		}
		budget := sim.Budget{MinLive: dstNodes, MaxDamagedDisks: dstNodes, MaxDamagedPerGroup: dstM, GroupOf: storage.StripeGroup, Lifted: mode.lifted}
		nem, err := s.StartNemesis(sim.NemesisConfig{
			Start: dstChaosStart, Phases: dstPhases, Chaos: dstChaos, Heal: dstHeal, MaxGap: dstFaultGap,
			Kinds:  []sim.FaultKind{sim.FaultBitRot, sim.FaultWipeDisk, sim.FaultReadError},
			Budget: budget,
		})
		if err != nil {
			s.Fail(err)
			return s
		}
		if !mode.lifted {
			s.AddInvariant("storage/fault-budget", dstInvariantEvery, func() error { return budget.Check(s) })
		}
		issue := func(o *dstObject, from env.NodeID) {
			n := node(from)
			if n == nil {
				return
			}
			res.pending++
			start := s.Now()
			n.Get(o.man, func(r storage.GetResult, err error) {
				res.pending--
				res.gets++
				lat := s.Now().Sub(start)
				res.getMax = max(res.getMax, lat)
				res.maxRequests = max(res.maxRequests, r.Requests/max(len(o.man.Stripes), 1))
				if r.Rejected > 0 {
					res.fallbacks++
				}
				if lat > dstGetBound {
					s.Fail(fmt.Errorf("get %s from %s took %v, bound %v", o.key, from, lat, dstGetBound))
				}
				if r.Requests > len(o.man.Stripes)*dstMaxRequests {
					s.Fail(fmt.Errorf("get %s made %d shard requests for %d stripes", o.key, r.Requests, len(o.man.Stripes)))
				}
				switch {
				case err == nil:
					if !bytes.Equal(r.Data, o.data) {
						s.Fail(fmt.Errorf("get %s from %s returned wrong bytes", o.key, from))
						return
					}
					if mode.lifted && o == target {
						s.Fail(fmt.Errorf("get %s succeeded with %d shards of stripe 1 rotted", o.key, dstM+1))
					}
					res.ok++
				case errors.Is(err, storage.ErrUnavailable) && (mode.lifted || errors.Is(err, env.ErrIO)):
					if r.Data != nil {
						s.Fail(fmt.Errorf("get %s returned bytes with an error", o.key))
					}
					res.unavailable++
					if !mode.lifted {
						res.transientOnly++
					}
				default:
					s.Fail(fmt.Errorf("get %s from %s inside the fault budget: %w", o.key, from, err))
				}
			})
		}
		s.Workload(dstChaosStart, dstGets, dstGetGap, func(int) {
			issue(objs[w.IntN(len(objs))], ids[w.IntN(len(ids))])
		})
		sweepAt := nem.QuietAt().Add(dstFinalSweep)
		s.At(sweepAt, "final-sweep", func() {
			for _, o := range objs {
				for r := 0; r < dstRegions; r++ {
					issue(o, ids[r])
				}
			}
		})
		if err := s.RunUntil(sweepAt.Add(dstGetBound)); err != nil {
			return s
		}
		for _, h := range nem.History() {
			switch h.Outcome {
			case sim.OutcomeApplied:
				res.applied++
			case sim.OutcomeRejected:
				res.rejected++
			}
		}
		if res.pending != 0 {
			s.Fail(fmt.Errorf("%d gets never called back", res.pending))
		}
		if mode.lifted && !res.targetDamaged {
			s.Fail(errors.New("the lifted run did not damage its target stripe"))
		}
		return s
	}
}

func TestStripedPutGetDST(t *testing.T) {
	for _, mode := range []dstMode{{lifted: false}, {lifted: true}} {
		name := "within-budget"
		if mode.lifted {
			name = "budget-lifted"
		}
		t.Run(name, func(t *testing.T) {
			agg := dstOutcome{minDurableAtAck: dstK + dstM}
			simtest.Sweep(t, func(t *testing.T, seed int64) {
				var outs []*dstOutcome
				simtest.RequireDeterministic(t, seed, stripedRun(mode, &outs))
				r := outs[0]
				if r.gets < dstGets/2 {
					t.Fatalf("only %d gets completed", r.gets)
				}
				if mode.lifted && r.unavailable == 0 {
					t.Fatal("the lifted run never saw ErrUnavailable")
				}
				agg.putMax = max(agg.putMax, r.putMax)
				agg.getMax = max(agg.getMax, r.getMax)
				agg.maxRequests = max(agg.maxRequests, r.maxRequests)
				agg.gets += r.gets
				agg.ok += r.ok
				agg.unavailable += r.unavailable
				agg.fallbacks += r.fallbacks
				agg.applied += r.applied
				agg.rejected += r.rejected
				agg.transientOnly += r.transientOnly
				agg.minDurableAtAck = min(agg.minDurableAtAck, r.minDurableAtAck)
			})
			if agg.fallbacks == 0 || agg.applied == 0 {
				t.Fatalf("the faults never reached a read: %d gets fell back, %d faults applied", agg.fallbacks, agg.applied)
			}
			t.Logf("MEASURED %s: put max %v (bound %v), get max %v (bound %v), max mean requests per stripe %d (bound %d), gets %d ok %d unavailable %d fell-back %d, faults applied %d rejected-by-budget %d, unavailable-with-transient-cause %d, fewest durable shards of a stripe at its PUT ack %d (need %d)",
				name, agg.putMax, dstPutBound, agg.getMax, dstGetBound, agg.maxRequests, dstMaxRequests, agg.gets, agg.ok, agg.unavailable, agg.fallbacks, agg.applied, agg.rejected, agg.transientOnly, agg.minDurableAtAck, dstK+1)
		})
	}
}

func TestCanaryChecksumSkipFailsStripedDST(t *testing.T) {
	restore := storage.PlantSkipChecksum()
	defer restore()
	var outs []*dstOutcome
	_, err := simtest.CheckDeterministic(1, stripedRun(dstMode{}, &outs))
	if err == nil {
		t.Fatalf("the striped DST passes with checksum verification skipped (%d gets, %d ok)", outs[0].gets, outs[0].ok)
	}
	if !errors.Is(err, storage.ErrCorrupt) {
		t.Fatalf("the striped DST failed for another reason: %v", err)
	}
}
