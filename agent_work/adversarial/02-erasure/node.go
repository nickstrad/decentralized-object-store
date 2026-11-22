package main

import (
	"bytes"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	s "github.com/nickstrad/decentralized-object-store/internal/storage"
)

type cluster struct {
	world *sim.Sim
	ids   []env.NodeID
	nodes map[env.NodeID]*s.Node
}

func newCluster(seed int64, n, window, stripe int, network ...sim.NetConfig) *cluster {
	codes, err := s.Codes()
	must(err)
	cfg := sim.Config{Seed: seed, Codes: codes}
	if len(network) > 0 {
		cfg.Net = network[0]
	}
	w := sim.New(cfg)
	ids := sim.NodeIDs(n)
	ms := s.StaticMembership{}
	for i, id := range ids {
		ms = append(ms, s.Member{ID: id, Region: env.Region(fmt.Sprintf("r%d", i%3))})
	}
	c := &cluster{w, ids, map[env.NodeID]*s.Node{}}
	must(w.AddCluster(ids, 3, func(e env.Env) env.Process {
		nd, err := s.NewNode(e, s.Config{Layout: s.Layout{K: 4, M: 2, StripeSize: stripe}, Members: ms, Window: window})
		must(err)
		c.nodes[e.ID] = nd
		return nd
	}))
	must(w.RunUntilQuiet())
	return c
}
func (c *cluster) put(data []byte) (*s.Manifest, error) {
	count := 0
	var m *s.Manifest
	var out error
	c.nodes[c.ids[0]].Put("b", "key", data, func(man *s.Manifest, err error) { count++; m, out = man, err })
	must(c.world.RunUntilQuiet())
	check(count == 1, "put callbacks=%d", count)
	return m, out
}
func (c *cluster) get(node env.NodeID, m *s.Manifest) (s.GetResult, error) {
	count := 0
	var res s.GetResult
	var out error
	c.nodes[node].Get(m, func(r s.GetResult, err error) { count++; res, out = r, err })
	check(count == 0, "get reentrant")
	must(c.world.RunUntilQuiet())
	check(count == 1, "get callbacks=%d", count)
	return res, out
}
func nodes() {
	attack("C21", "node-clean-boundaries", func() string {
		for _, window := range []int{1, 4, 9} {
			c := newCluster(220211, 20, window, 64)
			for _, size := range []int{0, 1, 3, 4, 63, 64, 65, 4099} {
				data := payload(size)
				m, err := c.put(data)
				must(err)
				for i, st := range m.Stripes {
					for j, ref := range st.Shards {
						b, ok := c.world.Disk(ref.Node).PeekDurable(m.ShardID(i, j).BlobName())
						check(ok, "not durable stripe=%d shard=%d", i, j)
						check(s.BlobChecksum(b) == ref.Checksum, "manifest checksum")
					}
				}
				for _, id := range c.ids {
					res, err := c.get(id, m)
					must(err)
					same(res.Data, data)
					check(res.Requests == 4*len(m.Stripes), "clean requests=%d stripes=%d", res.Requests, len(m.Stripes))
				}
			}
		}
		return "480 GETs from all 20 nodes over eight sizes and three windows; exact bytes, k requests, durable manifests"
	})
	for _, kind := range []string{"rot", "wipe", "read-once", "read-persistent"} {
		kind := kind
		attack("C22,C24,C27,C28", "node-fault-"+kind, func() string {
			for seed := int64(220211); seed < 220223; seed++ {
				for damage := 2; damage <= 3; damage++ {
					c := newCluster(seed, 20, 2, 127)
					data := payload(509)
					m, err := c.put(data)
					must(err)
					for i, st := range m.Stripes {
						for j := 0; j < damage; j++ {
							ref := st.Shards[j]
							name := m.ShardID(i, j).BlobName()
							switch kind {
							case "rot":
								must(c.world.BitRot(ref.Node, name))
							case "wipe": // Per-blob durable deletion avoids wiping unrelated stripes outside the budget.
								must(c.nodes[ref.Node].Store().Delete(m.ShardID(i, j)))
							case "read-once":
								must(c.world.FailReads(ref.Node, name, 1))
							case "read-persistent":
								must(c.world.FailReads(ref.Node, name, 100))
							}
						}
					}
					for _, id := range []env.NodeID{c.ids[1], c.ids[7], c.ids[19]} {
						if kind == "read-once" {
							for i, st := range m.Stripes {
								for j := 0; j < damage; j++ {
									must(c.world.FailReads(st.Shards[j].Node, m.ShardID(i, j).BlobName(), 1))
								}
							}
						}
						res, err := c.get(id, m)
						if damage <= 2 || kind == "read-once" {
							must(err)
							same(res.Data, data)
						} else {
							check(errors.Is(err, s.ErrUnavailable) && res.Data == nil, "seed=%d damage=%d err=%v", seed, damage, err)
							var oe *s.ObjectError
							check(errors.As(err, &oe), "missing ObjectError")
							if kind == "rot" {
								check(errors.Is(err, s.ErrCorrupt) && errors.Is(err, env.ErrRemote), "remote corrupt lost")
							}
							if kind == "read-persistent" {
								check(errors.Is(err, env.ErrIO), "IO cause lost")
							}
						}
						check(res.Requests <= 24*len(m.Stripes), "unbounded retries %d", res.Requests)
					}
				}
			}
			return "72 GETs across 12 seeds and two/three damaged shards per stripe satisfy recovery/error boundary and retry bounds"
		})
	}
	attack("C23", "quorum-and-slow-holder", func() string {
		for down := 1; down <= 2; down++ {
			for _, pause := range []bool{false, true} {
				c := newCluster(220211, 6, 1, 64)
				for j := 0; j < down; j++ {
					if pause {
						must(c.world.Pause(c.ids[5-j]))
					} else {
						must(c.world.Crash(c.ids[5-j]))
					}
				}
				calls := 0
				var out error
				var at env.Time
				c.nodes[c.ids[0]].Put("b", "k", payload(129), func(_ *s.Manifest, err error) { calls++; out = err; at = c.world.Now() })
				check(calls == 0, "put reentrant")
				must(c.world.RunUntilQuiet())
				check(calls == 1, "callbacks %d", calls)
				if down == 1 {
					must(out)
					check(at < env.Time(time.Second), "waited for timeout at %v", at)
				} else {
					check(errors.Is(out, s.ErrUnavailable), "two unavailable err=%v", out)
					if !pause {
						check(errors.Is(out, env.ErrUnreachable), "unreachable cause lost")
					}
				}
			}
		}
		return "one paused/crashed holder succeeds before timeout; two fail with one callback"
	})
	attack("C25", "content-hash-and-forged-manifest", func() string {
		for _, size := range []int{0, 1, 129} {
			c := newCluster(220211, 6, 2, 64)
			m, err := c.put(payload(size))
			must(err)
			m.ContentSHA[0] ^= 1
			res, err := c.get(c.ids[1], m)
			check(errors.Is(err, s.ErrCorrupt) && res.Data == nil, "wrong hash not rejected size=%d", size)
		}
		c := newCluster(220211, 6, 2, 64)
		data := payload(129)
		m, err := c.put(data)
		must(err)
		for i, st := range m.Stripes {
			for j, ref := range st.Shards {
				id := m.ShardID(i, j)
				b, err := c.nodes[ref.Node].Store().GetBlob(id)
				must(err)
				h, p, err := s.DecodeShard(b)
				must(err)
				p = append([]byte{}, p...)
				p[0] ^= 1
				b, err = s.EncodeShard(h, p)
				must(err)
				must(c.nodes[ref.Node].Store().Delete(id))
				must(c.nodes[ref.Node].Store().Put(id, b))
				m.Stripes[i].Shards[j].Checksum = s.BlobChecksum(b)
			}
		}
		res, err := c.get(c.ids[1], m)
		check(errors.Is(err, s.ErrCorrupt) && res.Data == nil, "forged shard checksums bypass content hash")
		return "empty/nonempty wrong hashes and all rechecksummed forged shards rejected by end-to-end content hash"
	})
	attack("C27,C28", "region-loss-and-wipes", func() string {
		for seed := int64(220211); seed < 220221; seed++ {
			for _, kind := range []string{"wipe", "partition"} {
				c := newCluster(seed, 20, 3, 257)
				data := payload(1801)
				m, err := c.put(data)
				must(err)
				if kind == "wipe" {
					for _, id := range c.ids {
						if c.world.Region(id) == "r2" {
							must(c.world.WipeDisk(id))
						}
					}
				} else {
					must(c.world.PartitionRegion("r2"))
				}
				res, err := c.get(c.ids[0], m)
				must(err)
				same(res.Data, data)
			}
		}
		return "20 schedules across ten seeds recover exact bytes after complete region disk wipe or partition"
	})
	attack("C30", "multimb-memory", func() string {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		c := newCluster(220211, 6, 2, 1<<20)
		data := payload(8<<20 | 17)
		m, err := c.put(data)
		must(err)
		res, err := c.get(c.ids[1], m)
		must(err)
		same(res.Data, data)
		runtime.ReadMemStats(&after)
		alloc := after.TotalAlloc - before.TotalAlloc
		check(alloc < 1024<<20, "8MiB total allocation=%d exceeds 1GiB diagnostic ceiling", alloc)
		return fmt.Sprintf("8MiB+17 bytes round-trip; total allocations=%d bytes below 1GiB diagnostic ceiling (whole-object GET is documented)", alloc)
	})
	attack("C30", "window-pending-stripes", func() string {
		observed := []uint64{}
		for _, count := range []int{2, 8, 64} {
			c := newCluster(220211, 6, 1, 4, sim.NetConfig{Latency: sim.Range{Min: time.Microsecond, Max: time.Microsecond}, InterRegionLatency: sim.Range{Min: time.Microsecond, Max: time.Microsecond}})
			must(c.world.Pause(c.ids[5]))
			done := false
			c.nodes[c.ids[0]].Put("b", "k", payload(count*4-3), func(_ *s.Manifest, err error) { must(err); done = true })
			must(c.world.RunUntilConverged(c.world.Now().Add(time.Second/2), func() bool { return done }))
			sent := c.nodes[c.ids[0]].Stats().Requests
			served := uint64(0)
			for _, n := range c.nodes {
				served += n.Stats().Served
			}
			observed = append(observed, sent-served)
			// All six nodes hold each stripe; only n005 is paused. Its requests
			// have not been served, so each pending request is a distinct stripe.
		}
		check(observed[0] <= 1 && observed[1] <= 1 && observed[2] <= 1,
			"Window=1, k=4,m=2,stripe=4,pause n005: pending stripes at PUT ack=%v for 2/8/64 stripes; smallest reproduction is 5 bytes, seed=220211", observed)
		return "outstanding stripes stay within Window even with a paused holder"
	})

	attack("C27", "scheduled-fault-replay", func() string {
		run := func() sim.Digests {
			c := newCluster(220211, 20, 2, 64)
			data := payload(1025)
			m, err := c.put(data)
			must(err)
			for i, st := range m.Stripes {
				for j := 0; j < 2; j++ {
					c.world.InjectAt(c.world.Now().Add(time.Duration(i+1)*time.Millisecond), sim.Fault{Kind: sim.FaultBitRot, Node: st.Shards[j].Node, Blob: m.ShardID(i, j).BlobName()})
				}
			}
			for i := 0; i < 10; i++ {
				c.world.At(c.world.Now().Add(time.Duration(i)*5*time.Millisecond), "concurrent-get", func() {
					c.nodes[c.ids[1]].Get(m, func(r s.GetResult, err error) { must(err); check(bytes.Equal(r.Data, data), "concurrent wrong bytes") })
				})
			}
			must(c.world.RunUntilQuiet())
			check(c.nodes[c.ids[1]].Stats().GetsDone == 10, "missing callbacks")
			return c.world.Digests()
		}
		a, b := run(), run()
		check(a == b, "replay digest mismatch")
		return fmt.Sprintf("ten concurrent GETs amid 34 scheduled rotations replay exactly: events=%x", a.Events)
	})
}
