package main

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

type proc struct {
	e      env.Env
	got    []byte
	dg     func(env.NodeID, []byte)
	rq     func(env.NodeID, []byte, env.Reply)
	start  func()
	encode func() []byte
}

func (p *proc) Start() {
	if p.start != nil {
		p.start()
	}
}
func (p *proc) OnDatagram(n env.NodeID, b []byte) {
	p.got = append(p.got, b...)
	if p.dg != nil {
		p.dg(n, b)
	}
}
func (p *proc) OnRequest(n env.NodeID, b []byte, r env.Reply) {
	p.got = append(p.got, b...)
	if p.rq != nil {
		p.rq(n, b, r)
	} else {
		r(b, nil)
	}
}
func (p *proc) EncodeState() []byte {
	if p.encode != nil {
		return p.encode()
	}
	return p.got
}
func must(e error) {
	if e != nil {
		panic(e)
	}
}
func check(ok bool, f string, a ...any) {
	if !ok {
		panic(fmt.Sprintf(f, a...))
	}
}

var broken int

func attack(c, n string, f func() string) {
	status, detail := "HELD", ""
	func() {
		defer func() {
			if v := recover(); v != nil {
				status = "BROKEN"
				detail = fmt.Sprint(v)
				broken++
			}
		}()
		detail = f()
	}()
	fmt.Printf("%s %s %s: %s\n", status, c, n, strings.ReplaceAll(detail, "\n", " "))
}
func netcfg() sim.NetConfig { return sim.NetConfig{MaxDatagram: 64, MaxMessage: 64} }
func cluster(seed int64, n int, c sim.NetConfig) (*sim.Sim, []*proc) {
	s := sim.New(sim.Config{Seed: seed, Net: c, MaxSteps: 100000, LogCap: 1000})
	p := make([]*proc, n)
	for i := range n {
		i := i
		must(s.AddNode(sim.NodeSpec{ID: env.NodeID(fmt.Sprint(i)), Region: env.Region(fmt.Sprint(i % 2))}, func(e env.Env) env.Process { p[i] = &proc{e: e}; return p[i] }))
	}
	must(s.RunUntil(0))
	return s, p
}
func replay(seed int64, n int) *sim.Sim {
	c := netcfg()
	c.Latency = sim.Range{Max: 17}
	c.InterRegionLatency = c.Latency
	c.DropPPM = 130000
	c.DuplicatePPM = 210000
	s, p := cluster(seed, n, c)
	for i := range p {
		i := i
		p[i].dg = func(_ env.NodeID, b []byte) {
			if len(b) > 0 && b[0]%7 == 0 {
				must(s.Pause(p[i].e.ID))
				s.At(s.Now()+3, "resume-handler", func() { _ = s.Resume(p[i].e.ID) })
			}
		}
	}
	for k := range 150 {
		k := k
		s.At(env.Time(k), "work", func() {
			i := k % n
			j := (i + 1) % n
			e := p[i].e
			_ = e.Disk.Append("tail", []byte{byte(e.Rand.Uint32())})
			if k%3 == 0 {
				_ = e.Disk.Sync("tail")
			}
			e.Clock.AfterFunc(0, func() { _ = e.Net.Send(p[j].e.ID, []byte{byte(k)}) })
			e.Net.Request(p[j].e.ID, []byte{byte(k)}, 100, func(b []byte, err error) {
				if err == nil {
					_ = e.Disk.Write("reply", b)
				}
			})
			if k%19 == 0 {
				_ = s.CrashTorn(p[j].e.ID)
				_ = s.Restart(p[j].e.ID)
			}
			if k%29 == 0 {
				_ = s.WipeRejoin(p[i].e.ID)
			}
		})
	}
	once := false
	s.AddInvariant("fault-from-invariant", 1, func() error {
		if !once && s.Now() > 30 {
			once = true
			_ = s.Clog("0", env.NodeID(fmt.Sprint((1)%n)))
		}
		return nil
	})
	s.At(300, "heal", s.HealAll)
	_ = s.RunUntilQuiet()
	return s
}
func c1() {
	attack("C1", "custom-replay", func() string {
		for _, n := range []int{1, 2, 3, 7, 31, 100} {
			for seed := int64(1); seed <= 20; seed++ {
				a, b := replay(seed, n), replay(seed, n)
				must(a.Verify())
				must(b.Verify())
				check(a.Digests() == b.Digests(), "seed=%d nodes=%d digests differ", seed, n)
				check(reflect.DeepEqual(a.LogTail(1000), b.LogTail(1000)), "seed=%d nodes=%d log differs", seed, n)
			}
		}
		return "120 custom callback/timer/invariant/restart/wipe schedules replayed twice with equal logs and digests"
	})
	attack("C1", "separate-process-runtime", func() string {
		var base []byte
		for _, settings := range [][]string{{"GOMAXPROCS=1", "GOGC=1"}, {"GOMAXPROCS=4", "GOGC=off"}, {"GOMAXPROCS=2", "GOGC=20"}} {
			cmd := exec.Command(os.Args[0], "fingerprints")
			cmd.Env = append(os.Environ(), settings...)
			out, err := cmd.CombinedOutput()
			must(err)
			if base == nil {
				base = out
			} else {
				check(bytes.Equal(base, out), "seed=1..20 custom fingerprints differ across runtime settings")
			}
		}
		return "120 custom fingerprints equal across three fresh processes with varied GOMAXPROCS and GOGC"
	})
	attack("C1", "equal-time-timer-storm", func() string {
		run := func() sim.Digests {
			s, p := cluster(42, 3, netcfg())
			for i := range 10000 {
				i := i
				p[i%3].e.Clock.AfterFunc(0, func() { p[i%3].got = append(p[i%3].got, byte(i)) })
			}
			must(s.RunUntilQuiet())
			return s.Digests()
		}
		a := run()
		for range 5 {
			check(run() == a, "seed=42 timer storm differs")
		}
		return "six runs of 10000 timers at one instant gave identical digests"
	})
}
func c2() {
	for _, direction := range []string{"request", "response"} {
		direction := direction
		attack("C2", "stream-loss-rate-"+direction, func() string {
			delivered, success, timeout := 0, 0, 0
			for seed := int64(1); seed <= 40; seed++ {
				s, p := cluster(seed, 2, netcfg())
				from, to := env.NodeID("0"), env.NodeID("1")
				if direction == "response" {
					from, to = to, from
				}
				must(s.SetLinkLoss(from, to, 300000))
				for range 1000 {
					p[0].e.Net.Request("1", []byte{1}, 100, func(_ []byte, e error) {
						if e == nil {
							success++
						} else if errors.Is(e, env.ErrTimeout) {
							timeout++
						} else {
							panic(e)
						}
					})
				}
				must(s.RunUntilQuiet())
				delivered += len(p[1].got)
			}
			check(abs(success-28000) <= 400 && success+timeout == 40000, "seed=1..40 %s success=%d timeout=%d", direction, success, timeout)
			want := success
			if direction == "response" {
				want = 40000
			}
			check(delivered == want, "seed=1..40 %s server received=%d expected=%d", direction, delivered, want)
			return fmt.Sprintf("40 seeds x 1000 requests success=%d timeout=%d server deliveries=%d within 400 of expected 28000 successes", success, timeout, delivered)
		})
	}

	for _, mode := range []string{"drop", "duplicate", "link-loss", "off"} {
		mode := mode
		attack("C2", "rate-"+mode, func() string {
			total := 0
			const count = 40000
			for seed := int64(1); seed <= 40; seed++ {
				c := netcfg()
				if mode == "drop" {
					c.DropPPM = 300000
				}
				if mode == "duplicate" {
					c.DuplicatePPM = 300000
				}
				s, p := cluster(seed, 2, c)
				if mode == "link-loss" {
					must(s.SetLinkLoss("0", "1", 300000))
				}
				for range 1000 {
					must(p[0].e.Net.Send("1", []byte{1}))
				}
				must(s.RunUntilQuiet())
				total += len(p[1].got)
			}
			want := count
			if mode == "duplicate" {
				want = 52000
			}
			if mode == "drop" || mode == "link-loss" {
				want = 28000
			}
			tol := 400
			if mode == "off" {
				tol = 0
			}
			check(abs(total-want) <= tol, "seed=1..40 %s delivered=%d expected=%d tolerance=%d", mode, total, want, tol)
			return fmt.Sprintf("40 seeds x 1000 sends delivered=%d expected=%d tolerance=%d deliveries", total, want, tol)
		})
	}
	attack("C2", "delay-reorder", func() string {
		c := netcfg()
		c.Latency = sim.Range{Min: 10, Max: 100}
		c.InterRegionLatency = c.Latency
		s, p := cluster(17, 2, c)
		times := []env.Time{}
		p[1].dg = func(_ env.NodeID, _ []byte) { times = append(times, s.Now()) }
		for i := range 200 {
			must(p[0].e.Net.Send("1", []byte{byte(i)}))
		}
		must(s.RunUntilQuiet())
		reordered := false
		for i, t := range times {
			check(t >= 10 && t <= 100, "seed=17 latency out of range %d", t)
			if i > 0 && p[1].got[i] < p[1].got[i-1] {
				reordered = true
			}
		}
		check(reordered && len(times) == 200, "seed=17 no reorder or missing delivery")
		return "200 deliveries stayed within 10..100ns and included reordered datagrams"
	})
	attack("C2", "stream-fifo-no-duplicates", func() string {
		for seed := int64(1); seed <= 50; seed++ {
			c := netcfg()
			c.DropPPM = sim.PPM
			c.DuplicatePPM = sim.PPM
			c.Latency = sim.Range{Max: 100}
			c.InterRegionLatency = c.Latency
			s, p := cluster(seed, 2, c)
			var replies []byte
			for i := range 200 {
				p[0].e.Net.Request("1", []byte{byte(i)}, 1000, func(b []byte, e error) { must(e); replies = append(replies, b...) })
			}
			must(s.RunUntilQuiet())
			check(len(replies) == 200 && bytes.Equal(replies, p[1].got), "seed=%d lost/duplicated stream", seed)
			for i, b := range replies {
				check(int(b) == i, "seed=%d stream reordered at %d", seed, i)
			}
		}
		return "50 seeds x 200 requests and replies stayed FIFO despite 100% datagram drop and duplication"
	})
	for _, mode := range []string{"lost-request", "lost-response", "refused", "no-reply", "oversize-request", "oversize-reply", "invalid-timeout"} {
		mode := mode
		attack("C2", mode, func() string {
			s, p := cluster(23, 2, netcfg())
			expected := env.ErrTimeout
			req := []byte{1}
			timeout := time.Duration(20)
			wantServe := true
			switch mode {
			case "lost-request":
				must(s.SetLinkLoss("0", "1", sim.PPM))
				wantServe = false
			case "lost-response":
				must(s.SetLinkLoss("1", "0", sim.PPM))
			case "refused":
				must(s.Crash("1"))
				expected = env.ErrUnreachable
				wantServe = false
			case "no-reply":
				p[1].rq = func(env.NodeID, []byte, env.Reply) {}
			case "oversize-request":
				req = make([]byte, 65)
				expected = env.ErrTooLarge
				wantServe = false
			case "oversize-reply":
				p[1].rq = func(_ env.NodeID, _ []byte, r env.Reply) { r(make([]byte, 65), nil) }
				expected = env.ErrTooLarge
			case "invalid-timeout":
				timeout = 0
				expected = env.ErrInvalidArgument
				wantServe = false
			}
			calls := 0
			var got error
			p[0].e.Net.Request("1", req, timeout, func(_ []byte, e error) { calls++; got = e })
			check(calls == 0, "seed=23 callback was reentrant")
			must(s.RunUntilQuiet())
			check(calls == 1 && errors.Is(got, expected), "seed=23 callbacks=%d error=%v expected=%v", calls, got, expected)
			check((len(p[1].got) > 0) == wantServe, "seed=23 wrong server delivery")
			return "seed=23 produced one deferred callback with the expected error and server-delivery status"
		})
	}
	attack("C2", "datagram-size-boundary", func() string {
		s, p := cluster(5, 2, netcfg())
		must(p[0].e.Net.Send("1", make([]byte, 64)))
		e := p[0].e.Net.Send("1", make([]byte, 65))
		must(s.RunUntilQuiet())
		check(errors.Is(e, env.ErrTooLarge) && len(p[1].got) == 64, "seed=5 boundary delivered=%d error=%v", len(p[1].got), e)
		return "64-byte datagram delivered and 65-byte datagram rejected"
	})
}
func abs(i int) int {
	if i < 0 {
		return -i
	}
	return i
}
func c3() {
	for _, mode := range []string{"one-way", "region", "isolate", "symmetric"} {
		mode := mode
		attack("C3", mode+"-heal", func() string {
			s, p := cluster(11, 4, netcfg())
			blocked := func(i, j int) bool { return false }
			switch mode {
			case "one-way":
				must(s.PartitionOneWay([]env.NodeID{"0"}, []env.NodeID{"1"}))
				blocked = func(i, j int) bool { return i == 0 && j == 1 }
			case "region":
				must(s.PartitionRegion("0"))
				blocked = func(i, j int) bool { return i%2 != j%2 }
			case "isolate":
				must(s.Isolate("0"))
				blocked = func(i, j int) bool { return i != j && (i == 0 || j == 0) }
			case "symmetric":
				must(s.Partition([]env.NodeID{"0", "1"}, nil))
				blocked = func(i, j int) bool { return (i < 2) != (j < 2) }
			}
			for i := range 4 {
				for j := range 4 {
					must(p[i].e.Net.Send(p[j].e.ID, []byte{byte(i)}))
				}
			}
			must(s.RunUntilQuiet())
			for j := range 4 {
				want := []byte{}
				for i := range 4 {
					if !blocked(i, j) {
						want = append(want, byte(i))
					}
				}
				check(bytes.Equal(p[j].got, want), "seed=11 %s receiver=%d got=%v want=%v", mode, j, p[j].got, want)
				p[j].got = nil
			}
			s.HealNet()
			for i := range 4 {
				for j := range 4 {
					must(p[i].e.Net.Send(p[j].e.ID, []byte{byte(i)}))
				}
			}
			must(s.RunUntilQuiet())
			for _, p := range p {
				check(bytes.Equal(p.got, []byte{0, 1, 2, 3}), "seed=11 post-heal delivery=%v", p.got)
			}
			return "all 16 directed paths matched the partition matrix and all 16 post-heal datagrams arrived"
		})
	}
	for _, order := range []string{"unclog-resume", "resume-unclog", "heal-all"} {
		order := order
		attack("C3", "clog-"+order, func() string {
			for seed := int64(1); seed <= 50; seed++ {
				s, p := cluster(seed, 2, netcfg())
				must(s.Pause("1"))
				var got []byte
				send := func(v byte) {
					p[0].e.Net.Request("1", []byte{v}, 1000, func(b []byte, e error) { must(e); got = append(got, b...) })
				}
				send(1)
				must(s.RunUntil(1))
				must(s.Clog("0", "1"))
				send(2)
				must(s.RunUntil(2))
				check(len(p[1].got) == 0, "seed=%d clog leaked", seed)
				switch order {
				case "unclog-resume":
					must(s.Unclog("0", "1"))
					must(s.Resume("1"))
				case "resume-unclog":
					must(s.Resume("1"))
					must(s.Unclog("0", "1"))
				case "heal-all":
					s.HealAll()
				}
				send(3)
				must(s.RunUntilQuiet())
				check(bytes.Equal(got, []byte{1, 2, 3}) && bytes.Equal(p[1].got, got), "seed=%d %s replies=%v received=%v", seed, order, got, p[1].got)
			}
			return "50 seeds held traffic and released requests and replies in order through pause/clog composition"
		})
	}
}
func c4() {
	attack("C4", "whole-read-overflow-budget", func() string {
		s, p := cluster(1, 1, netcfg())
		must(p[0].e.Disk.Write("x", []byte{1}))
		must(p[0].e.Disk.Sync("x"))
		must(s.FailReads("0", "", math.MaxInt))
		err := s.FailReads("0", "", 1)
		b := sim.Budget{MaxDamagedDisks: 0}
		before := b.Check(s)
		_, got := p[0].e.Disk.Read("x")
		check(err != nil || errors.Is(got, env.ErrIO) && errors.Is(before, sim.ErrBudget), "seed=1 FailReads(0,empty,MaxInt) then +1: Read=%v and zero-damage Budget.Check=%v", got, before)
		return "whole-disk error count overflow retained errors and budget accounting or was rejected"
	})
	attack("C4", "group-budget-read-faults", func() string {
		s, p := cluster(12, 2, netcfg())
		for _, p := range p {
			for _, name := range []string{"shard", "meta"} {
				must(p.e.Disk.Write(name, []byte{1}))
				must(p.e.Disk.Sync(name))
			}
		}
		b := sim.Budget{MaxDamagedDisks: 2, MaxDamagedPerGroup: 1, GroupOf: func(_ env.NodeID, name string) string {
			if name == "shard" {
				return "stripe"
			}
			return ""
		}}
		must(s.BitRot("0", "shard"))
		must(b.Check(s))
		for _, f := range []sim.Fault{{Kind: sim.FaultReadError, Node: "1", Count: 1}, {Kind: sim.FaultReadError, Node: "1", Blob: "shard", Count: 1}, {Kind: sim.FaultBitRot, Node: "1"}, {Kind: sim.FaultWipeDisk, Node: "1"}} {
			check(!b.Allows(s, f), "seed=12 budget allowed %v beside damaged shard", f)
		}
		f := sim.Fault{Kind: sim.FaultBitRot, Node: "1", Blob: "meta"}
		check(b.Allows(s, f), "seed=12 ungrouped metadata rejected")
		must(s.Apply(f))
		must(b.Check(s))
		return "whole/named read errors, unnamed rot and wipe were rejected beside an existing damaged shard; ungrouped metadata allowed"
	})
	attack("C4", "silent-single-bit-rot", func() string {
		for seed := int64(1); seed <= 200; seed++ {
			s, p := cluster(seed, 1, netcfg())
			d := p[0].e.Disk
			orig := []byte("abcdefgh")
			must(d.Write("x", orig))
			must(d.Sync("x"))
			must(s.BitRot("0", "x"))
			got, e := d.Read("x")
			must(e)
			diff := 0
			for i := range orig {
				diff += bits.OnesCount8(orig[i] ^ got[i])
			}
			check(diff == 1 && reflect.DeepEqual(s.Disk("0").Damaged(), []string{"x"}), "seed=%d bit differences=%d damage=%v", seed, diff, s.Disk("0").Damaged())
		}
		return "200 seeds silently flipped exactly one durable bit and marked the blob"
	})
	attack("C4", "crash-durability-repair", func() string {
		for _, op := range []string{"write", "append", "delete"} {
			s, p := cluster(7, 1, netcfg())
			d := p[0].e.Disk
			must(d.Write("x", []byte("base")))
			must(d.Sync("x"))
			must(s.BitRot("0", "x"))
			rot, _ := s.Disk("0").PeekDurable("x")
			switch op {
			case "write":
				must(d.Write("x", []byte("repair")))
			case "append":
				must(d.Append("x", []byte("tail")))
			case "delete":
				must(d.Delete("x"))
			}
			must(d.Write("new", []byte("unsynced")))
			must(s.Crash("0"))
			must(s.Restart("0"))
			must(s.RunUntilQuiet())
			got, e := p[0].e.Disk.Read("x")
			must(e)
			check(bytes.Equal(got, rot) && len(s.Disk("0").Damaged()) == 1, "seed=7 %s lost durable rot or mark", op)
			_, e = p[0].e.Disk.Read("new")
			check(errors.Is(e, env.ErrNotFound), "seed=7 unsynced new blob survived")
			must(p[0].e.Disk.Write("x", []byte("repair")))
			must(p[0].e.Disk.Sync("x"))
			check(len(s.Disk("0").Damaged()) == 0, "seed=7 synced repair still marked")
		}
		return "crash discarded unsynced write/append/delete/new blob while preserving synced rot and its mark until repair sync"
	})
	attack("C4", "torn-prefix", func() string {
		seen := map[int]bool{}
		for seed := int64(1); seed <= 200; seed++ {
			s, p := cluster(seed, 1, netcfg())
			d := p[0].e.Disk
			must(d.Write("x", []byte("base")))
			must(d.Sync("x"))
			must(d.Append("x", []byte("12345678")))
			must(d.Append("new", []byte("abc")))
			must(s.CrashTorn("0"))
			got, ok := s.Disk("0").PeekDurable("x")
			check(ok && len(got) >= 4 && bytes.HasPrefix([]byte("base12345678"), got), "seed=%d torn data=%q", seed, got)
			seen[len(got)] = true
			b, ok := s.Disk("0").PeekDurable("new")
			check(!ok || len(b) > 0 && bytes.HasPrefix([]byte("abc"), b), "seed=%d new torn data=%q", seed, b)
		}
		check(len(seen) == 9, "seed=1..200 prefix lengths=%v", seen)
		return "200 seeds retained only append prefixes, covering all nine tail lengths and first-append creation"
	})
	attack("C4", "wipe-and-read-errors", func() string {
		s, p := cluster(8, 1, netcfg())
		d := p[0].e.Disk
		must(d.Write("x", []byte{1}))
		must(d.Sync("x"))
		must(d.Write("y", []byte{2}))
		must(s.FailReads("0", "x", 2))
		for range 2 {
			_, e := d.Read("x")
			check(errors.Is(e, env.ErrIO), "seed=8 missing read error")
		}
		_, e := d.Read("x")
		must(e)
		must(s.WipeDisk("0"))
		check(len(s.Disk("0").Names()) == 0 && len(s.Disk("0").Damaged()) == 2, "seed=8 wipe left data or lost damage marks")
		for _, name := range []string{"x", "y"} {
			_, ok := s.Disk("0").PeekDurable(name)
			check(!ok, "seed=8 wipe kept durable %s", name)
		}
		return "exactly two injected read errors then success; wipe removed both layers and marked two lost blobs"
	})
	attack("C4", "budget-fault-sequences", func() string {
		for seed := int64(1); seed <= 50; seed++ {
			s, p := cluster(seed, 4, netcfg())
			for _, p := range p {
				must(p.e.Disk.Write("shard", []byte("durable")))
				must(p.e.Disk.Sync("shard"))
			}
			b := sim.Budget{MinLive: 2, MaxDamagedDisks: 2, MaxDamagedPerGroup: 1, GroupOf: func(env.NodeID, string) string { return "stripe" }}
			for k := range 300 {
				i := int(s.WorkloadRand().Uint32N(4))
				id := env.NodeID(fmt.Sprint(i))
				f := sim.Fault{Node: id, Blob: "shard", Count: 2}
				switch k % 6 {
				case 0:
					f.Kind = sim.FaultBitRot
				case 1:
					f.Kind = sim.FaultWipeDisk
				case 2:
					f.Kind = sim.FaultReadError
				case 3:
					if s.Status(id).Up {
						_ = p[i].e.Disk.Write("shard", []byte("fixed"))
						_ = p[i].e.Disk.Sync("shard")
					}
				case 4:
					if s.Status(id).Up {
						_ = s.Crash(id)
						_ = s.Restart(id)
						must(s.RunUntilQuiet())
					}
				case 5:
					s.HealAll()
					must(s.RunUntilQuiet())
				}
				if f.Kind != 0 && b.Allows(s, f) {
					_ = s.Apply(f)
				}
				check(b.Check(s) == nil, "seed=%d step=%d accepted fault broke budget: %v", seed, k, b.Check(s))
				marks := 0
				for _, id := range s.Nodes() {
					marks += len(s.Disk(id).Damaged())
				}
				check(marks <= 1, "seed=%d step=%d marks=%d", seed, k, marks)
			}
		}
		return "15000 mixed fault/repair/crash/heal steps stayed within checked group and disk budgets"
	})
	attack("C4", "read-count-overflow", func() string {
		s, p := cluster(1, 1, netcfg())
		must(p[0].e.Disk.Write("x", []byte{1}))
		must(s.FailReads("0", "x", math.MaxInt))
		err := s.FailReads("0", "x", 1)
		_, got := p[0].e.Disk.Read("x")
		check(err != nil || errors.Is(got, env.ErrIO), "seed=1 FailReads(0,x,MaxInt), FailReads(0,x,1), Read(x) returned %v instead of IO or overflow rejection", got)
		return "read-fault count overflow was rejected or retained pending IO failures"
	})
}
func nem(seed int64, drop uint32, dynamic bool) []sim.NemesisStep {
	c := netcfg()
	c.DropPPM = drop
	s, p := cluster(seed, 3, c)
	for _, p := range p {
		must(p.e.Disk.Write("base", []byte{1}))
		must(p.e.Disk.Sync("base"))
		p.dg = func(_ env.NodeID, b []byte) {
			if dynamic {
				must(p.e.Disk.Write("received", b))
				must(p.e.Disk.Sync("received"))
			}
		}
	}
	for range 1 {
		_ = p[0].e.Net.Send("1", []byte{1})
	}
	must(s.RunUntil(1))
	n, e := s.StartNemesis(sim.NemesisConfig{Start: 2, Phases: 2, Chaos: 100, Heal: 10, MaxGap: 3, Kinds: []sim.FaultKind{sim.FaultBitRot}, Budget: sim.Budget{Lifted: true}})
	must(e)
	must(s.RunUntilQuiet())
	return n.History()
}
func c5() {
	attack("C5", "named-stream-draws", func() string {
		for seed := int64(1); seed <= 100; seed++ {
			for _, label := range []string{sim.StreamNet, sim.StreamDisk, sim.StreamNemesis, sim.StreamWorkload, sim.NodeStream("a"), sim.NodeStream("b")} {
				a, b := sim.NewStream(seed, label), sim.NewStream(seed, label)
				other := sim.NewStream(seed, "other")
				for range 1000 {
					_ = other.Uint64()
					check(a.Uint64() == b.Uint64(), "seed=%d stream=%s shifted", seed, label)
				}
			}
		}
		return "600000 comparisons unchanged by draws on an unrelated stream"
	})
	attack("C5", "node-stream-isolation", func() string {
		a, p := cluster(13, 2, netcfg())
		_ = a
		_, q := cluster(13, 2, netcfg())
		for range 1000 {
			_ = p[0].e.Rand.Uint64()
		}
		for range 1000 {
			check(p[1].e.Rand.Uint64() == q[1].e.Rand.Uint64(), "seed=13 node 0 draws shifted node 1")
		}
		check(q[0].e.Rand.Uint64() != q[1].e.Rand.Uint64(), "seed=13 node streams coincide")
		return "1000 extra node-0 draws did not shift node-1"
	})
	attack("C5", "network-nemesis-static", func() string {
		for seed := int64(1); seed <= 50; seed++ {
			check(reflect.DeepEqual(nem(seed, 0, false), nem(seed, sim.PPM, false)), "seed=%d network setting shifted static nemesis", seed)
		}
		return "50 bit-rot schedules unchanged by network drop changes with identical blob inventories"
	})
	attack("C5", "network-nemesis-dynamic-blobs", func() string {
		a, b := nem(1, 0, true), nem(1, sim.PPM, true)
		for i := range a {
			check(i < len(b) && reflect.DeepEqual(a[i], b[i]), "seed=1 DropPPM 0 vs 1000000 with OnDatagram writing received changed nemesis step %d: %v versus %v", i, a[i], b[i])
		}
		check(len(a) == len(b), "seed=1 history lengths differ")
		return "network setting did not shift nemesis with message-created durable blobs"
	})
}
func c6() {
	attack("C6", "nemesis-callback-panic", func() string {
		s, p := cluster(43, 1, netcfg())
		must(p[0].e.Disk.Write("x", []byte{1}))
		must(p[0].e.Disk.Sync("x"))
		_, err := s.StartNemesis(sim.NemesisConfig{Start: 1, Phases: 1, Chaos: 2, Heal: 1, MaxGap: 1, Kinds: []sim.FaultKind{sim.FaultBitRot}, Budget: sim.Budget{MaxDamagedDisks: 1, MaxDamagedPerGroup: 1, GroupOf: func(env.NodeID, string) string { panic("group-probe") }}})
		must(err)
		var escaped any
		func() { defer func() { escaped = recover() }(); _ = s.RunUntilQuiet() }()
		var pe *sim.PanicError
		check(escaped == nil && errors.As(s.Err(), &pe) && pe.Seed == 43, "seed=43 nemesis bit-rot Budget.GroupOf panic escaped RunUntilQuiet=%v; Sim.Err=%v", escaped, s.Err())
		return "nemesis callback panic captured as seeded sticky PanicError"
	})
	for _, mode := range []string{"empty", "deadline", "zero-timer", "self-send", "actions"} {
		mode := mode
		attack("C6", mode+"-stall", func() string {
			s := sim.New(sim.Config{Seed: 37, Net: netcfg(), MaxSteps: 200})
			p := &proc{}
			must(s.AddNode(sim.NodeSpec{ID: "0"}, func(e env.Env) env.Process { p.e = e; return p }))
			must(s.RunUntil(0))
			want := sim.ErrBudgetExhausted
			switch mode {
			case "empty":
				want = sim.ErrQueueEmpty
			case "deadline":
				want = sim.ErrDeadline
				s.At(101, "late", func() {})
			case "zero-timer":
				var f func()
				f = func() { p.e.Clock.AfterFunc(0, f) }
				f()
			case "self-send":
				p.dg = func(env.NodeID, []byte) { _ = p.e.Net.Send("0", []byte{1}) }
				_ = p.e.Net.Send("0", []byte{1})
			case "actions":
				var f func()
				f = func() { s.At(s.Now(), "again", f) }
				f()
			}
			e := s.RunUntilConverged(100, func() bool { return false })
			check(errors.Is(e, want) && strings.Contains(e.Error(), "37") && s.Err() == e, "seed=37 %s error=%v wanted=%v", mode, e, want)
			return "seed=37 returned the expected sticky stall with seed before the 200-step cap"
		})
	}
	for _, mode := range []string{"factory", "start", "handler", "timer", "action", "invariant", "predicate", "encode"} {
		mode := mode
		attack("C6", "panic-"+mode, func() string {
			s := sim.New(sim.Config{Seed: 41, Net: netcfg()})
			p := &proc{}
			boom := func() { panic("probe") }
			must(s.AddNode(sim.NodeSpec{ID: "0"}, func(e env.Env) env.Process {
				p.e = e
				if mode == "factory" {
					boom()
				}
				if mode == "start" {
					p.start = boom
				}
				return p
			}))
			_ = s.RunUntil(0)
			switch mode {
			case "handler":
				p.dg = func(env.NodeID, []byte) { boom() }
				_ = p.e.Net.Send("0", nil)
			case "timer":
				p.e.Clock.AfterFunc(0, boom)
			case "action":
				s.At(0, "panic", boom)
			case "invariant":
				s.AddInvariant("panic", 1, func() error { boom(); return nil })
				s.At(0, "tick", func() {})
			case "predicate":
				_ = s.RunUntilConverged(1, func() bool { boom(); return false })
			case "encode":
				p.encode = func() []byte { boom(); return nil }
				_ = s.Digests()
			}
			_ = s.RunUntilQuiet()
			var pe *sim.PanicError
			check(errors.As(s.Err(), &pe) && pe.Seed == 41, "seed=41 %s panic not captured: %v", mode, s.Err())
			return "seed=41 panic became a sticky PanicError naming the seed"
		})
	}
	attack("C2", "huge-latency-range", func() string {
		c := netcfg()
		c.Latency = sim.Range{Max: math.MaxInt64}
		s, p := cluster(1, 1, c)
		s.At(0, "send", func() { must(p[0].e.Net.Send("0", []byte{1})) })
		e := s.RunUntilQuiet()
		check(e == nil, "seed=1 valid latency [0,MaxInt64] send failed: %v", e)
		return "full nonnegative int64 latency range delivered without overflow"
	})
	attack("C6", "clock-overflow", func() string {
		s := sim.New(sim.Config{Seed: 1, Net: netcfg()})
		var e env.Env
		must(s.AddNode(sim.NodeSpec{ID: "0", Skew: time.Duration(math.MaxInt64)}, func(x env.Env) env.Process { e = x; return &proc{e: x} }))
		must(s.RunUntil(0))
		a := e.Clock.Now()
		must(s.RunUntil(1))
		b := e.Clock.Now()
		check(b >= a || s.Err() != nil, "seed=1 skew=MaxInt64 RunUntil(0)->RunUntil(1) local clock went from %d to %d with no error", a, b)
		return "local time overflow rejected or remained monotonic"
	})
	attack("C6", "timer-duration-clamp", func() string {
		s, p := cluster(1, 1, netcfg())
		fired := false
		p[0].e.Clock.AfterFunc(time.Duration(1<<62)+1, func() { fired = true })
		must(s.RunUntil(1 << 62))
		check(!fired, "seed=1 AfterFunc((1<<62)+1) fired at %d one ns early", s.Now())
		return "huge valid timer did not fire before its duration elapsed"
	})
	attack("C6", "global-time-past-events", func() string {
		s := sim.New(sim.Config{Seed: 9})
		must(s.RunUntil(100))
		s.At(-1, "past", func() { check(s.Now() == 100, "seed=9 past action moved clock") })
		must(s.RunFor(-200))
		must(s.RunUntilQuiet())
		check(s.Now() == 100, "seed=9 clock=%d", s.Now())
		return "past events and negative RunFor did not move global time backwards"
	})
}
func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "c1":
		c1()
	case "c2":
		c2()
	case "c3":
		c3()
	case "c4":
		c4()
	case "c5":
		c5()
	case "c6":
		c6()
	case "fingerprints":
		for _, n := range []int{1, 2, 3, 7, 31, 100} {
			for seed := int64(1); seed <= 20; seed++ {
				s := replay(seed, n)
				fmt.Println(strconv.Itoa(n), seed, s.Digests(), s.Err())
			}
		}
	default:
		os.Exit(2)
	}
	if broken > 0 {
		os.Exit(1)
	}
}
