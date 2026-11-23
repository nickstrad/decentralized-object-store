package node_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/sim"
	"github.com/nickstrad/object-store/internal/sim/simtest"
	"github.com/nickstrad/object-store/internal/storage"
)

func quotaDriftRun(seed int64) *sim.Sim {
	cfg := partitionConfig()
	cfg.Layout = smallLayout()
	h, err := newHarness(seed, 6, cfg, sim.DefaultNetConfig())
	if err != nil {
		h.s.Fail(err)
		return h.s
	}
	s := h.s
	a, b := []env.NodeID{h.ids[0], h.ids[1], h.ids[2]}, []env.NodeID{h.ids[3], h.ids[4], h.ids[5]}
	put := func(id env.NodeID, size int) {
		done := false
		h.node(id).PutObject(storage.PutRequest{Bucket: "q", Key: "k", Data: payload(s.WorkloadRand(), size)}, func(_ storage.ObjectInfo, err error) {
			done = true
			if err != nil {
				s.Fail(fmt.Errorf("put %d bytes on %s: %w", size, id, err))
			}
		})
		if err := s.RunUntilConverged(s.Now().Add(10*time.Second), func() bool { return done }); err != nil {
			s.Fail(fmt.Errorf("put %d bytes on %s never completed: %w", size, id, err))
		}
	}
	if err := s.RunFor(3 * time.Second); err != nil {
		return s
	}
	if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: "q", QuotaBytes: 1000}); err != nil {
		s.Fail(err)
		return s
	}
	put(h.ids[0], 100)
	if err := s.RunUntilConverged(s.Now().Add(20*time.Second), h.converged); err != nil {
		s.Fail(fmt.Errorf("first converge: %w", err))
		return s
	}
	if err := s.Partition(a, b); err != nil {
		s.Fail(err)
		return s
	}
	if err := s.RunUntilConverged(s.Now().Add(30*time.Second), func() bool {
		for _, id := range h.ids {
			if len(h.node(id).Gossip().Members()) != 3 {
				return false
			}
		}
		return true
	}); err != nil {
		s.Fail(fmt.Errorf("sides never saw each other die: %w", err))
		return s
	}
	put(a[1], 400)
	if err := s.RunFor(500 * time.Millisecond); err != nil {
		return s
	}
	put(b[1], 100)
	s.HealAll()
	if err := s.RunUntilConverged(s.Now().Add(40*time.Second), h.converged); err != nil {
		s.Fail(fmt.Errorf("heal converge: %w", err))
		return s
	}
	if err := s.RunUntilConverged(s.Now().Add(30*time.Second), func() bool { return h.metaConverged() && h.quotaAgrees("q") }); err != nil {
		truth, _ := h.quotaTruth("q")
		q, _ := h.up()[0].Quota("q")
		s.Fail(fmt.Errorf("usage %d B / %d objects never matched the manifests (%d B / %d objects) after heal: %w", q.UsedBytes, q.UsedObjects, truth.Bytes, truth.Objects, err))
	}
	return s
}

// TestQuotaRecountsAfterAManifestDiscardDST proves a manifest-key LWW discard
// without a quota-key discard (unequal-size vs same-size overwrite across a
// partition) still triggers a recount and brings usage back in line with live
// manifests after heal.
func TestQuotaRecountsAfterAManifestDiscardDST(t *testing.T) {
	t.Parallel()
	sweepSharded(t, func(t *testing.T, seed int64) {
		simtest.RequireDeterministic(t, seed, quotaDriftRun)
	})
}
