package main

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/nickstrad/object-store/internal/env"
	g "github.com/nickstrad/object-store/internal/gossip"
	"github.com/nickstrad/object-store/internal/sim"
	"reflect"
	"time"
)

type responder struct{ response []byte }

func (*responder) Start()                                              {}
func (*responder) OnDatagram(env.NodeID, []byte)                       {}
func (p *responder) OnRequest(_ env.NodeID, _ []byte, reply env.Reply) { reply(p.response, nil) }
func (p *responder) EncodeState() []byte                               { return p.response }
func extra() {
	attack("C7", "relay-once-original-sequence", func() string {
		c, n := one(g.Config{})
		t := c.taps[c.ids[0]]
		n.OnDatagram("origin", wire(g.Message{Kind: g.KindPingReq, Target: "target", Seq: 777}))
		check(len(t.packets) == 1 && t.packets[0].m.Kind == g.KindPing, "no relay ping")
		seq := t.packets[0].m.Seq
		for range 2 {
			n.OnDatagram("target", wire(g.Message{Kind: g.KindAck, Seq: seq}))
		}
		check(len(t.packets) == 2 && t.packets[1].to == "origin" && t.packets[1].m.Seq == 777, "seed=901 duplicate or wrong relay %v", t.packets)
		return "duplicate ACK forwarded once with requester sequence 777"
	})
	attack("C7", "expired-relay-dropped", func() string {
		c, n := one(g.Config{})
		t := c.taps[c.ids[0]]
		n.OnDatagram("origin", wire(g.Message{Kind: g.KindPingReq, Target: "target", Seq: 777}))
		seq := t.packets[0].m.Seq
		c.run(1100 * time.Millisecond)
		n.OnDatagram("target", wire(g.Message{Kind: g.KindAck, Seq: seq}))
		check(len(t.packets) == 1, "seed=901 expired relay forwarded")
		return "ACK at 1.1s did not forward expired 1s relay"
	})
	attack("C7", "indirect-fanout-distinct", func() string {
		c, n := one(g.Config{})
		for i := 0; i < 8; i++ {
			inject(n, member(fmt.Sprintf("peer%d", i), 0, g.StateAlive))
		}
		c.run(1500 * time.Millisecond)
		t := c.taps[c.ids[0]]
		groups := map[uint32][]packet{}
		for _, p := range t.packets {
			if p.m.Kind == g.KindPingReq {
				groups[p.m.Seq] = append(groups[p.m.Seq], p)
			}
		}
		check(len(groups) > 0, "seed=901 no indirect stage")
		for _, ps := range groups {
			check(len(ps) == 3, "seed=901 fanout=%d", len(ps))
			seen := map[env.NodeID]bool{}
			for _, p := range ps {
				check(!seen[p.to] && p.to != p.m.Target, "duplicate helper or target used")
				seen[p.to] = true
			}
		}
		return "each timeout selected exactly three distinct helpers excluding target"
	})
	for _, good := range []bool{false, true} {
		attack("C8", fmt.Sprintf("malformed-seed-with-success-%v", good), func() string {
			c, n := one(g.Config{})
			must(c.s.AddNode(sim.NodeSpec{ID: "bad"}, func(env.Env) env.Process { return &responder{[]byte{1, 255}} }))
			seeds := []env.NodeID{"bad"}
			if good {
				must(c.s.AddNode(sim.NodeSpec{ID: "good"}, func(env.Env) env.Process {
					return &responder{wire(g.Message{Kind: g.KindSyncResp, Updates: []g.Member{member("good", 0, g.StateAlive)}})}
				}))
				seeds = append(seeds, "good")
			}
			count := 0
			var result error
			must(n.Join(seeds, func(e error) { count++; result = e }))
			c.run(3 * time.Second)
			check(count == 1, "seed=901 callback count=%d", count)
			if good {
				check(result == nil, "seed=901 good seed failed %v", result)
			} else {
				check(errors.Is(result, g.ErrJoinFailed) && errors.Is(result, g.ErrMalformed), "seed=901 malformed reply causes=%v", result)
			}
			return "malformed reply rejected; aggregate failure or other-seed success delivered once"
		})
	}
	attack("C9", "periodic-live-and-dead-sync", func() string {
		for _, heal := range []string{"dead", "seed"} {
			cfg := g.Config{SyncInterval: time.Second}
			if heal == "seed" {
				cfg.Seeds = []env.NodeID{"seed"}
			}
			c, n := one(cfg)
			inject(n, member("live", 0, g.StateAlive))
			if heal == "dead" {
				inject(n, member("dead", 0, g.StateDead))
			}
			c.run(3 * time.Second)
			found := map[env.NodeID]bool{}
			for _, id := range c.taps[c.ids[0]].requests {
				found[id] = true
			}
			check(found["live"] && found[env.NodeID(heal)], "seed=901 periodic targets=%v", found)
		}
		return "with one eligible heal candidate, periodic sync contacted both live and dead/seed targets"
	})
	attack("C6", "shuffle-varies-across-seeds", func() string {
		seen := map[string]bool{}
		for seed := int64(2101); seed <= 2112; seed++ {
			c := clusterNew(seed, 1, g.Config{}, sim.NetConfig{})
			n := c.nodes[c.ids[0]]
			for i := 0; i < 6; i++ {
				inject(n, member(fmt.Sprintf("peer%d", i), 0, g.StateAlive))
			}
			c.run(6 * time.Second)
			seq := ""
			for _, p := range c.taps[c.ids[0]].packets {
				if p.m.Kind == g.KindPing {
					seq += string(p.to) + ","
				}
			}
			seen[seq] = true
		}
		check(len(seen) > 1, "seeds=2101..2112 identical rotation")
		return fmt.Sprintf("12 seeds produced %d distinct six-target rotation orders", len(seen))
	})
	attack("C14", "read-only-snapshot-isolation", func() string {
		_, n := one(g.Config{})
		inject(n, member("x", 4, g.StateAlive))
		before := n.EncodeState()
		all := n.All()
		live := n.Members()
		all[0].Incarnation = 999
		live[0].ID = "mutant"
		after := n.EncodeState()
		check(bytes.Equal(before, after), "seed=901 read-only view mutation changed state")
		before[0] ^= 255
		check(bytes.Equal(after, n.EncodeState()), "encoded bytes alias state")
		return "editing Members, All and encoded-byte snapshots did not change subsequent canonical bytes"
	})
	attack("C13", "structured-hostile-self-updates", func() string {
		_, n := one(g.Config{})
		for i := uint32(0); i < 1000; i++ {
			m := n.Self()
			m.Incarnation = i
			m.State = g.State(i%4 + 1)
			n.OnRequest("fuzzer", wire(g.Message{Kind: g.KindSyncReq, Updates: []g.Member{m, m}}), func(_ []byte, e error) { must(e) })
			check(n.Self().State == g.StateAlive && n.Self().Incarnation >= i, "seed=901 structured input broke self at %d", i)
		}
		return "1000 valid hostile sync requests with duplicate self claims preserved self invariants"
	})
	attack("C20", "explicit-false-suspicion-refuted", func() string {
		c := clusterNew(2201, 10, g.Config{Seeds: []env.NodeID{"n000"}}, sim.NetConfig{})
		c.run(10 * time.Second)
		victim := c.ids[9]
		old := c.nodes[victim].Self()
		u := old
		u.State = g.StateSuspect
		inject(c.nodes[c.ids[0]], u)
		c.events = nil
		c.run(25 * time.Second)
		check(c.nodes[victim].Self().Incarnation > old.Incarnation && c.agree(""), "seed=2201 no refutation/convergence")
		for _, ev := range c.events {
			check(ev.Kind != g.EventDied, "seed=2201 false suspicion killed %s", ev.Member.ID)
		}
		return "one forged same-incarnation suspicion raised victim incarnation and converged without death"
	})
	attack("C15", "join-leave-crash-churn", func() string {
		for seed := int64(2301); seed <= 2306; seed++ {
			c := clusterNew(seed, 20, g.Config{Seeds: []env.NodeID{"n000", "n001"}}, sim.NetConfig{})
			must(c.s.Isolate(c.ids[19]))
			for _, a := range c.ids {
				for _, b := range c.ids {
					if a != b {
						must(c.s.SetLinkLoss(a, b, 75000))
					}
				}
			}
			c.run(10 * time.Second)
			must(c.nodes[c.ids[17]].Leave())
			must(c.s.Crash(c.ids[18]))
			c.run(20 * time.Second)
			c.s.HealNet()
			ok := false
			for range 161 {
				var first []g.Member
				ok = true
				for _, id := range c.ids {
					if id == c.ids[17] || id == c.ids[18] {
						continue
					}
					ms := c.nodes[id].Members()
					if len(ms) != 18 {
						ok = false
						break
					}
					for _, m := range ms {
						if m.State != g.StateAlive {
							ok = false
						}
					}
					if first == nil {
						first = ms
					} else if !reflect.DeepEqual(first, ms) {
						ok = false
					}
				}
				if ok {
					break
				}
				c.run(250 * time.Millisecond)
			}
			check(ok, "seed=%d N=20 late join, leave, crash did not converge in 40s", seed)
		}
		return "6 seeds 2301..2306, N=20, 7.5% loss, delayed join/leave/crash: converged within 40s after heal"
	})
}
