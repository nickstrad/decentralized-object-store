package main

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"math/rand/v2"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	g "github.com/nickstrad/object-store/internal/gossip"
	"github.com/nickstrad/object-store/internal/sim"
)

var broken int

func check(ok bool, format string, args ...any) {
	if !ok {
		panic(fmt.Sprintf(format, args...))
	}
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
func attack(claim, name string, fn func() string) {
	defer func() {
		if x := recover(); x != nil {
			broken++
			fmt.Printf("BROKEN %s %s: %v\n", claim, name, x)
		}
	}()
	msg := fn()
	fmt.Printf("HELD %s %s: %s\n", claim, name, msg)
}
func wire(m g.Message) []byte { b, e := m.Encode(); must(e); return b }
func inject(n *g.Node, us ...g.Member) {
	n.OnDatagram("attacker", wire(g.Message{Kind: g.KindGossip, Updates: us}))
}
func member(id string, inc uint32, st g.State) g.Member {
	return g.Member{ID: env.NodeID(id), Region: "r0", Incarnation: inc, State: st}
}

type packet struct {
	to   env.NodeID
	m    g.Message
	size int
}
type tap struct {
	env.Transport
	packets  []packet
	requests []env.NodeID
	err      error
}

func (t *tap) Send(to env.NodeID, b []byte) error {
	m, e := g.Decode(b)
	must(e)
	t.packets = append(t.packets, packet{to, m, len(b)})
	if t.err != nil {
		return t.err
	}
	return t.Transport.Send(to, b)
}
func (t *tap) Request(to env.NodeID, b []byte, d time.Duration, cb func([]byte, error)) {
	t.requests = append(t.requests, to)
	t.Transport.Request(to, b, d, cb)
}

type cluster struct {
	s      *sim.Sim
	nodes  map[env.NodeID]*g.Node
	taps   map[env.NodeID]*tap
	ids    []env.NodeID
	events []g.Event
	cfg    g.Config
}

func clusterNew(seed int64, n int, cfg g.Config, net sim.NetConfig) *cluster {
	codes, e := g.CodeTable()
	must(e)
	c := &cluster{s: sim.New(sim.Config{Seed: seed, Codes: codes, Net: net, LogCap: 64}), nodes: map[env.NodeID]*g.Node{}, taps: map[env.NodeID]*tap{}, cfg: cfg}
	for i := 0; i < n; i++ {
		id := env.NodeID(fmt.Sprintf("n%03d", i))
		c.ids = append(c.ids, id)
		must(c.s.AddNode(sim.NodeSpec{ID: id, Region: env.Region(fmt.Sprintf("r%d", i%3))}, func(e env.Env) env.Process {
			t := &tap{Transport: e.Net}
			e.Net = t
			node, err := g.New(e, cfg)
			must(err)
			c.nodes[e.ID] = node
			c.taps[e.ID] = t
			node.Subscribe(func(ev g.Event) { c.events = append(c.events, ev) })
			return node
		}))
	}
	must(c.s.RunFor(0))
	return c
}
func (c *cluster) run(d time.Duration) { must(c.s.RunFor(d)) }
func (c *cluster) bootstrap() {
	for _, id := range c.ids[1:] {
		must(c.nodes[id].Join(c.ids[:1], nil))
	}
	c.run(5 * time.Second)
}
func (c *cluster) agree(exclude env.NodeID) bool {
	var first []g.Member
	for _, id := range c.ids {
		if id == exclude || !c.s.Status(id).Up || c.nodes[id].Closed() {
			continue
		}
		ms := c.nodes[id].Members()
		want := len(c.ids)
		if exclude != "" {
			want--
		}
		if len(ms) != want {
			return false
		}
		for _, m := range ms {
			if m.State != g.StateAlive {
				return false
			}
		}
		if first == nil {
			first = ms
		} else if !reflect.DeepEqual(first, ms) {
			return false
		}
	}
	return true
}
func (c *cluster) until(d time.Duration, exclude env.NodeID) time.Duration {
	for elapsed := time.Duration(0); elapsed <= d; elapsed += 250 * time.Millisecond {
		if c.agree(exclude) {
			return elapsed
		}
		c.run(250 * time.Millisecond)
	}
	return -1
}
func one(cfg g.Config) (*cluster, *g.Node) {
	c := clusterNew(901, 1, cfg, sim.NetConfig{})
	return c, c.nodes[c.ids[0]]
}
func core() {
	for _, nodeMode := range []bool{false, true} {
		attack("C1", fmt.Sprintf("transition-matrix-node-%v", nodeMode), func() string {
			for old := g.StateAlive; old <= g.StateLeft; old++ {
				for next := g.StateAlive; next <= g.StateLeft; next++ {
					for _, inc := range []uint32{6, 7, 8} {
						a, b := member("x", 7, old), member("x", inc, next)
						want := a
						if inc > 7 || (inc == 7 && next > old) {
							want = b
						}
						if nodeMode {
							_, n := one(g.Config{})
							inject(n, a)
							inject(n, b)
							got, e := n.Lookup("x")
							must(e)
							check(got == want, "seed=901 old=%v update=%v got=%v", a, b, got)
						} else {
							v := g.NewView()
							v.Apply(a)
							v.Apply(b)
							got, _ := v.Get("x")
							check(got == want, "old=%v update=%v got=%v", a, b, got)
						}
					}
				}
			}
			return "48 lower/equal/higher transitions matched an independent rank oracle"
		})
	}
	attack("C1", "event-sequence", func() string {
		c, n := one(g.Config{})
		for _, u := range []g.Member{member("x", 0, g.StateAlive), member("x", 0, g.StateSuspect), member("x", 1, g.StateAlive), member("x", 1, g.StateDead), member("x", 2, g.StateAlive), member("x", 2, g.StateLeft)} {
			inject(n, u)
		}
		var kinds []g.EventKind
		for _, ev := range c.events {
			kinds = append(kinds, ev.Kind)
		}
		check(reflect.DeepEqual(kinds, []g.EventKind{g.EventJoined, g.EventSuspected, g.EventRecovered, g.EventDied, g.EventJoined, g.EventLeft}), "seed=901 events=%v", kinds)
		return "join/suspect/recover/death/rejoin/leave emitted exactly once"
	})
	for _, state := range []g.State{g.StateAlive, g.StateSuspect, g.StateDead, g.StateLeft} {
		attack("C2", fmt.Sprintf("self-refute-%s", state), func() string {
			_, n := one(g.Config{})
			u := n.Self()
			u.State = state
			u.Incarnation = 42
			inject(n, u)
			check(n.Self().Incarnation == 43 && n.Self().State == g.StateAlive, "seed=901 self=%v", n.Self())
			u.Incarnation = 1
			inject(n, u)
			check(n.Self().Incarnation == 43, "seed=901 stale self reduced incarnation")
			u.Incarnation = math.MaxUint32
			inject(n, u)
			check(n.Self().Incarnation == math.MaxUint32 && n.Self().State == g.StateAlive && n.Stats().RefuteOverflows == 1, "seed=901 overflow=%v", n.Self())
			return "higher claims refuted, stale claims ignored, maximum saturated without self death"
		})
	}
	for _, seed := range []uint64{901, 902, 903} {
		attack("C3", fmt.Sprintf("merge-permutations-%d", seed), func() string {
			r := rand.New(rand.NewPCG(seed, 37))
			us := make([]g.Member, 500)
			want := map[env.NodeID]g.Member{}
			for i := range us {
				u := member(fmt.Sprintf("x%02d", r.IntN(25)), uint32(r.IntN(12)), g.State(1+r.IntN(4)))
				u.Region = env.Region(fmt.Sprint(r.IntN(3)))
				us[i] = u
				o, ok := want[u.ID]
				if !ok || u.Incarnation > o.Incarnation || (u.Incarnation == o.Incarnation && (u.State > o.State || u.State == o.State && u.Region > o.Region)) {
					want[u.ID] = u
				}
			}
			for range 20 {
				r.Shuffle(len(us), func(i, j int) { us[i], us[j] = us[j], us[i] })
				v := g.NewView()
				left, right := g.NewView(), g.NewView()
				for i, u := range us {
					v.Apply(u)
					v.Apply(u)
					if i%2 == 0 {
						left.Apply(u)
					} else {
						right.Apply(u)
					}
				}
				for _, u := range right.All() {
					left.Apply(u)
				}
				check(reflect.DeepEqual(v.All(), left.All()), "seed=%d partial merge mismatch", seed)
				for _, u := range v.All() {
					check(u == want[u.ID], "seed=%d maximum mismatch", seed)
				}
			}
			return "500 updates, 20 permutations, duplicates and partial merges matched independent maxima"
		})
	}
	attack("C4", "monotonic-and-copy-isolation", func() string {
		_, n := one(g.Config{})
		r := rand.New(rand.NewPCG(904, 1))
		prev := map[env.NodeID]uint32{}
		for range 3000 {
			inject(n, member(fmt.Sprintf("x%d", r.IntN(20)), uint32(r.IntN(300)), g.State(r.IntN(4)+1)))
			all := n.All()
			check(slices.IsSortedFunc(all, func(a, b g.Member) int { return strings.Compare(string(a.ID), string(b.ID)) }), "seed=904 unsorted")
			for _, m := range all {
				check(m.Incarnation >= prev[m.ID], "seed=904 decreased")
				prev[m.ID] = m.Incarnation
			}
			all[0].ID = "tamper"
		}
		check(n.Self().State == g.StateAlive, "self not alive")
		return "3000 hostile updates preserved per-ID incarnation order, sorting and snapshot isolation"
	})
	attack("C4", "retention-monotonicity", func() string {
		c, n := one(g.Config{DeadRetention: time.Second})
		inject(n, member("x", 9, g.StateDead))
		c.run(2 * time.Second)
		inject(n, member("x", 0, g.StateAlive))
		m, e := n.Lookup("x")
		must(e)
		check(m.Incarnation >= 9, "seed=901 dead(x,9), wait 2s with retention=1s, replay alive(x,0): incarnation became %d", m.Incarnation)
		return "incarnation stayed monotonic across purge"
	})
	for _, limit := range []int{g.MinPacket, 1400} {
		attack("C5", fmt.Sprintf("queue-budget-%d", limit), func() string {
			c, n := one(g.Config{MaxPacket: limit})
			for i := 0; i < 50; i++ {
				inject(n, member(fmt.Sprintf("x%03d", i), 0, g.StateAlive))
			}
			t := c.taps[c.ids[0]]
			t.packets = nil
			for i := 0; i < 120; i++ {
				n.OnDatagram("sink", wire(g.Message{Kind: g.KindPing, Seq: uint32(i)}))
			}
			counts := map[env.NodeID]int{}
			for _, p := range t.packets {
				check(p.size <= limit, "seed=901 size=%d budget=%d", p.size, limit)
				for _, u := range p.m.Updates {
					counts[u.ID]++
				}
			}
			expected := g.Config{}.WithDefaults().RetransmitLimit(51)
			for _, m := range n.All() {
				check(counts[m.ID] == expected, "seed=901 id=%s count=%d want=%d", m.ID, counts[m.ID], expected)
			}
			check(t.packets[0].m.Updates[0].ID == "x049", "freshest not first")
			return fmt.Sprintf("51 queued entries each transmitted %d times within budget, freshest first", expected)
		})
	}
	attack("C5", "replacement-and-destination-lead", func() string {
		c, n := one(g.Config{})
		inject(n, member("x", 3, g.StateAlive))
		inject(n, member("x", 4, g.StateDead))
		for range 50 {
			n.OnDatagram("sink", wire(g.Message{Kind: g.KindPing}))
		}
		t := c.taps[c.ids[0]]
		t.packets = nil
		inject(n, member("x", 3, g.StateAlive))
		n.OnDatagram("x", wire(g.Message{Kind: g.KindPing}))
		check(len(t.packets) == 1 && len(t.packets[0].m.Updates) == 1 && t.packets[0].m.Updates[0].State == g.StateDead, "seed=901 stale replay or destination lead wrong: %v", t.packets)
		return "stale replay not enqueued; spent death update still led reply to its subject"
	})
	attack("C6", "rotation-and-lonely", func() string {
		c, n := one(g.Config{SyncInterval: time.Hour})
		c.run(3 * time.Second)
		check(n.Stats().Probes == 0, "lonely probe")
		for i := 0; i < 5; i++ {
			inject(n, member(fmt.Sprintf("x%d", i), 0, g.StateAlive))
		}
		inject(n, member("dead", 0, g.StateDead))
		c.run(5 * time.Second)
		seen := map[env.NodeID]bool{}
		for _, p := range c.taps[c.ids[0]].packets {
			if p.m.Kind == g.KindPing {
				check(p.to != "dead", "seed=901 dead probed")
				seen[p.to] = true
			}
		}
		check(len(seen) == 5, "seed=901 first round has %d distinct targets", len(seen))
		return "lonely node silent; five new live members probed once each, tombstone skipped"
	})
	attack("C7", "suspicion-deadline-and-refutation", func() string {
		c, n := one(g.Config{})
		inject(n, member("x", 0, g.StateSuspect))
		c.run(3900 * time.Millisecond)
		m, e := n.Lookup("x")
		must(e)
		check(m.State == g.StateSuspect, "early death")
		c.run(200 * time.Millisecond)
		m, e = n.Lookup("x")
		must(e)
		check(m.State == g.StateDead, "late death")
		inject(n, member("y", 0, g.StateSuspect))
		inject(n, member("y", 1, g.StateAlive))
		c.run(4100 * time.Millisecond)
		m, e = n.Lookup("y")
		must(e)
		check(m.State != g.StateDead, "old timer killed refuted entry")
		return "4s two-member suspicion deadline respected; refutation cancelled original timer"
	})
	attack("C7", "log-parameter-boundaries", func() string {
		cfg := g.Config{}.WithDefaults()
		for _, n := range []int{1, 2, 3, 4, 5, 31, 32, 33, 199, 200, 65535} {
			k := 1
			if n > 2 {
				k = bits.Len(uint(n - 1))
			}
			check(cfg.SuspicionTimeout(n) == time.Duration(4*k)*time.Second && cfg.RetransmitLimit(n) == 4*k, "n=%d wrong logarithm", n)
		}
		return "timeout and retransmit formulas matched independent ceil-log2 at 11 boundaries"
	})
	for _, empty := range []bool{false, true} {
		attack("C8", fmt.Sprintf("join-unreachable-empty-%v", empty), func() string {
			c, n := one(g.Config{})
			seeds := []env.NodeID{"missing", "also-missing", "missing"}
			if empty {
				seeds = nil
			}
			calls := 0
			var result error
			must(n.Join(seeds, func(e error) { calls++; result = e }))
			c.run(3 * time.Second)
			check(calls == 1, "seed=901 callback count=%d", calls)
			if empty {
				check(result == nil, "bootstrap rejected")
			} else {
				check(errors.Is(result, g.ErrJoinFailed) && errors.Is(result, env.ErrUnreachable), "seed=901 missing causes %v", result)
			}
			return "callback fired once with documented bootstrap success or aggregate failure causes"
		})
	}
	attack("C9", "sync-merge-and-kind-rejection", func() string {
		_, n := one(g.Config{})
		for _, kind := range []g.Kind{g.KindSyncReq, g.KindPing, g.KindSyncResp} {
			var result error
			var resp []byte
			n.OnRequest("x", wire(g.Message{Kind: kind, Updates: []g.Member{member("x", 5, g.StateAlive)}}), func(b []byte, e error) { resp = b; result = e })
			if kind == g.KindSyncReq {
				must(result)
				m, e := g.Decode(resp)
				must(e)
				check(m.Kind == g.KindSyncResp && len(m.Updates) == 2, "sync reply not merged")
			} else {
				check(errors.Is(result, g.ErrMalformed), "invalid kind accepted")
			}
		}
		return "sync reply included merged input; datagram and response kinds rejected as requests"
	})
	attack("C9", "purge-boundary", func() string {
		c, n := one(g.Config{DeadRetention: 2 * time.Second})
		inject(n, member("dead", 3, g.StateDead), member("left", 4, g.StateLeft))
		c.run(time.Second)
		_, e := n.Lookup("dead")
		must(e)
		c.run(3 * time.Second)
		for _, id := range []env.NodeID{"dead", "left"} {
			_, e := n.Lookup(id)
			check(errors.Is(e, g.ErrNotMember), "seed=901 retained %s after expiry", id)
		}
		return "dead and left retained before deadline and removed by next protocol tick"
	})
	attack("C10", "closed-operations-and-silence", func() string {
		c, n := one(g.Config{})
		must(n.Leave())
		sent := n.Stats().Sent
		check(errors.Is(n.Leave(), g.ErrClosed) && errors.Is(n.Join(nil, nil), g.ErrClosed), "closed operations accepted")
		inject(n, member("x", 3, g.StateAlive))
		var reply error
		n.OnRequest("x", nil, func(_ []byte, e error) { reply = e })
		check(errors.Is(reply, g.ErrClosed) && n.Stats().DroppedClosed == 1, "closed incoming path")
		c.run(50 * time.Second)
		check(n.Stats().Sent == sent && n.Self().State == g.StateLeft, "closed timers sent")
		return "leave closed APIs, rejected request, counted drop and stopped outgoing traffic for 50s"
	})
	attack("C10", "closed-wire-error", func() string {
		c := clusterNew(905, 2, g.Config{}, sim.NetConfig{})
		must(c.nodes[c.ids[0]].Leave())
		var result error
		must(c.nodes[c.ids[1]].Join(c.ids[:1], func(e error) { result = e }))
		c.run(3 * time.Second)
		check(errors.Is(result, g.ErrClosed) && errors.Is(result, env.ErrRemote) && errors.Is(result, g.ErrJoinFailed), "seed=905 remote error=%v", result)
		return "remote closure preserved closed, remote and aggregate join sentinels"
	})
	attack("C11", "config-rejections", func() string {
		c, n := one(g.Config{})
		_ = n
		_ = c
		for _, cfg := range []g.Config{{MaxPacket: g.MinPacket - 1}, {ProbeInterval: -1}, {ProbeTimeout: time.Second}, {IndirectProbes: 65}, {SuspicionMult: -1}, {SyncTimeout: -1}, {DeadRetention: -1}, {Seeds: []env.NodeID{""}}} {
			cfg = cfg.WithDefaults()
			check(errors.Is(cfg.Validate(), env.ErrInvalidArgument), "invalid config accepted %+v", cfg)
		}
		return "eight invalid configurations correctly rejected (rejections count as HELD)"
	})
	attack("C11", "send-errors-and-own-id", func() string {
		c, n := one(g.Config{})
		t := c.taps[c.ids[0]]
		t.err = env.ErrTooLarge
		n.OnDatagram("sink", wire(g.Message{Kind: g.KindPing}))
		check(n.Stats().SendErrors == 1 && errors.Is(n.LastError(), env.ErrTooLarge), "transport error lost")
		before := n.Stats()
		n.OnDatagram(n.Self().ID, []byte{255})
		check(n.Stats() == before, "own datagram processed")
		_, e := n.Lookup("absent")
		check(errors.Is(e, g.ErrNotMember), "lookup sentinel")
		table, e := g.CodeTable()
		must(e)
		for _, entry := range g.Codes() {
			code, msg := table.Encode(entry.Err)
			check(errors.Is(table.Decode(code, msg), entry.Err), "code roundtrip %d", code)
		}
		return "transport oversize retained; own sender ignored; lookup and code-table sentinels preserved"
	})
	for _, delta := range []int{-1, 0, 1} {
		attack("C11", fmt.Sprintf("packet-limit-%+d", delta), func() string {
			_, n := one(g.Config{MaxPacket: g.MinPacket})
			size := g.MinPacket + delta
			us := []g.Member{member(strings.Repeat("a", 255), 0, g.StateAlive), member(strings.Repeat("b", size-8-517-7-2), 0, g.StateAlive)}
			us[0].Region = env.Region(strings.Repeat("r", 255))
			b := wire(g.Message{Kind: g.KindGossip, Updates: us})
			check(len(b) == size, "fixture size=%d want=%d", len(b), size)
			n.OnDatagram("sender", b)
			if delta <= 0 {
				check(n.Stats().Malformed == 0 && len(n.Members()) == 3, "valid boundary rejected")
			} else {
				check(n.Stats().Malformed == 1 && len(n.Members()) == 1, "oversize accepted")
			}
			return fmt.Sprintf("valid %d-byte packet %s at %d-byte budget", size, map[bool]string{true: "rejected", false: "accepted"}[delta > 0], g.MinPacket)
		})
	}
	attack("C12", "wire-all-kinds-max-fields", func() string {
		for k := g.KindPing; k <= g.KindSyncResp; k++ {
			m := g.Message{Kind: k, Seq: math.MaxUint32, Updates: []g.Member{member(strings.Repeat("x", 255), math.MaxUint32, g.StateLeft)}}
			m.Updates[0].Region = env.Region(strings.Repeat("r", 255))
			if k == g.KindPingReq {
				m.Target = env.NodeID(strings.Repeat("t", 255))
			}
			b := wire(m)
			out, e := g.Decode(b)
			must(e)
			check(reflect.DeepEqual(m, out) && len(b) == m.Size(), "kind %d mismatch", k)
			for i := 0; i < len(b); i++ {
				_, e = g.Decode(b[:i])
				check(errors.Is(e, g.ErrMalformed), "truncation %d/%d accepted", i, len(b))
			}
		}
		return "all six kinds round-tripped maximal fields and rejected every truncated prefix"
	})
	attack("C12", "invalid-encoders", func() string {
		for _, m := range []g.Message{{Kind: 99}, {Kind: g.KindPingReq}, {Kind: g.KindPing, Target: "x"}, {Kind: g.KindGossip, Updates: []g.Member{member("", 0, g.StateAlive)}}, {Kind: g.KindGossip, Updates: []g.Member{member("x", 0, 0)}}, {Kind: g.KindGossip, Updates: []g.Member{member(strings.Repeat("x", 256), 0, g.StateAlive)}}} {
			_, e := m.Encode()
			check(errors.Is(e, env.ErrInvalidArgument), "invalid encoding accepted")
		}
		return "six malformed outbound structures correctly rejected"
	})
	attack("C13", "mutational-wire-invariants", func() string {
		_, n := one(g.Config{})
		r := rand.New(rand.NewPCG(906, 3))
		base := wire(g.Message{Kind: g.KindGossip, Updates: []g.Member{member("x", 1, g.StateAlive)}})
		for i := 0; i < 20000; i++ {
			b := bytes.Clone(base)
			if i%2 == 0 {
				b = make([]byte, r.IntN(100))
				for j := range b {
					b[j] = byte(r.Uint32())
				}
			} else {
				b[r.IntN(len(b))] = byte(r.Uint32())
			}
			m, e := g.Decode(b)
			if e == nil {
				check(bytes.Equal(wire(m), b), "seed=906 accepted noncanonical %x", b)
			}
			n.OnDatagram("fuzzer", b)
			n.OnRequest("fuzzer", b, func(_ []byte, _ error) {})
			check(n.Self().State == g.StateAlive, "seed=906 self killed at case %d", i)
		}
		return "20000 seeded random/mutated inputs: no panic, accepted bytes canonical, self alive"
	})
	attack("C14", "snapshot-repeat-and-replay", func() string {
		makeRun := func() *cluster {
			c := clusterNew(907, 8, g.Config{Seeds: []env.NodeID{"n000"}}, sim.NetConfig{DropPPM: 75000, DuplicatePPM: 250000})
			c.run(30 * time.Second)
			return c
		}
		a, b := makeRun(), makeRun()
		check(a.s.Digests() == b.s.Digests(), "seed=907 simulator digests differ")
		for _, id := range a.ids {
			check(bytes.Equal(a.nodes[id].EncodeState(), b.nodes[id].EncodeState()), "seed=907 state differs")
			check(bytes.Equal(a.nodes[id].EncodeState(), a.nodes[id].EncodeState()), "snapshot unstable")
		}
		return "two duplicate/loss runs matched both digests and repeated node encodings"
	})
	for _, st := range []g.State{g.StateDead, g.StateLeft} {
		attack("C23", fmt.Sprintf("stale-replay-retained-%s", st), func() string {
			_, n := one(g.Config{})
			inject(n, member("x", 7, st))
			for _, i := range []uint32{0, 6, 7} {
				inject(n, member("x", i, g.StateAlive), member("x", i, g.StateSuspect))
			}
			m, e := n.Lookup("x")
			must(e)
			check(m.State == st && m.Incarnation == 7, "seed=901 resurrected retained entry")
			inject(n, member("x", 8, g.StateAlive))
			m, e = n.Lookup("x")
			must(e)
			check(m.State == g.StateAlive && m.Incarnation == 8, "higher alive not accepted")
			return "lower/equal alive and suspect replays rejected; higher alive accepted"
		})
	}
	attack("C23", "stale-replay-after-purge", func() string {
		c, n := one(g.Config{DeadRetention: time.Second})
		inject(n, member("x", 7, g.StateAlive))
		inject(n, member("x", 7, g.StateDead))
		c.run(2 * time.Second)
		inject(n, member("x", 0, g.StateAlive))
		m, e := n.Lookup("x")
		must(e)
		check(m.State != g.StateAlive || m.Incarnation > 7, "seed=901 alive(x,7), dead(x,7), wait 2s at retention=1s, replay alive(x,0): dead verdict overturned at incarnation 0")
		return "dead verdict survives stale replay after purge"
	})
}
func scenarios() {
	for _, loss := range []uint32{50000, 100000} {
		attack("C18", fmt.Sprintf("healthy-and-crash-loss-%d", loss), func() string {
			maxDetect := time.Duration(0)
			for seed := int64(1101); seed <= 1112; seed++ {
				c := clusterNew(seed, 20, g.Config{Seeds: []env.NodeID{"n000", "n001", "n002"}}, sim.NetConfig{DropPPM: loss})
				c.run(10 * time.Second)
				victim := c.ids[19]
				for _, id := range c.ids {
					_, err := c.nodes[id].Lookup(victim)
					must(err)
				}
				must(c.s.Crash(victim))
				delay := c.until(40*time.Second, victim)
				check(delay >= 0, "seed=%d loss=%d crash not converged in 40s", seed, loss)
				maxDetect = max(maxDetect, delay)
				for _, id := range c.ids[:19] {
					m, err := c.nodes[id].Lookup(victim)
					must(err)
					check(m.State == g.StateDead, "seed=%d observer=%s victim not dead: %v", seed, id, m)
				}
				for _, ev := range c.events {
					check(ev.Kind != g.EventDied || ev.Member.ID == victim, "seed=%d loss=%d healthy eviction=%s at %v", seed, loss, ev.Member.ID, ev.At)
				}
			}
			return fmt.Sprintf("12 seeds 1101..1112, N=20: zero healthy deaths; all crash verdicts within %s", maxDetect)
		})
	}
	for _, seed := range []int64{1201, 1202, 1203} {
		attack("C16", fmt.Sprintf("one-way-long-%d", seed), func() string {
			c := clusterNew(seed, 12, g.Config{Seeds: []env.NodeID{"n000"}}, sim.NetConfig{})
			c.run(10 * time.Second)
			c.events = nil
			must(c.s.PartitionOneWay(c.ids[3:4], c.ids[7:8]))
			c.run(300 * time.Second)
			for _, ev := range c.events {
				check(ev.Kind != g.EventDied && ev.Kind != g.EventSuspected, "seed=%d one-way n003->n007 caused %s %s", seed, ev.Kind, ev.Member.ID)
			}
			check(c.agree(""), "seed=%d views disagree", seed)
			return "12 nodes, one-way n003->n007 blocked for 300s: no suspicions/deaths and identical views"
		})
	}
	for _, duplicate := range []uint32{0, 500000} {
		attack("C15", fmt.Sprintf("loss-duplicates-heal-%d", duplicate), func() string {
			worst := time.Duration(0)
			for seed := int64(1301); seed <= 1308; seed++ {
				c := clusterNew(seed, 50, g.Config{Seeds: []env.NodeID{"n000", "n001", "n002"}}, sim.NetConfig{DuplicatePPM: duplicate})
				for _, a := range c.ids {
					for _, b := range c.ids {
						if a != b {
							must(c.s.SetLinkLoss(a, b, 100000))
						}
					}
				}
				c.run(25 * time.Second)
				must(c.s.Crash(c.ids[49]))
				c.run(5 * time.Second)
				c.s.HealNet()
				d := c.until(48*time.Second, c.ids[49])
				check(d >= 0, "seed=%d N=50 did not converge in 48s after heal", seed)
				worst = max(worst, d)
			}
			return fmt.Sprintf("8 seeds 1301..1308, N=50, 10%% link loss and late crash: worst post-heal %s <= 48s", worst)
		})
	}
	for _, retain := range []bool{true, false} {
		attack("C19", fmt.Sprintf("partition-heal-retained-%v", retain), func() string {
			for seed := int64(1401); seed <= 1404; seed++ {
				cfg := g.Config{}
				if !retain {
					cfg.DeadRetention = 5 * time.Second
				}
				c := clusterNew(seed, 4, cfg, sim.NetConfig{})
				c.bootstrap()
				check(c.agree(""), "seed=%d bootstrap failed", seed)
				must(c.s.Partition(c.ids[:2], c.ids[2:]))
				c.run(40 * time.Second)
				for _, id := range c.ids {
					check(c.nodes[id].LiveCount() == 2, "seed=%d partition not detected", seed)
				}
				c.s.HealNet()
				d := c.until(120*time.Second, "")
				check(d >= 0, "seed=%d N=4 explicit Join(n000), split {n000,n001}|{n002,n003} for 40s, retention=%s, heal: still two disjoint views after 120s", seed, cfg.WithDefaults().DeadRetention)
			}
			return "4 seeds 1401..1404: both halves detected death and re-merged within 120s"
		})
	}
	attack("C19", "default-retention-long-partition", func() string {
		c := clusterNew(1405, 2, g.Config{}, sim.NetConfig{})
		c.bootstrap()
		must(c.s.Partition(c.ids[:1], c.ids[1:]))
		c.run(310 * time.Second)
		c.s.HealNet()
		d := c.until(120*time.Second, "")
		check(d >= 0, "seed=1405 N=2 default config, explicit Join(n000), partition n000|n001 for 310s then heal: permanently disconnected address sets, still split after 120s")
		return "default-retention partition healed"
	})
	attack("C19", "long-partition-with-seed-control", func() string {
		c := clusterNew(1405, 2, g.Config{Seeds: []env.NodeID{"n000"}}, sim.NetConfig{})
		c.run(5 * time.Second)
		must(c.s.Partition(c.ids[:1], c.ids[1:]))
		c.run(310 * time.Second)
		c.s.HealNet()
		d := c.until(60*time.Second, "")
		check(d >= 0, "seed=1405 configured seed failed to heal")
		return fmt.Sprintf("same 310s partition with persistent seed n000 healed in %s", d)
	})
	for _, pause := range []time.Duration{2 * time.Second, 8 * time.Second} {
		attack("C20", fmt.Sprintf("pause-refute-%s", pause), func() string {
			for seed := int64(1501); seed <= 1508; seed++ {
				c := clusterNew(seed, 10, g.Config{Seeds: []env.NodeID{"n000"}}, sim.NetConfig{})
				c.run(10 * time.Second)
				id := c.ids[9]
				must(c.s.Pause(id))
				c.run(pause)
				must(c.s.Resume(id))
				c.run(30 * time.Second)
				for _, ev := range c.events {
					check(ev.Kind != g.EventDied, "seed=%d pause=%s death=%s", seed, pause, ev.Member.ID)
				}
				check(c.agree(""), "seed=%d refutation did not converge", seed)
			}
			return "8 seeds 1501..1508: resumed healthy node avoided death and views converged"
		})
	}
	for _, wait := range []time.Duration{time.Second, 25 * time.Second, 330 * time.Second} {
		attack("C21", fmt.Sprintf("wipe-same-id-after-%s", wait), func() string {
			for seed := int64(1601); seed <= 1606; seed++ {
				c := clusterNew(seed, 10, g.Config{Seeds: []env.NodeID{"n000", "n001"}}, sim.NetConfig{})
				c.run(10 * time.Second)
				id := c.ids[9]
				u := c.nodes[id].Self()
				u.Incarnation = 17
				u.State = g.StateSuspect
				inject(c.nodes[id], u)
				c.run(10 * time.Second)
				must(c.s.Crash(id))
				c.run(wait)
				old, e := c.nodes[c.ids[0]].Lookup(id)
				retained := e == nil
				must(c.s.WipeRejoin(id))
				d := c.until(30*time.Second, "")
				check(d >= 0, "seed=%d wait=%s wipe-rejoin not converged in 30s", seed, wait)
				if retained {
					now, e := c.nodes[c.ids[0]].Lookup(id)
					must(e)
					check(now.Incarnation >= old.Incarnation && (old.State == g.StateAlive || now.Incarnation > old.Incarnation), "seed=%d restart old=%v now=%v", seed, old, now)
				}
			}
			return "6 seeds 1601..1606: wipe/rejoin converged within 30s with retained or purged history"
		})
	}
	for _, cycles := range []int{4, 12} {
		attack("C21", fmt.Sprintf("flapping-%d-cycles", cycles), func() string {
			c := clusterNew(1701, 12, g.Config{Seeds: []env.NodeID{"n000", "n001"}}, sim.NetConfig{DropPPM: 100000})
			c.run(10 * time.Second)
			id := c.ids[11]
			for range cycles {
				must(c.s.Crash(id))
				c.run(3 * time.Second)
				must(c.s.WipeRejoin(id))
				c.run(2 * time.Second)
			}
			c.s.HealNet()
			d := c.until(60*time.Second, "")
			check(d >= 0, "seed=1701 cycles=%d flapper never converged", cycles)
			for _, ev := range c.events {
				check(ev.Kind != g.EventDied || ev.Member.ID == id, "seed=1701 flapper caused healthy eviction %s", ev.Member.ID)
			}
			return fmt.Sprintf("seed=1701, 3s down/2s up, 10%% loss: %d flaps healed in %s without other deaths", cycles, d)
		})
	}
	for _, kind := range []string{"unknown", "crashed"} {
		attack("C22", "no-seed-"+kind, func() string {
			c := clusterNew(1801, 2, g.Config{}, sim.NetConfig{})
			target := env.NodeID("unknown")
			if kind == "crashed" {
				target = c.ids[1]
				must(c.s.Crash(target))
			}
			count := 0
			var result error
			must(c.nodes[c.ids[0]].Join([]env.NodeID{target}, func(e error) { count++; result = e }))
			c.run(5 * time.Second)
			check(count == 1 && errors.Is(result, g.ErrJoinFailed) && errors.Is(result, env.ErrUnreachable), "seed=1801 %s count=%d error=%v", kind, count, result)
			return "unreachable seed produced exactly one aggregate failure with transport cause"
		})
	}
}
func scaling() {
	ns := []int{20, 50, 100, 200}
	var xs, ys []float64
	for _, n := range ns {
		attack("C15", fmt.Sprintf("controlled-scaling-N%d", n), func() string {
			var samples []float64
			maxCost := 0.0
			for seed := int64(1901); seed <= 1908; seed++ {
				c := clusterNew(seed, n, g.Config{Seeds: []env.NodeID{"n000", "n001", "n002"}}, sim.NetConfig{})
				c.run(20 * time.Second)
				check(c.agree(""), "seed=%d N=%d setup not converged", seed, n)
				for _, a := range c.ids {
					for _, b := range c.ids {
						if a != b {
							must(c.s.SetLinkLoss(a, b, 75000))
						}
					}
				}
				c.run(10 * time.Second)
				must(c.s.Crash(c.ids[n-1]))
				c.s.HealNet()
				before := uint64(0)
				for _, id := range c.ids[:n-1] {
					before += c.nodes[id].Stats().Sent
				}
				bound := time.Duration(8*bits.Len(uint(n-1))) * time.Second
				d := c.until(bound, c.ids[n-1])
				check(d >= 0, "seed=%d N=%d simultaneous crash/heal exceeded %s", seed, n, bound)
				samples = append(samples, d.Seconds())
				c.run(30 * time.Second)
				after := uint64(0)
				for _, id := range c.ids[:n-1] {
					after += c.nodes[id].Stats().Sent
				}
				cost := float64(after-before) / float64(n-1) / (d.Seconds() + 30)
				maxCost = max(maxCost, cost)
				check(cost <= 5, "seed=%d N=%d sends/node/period=%f exceeds 5", seed, n, cost)
			}
			slices.Sort(samples)
			worst := samples[len(samples)-1]
			xs = append(xs, float64(n))
			ys = append(ys, worst)
			return fmt.Sprintf("8 seeds 1901..1908, crash exactly at heal: min=%.2fs median=%.2fs max=%.2fs max/log2ceil=%.3f max_cost=%.3f", samples[0], samples[4], worst, worst/float64(bits.Len(uint(n-1))), maxCost)
		})
	}
	attack("C15", "log-versus-linear-fit", func() string {
		check(len(xs) == 4, "scaling sample incomplete")
		fit := func(log bool) (float64, float64, float64) {
			x := slices.Clone(xs)
			if log {
				for i := range x {
					x[i] = math.Log2(x[i])
				}
			}
			mx, my := 0.0, 0.0
			for i := range x {
				mx += x[i] / 4
				my += ys[i] / 4
			}
			num, den := 0.0, 0.0
			for i := range x {
				num += (x[i] - mx) * (ys[i] - my)
				den += (x[i] - mx) * (x[i] - mx)
			}
			slope := num / den
			intercept := my - slope*mx
			sse := 0.0
			for i := range x {
				d := ys[i] - (intercept + slope*x[i])
				sse += d * d
			}
			return intercept, slope, sse
		}
		a, b, logerr := fit(true)
		_, _, linearerr := fit(false)
		check(logerr <= linearerr, "seeds=1901..1908 N=20,50,100,200 worst times=%v log SSE=%.3f > linear SSE=%.3f", ys, logerr, linearerr)
		return fmt.Sprintf("four observed maxima fit %.3f + %.3f*log2(N), SSE log=%.3f linear=%.3f; descriptive finite-range check only", a, b, logerr, linearerr)
	})
}
func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "group required: core, scenarios, scaling")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "core":
		core()
		extra()
	case "scenarios":
		scenarios()
	case "scaling":
		scaling()
	default:
		os.Exit(2)
	}
	if broken > 0 {
		os.Exit(1)
	}
}
