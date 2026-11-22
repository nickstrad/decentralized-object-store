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
	"github.com/nickstrad/decentralized-object-store/internal/gossip"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

const (
	phBucket     = "ph"
	phQuota      = 800
	phObjectSize = 100
	phCut        = env.Time(8 * time.Second)
	phHeal       = env.Time(17 * time.Second)
	phRounds     = 6
	phGrace      = 6 * time.Second
)

type partitionResult struct {
	acked, quotaRefused, objectLosers, events int
	distinctLosers                            int
	overshoot                                 int64
	converge, quotaSettle                     time.Duration
	recountWrites, collectedLoser             uint64
	quotaOK                                   bool
	tally                                     map[string]int
}

func partitionConfig() node.Config {
	c := dstConfig()
	c.Gossip = gossip.Config{ProbeInterval: 250 * time.Millisecond, ProbeTimeout: 100 * time.Millisecond, SyncInterval: 2 * time.Second}
	return c
}

func phConfig() node.Config {
	c := partitionConfig()
	c.GCGrace = phGrace
	return c
}

func (h *harness) quotaTruth(bucket string) (occ.Usage, error) {
	rd, err := h.up()[0].OCC().Reader()
	if err != nil {
		return occ.Usage{}, err
	}
	var u occ.Usage
	for _, k := range rd.Keys() {
		if len(k) == 0 || occ.Kind(k[0]) != occ.KindManifest {
			continue
		}
		_, parts, err := occ.ParseKey(k)
		if err != nil || parts[0] != bucket {
			continue
		}
		val, _, err := rd.Get(k)
		if err != nil {
			continue
		}
		man, err := manifestOf(val)
		if err != nil {
			return u, err
		}
		u.Bytes += uint64(man.Size)
		u.Objects++
	}
	return u, nil
}

func (h *harness) quotaAgrees(bucket string) bool {
	truth, err := h.quotaTruth(bucket)
	if err != nil {
		return false
	}
	for _, n := range h.up() {
		q, err := n.Quota(bucket)
		if err != nil || q.UsedBytes != truth.Bytes || q.UsedObjects != truth.Objects {
			return false
		}
	}
	return true
}

func partitionRun(res *partitionResult) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		*res = partitionResult{}
		h, err := newHarness(seed, dstNodes, phConfig(), sim.DefaultNetConfig())
		if err != nil {
			h.s.Fail(err)
			return h.s
		}
		s := h.s
		w := s.WorkloadRand()
		h.createBucket(env.Time(2*time.Second), storage.BucketSpec{Name: phBucket, QuotaBytes: phQuota})
		side := h.region("r0")
		var other []env.NodeID
		for _, id := range h.ids {
			if !slices.Contains(side, id) {
				other = append(other, id)
			}
		}
		s.At(phCut, "partition", func() {
			if err := s.Partition(side, other); err != nil {
				s.Fail(err)
			}
		})
		data := map[string][]byte{}
		peak := map[string]uint64{}
		put := func(group []env.NodeID, key string, at env.Time, size int) {
			s.At(at, "put "+key, func() {
				id := group[w.IntN(len(group))]
				n := h.node(id)
				if n == nil {
					return
				}
				body := payload(w, size)
				n.PutObject(storage.PutRequest{Bucket: phBucket, Key: key, Data: body}, func(oi storage.ObjectInfo, err error) {
					switch {
					case err == nil:
						res.acked++
						data[oi.Version] = body
						if q, err := n.Quota(phBucket); err == nil {
							peak[string(id)] = max(peak[string(id)], q.UsedBytes)
						}
					case errors.Is(err, occ.ErrQuotaExceeded):
						res.quotaRefused++
					case errors.Is(err, storage.ErrUnavailable) || errors.Is(err, storage.ErrNoSuchBucket):
					default:
						s.Fail(fmt.Errorf("put %s on %s: %w", key, id, err))
					}
				})
			})
		}
		tag := func(group []env.NodeID, key, name string, at env.Time) {
			s.At(at, "tag "+key, func() {
				id := group[w.IntN(len(group))]
				n := h.node(id)
				if n == nil {
					return
				}
				_, _ = n.PutTag(phBucket, key, name, fmt.Sprintf("%s@%d", id, s.Now()), storage.Condition{})
			})
		}
		for i := range 3 {
			put(h.ids, fmt.Sprintf("c%d", i), env.Time(4*time.Second+time.Duration(i)*200*time.Millisecond), phObjectSize)
		}
		cutPhase := phCut.Add(4 * time.Second)
		for i := range phRounds {
			at := cutPhase.Add(time.Duration(i) * 600 * time.Millisecond)
			put(side, fmt.Sprintf("a%d", i), at, phObjectSize)
			put(other, fmt.Sprintf("b%d", i), at.Add(100*time.Millisecond), phObjectSize)
			c := fmt.Sprintf("c%d", i%3)
			put(side, c, at.Add(200*time.Millisecond), phObjectSize/2+w.IntN(phObjectSize))
			put(other, c, at.Add(250*time.Millisecond), phObjectSize)
			tag(side, c, "owner", at.Add(300*time.Millisecond))
			tag(other, c, "owner", at.Add(350*time.Millisecond))
		}
		s.At(phHeal, "heal", s.HealAll)
		if err := s.RunUntil(phHeal); err != nil {
			return s
		}
		if err := s.RunUntilConverged(phHeal.Add(40*time.Second), h.converged); err != nil {
			return s
		}
		res.converge = s.Now().Sub(phHeal)
		if err := s.RunUntilConverged(s.Now().Add(30*time.Second), func() bool { return h.metaConverged() && h.quotaAgrees(phBucket) }); err != nil {
			s.Fail(fmt.Errorf("quota usage never matched the manifests after heal: %w", err))
			return s
		}
		res.quotaSettle = s.Now().Sub(phHeal)
		res.quotaOK = true
		rd, err := h.up()[0].OCC().Reader()
		if err != nil {
			s.Fail(err)
			return s
		}
		losers := map[occ.VersionID]bool{}
		for _, d := range h.discards {
			if !losers[d.loser.ID()] {
				losers[d.loser.ID()] = true
				if k, _, _ := occ.ParseKey(d.key); k == occ.KindManifest {
					res.objectLosers++
				}
			}
		}
		res.events, res.distinctLosers = len(h.discards), len(losers)
		tally, err := h.acks.check(rd, func(key string) bool {
			kind, parts, err := occ.ParseKey(key)
			return err == nil && (kind == occ.KindManifest || kind == occ.KindTag && !strings.HasPrefix(parts[2], "~"))
		})
		if err != nil {
			s.Fail(err)
			return s
		}
		res.tally = tally
		if res.events > 0 {
			seen := false
			for _, n := range h.up() {
				hl := n.Health()
				if hl.Discards > 0 && len(hl.RecentDiscards) > 0 {
					seen = true
					if k := hl.RecentDiscards[0].Key; strings.HasPrefix(k, `"`) {
						s.Fail(fmt.Errorf("health reports discard key %s quoted", k))
					}
				}
			}
			if !seen {
				s.Fail(fmt.Errorf("%d discard events and no node reports one through its health surface", res.events))
			}
		}
		truth, _ := h.quotaTruth(phBucket)
		for _, p := range peak {
			res.overshoot = max(res.overshoot, int64(p)-phQuota)
		}
		res.overshoot = max(res.overshoot, int64(truth.Bytes)-phQuota)
		end := s.Now()
		for i := range 3 {
			key := fmt.Sprintf("c%d", i)
			for j := range 3 {
				s.At(end.Add(time.Duration(i*3+j)*30*time.Millisecond), "final get", func() {
					from := h.pick()
					h.node(from).GetObject(phBucket, key, func(oi storage.ObjectInfo, got []byte, err error) {
						if err != nil {
							s.Fail(fmt.Errorf("final get %s from %s: %w", key, from, err))
							return
						}
						if want, ok := data[oi.Version]; !ok || !bytes.Equal(want, got) {
							s.Fail(fmt.Errorf("final get %s from %s returned bytes of no acknowledged write", key, from))
						}
					})
				})
			}
		}
		if err := s.RunFor(h.cfg.GCGrace + 5*time.Second); err != nil {
			return s
		}
		if err := h.checkShardsReferenced(true); err != nil {
			s.Fail(err)
		}
		for _, n := range h.up() {
			res.recountWrites += n.Stats().RecountWrites
			res.collectedLoser += n.Stats().CollectedLoser
		}
		if res.objectLosers > 0 && res.collectedLoser == 0 {
			s.Fail(fmt.Errorf("%d object versions lost under LWW and none of their shards were collected", res.objectLosers))
		}
		return s
	}
}

func TestClusterPartitionHealDST(t *testing.T) {
	var m measure
	sweepSharded(t, func(t *testing.T, seed int64) {
		var res partitionResult
		simtest.RequireDeterministic(t, seed, partitionRun(&res))
		m.add("partition/converge_ms", res.converge.Milliseconds())
		m.add("partition/quota_settle_ms", res.quotaSettle.Milliseconds())
		m.add("partition/discard_events", int64(res.events))
		m.add("partition/distinct_losers", int64(res.distinctLosers))
		m.add("partition/object_losers", int64(res.objectLosers))
		m.add("partition/quota_overshoot_bytes", res.overshoot)
		m.add("partition/recount_writes", int64(res.recountWrites))
		m.add("partition/collected_loser_shards", int64(res.collectedLoser))
		m.add("partition/acked", int64(res.acked))
		m.add("partition/quota_refused", int64(res.quotaRefused))
		for k, v := range res.tally {
			m.add("partition/oracle_"+k, int64(v))
		}
	})
	t.Cleanup(func() { m.report(t) })
}

func TestCanaryNodeSkipsDiscardRecount(t *testing.T) {
	node.SetCanaryNoDiscardRecount(true)
	t.Cleanup(func() { node.SetCanaryNoDiscardRecount(false) })
	failed := 0
	for _, seed := range []int64{1, 2, 3} {
		var res partitionResult
		s := partitionRun(&res)(seed)
		if s.Err() != nil && !res.quotaOK {
			failed++
		}
	}
	if failed == 0 {
		t.Fatal("with the recount after a quota discard switched off, the quota check still passed on every seed")
	}
}
