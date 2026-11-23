package node_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/node"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/sim"
	"github.com/nickstrad/object-store/internal/sim/simtest"
	"github.com/nickstrad/object-store/internal/storage"
)

const crBucket = "cr"

type crashResult struct {
	deleted, acked      int
	converge            time.Duration
	crashRepair, refill time.Duration
	rebuilds            uint64
	resurrected         bool
}

func (h *harness) holdsExpected(id env.NodeID) bool {
	n := h.node(id)
	if n == nil {
		return false
	}
	rd, err := n.OCC().Reader()
	if err != nil {
		return false
	}
	for _, k := range rd.Keys() {
		if len(k) == 0 || occ.Kind(k[0]) != occ.KindManifest {
			continue
		}
		val, _, err := rd.Get(k)
		if err != nil {
			continue
		}
		man, err := manifestOf(val)
		if err != nil {
			return false
		}
		for s := range man.Stripes {
			for i, sh := range man.Stripes[s].Shards {
				if sh.Node != id {
					continue
				}
				blob, ok := h.s.Disk(id).PeekDurable(man.ShardID(s, i).BlobName())
				if !ok || storage.BlobChecksum(blob) != sh.Checksum {
					return false
				}
				if _, _, err := storage.DecodeShard(blob); err != nil {
					return false
				}
			}
		}
	}
	return true
}

func (h *harness) deletedEverywhere(keys []string) error {
	for _, n := range h.up() {
		for _, k := range keys {
			if n.OCC().Record(occ.ManifestKey(crBucket, k)).Live() {
				return fmt.Errorf("deleted object %s is live again on %s", k, n.ID())
			}
		}
	}
	return nil
}

func crashRun(res *crashResult) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		*res = crashResult{}
		h, err := newHarness(seed, dstNodes, partitionConfig(), sim.DefaultNetConfig())
		if err != nil {
			h.s.Fail(err)
			return h.s
		}
		s := h.s
		w := s.WorkloadRand()
		h.createBucket(env.Time(2*time.Second), storage.BucketSpec{Name: crBucket})
		data := map[string][]byte{}
		var deleted []string
		for i := range 10 {
			key := fmt.Sprintf("k%d", i)
			s.At(env.Time(4*time.Second+time.Duration(i)*300*time.Millisecond), "put "+key, func() {
				body := payload(w, 1+w.IntN(3*dstStripe))
				n := h.node(h.ids[0])
				n.PutObject(storage.PutRequest{Bucket: crBucket, Key: key, Data: body}, func(_ storage.ObjectInfo, err error) {
					if err != nil {
						s.Fail(fmt.Errorf("put %s before any fault: %w", key, err))
						return
					}
					data[key] = body
					res.acked++
				})
			})
		}
		crashed := h.ids[3+w.IntN(6)]
		wiped := h.ids[9+w.IntN(6)]
		isolated := env.Region(fmt.Sprintf("r%d", w.IntN(dstRegions)))
		s.InjectAt(env.Time(10*time.Second), sim.Fault{Kind: sim.FaultCrash, Node: crashed})
		s.InjectAt(env.Time(11*time.Second), sim.Fault{Kind: sim.FaultWipeRejoin, Node: wiped})
		s.InjectAt(env.Time(12*time.Second), sim.Fault{Kind: sim.FaultPartitionRegion, Region: isolated})
		for i := range 4 {
			key := fmt.Sprintf("k%d", i)
			s.At(env.Time(14*time.Second+time.Duration(i)*400*time.Millisecond), "delete "+key, func() {
				for range 10 {
					id := h.pick(crashed)
					if h.s.Region(id) == isolated {
						continue
					}
					if !h.node(id).OCC().Record(occ.ManifestKey(crBucket, key)).Live() {
						continue
					}
					if _, err := h.node(id).DeleteObject(crBucket, key, storage.Condition{}); err != nil {
						s.Fail(fmt.Errorf("delete %s on %s: %w", key, id, err))
						return
					}
					deleted = append(deleted, key)
					res.deleted++
					return
				}
			})
		}
		for i := 4; i < 6; i++ {
			key := fmt.Sprintf("k%d", i)
			s.At(env.Time(16*time.Second+time.Duration(i)*300*time.Millisecond), "overwrite "+key, func() {
				id := h.pick(crashed)
				if h.s.Region(id) == isolated {
					return
				}
				body := payload(w, 1+w.IntN(dstStripe))
				h.node(id).PutObject(storage.PutRequest{Bucket: crBucket, Key: key, Data: body}, func(_ storage.ObjectInfo, err error) {
					if err == nil {
						data[key] = body
					} else if !errors.Is(err, storage.ErrUnavailable) {
						s.Fail(fmt.Errorf("overwrite %s: %w", key, err))
					}
				})
			})
		}
		restart := env.Time(20 * time.Second)
		s.InjectAt(restart, sim.Fault{Kind: sim.FaultRestart, Node: crashed})
		heal := env.Time(24 * time.Second)
		s.At(heal, "heal", s.HealAll)
		if err := s.RunUntil(heal); err != nil {
			return s
		}
		if err := s.RunUntilConverged(heal.Add(40*time.Second), h.converged); err != nil {
			return s
		}
		res.converge = s.Now().Sub(heal)
		if err := s.RunUntilConverged(s.Now().Add(30*time.Second), func() bool { return h.metaConverged() && h.quotaAgrees(crBucket) }); err != nil {
			s.Fail(fmt.Errorf("usage never matched a recount of the manifests after the heal: %w", err))
			return s
		}
		if err := h.deletedEverywhere(deleted); err != nil {
			s.Fail(err)
			res.resurrected = true
			return s
		}
		if err := s.RunUntilConverged(s.Now().Add(30*time.Second), func() bool { return h.holdsExpected(crashed) }); err != nil {
			s.Fail(fmt.Errorf("restarted node %s never held its shards again: %w", crashed, err))
			return s
		}
		res.crashRepair = s.Now().Sub(restart)
		if err := s.RunUntilConverged(s.Now().Add(30*time.Second), func() bool { return h.holdsExpected(wiped) }); err != nil {
			s.Fail(fmt.Errorf("wiped node %s was never refilled: %w", wiped, err))
			return s
		}
		res.refill = s.Now().Sub(env.Time(11 * time.Second))
		end := s.Now()
		for i := range 10 {
			key := fmt.Sprintf("k%d", i)
			s.At(end.Add(time.Duration(i)*40*time.Millisecond), "final get", func() {
				from := h.pick()
				h.node(from).GetObject(crBucket, key, func(_ storage.ObjectInfo, got []byte, err error) {
					gone := false
					for _, d := range deleted {
						gone = gone || d == key
					}
					switch {
					case gone && !errors.Is(err, occ.ErrNotFound):
						s.Fail(fmt.Errorf("deleted %s read from %s: %v", key, from, err))
					case !gone && err != nil:
						s.Fail(fmt.Errorf("get %s from %s: %w", key, from, err))
					case !gone && !bytes.Equal(got, data[key]):
						s.Fail(fmt.Errorf("get %s from %s: wrong bytes", key, from))
					}
				})
			})
		}
		if err := s.RunFor(h.cfg.GCGrace + 5*time.Second); err != nil {
			return s
		}
		if err := h.deletedEverywhere(deleted); err != nil {
			s.Fail(err)
			res.resurrected = true
			return s
		}
		if err := h.checkShardsReferenced(true); err != nil {
			s.Fail(err)
		}
		h.checkLiveReadableOrLost(s)
		for _, n := range h.up() {
			res.rebuilds += n.Stats().Rebuilds
		}
		return s
	}
}

// TestClusterCrashRestartDST proves deterministic 21-node crash, wipe-rejoin,
// region partition, and delete durability: deleted keys never resurrect,
// restarted and wiped nodes rebuild all expected shards, and usage matches a
// recount after heal.
func TestClusterCrashRestartDST(t *testing.T) {
	t.Parallel()
	var m measure
	sweepSharded(t, func(t *testing.T, seed int64) {
		var res crashResult
		simtest.RequireDeterministic(t, seed, crashRun(&res))
		if res.deleted == 0 {
			t.Fatal("no delete was exercised")
		}
		m.add("crash/converge_ms", res.converge.Milliseconds())
		m.add("crash/restart_repair_ms", res.crashRepair.Milliseconds())
		m.add("crash/wipe_refill_ms", res.refill.Milliseconds())
		m.add("crash/rebuilt_shards", int64(res.rebuilds))
		m.add("crash/deleted", int64(res.deleted))
	})
	t.Cleanup(func() { m.report(t) })
}

// TestCanaryDeleteResurrected plants a bug that hides tombstones from metadata
// Merkle anti-entropy and proves crashRun catches a resurrected deleted object.
func TestCanaryDeleteResurrected(t *testing.T) {
	node.SetCanaryIgnoreTombstones(true)
	t.Cleanup(func() { node.SetCanaryIgnoreTombstones(false) })
	caught := 0
	for _, seed := range []int64{1, 2, 3} {
		var res crashResult
		s := crashRun(&res)(seed)
		if s.Err() != nil && res.resurrected {
			caught++
		}
	}
	if caught == 0 {
		t.Fatal("with tombstones ignored by anti-entropy, no deleted object came back on any seed")
	}
}

var driftSeeds = []int64{7, 383}

// TestCrashScenarioUsageMatchesARecountOnTheSeedsThatDrifted replays the
// specific crash seeds where a partition-era recount left usage drifted,
// proving auditQuotas restores agreement with live manifests.
func TestCrashScenarioUsageMatchesARecountOnTheSeedsThatDrifted(t *testing.T) {
	for _, seed := range driftSeeds {
		var res crashResult
		simtest.RequireDeterministic(t, seed, crashRun(&res))
	}
}

// TestCanaryNodeSkipsQuotaAudit plants a bug disabling auditQuotas and proves
// every drift seed fails the post-heal usage recount check.
func TestCanaryNodeSkipsQuotaAudit(t *testing.T) {
	node.SetCanaryNoQuotaAudit(true)
	t.Cleanup(func() { node.SetCanaryNoQuotaAudit(false) })
	failed := 0
	for _, seed := range driftSeeds {
		var res crashResult
		if crashRun(&res)(seed).Err() != nil {
			failed++
		}
	}
	if failed != len(driftSeeds) {
		t.Fatalf("with the quota audit switched off, %d of %d drift seeds still matched a recount", len(driftSeeds)-failed, len(driftSeeds))
	}
}
