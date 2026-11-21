package merkle_test

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/merkle"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
)

const (
	stopAtRequest  = 2
	restartAt      = env.Time(4 * time.Second)
	lifecycleRun   = 12 * time.Second
	shortDeadline  = 200 * time.Millisecond
	merkleCodeLow  = 112
	merkleCodeHigh = 127
)

type stopProbe struct {
	sent      int
	afterStop int
	stopped   bool
	didStop   bool
	atStop    merkle.Stats
	atRestart merkle.Stats
	errAtStop error
	sentAtRe  int
}

var probes = map[*sim.Sim]*stopProbe{}

type probeNet struct {
	inner env.Transport
	on    func()
}

func (n probeNet) Send(to env.NodeID, msg []byte) error { return n.inner.Send(to, msg) }

func (n probeNet) Request(to env.NodeID, req []byte, timeout time.Duration, cb func([]byte, error)) {
	n.inner.Request(to, req, timeout, cb)
	n.on()
}

func stopRun(seed int64) *sim.Sim {
	probe := &stopProbe{}
	var s *sim.Sim
	worker := func() *merkle.Worker { return storeOf(s, s.Nodes()[0]).w }
	p := pairRun{
		seedPeer: true,
		wrap: func(sm *sim.Sim, ids []env.NodeID) func(env.Transport) env.Transport {
			s = sm
			return func(inner env.Transport) env.Transport {
				return probeNet{inner: inner, on: func() {
					probe.sent++
					if probe.stopped {
						probe.afterStop++
						return
					}
					if probe.sent == stopAtRequest {
						w := worker()
						w.Stop()
						w.Stop()
						probe.stopped = true
						probe.didStop = true
						probe.atStop = w.Stats()
						probe.errAtStop = w.LastErr()
					}
				}}
			}
		},
		during: func(sm *sim.Sim, ids []env.NodeID) {
			sm.At(restartAt, "restart-worker", func() {
				w := worker()
				probe.atRestart = w.Stats()
				probe.sentAtRe = probe.afterStop
				probe.stopped = false
				if err := w.Start(); err != nil {
					sm.Fail(err)
				}
				if err := w.Start(); err != nil {
					sm.Fail(err)
				}
			})
		},
		runFor: lifecycleRun,
	}
	out := p.run(seed)
	probes[out] = probe
	return out
}

func TestWorkerStop(t *testing.T) {
	s := simtest.RequireDeterministic(t, 1, stopRun)
	probe := probes[s]
	clear(probes)
	w := firstWorker(t, s)
	at := probe.atStop
	if !probe.didStop {
		t.Fatal("the worker was never stopped")
	}
	t.Run("stopping ends the round in flight as failed with its cost", func(t *testing.T) {
		if at.InFlight || at.Rounds != at.Completed+at.Failed || at.Failed != 1 {
			t.Fatalf("stats at stop %+v", at)
		}
		if at.RoundTrips != stopAtRequest-1 || at.MaxRoundTrips != stopAtRequest-1 {
			t.Fatalf("round trips at stop %d, want %d", at.RoundTrips, stopAtRequest-1)
		}
		if !errors.Is(probe.errAtStop, env.ErrClosed) {
			t.Fatalf("last error at stop %v, want ErrClosed", probe.errAtStop)
		}
	})
	t.Run("after Stop no tick, round, deadline or late reply changes anything", func(t *testing.T) {
		before, after := at, probe.atRestart
		before.Answered, after.Answered = 0, 0
		if before != after {
			t.Fatalf("stats moved while stopped:\n at stop    %+v\n at restart %+v", before, after)
		}
		if probe.sentAtRe != 0 {
			t.Fatalf("%d diff requests sent while stopped", probe.sentAtRe)
		}
	})
	t.Run("Start after Stop resumes rounds and repairs", func(t *testing.T) {
		st := w.Stats()
		if st.Rounds <= at.Rounds || st.Repaired != 20 {
			t.Fatalf("stats after restart %+v", st)
		}
	})
}

func TestWorkerStopBeforeStart(t *testing.T) {
	s := sim.New(sim.Config{Seed: 1})
	var e env.Env
	if err := s.AddNode(sim.NodeSpec{ID: "n", Region: "r"}, func(x env.Env) env.Process {
		e = x
		return fixedReplier{}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RunFor(time.Millisecond); err != nil {
		t.Fatal(err)
	}
	w, err := merkle.NewWorker(e, merkle.Config{Depth: 2}, staticPeers{"n"}, &chunkStore{e: e})
	if err != nil {
		t.Fatal(err)
	}
	w.Stop()
	if err := s.RunFor(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	if st := w.Stats(); st != (merkle.Stats{}) {
		t.Fatalf("a never-started worker did something: %+v", st)
	}
}

type swallowAfterFirst struct {
	inner env.Transport
	n     *int
}

func (n swallowAfterFirst) Send(to env.NodeID, msg []byte) error { return n.inner.Send(to, msg) }

func (n swallowAfterFirst) Request(to env.NodeID, req []byte, timeout time.Duration, cb func([]byte, error)) {
	*n.n++
	if *n.n == 1 {
		n.inner.Request(to, req, timeout, cb)
	}
}

func TestRoundDeadlineKeepsCost(t *testing.T) {
	p := pairRun{
		seedPeer: true,
		cfg: func(w *world) merkle.Config {
			c := w.workerConfig()
			c.RoundDeadline = shortDeadline
			return c
		},
		wrap: func(*sim.Sim, []env.NodeID) func(env.Transport) env.Transport {
			count := 0
			return func(inner env.Transport) env.Transport { return swallowAfterFirst{inner: inner, n: &count} }
		},
	}
	s := simtest.RequireDeterministic(t, 1, p.run)
	w := firstWorker(t, s)
	st := w.Stats()
	if st.Failed < 2 || st.Completed != 0 || !errors.Is(w.LastErr(), env.ErrTimeout) {
		t.Fatalf("stats %+v last error %v", st, w.LastErr())
	}
	if st.RoundTrips != 1 || st.MaxRoundTrips != 1 || st.Hashes == 0 || st.DiffBytes == 0 {
		t.Fatalf("the completed round trip of a round ended by its deadline was not counted: %+v", st)
	}
}

type truncatingPeer struct{}

func (truncatingPeer) Start()                        {}
func (truncatingPeer) OnDatagram(env.NodeID, []byte) {}
func (truncatingPeer) EncodeState() []byte           { return nil }
func (truncatingPeer) OnRequest(_ env.NodeID, req []byte, reply env.Reply) {
	if len(req) < 4 || req[0] != tagMerkle {
		reply(nil, env.ErrInvalidArgument)
		return
	}
	level := req[3]
	if level == 2 {
		reply([]byte{0x03, 2, 1, 0}, nil)
		return
	}
	out := []byte{0x02, level, 1, 0xff, 0xff}
	for i := 0; i < merkle.Fanout; i++ {
		out = append(out, bytes.Repeat([]byte{byte(i + 1)}, 32)...)
	}
	reply(out, nil)
}

func TestTruncatedRoundIsNotInSync(t *testing.T) {
	p := pairRun{
		peer: func(env.Env) env.Process { return truncatingPeer{} },
		cfg: func(w *world) merkle.Config {
			c := w.workerConfig()
			c.Limits = merkle.Limits{Frontier: 1}
			return c
		},
	}
	s := simtest.RequireDeterministic(t, 1, p.run)
	w := firstWorker(t, s)
	st := w.Stats()
	if st.Truncated == 0 || st.Completed == 0 || st.Failed != 0 {
		t.Fatalf("stats %+v last error %v", st, w.LastErr())
	}
	if st.InSync != 0 {
		t.Fatalf("a truncated round with no differences counted as in sync: %+v", st)
	}
}

func TestCodeRange(t *testing.T) {
	for _, c := range []env.Code{merkle.CodeMalformed, merkle.CodeDepth} {
		if c < merkleCodeLow || c > merkleCodeHigh {
			t.Fatalf("code %d outside the merkle block %d-%d", c, merkleCodeLow, merkleCodeHigh)
		}
	}
	for _, e := range merkle.CodeEntries() {
		if e.Code < merkleCodeLow || e.Code > merkleCodeHigh {
			t.Fatalf("entry %+v outside the merkle block %d-%d", e, merkleCodeLow, merkleCodeHigh)
		}
	}
}

type stopOnRead struct {
	w     **merkle.Worker
	reads int
	stop  int
}

func (r *stopOnRead) Keys() ([]string, error)        { return []string{"k"}, nil }
func (r *stopOnRead) Valid(string, merkle.Hash) bool { return true }
func (r *stopOnRead) Put(string, []byte) error       { return nil }
func (r *stopOnRead) Fetch(_ env.NodeID, _ string, done func([]byte, error)) {
	done(nil, env.ErrNotFound)
}
func (r *stopOnRead) Read(string) ([]byte, error) {
	r.reads++
	if r.reads == r.stop {
		(*r.w).Stop()
	}
	return []byte("v"), nil
}

func TestWorkerStopFromInsideATick(t *testing.T) {
	s := sim.New(sim.Config{Seed: 1})
	var e env.Env
	if err := s.AddNode(sim.NodeSpec{ID: "n", Region: "r"}, func(x env.Env) env.Process {
		e = x
		return fixedReplier{}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RunFor(time.Millisecond); err != nil {
		t.Fatal(err)
	}
	var w *merkle.Worker
	r := &stopOnRead{w: &w, stop: 2}
	w, err := merkle.NewWorker(e, merkle.Config{Depth: 1}, staticPeers{"n", "m"}, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Start(); err != nil {
		t.Fatal(err)
	}
	if err := s.RunFor(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	if st := w.Stats(); r.reads != 2 || st.Rounds != 0 || st.Scrubbed != 2 {
		t.Fatalf("a worker stopped during its scrub kept going: reads %d stats %+v", r.reads, st)
	}
}
