package merkle_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/merkle"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
)

const (
	errorRunFor   = 10 * time.Second
	restartAfter  = 5 * time.Second
	refillBound   = 30 * time.Second
	wipeAt        = env.Time(12 * time.Second)
	readFailCount = 3
)

// TestCodeEntries proves that every entry returned by CodeEntries sits at or
// above env.CodeAppMin, round-trips through env.CodeTable, and unwraps to both
// its sentinel and env.ErrRemote.
func TestCodeEntries(t *testing.T) {
	entries := merkle.CodeEntries()
	tbl, err := env.NewCodeTable(entries...)
	if err != nil {
		t.Fatal(err)
	}
	want := []error{merkle.ErrMalformed, merkle.ErrDepth}
	if len(entries) != len(want) {
		t.Fatalf("%d entries, want %d", len(entries), len(want))
	}
	for i, e := range entries {
		if e.Code < env.CodeAppMin || e.Err != want[i] {
			t.Fatalf("entry %d = %+v", i, e)
		}
		wrapped := errors.Join(errors.New("context"), e.Err)
		code, msg := tbl.Encode(wrapped)
		if code != e.Code {
			t.Fatalf("Encode(%v) = %d, want %d", e.Err, code, e.Code)
		}
		back := tbl.Decode(code, msg)
		if !errors.Is(back, e.Err) || !errors.Is(back, env.ErrRemote) {
			t.Fatalf("Decode(%d) = %v, want %v and ErrRemote", code, back, e.Err)
		}
	}
	if merkle.CodeMalformed == merkle.CodeDepth {
		t.Fatal("codes collide")
	}
}

type fixedReplier struct {
	reply []byte
	err   error
}

func (f fixedReplier) Start()                        {}
func (f fixedReplier) OnDatagram(env.NodeID, []byte) {}
func (f fixedReplier) EncodeState() []byte           { return nil }
func (f fixedReplier) OnRequest(_ env.NodeID, _ []byte, reply env.Reply) {
	reply(f.reply, f.err)
}

type pairRun struct {
	codes    *env.CodeTable
	peer     sim.Factory
	cfg      func(w *world) merkle.Config
	wrap     func(s *sim.Sim, ids []env.NodeID) func(env.Transport) env.Transport
	during   func(s *sim.Sim, ids []env.NodeID)
	runFor   time.Duration
	seedPeer bool
}

func (p pairRun) run(seed int64) *sim.Sim {
	tbl := p.codes
	if tbl == nil {
		tbl = codes()
	}
	s := sim.New(sim.Config{Seed: seed, Codes: tbl})
	w := newWorld(s, 2)
	ids := sim.NodeIDs(2)
	w.ids, w.honest = ids, ids[:1]
	cfg := storeConfig{peers: ids, worker: w.workerConfig()}
	if p.cfg != nil {
		cfg.worker = p.cfg(w)
	}
	if p.wrap != nil {
		cfg.wrapNet = p.wrap(s, ids)
	}
	if err := s.AddNode(sim.NodeSpec{ID: ids[0], Region: "a"}, storeFactory(cfg)); err != nil {
		s.Fail(err)
		return remember(w)
	}
	peer := p.peer
	if peer == nil {
		peer = storeFactory(storeConfig{peers: ids, worker: w.workerConfig()})
	}
	if err := s.AddNode(sim.NodeSpec{ID: ids[1], Region: "b"}, peer); err != nil {
		s.Fail(err)
		return remember(w)
	}
	if p.seedPeer {
		s.At(env.Time(100*time.Millisecond), "seed-peer", func() {
			if st := storeOf(s, ids[1]); st != nil {
				for i := 0; i < 20; i++ {
					if err := st.ClientWrite([]byte{byte(i), 1, 2, 3}); err != nil {
						s.Fail(err)
					}
				}
			}
		})
	}
	if p.during != nil {
		p.during(s, ids)
	}
	runFor := p.runFor
	if runFor == 0 {
		runFor = errorRunFor
	}
	_ = s.RunFor(runFor)
	return remember(w)
}

func firstWorker(t *testing.T, s *sim.Sim) *merkle.Worker {
	t.Helper()
	take(s)
	st := storeOf(s, s.Nodes()[0])
	if st == nil || st.w == nil {
		t.Fatal("worker not running")
	}
	return st.w
}

// TestErrorPeerVanishesMidDiff proves that when a peer crashes mid-diff the
// round fails with a transport error without repairing anything, and the next
// round after the peer restarts repairs all missing chunks.
func TestErrorPeerVanishesMidDiff(t *testing.T) {
	crashed := false
	p := pairRun{
		seedPeer: true,
		wrap: func(s *sim.Sim, ids []env.NodeID) func(env.Transport) env.Transport {
			crashed = false
			return func(inner env.Transport) env.Transport {
				return crashAfterFirst{inner: inner, crash: func() {
					if !crashed {
						crashed = true
						if err := s.Crash(ids[1]); err != nil {
							s.Fail(err)
						}
						s.At(s.Now().Add(restartAfter), "restart", func() {
							if err := s.Restart(ids[1]); err != nil {
								s.Fail(err)
							}
						})
					}
				}}
			}
		},
		runFor: restartAfter + errorRunFor,
	}
	t.Run("the round fails with a transport error", func(t *testing.T) {
		p.runFor = restartAfter
		s := simtest.RequireDeterministic(t, 1, p.run)
		w := firstWorker(t, s)
		st := w.Stats()
		if st.Failed == 0 || !(errors.Is(w.LastErr(), env.ErrUnreachable) || errors.Is(w.LastErr(), env.ErrTimeout)) {
			t.Fatalf("stats %+v last error %v", st, w.LastErr())
		}
		if st.Repaired != 0 {
			t.Fatalf("repaired %d from a vanished peer", st.Repaired)
		}
	})
	t.Run("the next round after the peer returns is the retry", func(t *testing.T) {
		p.runFor = restartAfter + errorRunFor
		s := simtest.RequireDeterministic(t, 1, p.run)
		w := firstWorker(t, s)
		if st := w.Stats(); st.Failed == 0 || st.Repaired != 20 {
			t.Fatalf("stats %+v", st)
		}
	})
}

type crashAfterFirst struct {
	inner env.Transport
	crash func()
}

func (c crashAfterFirst) Send(to env.NodeID, msg []byte) error { return c.inner.Send(to, msg) }

func (c crashAfterFirst) Request(to env.NodeID, req []byte, timeout time.Duration, cb func([]byte, error)) {
	c.inner.Request(to, req, timeout, func(b []byte, err error) {
		if err == nil {
			c.crash()
		}
		cb(b, err)
	})
}

// TestErrorMalformedNode proves that garbage replies, zero-hash inner children,
// and remote ErrMalformed errors fail the worker round with ErrMalformed, and
// that malformed requests to OnRequest return CodeMalformed across the wire.
func TestErrorMalformedNode(t *testing.T) {
	cases := []struct {
		name  string
		reply []byte
		err   error
		want  error
	}{
		{"a garbage reply", []byte{0x02, 0x00}, nil, merkle.ErrMalformed},
		{"a reply naming a zero hash", append([]byte{0x02, 0, 1, 0x80, 0x00}, make([]byte, 32)...), nil, merkle.ErrMalformed},
		{"a peer that reports our request malformed", nil, merkle.ErrMalformed, merkle.ErrMalformed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := pairRun{peer: func(env.Env) env.Process { return fixedReplier{reply: c.reply, err: c.err} }}
			s := simtest.RequireDeterministic(t, 1, p.run)
			w := firstWorker(t, s)
			if st := w.Stats(); st.Failed == 0 || st.Completed != 0 || !errors.Is(w.LastErr(), c.want) {
				t.Fatalf("stats %+v last error %v", st, w.LastErr())
			}
		})
	}
	t.Run("a malformed request is answered with the malformed code", func(t *testing.T) {
		var seen error
		p := pairRun{during: func(s *sim.Sim, ids []env.NodeID) {
			s.At(env.Time(time.Second), "probe", func() {
				st := storeOf(s, ids[1])
				st.e.Net.Request(ids[0], []byte{tagMerkle, 0x07}, time.Second, func(_ []byte, err error) { seen = err })
			})
		}}
		s := simtest.RequireDeterministic(t, 1, p.run)
		take(s)
		if !errors.Is(seen, merkle.ErrMalformed) || !errors.Is(seen, env.ErrRemote) {
			t.Fatalf("probe error %v", seen)
		}
	})
}

// TestErrorDepthOverflow proves that diffing peers of different depths surfaces
// ErrDepth and env.ErrRemote when CodeEntries are registered, and only
// env.ErrRemote with env.DefaultCodes.
func TestErrorDepthOverflow(t *testing.T) {
	deeper := func(w *world) merkle.Config {
		c := w.workerConfig()
		c.Depth = 3
		return c
	}
	t.Run("a peer with a different depth fails the round with ErrDepth", func(t *testing.T) {
		p := pairRun{cfg: deeper, seedPeer: true}
		s := simtest.RequireDeterministic(t, 1, p.run)
		w := firstWorker(t, s)
		if st := w.Stats(); st.Failed == 0 || st.Repaired != 0 || !errors.Is(w.LastErr(), merkle.ErrDepth) || !errors.Is(w.LastErr(), env.ErrRemote) {
			t.Fatalf("stats %+v last error %v", st, w.LastErr())
		}
	})
	t.Run("without the registered codes the cause is only a remote error", func(t *testing.T) {
		p := pairRun{cfg: deeper, codes: env.DefaultCodes()}
		s := simtest.RequireDeterministic(t, 1, p.run)
		w := firstWorker(t, s)
		if errors.Is(w.LastErr(), merkle.ErrDepth) || !errors.Is(w.LastErr(), env.ErrRemote) {
			t.Fatalf("last error %v", w.LastErr())
		}
	})
}

type staticPeers []env.NodeID

func (p staticPeers) Peers() []env.NodeID { return p }

// TestNewWorkerValidation proves NewWorker argument validation (nil interfaces,
// out-of-range depths, negative settings) and basic Record/Forget behavior.
func TestNewWorkerValidation(t *testing.T) {
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
	store := &chunkStore{e: e}
	good := merkle.Config{Depth: 2}
	cases := []struct {
		name string
		cfg  merkle.Config
		m    merkle.Membership
		r    merkle.Repairer
		want error
	}{
		{"nil membership", good, nil, store, env.ErrInvalidArgument},
		{"nil repairer", good, staticPeers{"n"}, nil, env.ErrInvalidArgument},
		{"depth zero", merkle.Config{}, staticPeers{"n"}, store, merkle.ErrDepth},
		{"depth over the maximum", merkle.Config{Depth: merkle.MaxDepth + 1}, staticPeers{"n"}, store, merkle.ErrDepth},
		{"negative period", merkle.Config{Depth: 2, Period: -1}, staticPeers{"n"}, store, env.ErrInvalidArgument},
		{"negative timeout", merkle.Config{Depth: 2, Timeout: -1}, staticPeers{"n"}, store, env.ErrInvalidArgument},
		{"negative scrub batch", merkle.Config{Depth: 2, ScrubBatch: -1}, staticPeers{"n"}, store, env.ErrInvalidArgument},
		{"negative repair cap", merkle.Config{Depth: 2, MaxRepairs: -1}, staticPeers{"n"}, store, env.ErrInvalidArgument},
		{"negative deadline", merkle.Config{Depth: 2, RoundDeadline: -1}, staticPeers{"n"}, store, env.ErrInvalidArgument},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := merkle.NewWorker(e, c.cfg, c.m, c.r); !errors.Is(err, c.want) {
				t.Fatalf("NewWorker = %v, want %v", err, c.want)
			}
		})
	}
	w, err := merkle.NewWorker(e, good, staticPeers{"n"}, store)
	if err != nil {
		t.Fatal(err)
	}
	if w.Tree().Depth() != 2 || w.LastErr() != nil || len(w.EncodeState()) == 0 {
		t.Fatal("fresh worker state")
	}
	if err := w.Record("k", sum("v")); err != nil {
		t.Fatal(err)
	}
	if err := w.Record(string(make([]byte, merkle.MaxKeyLen+1)), sum("v")); !errors.Is(err, merkle.ErrKey) {
		t.Fatalf("Record of a long key = %v", err)
	}
	if !w.Forget("k") || w.Forget("k") {
		t.Fatal("Forget")
	}
}

func scrubRun(seed int64) *sim.Sim {
	w := newWorld(newSim(seed), merkle.DepthFor(dstChunks))
	if err := w.cluster(3, true, nil); err != nil {
		w.s.Fail(err)
		return remember(w)
	}
	w.writes(dstChunks, dstWriteStart, dstWriteWindow)
	victim := w.ids[0]
	w.s.At(wipeAt, "wipe", func() {
		if err := w.s.WipeDisk(victim); err != nil {
			w.s.Fail(err)
		}
	})
	w.s.At(wipeAt.Add(-2*time.Second), "read-errors", func() {
		if err := w.s.FailReads(w.ids[1], "", readFailCount); err != nil {
			w.s.Fail(err)
		}
	})
	w.quietAt = wipeAt
	if err := w.s.RunUntil(wipeAt); err != nil {
		return remember(w)
	}
	w.finish(wipeAt.Add(refillBound))
	return remember(w)
}

// TestScrubRefillsAWipedDiskDST proves that periodic scrubbing drops wiped
// blobs from the victim's tree, refills every chunk from healthy peers within
// refillBound, and counts injected disk read errors.
func TestScrubRefillsAWipedDiskDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		s := simtest.RequireDeterministic(t, seed, scrubRun)
		w := take(s)
		if w.convergeAt == 0 {
			t.Fatalf("wiped replica not refilled within %v", refillBound)
		}
		victim := storeOf(s, w.ids[0]).w.Stats()
		if victim.Dropped < uint64(len(w.keys)) || victim.Repaired < uint64(len(w.keys)) {
			t.Fatalf("victim dropped %d repaired %d of %d", victim.Dropped, victim.Repaired, len(w.keys))
		}
		if storeOf(s, w.ids[1]).w.Stats().ReadErrors == 0 {
			t.Fatal("injected read errors were not seen by the scrub")
		}
		t.Logf("refilled %d chunks in %v", victim.Repaired, w.convergeAt.Sub(wipeAt))
	})
}
