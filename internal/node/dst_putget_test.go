package node_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

var putGetSizes = []int{0, 1, 7, 4093, dstStripe, dstStripe + 1, 3*dstStripe + 11}

type putGetResult struct {
	acked, gets, getOK, getMiss int
	converge                    time.Duration
}

func putGetRun(res *putGetResult) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		*res = putGetResult{}
		net := sim.DefaultNetConfig()
		net.DropPPM = 10_000
		h, err := newHarness(seed, dstNodes, dstConfig(), net)
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
		return s
	}
}

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
	})
	t.Cleanup(func() { m.report(t) })
}
