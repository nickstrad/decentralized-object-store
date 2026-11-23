package main

import (
	"bytes"
	"errors"
	"fmt"
	e "github.com/nickstrad/object-store/internal/env"
	m "github.com/nickstrad/object-store/internal/merkle"
	"github.com/nickstrad/object-store/internal/sim"
	"time"
)

type replica struct {
	env         e.Env
	w           *m.Worker
	peers       []e.NodeID
	cfg         m.Config
	bad         bool
	putError    bool
	delay       time.Duration
	fetched     uint64
	invalidPuts int
	prefixOnly  bool
	vanish      func()
}

func (p *replica) Peers() []e.NodeID             { return p.peers }
func (p *replica) Keys() ([]string, error)       { return p.env.Disk.List("") }
func (p *replica) Read(k string) ([]byte, error) { return p.env.Disk.Read(k) }
func (p *replica) Valid(k string, h m.Hash) bool { return k == h.String() }
func (p *replica) Put(k string, b []byte) error {
	if !p.Valid(k, m.Sum(b)) {
		p.invalidPuts++
		return m.ErrRejected
	}
	if p.putError {
		return e.ErrIO
	}
	if err := p.env.Disk.Write(k, b); err != nil {
		return err
	}
	return p.env.Disk.Sync(k)
}
func (p *replica) Fetch(peer e.NodeID, k string, cb func([]byte, error)) {
	p.env.Net.Request(peer, append([]byte{99}, []byte(k)...), p.cfg.Timeout, func(b []byte, err error) {
		if err == nil {
			p.fetched += uint64(len(b))
		}
		if p.delay > 0 {
			p.env.Clock.AfterFunc(p.delay, func() { cb(b, err) })
		} else {
			cb(b, err)
		}
	})
}
func (p *replica) Start() {
	var err error
	p.w, err = m.NewWorker(p.env, p.cfg, p, p)
	must(err)
	must(p.w.Start())
}
func (p *replica) OnDatagram(e.NodeID, []byte) {}
func (p *replica) OnRequest(from e.NodeID, b []byte, reply e.Reply) {
	if len(b) > 0 && b[0] == 99 {
		if p.bad {
			reply([]byte("incorrect bytes"), nil)
		} else {
			x, err := p.Read(string(b[1:]))
			reply(x, err)
		}
		return
	}
	if p.vanish != nil && len(b) > 2 && b[2] == 1 {
		f := p.vanish
		p.vanish = nil
		f()
		return
	}
	if p.prefixOnly && len(b) > 4 && b[2] == 1 {
		b = []byte{1, 1, 1, 1, b[4]}
	}
	p.w.OnRequest(from, b, reply)
}
func (p *replica) EncodeState() []byte { return p.w.EncodeState() }
func (p *replica) write(b []byte) {
	k := m.Sum(b).String()
	must(p.Put(k, b))
	must(p.w.Record(k, m.Sum(b)))
}
func newWorld(n int, sd int64, depth ...int) (*sim.Sim, []*replica) {
	codes, err := e.NewCodeTable(m.CodeEntries()...)
	must(err)
	nc := sim.DefaultNetConfig()
	nc.Latency = sim.Range{Min: time.Millisecond, Max: 3 * time.Millisecond}
	nc.InterRegionLatency = nc.Latency
	s := sim.New(sim.Config{Seed: sd, Codes: codes, Net: nc})
	ids := sim.NodeIDs(n)
	ps := make([]*replica, n)
	for i, id := range ids {
		i := i
		must(s.AddNode(sim.NodeSpec{ID: id, Region: "r"}, func(env e.Env) e.Process {
			p := &replica{env: env, peers: ids, cfg: m.Config{Depth: 3, Period: 200 * time.Millisecond, Timeout: 100 * time.Millisecond, ScrubBatch: 7, MaxRepairs: 3}}
			if len(depth) > 0 {
				p.cfg.Depth = depth[0]
			}
			ps[i] = p
			return p
		}))
	}
	must(s.RunFor(time.Millisecond))
	return s, ps
}
func dataSet() [][]byte {
	out := [][]byte{}
	for i := 0; i < 37; i++ {
		out = append(out, bytes.Repeat([]byte(fmt.Sprintf("chunk-%03d/", i)), 3+i))
	}
	return out
}
func allGood(ps []*replica, ds [][]byte) bool {
	for _, p := range ps {
		if p.w.Tree().Len() != len(ds) {
			return false
		}
		for _, b := range ds {
			k := m.Sum(b).String()
			x, err := p.Read(k)
			h, ok := p.w.Tree().Get(k)
			if err != nil || !bytes.Equal(x, b) || !ok || h != m.Sum(b) {
				return false
			}
		}
	}
	return true
}
func accounting(ps []*replica) {
	for _, p := range ps {
		st := p.w.Stats()
		flight := uint64(0)
		if st.InFlight {
			flight = 1
		}
		require(st.Rounds == st.Completed+st.Failed+flight, "unbalanced rounds %+v", st)
		require(p.invalidPuts == 0, "invalid Put called")
	}
}
func simChecks() {
	for _, mode := range []string{"clean", "rot", "partition", "loss", "wipe", "read-error", "tree-record", "restart"} {
		mode := mode
		claim := map[string]string{"clean": "C18", "rot": "C18", "partition": "C19", "loss": "C20", "wipe": "C22", "read-error": "C22", "tree-record": "C29", "restart": "C29"}[mode]
		check(claim, "sim-"+mode, func() string {
			maxTime := time.Duration(0)
			for trial := 0; trial < 8; trial++ {
				sd := int64(seed + trial)
				s, ps := newWorld(3, sd)
				ds := dataSet()
				if mode == "partition" {
					must(s.Isolate(ps[2].env.ID))
				}
				for _, b := range ds {
					if mode == "partition" || mode == "loss" {
						ps[0].write(b)
					} else {
						for _, p := range ps {
							p.write(b)
						}
					}
				}
				if mode == "loss" {
					for _, p := range ps {
						for _, q := range ps {
							if p != q {
								must(s.SetLinkLoss(p.env.ID, q.env.ID, 200000))
							}
						}
					}
				}
				if mode == "partition" {
					must(s.RunFor(2 * time.Second))
					require(!allGood(ps, ds), "isolation failed seed=%d", sd)
					s.HealNet()
				}
				var damaged uint64
				before := uint64(0)
				for _, p := range ps {
					before += p.fetched
				}
				switch mode {
				case "rot":
					for j := 0; j < 5; j++ {
						k := m.Sum(ds[j]).String()
						must(s.BitRot(ps[1].env.ID, k))
						damaged += uint64(len(ds[j]))
					}
				case "wipe":
					must(s.WipeDisk(ps[1].env.ID))
				case "read-error":
					must(s.FailReads(ps[1].env.ID, m.Sum(ds[0]).String(), 2))
				case "tree-record":
					for _, b := range ds[:5] {
						must(ps[1].w.Record(m.Sum(b).String(), m.Hash{}))
					}
					must(ps[1].w.Record("phantom", m.Hash{}))
				case "restart":
					id := ps[1].env.ID
					must(s.Crash(id))
					must(s.Restart(id))
					must(s.RunFor(time.Millisecond))
				}
				start := s.Now()
				if mode == "clean" || mode == "read-error" {
					must(s.RunFor(4 * time.Second))
				}
				must(s.RunUntilConverged(s.Now().Add(60*time.Second), func() bool { return allGood(ps, ds) }))
				maxTime = max(maxTime, s.Now().Sub(start))
				accounting(ps)
				var fetched, changed, reads, dropped, failed uint64
				for _, p := range ps {
					st := p.w.Stats()
					fetched += p.fetched
					changed += st.Changed
					reads += st.ReadErrors
					dropped += st.Dropped
					failed += st.Failed
				}
				switch mode {
				case "clean":
					require(fetched == 0, "clean fetched=%d seed=%d", fetched, sd)
				case "rot":
					require(changed > 0 && s.Now().Sub(start) <= 20*time.Second && fetched-before <= 4*damaged, "rot elapsed=%v changed=%d fetched=%d damaged=%d seed=%d", s.Now().Sub(start), changed, fetched-before, damaged, sd)
				case "partition":
					require(s.Now().Sub(start) <= 20*time.Second, "heal exceeded 20s seed=%d", sd)
				case "read-error":
					require(reads > 0, "read errors uncounted seed=%d", sd)
				case "wipe":
					require(dropped > 0, "wiped entries not scrubbed seed=%d", sd)
				case "loss":
					require(failed > 0, "loss not exercised seed=%d", sd)
				case "tree-record":
					require(changed > 0 && dropped > 0, "stale tree not scrubbed seed=%d", sd)
				case "restart":
					names, err := ps[1].Keys()
					must(err)
					require(len(names) == len(ds), "unexpected persisted tree blob seed=%d", sd)
				}
			}
			return fmt.Sprintf("8 seeds %d..%d, 37 chunks and 3 replicas converge with accounting intact; max elapsed %v", seed, seed+7, maxTime)
		})
	}
	check("C21", "sim-invalid-fetch", func() string {
		for trial := 0; trial < 8; trial++ {
			s, ps := newWorld(2, int64(seed+trial))
			ps[1].bad = true
			ps[1].write([]byte("expected chunk"))
			must(s.RunFor(4 * time.Second))
			st := ps[0].w.Stats()
			require(st.Rejected > 0 && st.Repaired == 0 && errors.Is(ps[0].w.LastErr(), m.ErrRejected), "invalid fetch stats=%+v", st)
			ks, err := ps[0].Keys()
			must(err)
			require(len(ks) == 0, "invalid data persisted")
			accounting(ps)
			ps[1].bad = false
			must(s.RunUntilConverged(s.Now().Add(5*time.Second), func() bool { return allGood(ps, [][]byte{[]byte("expected chunk")}) }))
		}
		return "8 fabricated-data peers produce ErrRejected and zero puts, then valid fetches recover"
	})
	check("C30", "sim-deadline-late-callback", func() string {
		s, ps := newWorld(2, seed)
		ps[0].delay = 3 * time.Second
		ps[1].write([]byte("late chunk"))
		must(s.RunFor(5 * time.Second))
		require(ps[0].w.Stats().Failed > 0 && ps[0].w.Stats().Repaired == 0, "deadline did not fence callbacks %+v", ps[0].w.Stats())
		ps[0].delay = 0
		must(s.RunUntilConverged(s.Now().Add(10*time.Second), func() bool { return allGood(ps, [][]byte{[]byte("late chunk")}) }))
		must(s.RunFor(4 * time.Second))
		require(ps[0].w.Stats().Repaired == 1, "stale callbacks applied twice")
		accounting(ps)
		return "callbacks delayed beyond 2s deadline cannot write and later rounds recover exactly once"
	})
	check("C30", "sim-put-failure-cap", func() string {
		s, ps := newWorld(2, seed)
		ds := dataSet()
		ps[0].putError = true
		for _, b := range ds {
			ps[1].write(b)
		}
		must(s.RunFor(3 * time.Second))
		st := ps[0].w.Stats()
		require(st.PutFailed > 0 && st.Deferred > 0 && st.Repaired == 0 && errors.Is(ps[0].w.LastErr(), e.ErrIO), "stats=%+v err=%v", st, ps[0].w.LastErr())
		ps[0].putError = false
		must(s.RunUntilConverged(s.Now().Add(20*time.Second), func() bool { return allGood(ps, ds) }))
		require(ps[0].w.Stats().Repaired == uint64(len(ds)), "repair count")
		accounting(ps)
		return "MaxRepairs=3 defers excess work, Put failures surface and all 37 chunks recover"
	})
	check("C21", "sim-prefix-reply-tight-bound", func() string {
		s, ps := newWorld(2, seed, 1)
		ps[1].prefixOnly = true
		ds := [][]byte{[]byte("prefix-0"), []byte("prefix-1")}
		p0 := m.Sum([]byte(m.Sum(ds[0]).String()))
		p1 := m.Sum([]byte(m.Sum(ds[1]).String()))
		require(p0[0]>>4 != p1[0]>>4, "fixture keys must use distinct leaves")
		for _, b := range ds {
			ps[1].write(b)
		}
		must(s.RunUntilConverged(s.Now().Add(5*time.Second), func() bool { return allGood(ps, ds) }))
		accounting(ps)
		st := ps[0].w.Stats()
		require(st.MaxRoundTrips <= 2, "2 replicas depth=1; remote holds prefix-0 and prefix-1 and answers one requested leaf at a time: Worker.MaxRoundTrips=%d > Depth+1=2; valid data converged", st.MaxRoundTrips)
		return "worker prefix replies obey tight bound"
	})
	check("C23", "sim-peer-vanishes", func() string {
		for trial := 0; trial < 3; trial++ {
			s, ps := newWorld(2, int64(seed+trial))
			ds := dataSet()
			for _, b := range ds {
				ps[1].write(b)
			}
			id := ps[1].env.ID
			ps[1].vanish = func() { must(s.Crash(id)) }
			must(s.RunFor(2 * time.Second))
			require(!s.Status(id).Up, "peer never vanished")
			st := ps[0].w.Stats()
			require(st.Failed > 0 && st.Repaired == 0, "vanished stats=%+v", st)
			err := ps[0].w.LastErr()
			require(errors.Is(err, e.ErrUnreachable) || errors.Is(err, e.ErrTimeout), "vanished err=%v", err)
			must(s.Restart(id))
			must(s.RunFor(time.Millisecond))
			must(s.RunUntilConverged(s.Now().Add(20*time.Second), func() bool { return allGood(ps, ds) }))
			accounting(ps)
		}
		return "3 seeds crash responder on level 1, fail without repair, then restart and recover all 37 chunks"
	})

	check("C26", "registered-codes", func() string {
		tab, err := e.NewCodeTable(m.CodeEntries()...)
		must(err)
		for _, row := range m.CodeEntries() {
			code, msg := tab.Encode(fmt.Errorf("wrapped: %w", row.Err))
			err := tab.Decode(code, msg)
			require(code == row.Code && errors.Is(err, row.Err) && errors.Is(err, e.ErrRemote), "code=%d", row.Code)
		}
		return "all code rows round-trip wrapped sentinels with ErrRemote"
	})
	check("C27", "worker-config-hooks", func() string {
		s, ps := newWorld(1, seed)
		p := ps[0]
		for _, cfg := range []m.Config{{Depth: 0}, {Depth: 17}, {Depth: 1, Period: -1}, {Depth: 1, Timeout: -1}, {Depth: 1, RoundDeadline: -1}, {Depth: 1, ScrubBatch: -1}, {Depth: 1, MaxRepairs: -1}} {
			_, err := m.NewWorker(p.env, cfg, p, p)
			require(err != nil, "invalid cfg accepted %+v", cfg)
		}
		must(p.w.Start())
		must(p.w.Record("", m.Hash{}))
		require(p.w.Forget("") && !p.w.Forget(""), "Forget semantics")
		require(errors.Is(p.w.Record(string(bytes.Repeat([]byte{'k'}, 1025)), m.Hash{}), m.ErrKey), "Record key cap")
		must(s.RunFor(time.Second))
		require(p.w.Stats().Rounds == 0, "self-only peer started round")
		return "seven invalid configurations reject, repeated Start is harmless and Record/Forget match inventory"
	})
}
