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
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

var putGetSizes = []int{0, 1, 7, 4093, dstStripe, dstStripe + 1, 3*dstStripe + 11}

type putGetResult struct {
	acked, gets, getOK, getMiss int
	converge                    time.Duration
	lateAcked, lateLost         int
	lateRecoverable, lateClass  int
}

func putGetConfig() node.Config {
	c := dstConfig()
	c.OrphanGrace = 60 * time.Second
	return c
}

func putGetRun(res *putGetResult) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		*res = putGetResult{}
		net := sim.DefaultNetConfig()
		net.DropPPM = 10_000
		h, err := newHarness(seed, dstNodes, putGetConfig(), net)
		if err != nil {
			h.s.Fail(err)
			return h.s
		}
		s := h.s
		w := s.WorkloadRand()
		h.createBucket(env.Time(3*time.Second), storage.BucketSpec{Name: "pg"})
		type obj struct {
			key  string
			data []byte
			ok   bool
		}
		var objs []*obj
		for i := range 18 {
			objs = append(objs, &obj{key: fmt.Sprintf("o%02d", i), data: payload(w, putGetSizes[i%len(putGetSizes)])})
		}
		get := func(o *obj, from env.NodeID, final bool) {
			n := h.node(from)
			if n == nil {
				return
			}
			res.gets++
			n.GetObject("pg", o.key, func(_ storage.ObjectInfo, data []byte, err error) {
				switch {
				case err == nil && !bytes.Equal(data, o.data):
					s.Fail(fmt.Errorf("get %s from %s returned %d wrong bytes", o.key, from, len(data)))
				case err == nil:
					res.getOK++
				case final:
					s.Fail(fmt.Errorf("get %s from %s after heal: %w", o.key, from, err))
				case errors.Is(err, occ.ErrNotFound) || errors.Is(err, storage.ErrUnavailable) || errors.Is(err, storage.ErrNoSuchBucket):
					res.getMiss++
				default:
					s.Fail(fmt.Errorf("get %s from %s: unexpected %w", o.key, from, err))
				}
			})
		}
		start := env.Time(8 * time.Second)
		for i, o := range objs {
			s.At(start.Add(time.Duration(i)*700*time.Millisecond), "put "+o.key, func() {
				from := h.pick()
				h.node(from).PutObject(storage.PutRequest{Bucket: "pg", Key: o.key, Data: o.data}, func(_ storage.ObjectInfo, err error) {
					if err != nil {
						if errors.Is(err, storage.ErrNoSuchBucket) || errors.Is(err, storage.ErrUnavailable) {
							return
						}
						s.Fail(fmt.Errorf("put %s on %s: %w", o.key, from, err))
						return
					}
					o.ok = true
					res.acked++
					for range 2 {
						at := s.Now().Add(time.Duration(w.IntN(3000)) * time.Millisecond)
						s.At(at, "get "+o.key, func() { get(o, h.pick(), false) })
					}
				})
			})
		}
		victim := h.ids[3+w.IntN(len(h.ids)-3)]
		s.InjectAt(env.Time(14*time.Second), sim.Fault{Kind: sim.FaultCrash, Node: victim})
		s.InjectAt(env.Time(18*time.Second), sim.Fault{Kind: sim.FaultRestart, Node: victim})
		s.InjectAt(env.Time(16*time.Second), sim.Fault{Kind: sim.FaultPartitionRegion, Region: env.Region(fmt.Sprintf("r%d", w.IntN(dstRegions)))})
		heal := env.Time(26 * time.Second)
		s.InjectAt(heal, sim.Fault{Kind: sim.FaultHealAll})
		if err := s.RunUntil(heal); err != nil {
			return s
		}
		if err := s.RunUntilConverged(heal.Add(40*time.Second), h.converged); err != nil {
			return s
		}
		res.converge = s.Now().Sub(heal)
		if err := s.RunUntilConverged(s.Now().Add(30*time.Second), func() bool { return h.metaConverged() && h.quotaAgrees("pg") }); err != nil {
			s.Fail(fmt.Errorf("usage never matched a recount of the manifests after the heal: %w", err))
			return s
		}
		end := s.Now()
		for i, o := range objs {
			if !o.ok {
				continue
			}
			for j := range 2 {
				s.At(end.Add(time.Duration(i*2+j)*50*time.Millisecond), "final get", func() { get(o, h.pick(), true) })
			}
		}
		if err := s.RunFor(5 * time.Second); err != nil {
			return s
		}
		if res.acked < len(objs)/2 {
			s.Fail(fmt.Errorf("only %d of %d puts acknowledged", res.acked, len(objs)))
		}
		if err := h.checkShardsReferenced(false); err != nil {
			s.Fail(err)
		}
		h.checkLiveReadableOrLost(s)
		if s.Err() != nil {
			return s
		}
		h.orphanedAfterAck(s, res)
		return s
	}
}

func (h *harness) orphanedAfterAck(s *sim.Sim, res *putGetResult) {
	w := s.WorkloadRand()
	h.gc.residual[occ.ManifestKey("pg", "late")] = true
	wtr := h.pick()
	gone := []env.NodeID{h.pick(wtr)}
	gone = append(gone, h.pick(wtr, gone[0]))
	for _, g := range gone {
		if err := s.SetLinkLoss(wtr, g, sim.PPM); err != nil {
			s.Fail(err)
			return
		}
	}
	var others []env.NodeID
	for _, id := range h.ids {
		if id != wtr {
			others = append(others, id)
		}
	}
	data := payload(w, 1+w.IntN(dstStripe))
	keep := h.cfg.Layout.K - 1 + w.IntN(2)
	mute := time.Millisecond + time.Duration(w.IntN(3))*time.Millisecond
	var oi storage.ObjectInfo
	h.node(wtr).PutObject(storage.PutRequest{Bucket: "pg", Key: "late", Data: data}, func(o storage.ObjectInfo, err error) {
		if err != nil {
			if err := s.Isolate(wtr); err != nil {
				s.Fail(err)
			}
			return
		}
		res.lateAcked++
		oi = o
		group := []env.NodeID{wtr}
		kept := 0
		if man := h.mans[o.Version]; man != nil {
			for _, sh := range man.Stripes[0].Shards {
				if sh.Node == wtr {
					kept++
				}
			}
			for _, sh := range man.Stripes[0].Shards {
				if kept < keep && sh.Node != wtr && !slices.Contains(gone, sh.Node) && !slices.Contains(group, sh.Node) {
					group = append(group, sh.Node)
					kept++
				}
			}
		}
		var rest []env.NodeID
		for _, id := range h.ids {
			if !slices.Contains(group, id) {
				rest = append(rest, id)
			}
		}
		for _, id := range group[1:] {
			if err := s.SetLinkLoss(wtr, id, 0); err != nil {
				s.Fail(err)
			}
		}
		if err := s.Partition(group, rest); err != nil {
			s.Fail(err)
		}
	})
	s.At(s.Now().Add(mute), "mute the writer", func() {
		for _, id := range others {
			if err := s.SetLinkLoss(wtr, id, sim.PPM); err != nil {
				s.Fail(err)
			}
		}
	})
	if err := s.RunFor(h.cfg.OrphanGrace + 20*time.Second); err != nil {
		return
	}
	recoverable := false
	if man := h.mans[oi.Version]; res.lateAcked > 0 && man != nil {
		_, lost := h.unrecoverableStripe(man)
		recoverable = !lost
		if lost && h.collectedAtMostM(man) {
			res.lateClass++
		}
	}
	s.HealAll()
	if err := s.RunUntilConverged(s.Now().Add(60*time.Second), h.converged); err != nil {
		return
	}
	if err := s.RunFor(30 * time.Second); err != nil {
		return
	}
	if res.lateAcked > 0 {
		if slices.Contains(h.declared, oi.Version) {
			res.lateLost++
		}
		if recoverable {
			res.lateRecoverable++
			if slices.Contains(h.declared, oi.Version) {
				s.Fail(fmt.Errorf("late write %s had k verifiable shards of every stripe at the heal and was declared lost", oi.Version))
				return
			}
		}
	}
	h.checkLiveReadableOrLost(s)
	if recoverable {
		pending := true
		h.node(h.pick()).GetObject("pg", "late", func(_ storage.ObjectInfo, got []byte, err error) {
			pending = false
			if err != nil || !bytes.Equal(got, data) {
				s.Fail(fmt.Errorf("late write %s was recoverable at the heal and reads %d bytes, %v", oi.Version, len(got), err))
			}
		})
		if err := s.RunUntilConverged(s.Now().Add(10*time.Second), func() bool { return !pending }); err != nil {
			s.Fail(err)
		}
	}
}

// TestClusterPutGetDST proves deterministic 21-node PUT/GET under 1% loss,
// region partition, and node crash: no GET returns wrong bytes, every acked PUT
// reads back after heal, metadata/membership/quota converge, and a post-heal
// orphaned-after-ack phase verifies recoverable writes stay readable while
// unrecoverable ones are declared lost on record.
func TestClusterPutGetDST(t *testing.T) {
	t.Parallel()
	var m measure
	sweepSharded(t, func(t *testing.T, seed int64) {
		var res putGetResult
		simtest.RequireDeterministic(t, seed, putGetRun(&res))
		m.add("putget/converge_ms", res.converge.Milliseconds())
		m.add("putget/acked", int64(res.acked))
		m.add("putget/get_ok", int64(res.getOK))
		m.add("putget/get_miss", int64(res.getMiss))
		m.add("putget/late_acked", int64(res.lateAcked))
		m.add("putget/late_lost", int64(res.lateLost))
		m.add("putget/late_recoverable", int64(res.lateRecoverable))
		m.add("putget/late_orphans_plus_unwritten", int64(res.lateClass))
	})
	t.Cleanup(func() { m.report(t) })
}

func (h *harness) collectedAtMostM(man *storage.Manifest) bool {
	per := map[uint32]int{}
	for _, d := range h.gc.deleted[man.Version] {
		if sid, err := storage.ParseBlobName(d[strings.IndexByte(d, '|')+1:]); err == nil {
			per[sid.Stripe]++
		}
	}
	for _, c := range per {
		if c > man.Layout.M {
			return false
		}
	}
	return true
}
