package sim_test

import (
	"bytes"
	"errors"
	"fmt"
	"math/bits"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtoy"
)

func TestPropStreamsIndependent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.Int64().Draw(t, "seed")
		labels := []string{sim.StreamNet, sim.StreamDisk, sim.StreamNemesis, sim.StreamWorkload, sim.NodeStream("n000"), sim.NodeStream("n001")}
		a := rapid.IntRange(0, len(labels)-1).Draw(t, "a")
		b := rapid.IntRange(0, len(labels)-1).Filter(func(i int) bool { return i != a }).Draw(t, "b")
		extra := rapid.IntRange(0, 1000).Draw(t, "extra")

		ra, rb := sim.NewStream(seed, labels[a]), sim.NewStream(seed, labels[b])
		for i := 0; i < extra; i++ {
			ra.Uint64()
		}
		fresh := sim.NewStream(seed, labels[b])
		for i := 0; i < 16; i++ {
			if rb.Uint64() != fresh.Uint64() {
				t.Fatalf("draws on %q changed %q", labels[a], labels[b])
			}
		}
		x, y := sim.NewStream(seed, labels[a]), sim.NewStream(seed, labels[b])
		same := true
		for i := 0; i < 4; i++ {
			same = same && x.Uint64() == y.Uint64()
		}
		if same {
			t.Fatalf("streams %q and %q start identically", labels[a], labels[b])
		}
	})
}

func TestPropNetworkDrawsDoNotShiftTheNemesis(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.Int64().Draw(t, "seed")
		drop := rapid.Uint32Range(1, 900_000).Draw(t, "drop")
		run := func(dropPPM uint32) []sim.NemesisStep {
			nc := sim.DefaultNetConfig()
			nc.DropPPM = dropPPM
			s := sim.New(sim.Config{Seed: seed, Net: nc})
			ids := sim.NodeIDs(5)
			if err := s.AddCluster(ids, 2, simtoy.Factory(simtoy.Config{Peers: ids})); err != nil {
				t.Fatal(err)
			}
			n, err := s.StartNemesis(sim.NemesisConfig{
				Start: 0, Phases: 2, Chaos: 2 * time.Second, Heal: time.Second, MaxGap: 100 * time.Millisecond,
				Kinds:  []sim.FaultKind{sim.FaultCrash, sim.FaultRestart, sim.FaultPause, sim.FaultResume, sim.FaultPartition, sim.FaultPartitionOneWay, sim.FaultIsolate, sim.FaultLinkLoss, sim.FaultClog, sim.FaultUnclog},
				Budget: sim.Budget{MinLive: 2},
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RunUntil(n.QuietAt()); err != nil {
				t.Fatal(err)
			}
			return n.History()
		}
		a, b := run(0), run(drop)
		if len(a) != len(b) {
			t.Fatalf("nemesis lengths %d vs %d", len(a), len(b))
		}
		for i := range a {
			if a[i].At != b[i].At || a[i].Fault.String() != b[i].Fault.String() || a[i].Outcome != b[i].Outcome {
				t.Fatalf("step %d: %v vs %v", i, a[i], b[i])
			}
		}
	})
}

type ticker struct {
	e     env.Env
	peers []env.NodeID
	left  int
	last  env.Time
	bad   error
}

func (k *ticker) Start() { k.arm() }
func (k *ticker) arm() {
	if k.left <= 0 {
		return
	}
	k.left--
	k.e.Clock.AfterFunc(time.Duration(k.e.Rand.IntN(50_000_000)), func() {
		k.observe()
		_ = k.e.Net.Send(k.peers[k.e.Rand.IntN(len(k.peers))], []byte("t"))
		k.arm()
	})
}
func (k *ticker) observe() {
	now := k.e.Clock.Now()
	if now < k.last && k.bad == nil {
		k.bad = fmt.Errorf("local clock of %s went from %d to %d", k.e.ID, k.last, now)
	}
	k.last = now
}
func (k *ticker) OnDatagram(env.NodeID, []byte)           { k.observe() }
func (k *ticker) OnRequest(env.NodeID, []byte, env.Reply) {}
func (k *ticker) EncodeState() []byte                     { return nil }

func TestPropTimeMonotonic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(t, "n")
		nc := sim.DefaultNetConfig()
		lo := rapid.Int64Range(0, 10_000_000).Draw(t, "lo")
		hi := rapid.Int64Range(lo, 50_000_000).Draw(t, "hi")
		nc.Latency = sim.Range{Min: time.Duration(lo), Max: time.Duration(hi)}
		nc.DuplicatePPM = rapid.Uint32Range(0, 1_000_000).Draw(t, "dup")
		s := sim.New(sim.Config{Seed: rapid.Int64().Draw(t, "seed"), Net: nc})
		ids := sim.NodeIDs(n)
		var procs []*ticker
		for _, id := range ids {
			sp := sim.NodeSpec{
				ID:       id,
				Skew:     time.Duration(rapid.Int64Range(-int64(time.Second), int64(time.Second)).Draw(t, "skew")),
				DriftPPM: rapid.Int64Range(-sim.MaxDriftPPM, sim.MaxDriftPPM).Draw(t, "drift"),
			}
			left := rapid.IntRange(0, 40).Draw(t, "ticks")
			if err := s.AddNode(sp, func(e env.Env) env.Process {
				k := &ticker{e: e, peers: ids, left: left, last: -1 << 62}
				procs = append(procs, k)
				return k
			}); err != nil {
				t.Fatal(err)
			}
		}
		var last env.Time
		s.AddInvariant("global time monotonic", 1, func() error {
			if s.Now() < last {
				return fmt.Errorf("global time went from %d to %d", last, s.Now())
			}
			last = s.Now()
			return nil
		})
		if err := s.RunUntilQuiet(); err != nil {
			t.Fatal(err)
		}
		for _, k := range procs {
			if k.bad != nil {
				t.Fatal(k.bad)
			}
		}
	})
}

func TestPropHealedNetworkDelivers(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(2, 6).Draw(t, "n")
		nc := sim.DefaultNetConfig()
		s := sim.New(sim.Config{Seed: rapid.Int64().Draw(t, "seed"), Net: nc})
		ids := sim.NodeIDs(n)
		recs := map[env.NodeID]*recorder{}
		for i, id := range ids {
			if err := s.AddNode(sim.NodeSpec{ID: id, Region: env.Region(fmt.Sprintf("r%d", i%2))}, func(e env.Env) env.Process {
				r := &recorder{e: e}
				recs[e.ID] = r
				return r
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.RunUntil(0); err != nil {
			t.Fatal(err)
		}
		pick := rapid.SampledFrom(ids)
		faults := rapid.IntRange(0, 8).Draw(t, "faults")
		for i := 0; i < faults; i++ {
			a, b := pick.Draw(t, "a"), pick.Draw(t, "b")
			var err error
			switch rapid.IntRange(0, 6).Draw(t, "kind") {
			case 0:
				mustNil(t, s.Partition([]env.NodeID{a}, []env.NodeID{b}))
			case 1:
				mustNil(t, s.PartitionOneWay([]env.NodeID{a}, []env.NodeID{b}))
			case 2:
				err = s.Isolate(a)
			case 3:
				err = s.PartitionRegion(s.Region(a))
			case 4:
				err = s.SetLinkLoss(a, b, rapid.Uint32Range(0, 1_000_000).Draw(t, "ppm"))
			case 5:
				err = s.Clog(a, b)
			case 6:
				_ = recs[a].e.Net.Send(b, []byte("pre"))
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := s.RunFor(time.Second); err != nil {
			t.Fatal(err)
		}
		s.HealNet()
		msgs := rapid.IntRange(1, 30).Draw(t, "msgs")
		wantDatagrams := map[env.NodeID]int{}
		wantReplies := map[env.NodeID]int{}
		for i := 0; i < msgs; i++ {
			a, b := pick.Draw(t, "from"), pick.Draw(t, "to")
			if rapid.Bool().Draw(t, "request") {
				recs[a].request(b, fmt.Sprintf("post%d", i), time.Minute)
				wantReplies[a]++
				continue
			}
			if err := recs[a].e.Net.Send(b, []byte(fmt.Sprintf("post%d", i))); err != nil {
				t.Fatal(err)
			}
			wantDatagrams[b]++
		}
		if err := s.RunFor(10 * time.Second); err != nil {
			t.Fatal(err)
		}
		for _, id := range ids {
			got := 0
			for _, d := range recs[id].datagrams {
				if strings.HasPrefix(d.msg, "post") {
					got++
				}
			}
			if got != wantDatagrams[id] {
				t.Fatalf("%s received %d post-heal datagrams, want %d", id, got, wantDatagrams[id])
			}
			ok := 0
			for _, r := range recs[id].replies {
				if r.err != nil {
					t.Fatalf("%s post-heal request failed: %v", id, r.err)
				}
				if strings.HasPrefix(r.reply, "echo:post") {
					ok++
				}
			}
			if ok != wantReplies[id] {
				t.Fatalf("%s got %d replies, want %d", id, ok, wantReplies[id])
			}
		}
	})
}

func TestPropNemesisRespectsBudget(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(3, 8).Draw(t, "n")
		s := sim.New(sim.Config{Seed: rapid.Int64().Draw(t, "seed")})
		ids := sim.NodeIDs(n)
		if err := s.AddCluster(ids, 3, simtoy.Factory(simtoy.Config{Peers: ids})); err != nil {
			t.Fatal(err)
		}
		b := sim.Budget{
			MinLive:            rapid.IntRange(0, n).Draw(t, "minLive"),
			MaxDamagedDisks:    rapid.IntRange(0, n).Draw(t, "maxDisks"),
			MaxDamagedPerGroup: rapid.IntRange(0, 3).Draw(t, "perGroup"),
		}
		if rapid.Bool().Draw(t, "groups") {
			b.GroupOf = func(env.NodeID, string) string { return "one" }
		}
		cfg := sim.NemesisConfig{
			Start: 0, Phases: rapid.IntRange(1, 3).Draw(t, "phases"),
			Chaos: 2 * time.Second, Heal: time.Second, MaxGap: 50 * time.Millisecond, Budget: b,
		}
		nem, err := s.StartNemesis(cfg)
		if err != nil {
			t.Fatal(err)
		}
		s.AddInvariant("budget", 1, func() error { return b.Check(s) })
		if err := s.RunUntil(nem.QuietAt()); err != nil {
			t.Fatal(err)
		}
		if err := s.Verify(); err != nil {
			t.Fatal(err)
		}
	})
}

type diskModelBlob struct {
	written, durable  []byte
	wExists, dExists  bool
	dirty, appendOnly bool
	dDamaged          bool
	wDamaged          bool
}

type diskMachine struct {
	s     *sim.Sim
	cur   env.Env
	model map[string]*diskModelBlob
	names []string
}

func (m *diskMachine) blob(name string) *diskModelBlob {
	b := m.model[name]
	if b == nil {
		b = &diskModelBlob{}
		m.model[name] = b
	}
	return b
}

func (m *diskMachine) restart(t *rapid.T) {
	if err := m.s.Restart("a"); err != nil {
		t.Fatal(err)
	}
	if err := m.s.RunUntil(m.s.Now()); err != nil {
		t.Fatal(err)
	}
}

func (m *diskMachine) check(t *rapid.T) {
	var want []string
	for _, name := range m.names {
		b := m.model[name]
		if b != nil && b.wExists {
			want = append(want, name)
		}
	}
	got, err := m.cur.Disk.List("")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("List = %v, model %v", got, want)
	}
	var wantDamaged []string
	for _, name := range m.names {
		if b := m.model[name]; b != nil && b.dDamaged {
			wantDamaged = append(wantDamaged, name)
		}
	}
	if gotDamaged := m.s.Disk("a").Damaged(); fmt.Sprint(gotDamaged) != fmt.Sprint(wantDamaged) {
		t.Fatalf("Damaged = %v, model %v", gotDamaged, wantDamaged)
	}
	for _, name := range m.names {
		b := m.model[name]
		data, err := m.cur.Disk.Read(name)
		if b == nil || !b.wExists {
			if !errors.Is(err, env.ErrNotFound) {
				t.Fatalf("Read %s = %v, want ErrNotFound", name, err)
			}
			continue
		}
		if err != nil || !bytes.Equal(data, b.written) {
			t.Fatalf("Read %s = %q %v, model %q", name, data, err, b.written)
		}
		dur, ok := m.s.Disk("a").PeekDurable(name)
		if ok != b.dExists || ok && !bytes.Equal(dur, b.durable) {
			t.Fatalf("durable %s = %q %v, model %q %v", name, dur, ok, b.durable, b.dExists)
		}
	}
}

func TestPropDiskModel(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		m := &diskMachine{model: map[string]*diskModelBlob{}, names: []string{"a", "b", "c/d"}}
		m.s = sim.New(sim.Config{Seed: rapid.Int64().Draw(t, "seed")})
		if err := m.s.AddNode(sim.NodeSpec{ID: "a"}, func(e env.Env) env.Process {
			m.cur = e
			return &recorder{e: e}
		}); err != nil {
			t.Fatal(err)
		}
		if err := m.s.RunUntil(0); err != nil {
			t.Fatal(err)
		}
		name := rapid.SampledFrom(m.names)
		data := rapid.SliceOfN(rapid.Byte(), 0, 12)
		t.Repeat(map[string]func(*rapid.T){
			"write": func(t *rapid.T) {
				n, d := name.Draw(t, "name"), data.Draw(t, "data")
				if err := m.cur.Disk.Write(n, d); err != nil {
					t.Fatal(err)
				}
				b := m.blob(n)
				b.written, b.wExists, b.dirty, b.appendOnly = append([]byte{}, d...), true, true, false
				b.wDamaged = false
			},
			"append": func(t *rapid.T) {
				n, d := name.Draw(t, "name"), data.Draw(t, "data")
				if err := m.cur.Disk.Append(n, d); err != nil {
					t.Fatal(err)
				}
				b := m.blob(n)
				if !b.dirty {
					b.appendOnly = true
				}
				if !b.wExists {
					b.wDamaged = false
				}
				b.written = append(append([]byte{}, b.written...), d...)
				b.wExists, b.dirty = true, true
			},
			"sync": func(t *rapid.T) {
				n := name.Draw(t, "name")
				err := m.cur.Disk.Sync(n)
				b := m.model[n]
				if b == nil || !b.wExists && !b.dExists {
					if !errors.Is(err, env.ErrNotFound) {
						t.Fatalf("Sync of missing = %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				b.durable, b.dExists, b.dirty, b.appendOnly = append([]byte{}, b.written...), b.wExists, false, false
				b.dDamaged = b.wExists && b.wDamaged
			},
			"delete": func(t *rapid.T) {
				n := name.Draw(t, "name")
				err := m.cur.Disk.Delete(n)
				b := m.model[n]
				if b == nil || !b.wExists {
					if !errors.Is(err, env.ErrNotFound) {
						t.Fatalf("Delete of missing = %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				b.written, b.wExists, b.dirty, b.appendOnly = nil, false, true, false
				b.wDamaged = false
			},
			"crash": func(t *rapid.T) {
				if err := m.s.Crash("a"); err != nil {
					t.Fatal(err)
				}
				for _, b := range m.model {
					b.written, b.wExists, b.dirty, b.appendOnly = append([]byte{}, b.durable...), b.dExists, false, false
					b.wDamaged = b.dDamaged && b.dExists
				}
				m.restart(t)
			},
			"crashTorn": func(t *rapid.T) {
				if err := m.s.CrashTorn("a"); err != nil {
					t.Fatal(err)
				}
				for _, n := range m.names {
					b := m.model[n]
					if b == nil {
						continue
					}
					if b.dirty && b.appendOnly && b.wExists {
						got, ok := m.s.Disk("a").Peek(n)
						switch {
						case !ok && !b.dExists:
						case !ok || len(got) < len(b.durable) || !bytes.Equal(got[:len(b.durable)], b.durable) ||
							!bytes.HasPrefix(b.written[len(b.durable):], got[len(b.durable):]):
							t.Fatalf("torn %s = %q %v, written %q durable %q", n, got, ok, b.written, b.durable)
						default:
							b.durable, b.dExists = append([]byte{}, got...), true
						}
					}
					b.written, b.wExists, b.dirty, b.appendOnly = append([]byte{}, b.durable...), b.dExists, false, false
					b.wDamaged = b.dDamaged && b.dExists
				}
				m.restart(t)
			},
			"bitrot": func(t *rapid.T) {
				n := name.Draw(t, "name")
				b := m.model[n]
				err := m.s.BitRot("a", n)
				if b == nil || !b.dExists || len(b.durable) == 0 {
					if err == nil {
						t.Fatalf("BitRot of %s with no durable bytes succeeded", n)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				got, _ := m.s.Disk("a").PeekDurable(n)
				flips := 0
				for i := range got {
					flips += bits.OnesCount8(got[i] ^ b.durable[i])
				}
				if flips != 1 {
					t.Fatalf("bit-rot flipped %d bits", flips)
				}
				if !b.dirty {
					b.written = append([]byte{}, got...)
					b.wDamaged = true
				}
				b.durable = append([]byte{}, got...)
				b.dDamaged = true
			},
			"wipe": func(t *rapid.T) {
				if err := m.s.WipeDisk("a"); err != nil {
					t.Fatal(err)
				}
				for _, b := range m.model {
					if b.wExists || b.dExists {
						b.dDamaged = true
					}
					b.wDamaged = false
					b.written, b.wExists, b.durable, b.dExists, b.dirty, b.appendOnly = nil, false, nil, false, false, false
				}
			},
			"failRead": func(t *rapid.T) {
				n := name.Draw(t, "name")
				if err := m.s.FailReads("a", n, 1); err != nil {
					t.Fatal(err)
				}
				if _, err := m.cur.Disk.Read(n); !errors.Is(err, env.ErrIO) {
					t.Fatalf("Read after FailReads = %v", err)
				}
			},
			"": func(t *rapid.T) { m.check(t) },
		})
	})
}

type streamMachine struct {
	s    *sim.Sim
	ids  []env.NodeID
	recs map[env.NodeID]*recorder
	sent map[[2]env.NodeID]int
}

func streamLabel(from, to env.NodeID, k int) string { return fmt.Sprintf("%s>%s#%d", from, to, k) }

func parseStreamLabel(t *rapid.T, s string) (env.NodeID, env.NodeID, int) {
	var k int
	head, num, ok := strings.Cut(s, "#")
	from, to, ok2 := strings.Cut(head, ">")
	if _, err := fmt.Sscanf(num, "%d", &k); err != nil || !ok || !ok2 {
		t.Fatalf("bad label %q", s)
	}
	return env.NodeID(from), env.NodeID(to), k
}

func (m *streamMachine) checkOrder(t *rapid.T) {
	for _, id := range m.ids {
		last := map[env.NodeID]int{}
		for _, req := range m.recs[id].requests {
			from, to, k := parseStreamLabel(t, req)
			if to != id {
				t.Fatalf("%s received %q", id, req)
			}
			if prev, ok := last[from]; ok && k <= prev {
				t.Fatalf("%s received requests from %s out of order or twice: %v", id, from, m.recs[id].requests)
			}
			last[from] = k
		}
		lastReply := map[env.NodeID]int{}
		for _, rep := range m.recs[id].replies {
			if rep.err != nil {
				t.Fatalf("%s request failed: %v", id, rep.err)
			}
			from, to, k := parseStreamLabel(t, strings.TrimPrefix(rep.reply, "echo:"))
			if from != id {
				t.Fatalf("%s got reply %q", id, rep.reply)
			}
			if prev, ok := lastReply[to]; ok && k <= prev {
				t.Fatalf("%s received responses from %s out of order or twice: %+v", id, to, m.recs[id].replies)
			}
			lastReply[to] = k
		}
	}
}

func TestPropStreamOrderPerConnection(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(2, 4).Draw(t, "n")
		m := &streamMachine{s: sim.New(sim.Config{Seed: rapid.Int64().Draw(t, "seed")}), ids: sim.NodeIDs(n),
			recs: map[env.NodeID]*recorder{}, sent: map[[2]env.NodeID]int{}}
		for _, id := range m.ids {
			if err := m.s.AddNode(sim.NodeSpec{ID: id, Region: "r"}, func(e env.Env) env.Process {
				r := &recorder{e: e}
				m.recs[e.ID] = r
				return r
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := m.s.RunUntil(0); err != nil {
			t.Fatal(err)
		}
		pick := rapid.SampledFrom(m.ids)
		pair := func(t *rapid.T) (env.NodeID, env.NodeID) {
			a := pick.Draw(t, "a")
			b := rapid.SampledFrom(m.ids).Filter(func(x env.NodeID) bool { return x != a }).Draw(t, "b")
			return a, b
		}
		t.Repeat(map[string]func(*rapid.T){
			"request": func(t *rapid.T) {
				a, b := pair(t)
				k := m.sent[[2]env.NodeID{a, b}]
				m.sent[[2]env.NodeID{a, b}] = k + 1
				m.recs[a].request(b, streamLabel(a, b, k), time.Hour)
			},
			"pause": func(t *rapid.T) {
				id := pick.Draw(t, "id")
				if m.s.Status(id).Paused {
					return
				}
				if err := m.s.Pause(id); err != nil {
					t.Fatal(err)
				}
			},
			"resume": func(t *rapid.T) {
				id := pick.Draw(t, "id")
				if !m.s.Status(id).Paused {
					return
				}
				if err := m.s.Resume(id); err != nil {
					t.Fatal(err)
				}
			},
			"clog": func(t *rapid.T) {
				a, b := pair(t)
				if err := m.s.Clog(a, b); err != nil {
					t.Fatal(err)
				}
			},
			"unclog": func(t *rapid.T) {
				a, b := pair(t)
				if err := m.s.Unclog(a, b); err != nil {
					t.Fatal(err)
				}
			},
			"healAll": func(t *rapid.T) { m.s.HealAll() },
			"healNet": func(t *rapid.T) { m.s.HealNet() },
			"run": func(t *rapid.T) {
				d := time.Duration(rapid.IntRange(0, 6).Draw(t, "ms")) * time.Millisecond
				if err := m.s.RunFor(d); err != nil {
					t.Fatal(err)
				}
				m.checkOrder(t)
			},
		})
		m.s.HealAll()
		if err := m.s.RunFor(time.Minute); err != nil {
			t.Fatal(err)
		}
		m.checkOrder(t)
		for _, a := range m.ids {
			for _, b := range m.ids {
				got := 0
				for _, req := range m.recs[b].requests {
					if from, _, _ := parseStreamLabel(t, req); from == a {
						got++
					}
				}
				if want := m.sent[[2]env.NodeID{a, b}]; got != want {
					t.Fatalf("%s received %d requests from %s, sent %d", b, got, a, want)
				}
			}
			total := 0
			for _, b := range m.ids {
				total += m.sent[[2]env.NodeID{a, b}]
			}
			if len(m.recs[a].replies) != total {
				t.Fatalf("%s got %d replies for %d requests", a, len(m.recs[a].replies), total)
			}
		}
	})
}
