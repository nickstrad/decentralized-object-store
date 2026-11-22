package main

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"time"
)

type world struct {
	s           *sim.Sim
	ids         []env.NodeID
	lost        map[occ.VersionID]bool
	hooks, logs int
	envs        map[env.NodeID]env.Env
}

func newWorld(seed int64, n int, skew bool) *world { return newWorldOrder(seed, n, skew, false) }
func newWorldOrder(seed int64, n int, skew bool, reverse bool) *world {
	codes, e := occ.Codes()
	must(e)
	net := sim.DefaultNetConfig()
	net.DuplicatePPM = 330000
	net.DropPPM = 30000
	w := &world{s: sim.New(sim.Config{Seed: seed, Net: net, Codes: codes}), ids: sim.NodeIDs(n), lost: map[occ.VersionID]bool{}, envs: map[env.NodeID]env.Env{}}
	for i, id := range w.ids {
		spec := sim.NodeSpec{ID: id, Region: env.Region(fmt.Sprint(i % 3))}
		if skew {
			spec.Skew = time.Duration(i-2) * 700 * time.Millisecond
			spec.DriftPPM = int64(i-2) * 1900
		}
		must(w.s.AddNode(spec, func(e env.Env) env.Process {
			w.envs[e.ID] = e
			return occ.NewNode(e, occ.Config{Membership: func() occ.StaticMembership {
				ps := append([]env.NodeID{}, w.ids...)
				if reverse {
					for i, j := 0, len(ps)-1; i < j; i, j = i+1, j-1 {
						ps[i], ps[j] = ps[j], ps[i]
					}
					ps = append(ps, ps...)
				}
				return occ.StaticMembership(ps)
			}(), Period: 70 * time.Millisecond, Timeout: time.Second, Log: func(string) { w.logs++ }, OnDiscard: func(k string, win, lose occ.Version) { w.hooks++; w.lost[lose.ID()] = true }})
		}))
	}
	must(w.s.RunFor(time.Millisecond))
	return w
}
func (w *world) node(i int) *occ.Node { return w.s.Process(w.ids[i]).(*occ.Node) }
func (w *world) settle() {
	w.s.HealNet()
	must(w.s.RunUntilConverged(w.s.Now().Add(12*time.Second), func() bool {
		for i := 1; i < len(w.ids); i++ {
			if !w.node(0).Doc().Equal(w.node(i).Doc()) {
				return false
			}
		}
		return true
	}))
	for i := 1; i < len(w.ids); i++ {
		require(bytes.Equal(w.node(0).Doc().Encode(), w.node(i).Doc().Encode()), "converged digests differ in bytes")
	}
}
func (w *world) counts() {
	var n uint64
	for i := range w.ids {
		n += w.node(i).LWWDiscards()
	}
	require(n == uint64(w.hooks) && w.logs == w.hooks, "counter=%d hooks=%d logs=%d", n, w.hooks, w.logs)
}
func scenarios() {
	check("C28", "peer-permutation-digests", func() string {
		for seed := int64(4701); seed < 4711; seed++ {
			run := func(reverse bool) sim.Digests {
				w := newWorldOrder(seed, 5, false, reverse)
				for i := range w.ids {
					_, e := w.node(i).Put(fmt.Sprint(i), []byte("x"), occ.Unconditional())
					must(e)
				}
				must(w.s.RunFor(3 * time.Second))
				return w.s.Digests()
			}
			require(run(false) == run(true), "seed=%d membership reverse/duplicate changed event or state digest", seed)
		}
		return "10 seeds with reversed and duplicated membership lists produced identical event and state digests"
	})
	check("C25", "read-errors-and-bitrot", func() string {
		w := newWorld(4701, 2, false)
		v, e := w.node(0).Put("k", nil, occ.Unconditional())
		must(e)
		w.settle()
		must(w.s.Crash(w.ids[0]))
		must(w.s.FailReads(w.ids[0], occ.StateBlob, 2))
		must(w.s.Restart(w.ids[0]))
		must(w.s.RunFor(time.Millisecond))
		require(!w.node(0).Ready(), "read error opened fresh store")
		_, e = w.node(0).Put("k", nil, occ.Unconditional())
		require(errors.Is(e, occ.ErrNotReady), "write accepted before state loaded")
		must(w.s.RunFor(300 * time.Millisecond))
		require(w.node(0).Ready() && w.node(0).Actor() == v.Dot().Actor && w.node(0).Record("k").ETag() == v.ETag(), "read retry lost state")
		must(w.s.Crash(w.ids[0]))
		must(w.s.BitRot(w.ids[0], occ.StateBlob))
		must(w.s.Restart(w.ids[0]))
		must(w.s.RunFor(time.Millisecond))
		require(w.node(0).Actor() != v.Dot().Actor && w.node(0).Stats().Corrupt == 1, "corruption reused actor")
		return "two transient reads kept node unready then restored acknowledged state; bit-rot produced a fresh epoch"
	})

	check("C9,C12,C29,C30", "partition-loss-oracle", func() string {
		totalLost, totalAck := 0, 0
		for seed := int64(4701); seed < 4741; seed++ {
			w := newWorld(seed, 6, true)
			must(w.s.Partition(w.ids[:3], w.ids[3:]))
			seen := map[occ.VersionID]map[occ.VersionID]bool{}
			acks := map[occ.VersionID]string{}
			r := sim.NewStream(seed, "independent-workload")
			for step := 0; step < 100; step++ {
				i := r.IntN(6)
				k := fmt.Sprint(r.IntN(8))
				held, ok := w.node(i).Record(k).Version()
				p := occ.Unconditional()
				if step%3 == 0 {
					p = occ.IfMatch(w.node(i).Record(k).ETag())
				}
				var v occ.Version
				var e error
				if step%7 == 0 {
					v, e = w.node(i).Delete(k, p)
				} else {
					v, e = w.node(i).Put(k, []byte(fmt.Sprint(step)), p)
				}
				if e == nil {
					ss := map[occ.VersionID]bool{}
					if ok {
						ss[held.ID()] = true
						for x := range seen[held.ID()] {
							ss[x] = true
						}
					}
					seen[v.ID()] = ss
					acks[v.ID()] = k
				} else {
					require(errors.Is(e, occ.ErrPreconditionFailed) || errors.Is(e, occ.ErrNotFound), "seed=%d unexpected write %v", seed, e)
				}
				must(w.s.RunFor(13 * time.Millisecond))
			}
			w.settle()
			for id, k := range acks {
				v, _ := w.node(0).Record(k).Version()
				covered := v.ID() == id || w.lost[id]
				for _, ss := range seen {
					covered = covered || ss[id]
				}
				require(covered, "seed=%d key=%s acknowledged version=%s vanished without observed successor or discard", seed, k, id)
				if w.lost[id] {
					totalLost++
				}
			}
			w.counts()
			totalAck += len(acks)
		}
		return fmt.Sprintf("40 seeds x 100 operations converged; %d acknowledgements and %d unique logged losses, with exact counter/log/hook equality", totalAck, totalLost)
	})
	check("C31", "same-node-wire-race", func() string {
		w := newWorld(4701, 3, false)
		v, e := w.node(0).Put("k", nil, occ.IfNoneMatch())
		must(e)
		client := occ.NewClient(w.envs[w.ids[1]].Net, time.Second)
		ok, bad := 0, 0
		for i := 0; i < 2; i++ {
			client.Put(w.ids[0], "k", []byte{byte(i)}, occ.IfMatch(v.ETag()), func(_ occ.Result, e error) {
				if e == nil {
					ok++
				} else if errors.Is(e, occ.ErrPreconditionFailed) {
					bad++
				} else {
					panic(e)
				}
			})
		}
		must(w.s.RunFor(2 * time.Second))
		require(ok == 1 && bad == 1, "successful=%d refused=%d", ok, bad)
		require(w.hooks == 0, "local race discarded")
		return "two wire clients with one ETag produced one success and one precondition error"
	})
	check("C6,C9,C31", "partition-two-writer", func() string {
		for seed := int64(4701); seed < 4711; seed++ {
			w := newWorld(seed, 2, false)
			v, e := w.node(0).Put("k", nil, occ.IfNoneMatch())
			must(e)
			w.settle()
			must(w.s.RunFor(time.Second))
			must(w.s.Partition(w.ids[:1], w.ids[1:]))
			a, e := w.node(0).Put("k", []byte("a"), occ.IfMatch(v.ETag()))
			must(e)
			b, e := w.node(1).Put("k", []byte("b"), occ.IfMatch(v.ETag()))
			must(e)
			w.settle()
			winner, _ := w.node(0).Record("k").Version()
			loser := a
			if winner.ID() == a.ID() {
				loser = b
			} else {
				require(winner.ID() == b.ID(), "unknown winner")
			}
			require(w.lost[loser.ID()] && len(w.lost) == 1 && w.hooks == 1, "seed=%d missing/excess discard", seed)
			w.counts()
		}
		return "10 seeds accepted both partition-side CAS writes and recorded exactly one losing version per seed"
	})
	check("C8,C25,C26", "crash-wipe-epochs", func() string {
		for seed := int64(4701); seed < 4721; seed++ {
			w := newWorld(seed, 2, false)
			v, e := w.node(0).Put("k", []byte("old"), occ.Unconditional())
			must(e)
			w.settle()
			must(w.s.Crash(w.ids[0]))
			must(w.s.Restart(w.ids[0]))
			must(w.s.RunFor(time.Millisecond))
			require(w.node(0).Actor() == v.Dot().Actor && w.node(0).Record("k").ETag() == v.ETag(), "seed=%d crash lost persisted write", seed)
			must(w.s.Isolate(w.ids[0]))
			must(w.s.WipeRejoin(w.ids[0]))
			must(w.s.RunFor(time.Millisecond))
			require(w.node(0).Actor() != v.Dot().Actor, "seed=%d wipe reused actor", seed)
			next, e := w.node(0).Put("k", []byte("new"), occ.Unconditional())
			must(e)
			require(next.Dot() != v.Dot(), "dot reused")
			w.settle()
			require(w.lost[v.ID()] || w.lost[next.ID()], "wipe contest classified causal")
			w.counts()
		}
		return "20 crash/restart/wipe cycles retained crash state, changed wiped epochs and logged concurrent losses"
	})
	check("C32,C17", "quota-partition-measurement", func() string {
		w := newWorld(4701, 4, false)
		for i := range w.ids {
			must(w.s.Isolate(w.ids[i]))
		}
		accepted := 0
		for i := range w.ids {
			for j := 0; j < 12; j++ {
				_, e := w.node(i).UpdateQuota("q", 2, occ.AddUsage(25, 1, occ.Usage{Bytes: 250}))
				if e == nil {
					accepted++
				} else {
					require(errors.Is(e, occ.ErrQuotaExceeded), "quota error %v", e)
				}
			}
		}
		require(accepted == 40, "accepted=%d", accepted)
		w.settle()
		require(w.hooks > 0, "no contested quota")
		w.counts()
		actual := occ.Usage{Bytes: uint64(accepted * 25), Objects: uint64(accepted)}
		_, wrote, e := w.node(0).RecountQuota("q", 3, func(string) (occ.Usage, error) { return actual, nil })
		must(e)
		require(wrote, "undercount not corrected")
		w.settle()
		for i := range w.ids {
			_, wrote, e = w.node(i).RecountQuota("q", 2, func(string) (occ.Usage, error) { return actual, nil })
			must(e)
			require(!wrote, "correct recount wrote again")
		}
		return fmt.Sprintf("40 accepted allocations total 1000 bytes at limit 250: overshoot 750 exactly (writers-1)*limit; %d discard events; recount settled", w.hooks)
	})
	check("C27", "malformed-node-wire", func() string {
		w := newWorld(4701, 2, false)
		before := w.node(0).Doc().Encode()
		for _, b := range [][]byte{nil, {255}, {1}, {2}, {3}, {0, 0}} {
			called := false
			w.node(0).OnRequest(w.ids[1], b, func(_ []byte, e error) {
				called = true
				require(errors.Is(e, occ.ErrMalformed), "request %x returned %v", b, e)
			})
			require(called, "no malformed reply")
			w.node(0).OnDatagram(w.ids[1], b)
		}
		require(bytes.Equal(before, w.node(0).Doc().Encode()), "malformed messages changed doc")
		return "six malformed request/datagram pairs rejected without document mutation"
	})
}
