package sim_test

import (
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

type gotDatagram struct {
	at   env.Time
	from env.NodeID
	msg  string
}

type gotReply struct {
	at    env.Time
	reply string
	err   error
}

type recorder struct {
	e         env.Env
	started   int
	datagrams []gotDatagram
	requests  []string
	replies   []gotReply
	onRequest func(r *recorder, from env.NodeID, req []byte, reply env.Reply)
	onStart   func(r *recorder)
}

func (r *recorder) Start() {
	r.started++
	if r.onStart != nil {
		r.onStart(r)
	}
}

func (r *recorder) OnDatagram(from env.NodeID, msg []byte) {
	r.datagrams = append(r.datagrams, gotDatagram{r.e.Clock.Now(), from, string(msg)})
}

func (r *recorder) OnRequest(from env.NodeID, req []byte, reply env.Reply) {
	r.requests = append(r.requests, string(req))
	if r.onRequest != nil {
		r.onRequest(r, from, req, reply)
		return
	}
	reply(append([]byte("echo:"), req...), nil)
}

func (r *recorder) EncodeState() []byte {
	out := []byte{byte(r.started)}
	for _, d := range r.datagrams {
		out = append(out, d.msg...)
	}
	return out
}

func (r *recorder) request(to env.NodeID, req string, timeout time.Duration) {
	r.e.Net.Request(to, []byte(req), timeout, func(reply []byte, err error) {
		r.replies = append(r.replies, gotReply{r.e.Clock.Now(), string(reply), err})
	})
}

type harness struct {
	t    *testing.T
	s    *sim.Sim
	recs map[env.NodeID][]*recorder
	hook map[env.NodeID]func(r *recorder)
}

func fixedNet() sim.NetConfig {
	nc := sim.DefaultNetConfig()
	nc.Latency = sim.Range{Min: time.Millisecond, Max: time.Millisecond}
	nc.InterRegionLatency = sim.Range{Min: 10 * time.Millisecond, Max: 10 * time.Millisecond}
	return nc
}

func newHarness(t *testing.T, cfg sim.Config, specs ...sim.NodeSpec) *harness {
	t.Helper()
	h := &harness{t: t, s: sim.New(cfg), recs: map[env.NodeID][]*recorder{}, hook: map[env.NodeID]func(*recorder){}}
	for _, spec := range specs {
		id := spec.ID
		if err := h.s.AddNode(spec, func(e env.Env) env.Process {
			r := &recorder{e: e}
			if f := h.hook[id]; f != nil {
				f(r)
			}
			h.recs[id] = append(h.recs[id], r)
			return r
		}); err != nil {
			t.Fatalf("AddNode %s: %v", id, err)
		}
	}
	if err := h.s.RunUntil(0); err != nil {
		t.Fatalf("start: %v", err)
	}
	return h
}

func (h *harness) rec(id env.NodeID) *recorder {
	h.t.Helper()
	rs := h.recs[id]
	if len(rs) == 0 {
		h.t.Fatalf("node %s never started", id)
	}
	return rs[len(rs)-1]
}

func (h *harness) run(d time.Duration) {
	h.t.Helper()
	if err := h.s.RunUntil(h.s.Now().Add(d)); err != nil {
		h.t.Fatalf("run: %v", err)
	}
}

func spec(id string, region string) sim.NodeSpec {
	return sim.NodeSpec{ID: env.NodeID(id), Region: env.Region(region)}
}

func twoNodes(t *testing.T, mut func(*sim.Config)) *harness {
	cfg := sim.Config{Seed: 1, Net: fixedNet()}
	if mut != nil {
		mut(&cfg)
	}
	return newHarness(t, cfg, spec("a", "r1"), spec("b", "r1"))
}

func mustNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
