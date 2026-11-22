package node_test

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

const brBucket = "br"

type bitRotResult struct {
	rotted, gets, getOK int
	repair              time.Duration
	rebuiltBytes        uint64
	rottedBytes         int
}

type rotted struct {
	node env.NodeID
	name string
	sum  [32]byte
}

func bitRotRun(res *bitRotResult) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		*res = bitRotResult{}
		h, err := newHarness(seed, dstNodes, dstConfig(), sim.DefaultNetConfig())
		if err != nil {
			h.s.Fail(err)
			return h.s
		}
		s := h.s
		w := s.WorkloadRand()
		h.createBucket(env.Time(2*time.Second), storage.BucketSpec{Name: brBucket})
		type obj struct {
			key  string
			data []byte
			man  *storage.Manifest
		}
		var objs []*obj
		for i := range 8 {
			o := &obj{key: fmt.Sprintf("r%d", i), data: payload(w, 1+w.IntN(4*dstStripe))}
			objs = append(objs, o)
			s.At(env.Time(4*time.Second+time.Duration(i)*200*time.Millisecond), "put", func() {
				h.node(h.ids[0]).PutObject(storage.PutRequest{Bucket: brBucket, Key: o.key, Data: o.data}, func(_ storage.ObjectInfo, err error) {
					if err != nil {
						s.Fail(fmt.Errorf("put %s: %w", o.key, err))
					}
				})
			})
		}
		var damage []rotted
		rotAt := env.Time(12 * time.Second)
		s.At(rotAt, "rot", func() {
			rd, err := h.node(h.ids[0]).OCC().Reader()
			if err != nil {
				s.Fail(err)
				return
			}
			for _, o := range objs {
				val, _, err := rd.Get(occManifestKey(brBucket, o.key))
				if err != nil {
					s.Fail(fmt.Errorf("manifest of %s missing at %s", o.key, h.ids[0]))
					return
				}
				man, err := manifestOf(val)
				if err != nil {
					s.Fail(err)
					return
				}
				o.man = man
				for st := range man.Stripes {
					for _, idx := range w.Perm(len(man.Stripes[st].Shards))[:w.IntN(3)] {
						sh := man.Stripes[st].Shards[idx]
						name := man.ShardID(st, idx).BlobName()
						blob, ok := s.Disk(sh.Node).PeekDurable(name)
						if !ok {
							continue
						}
						if err := s.BitRot(sh.Node, name); err != nil {
							s.Fail(err)
							return
						}
						damage = append(damage, rotted{node: sh.Node, name: name, sum: sh.Checksum})
						res.rottedBytes += len(blob)
					}
				}
			}
			res.rotted = len(damage)
		})
		for i := range 24 {
			s.At(rotAt.Add(time.Duration(i)*250*time.Millisecond), "get under rot", func() {
				o := objs[w.IntN(len(objs))]
				from := h.pick()
				res.gets++
				h.node(from).GetObject(brBucket, o.key, func(_ storage.ObjectInfo, got []byte, err error) {
					if err != nil {
						s.Fail(fmt.Errorf("get %s from %s with at most m rotted shards per stripe: %w", o.key, from, err))
						return
					}
					if !bytes.Equal(got, o.data) {
						s.Fail(fmt.Errorf("get %s from %s returned wrong bytes", o.key, from))
						return
					}
					res.getOK++
				})
			})
		}
		clean := func() bool {
			for _, d := range damage {
				blob, ok := s.Disk(d.node).PeekDurable(d.name)
				if !ok || storage.BlobChecksum(blob) != d.sum {
					return false
				}
				if _, _, err := storage.DecodeShard(blob); err != nil {
					return false
				}
			}
			return true
		}
		if err := s.RunUntil(rotAt.Add(time.Millisecond)); err != nil {
			return s
		}
		if err := s.RunUntilConverged(rotAt.Add(30*time.Second), clean); err != nil {
			s.Fail(fmt.Errorf("rotted shards were not repaired: %w", err))
			return s
		}
		res.repair = s.Now().Sub(rotAt)
		if err := s.RunFor(8 * time.Second); err != nil {
			return s
		}
		for _, n := range h.up() {
			res.rebuiltBytes += n.Stats().RebuiltBytes
		}
		if res.rotted == 0 {
			s.Fail(fmt.Errorf("the scenario rotted no shard"))
			return s
		}
		if res.rebuiltBytes != uint64(res.rottedBytes) {
			s.Fail(fmt.Errorf("rebuilt %d bytes for %d rotted bytes", res.rebuiltBytes, res.rottedBytes))
			return s
		}
		if err := h.checkShardsReferenced(false); err != nil {
			s.Fail(err)
		}
		return s
	}
}

func TestClusterBitRotDST(t *testing.T) {
	t.Parallel()
	var m measure
	sweepSharded(t, func(t *testing.T, seed int64) {
		var res bitRotResult
		simtest.RequireDeterministic(t, seed, bitRotRun(&res))
		m.add("bitrot/rotted_shards", int64(res.rotted))
		m.add("bitrot/repair_ms", res.repair.Milliseconds())
		if res.rottedBytes > 0 {
			m.add("bitrot/rebuilt_per_rotted_x1000", int64(res.rebuiltBytes)*1000/int64(res.rottedBytes))
		}
		m.add("bitrot/get_ok", int64(res.getOK))
	})
	t.Cleanup(func() { m.report(t) })
}
