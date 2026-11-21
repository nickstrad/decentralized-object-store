package sim_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

func TestProcessFaults(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, h *harness)
	}{
		{"crash discards volatile state, timers and in-flight datagrams", func(t *testing.T, h *harness) {
			b := h.rec("b")
			fired := false
			b.e.Clock.AfterFunc(10*time.Millisecond, func() { fired = true })
			sendN(t, h, "a", "b", 3)
			mustNil(t, h.s.Crash("b"))
			h.run(time.Second)
			if fired || len(b.datagrams) != 0 {
				t.Fatalf("crashed node ran: fired=%v datagrams=%d", fired, len(b.datagrams))
			}
			if h.s.Process("b") != nil || h.s.Status("b").Up {
				t.Fatal("crashed node still up")
			}
		}},
		{"restart builds a fresh process over the same disk", func(t *testing.T, h *harness) {
			mustNil(t, h.rec("b").e.Disk.Write("k", []byte("v")))
			mustNil(t, h.rec("b").e.Disk.Sync("k"))
			before := h.s.Status("b").Incarnation
			mustNil(t, h.s.Crash("b"))
			mustNil(t, h.s.Restart("b"))
			h.run(0)
			if len(h.recs["b"]) != 2 || h.rec("b").started != 1 {
				t.Fatalf("restart did not build a new process: %d", len(h.recs["b"]))
			}
			if got, err := h.rec("b").e.Disk.Read("k"); err != nil || string(got) != "v" {
				t.Fatalf("disk after restart: %q %v", got, err)
			}
			if h.s.Status("b").Incarnation != before+1 {
				t.Fatalf("incarnation %d", h.s.Status("b").Incarnation)
			}
			sendN(t, h, "a", "b", 1)
			h.run(time.Second)
			if len(h.rec("b").datagrams) != 1 {
				t.Fatal("restarted node does not receive")
			}
		}},
		{"a datagram in flight across a restart reaches the new incarnation", func(t *testing.T, h *harness) {
			sendN(t, h, "a", "b", 1)
			mustNil(t, h.s.Crash("b"))
			mustNil(t, h.s.Restart("b"))
			h.run(time.Second)
			if len(h.rec("b").datagrams) != 1 {
				t.Fatalf("got %d", len(h.rec("b").datagrams))
			}
		}},
		{"a reply addressed to a crashed caller is dropped", func(t *testing.T, h *harness) {
			a := h.rec("a")
			a.request("b", "q", time.Second)
			h.run(time.Millisecond + time.Microsecond)
			mustNil(t, h.s.Crash("a"))
			mustNil(t, h.s.Restart("a"))
			h.run(2 * time.Second)
			if len(a.replies) != 0 || len(h.rec("a").replies) != 0 {
				t.Fatalf("reply delivered to a dead incarnation: %+v", a.replies)
			}
		}},
		{"pause holds events while time passes and resume delivers them in order", func(t *testing.T, h *harness) {
			b := h.rec("b")
			var firedAt env.Time = -1
			b.e.Clock.AfterFunc(5*time.Millisecond, func() { firedAt = b.e.Clock.Now() })
			mustNil(t, h.s.Pause("b"))
			sendN(t, h, "a", "b", 3)
			h.run(time.Second)
			if len(b.datagrams) != 0 || firedAt != -1 {
				t.Fatal("paused node ran")
			}
			if st := h.s.Status("b"); !st.Up || !st.Paused {
				t.Fatalf("status %+v", st)
			}
			mustNil(t, h.s.Resume("b"))
			h.run(time.Millisecond)
			if len(b.datagrams) != 3 || b.datagrams[0].msg != "0" || b.datagrams[2].msg != "2" {
				t.Fatalf("after resume: %+v", b.datagrams)
			}
			if firedAt != env.Time(time.Second) {
				t.Fatalf("timer fired at %d", firedAt)
			}
		}},
		{"a paused server makes its callers time out", func(t *testing.T, h *harness) {
			mustNil(t, h.s.Pause("b"))
			h.rec("a").request("b", "q", 100*time.Millisecond)
			h.run(time.Second)
			reps := h.rec("a").replies
			if len(reps) != 1 || !errors.Is(reps[0].err, env.ErrTimeout) {
				t.Fatalf("replies %+v", reps)
			}
		}},
		{"wipe-and-rejoin restarts the node on an empty disk", func(t *testing.T, h *harness) {
			mustNil(t, h.rec("b").e.Disk.Write("k", []byte("v")))
			mustNil(t, h.rec("b").e.Disk.Sync("k"))
			mustNil(t, h.s.WipeRejoin("b"))
			h.run(0)
			if len(h.recs["b"]) != 2 {
				t.Fatal("no new incarnation")
			}
			if _, err := h.rec("b").e.Disk.Read("k"); !errors.Is(err, env.ErrNotFound) {
				t.Fatalf("disk not empty: %v", err)
			}
		}},
		{"invalid process transitions are refused", func(t *testing.T, h *harness) {
			cases := []struct {
				err  error
				want error
			}{
				{h.s.Restart("a"), sim.ErrNodeUp},
				{h.s.Resume("a"), sim.ErrNotPaused},
				{h.s.Crash("zz"), sim.ErrUnknownNode},
				{h.s.Pause("zz"), sim.ErrUnknownNode},
			}
			mustNil(t, h.s.Crash("b"))
			cases = append(cases,
				struct{ err, want error }{h.s.Crash("b"), sim.ErrNodeDown},
				struct{ err, want error }{h.s.Pause("b"), sim.ErrNodeDown},
			)
			for i, c := range cases {
				if !errors.Is(c.err, c.want) {
					t.Fatalf("case %d: err = %v, want %v", i, c.err, c.want)
				}
			}
		}},
		{"crash of a paused node drops its held events", func(t *testing.T, h *harness) {
			mustNil(t, h.s.Pause("b"))
			sendN(t, h, "a", "b", 2)
			h.run(time.Second)
			mustNil(t, h.s.Crash("b"))
			mustNil(t, h.s.Restart("b"))
			h.run(time.Second)
			if len(h.rec("b").datagrams) != 0 {
				t.Fatalf("held datagrams survived crash: %d", len(h.rec("b").datagrams))
			}
		}},
		{"HealAll restarts crashed and resumes paused nodes", func(t *testing.T, h *harness) {
			mustNil(t, h.s.Crash("a"))
			mustNil(t, h.s.Pause("b"))
			h.s.HealAll()
			h.run(0)
			if st := h.s.Status("a"); !st.Up {
				t.Fatalf("a %+v", st)
			}
			if st := h.s.Status("b"); st.Paused {
				t.Fatalf("b %+v", st)
			}
			if h.s.LiveCount() != 2 {
				t.Fatalf("live %d", h.s.LiveCount())
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, twoNodes(t, nil))
		})
	}
}

func TestNodeQueries(t *testing.T) {
	h := threeNodes(t, nil)
	ids := h.s.Nodes()
	if len(ids) != 3 || ids[0] != "a" || ids[2] != "c" {
		t.Fatalf("Nodes = %v", ids)
	}
	if h.s.Region("c") != "r2" {
		t.Fatalf("Region = %q", h.s.Region("c"))
	}
	if h.s.Status("zz").Known {
		t.Fatal("unknown node reported as known")
	}
	if h.s.Disk("zz") != nil || h.s.Process("zz") != nil {
		t.Fatal("unknown node has a disk or process")
	}
	if regions := h.s.Regions(); len(regions) != 2 || regions[0] != "r1" {
		t.Fatalf("Regions = %v", regions)
	}
}

func TestReleaseKeepsFIFOAtTheReleaseInstant(t *testing.T) {
	const T = env.Time(50 * time.Millisecond)
	t.Run("a request arriving exactly when its clogged link is released waits for the held one", func(t *testing.T) {
		h := twoNodes(t, nil)
		mustNil(t, h.s.Clog("a", "b"))
		h.rec("a").request("b", "first", time.Hour)
		h.s.At(T, "unclog", func() { mustNil(t, h.s.Unclog("a", "b")) })
		h.s.At(T-env.Time(time.Millisecond), "send second", func() { h.rec("a").request("b", "second", time.Hour) })
		h.run(time.Second)
		if got := h.rec("b").requests; len(got) != 2 || got[0] != "first" || got[1] != "second" {
			t.Fatalf("server order %v", got)
		}
	})
	t.Run("a datagram arriving exactly when its node resumes waits for the held ones", func(t *testing.T) {
		h := twoNodes(t, nil)
		mustNil(t, h.s.Pause("b"))
		mustNil(t, h.rec("a").e.Net.Send("b", []byte("first")))
		h.s.At(T, "resume", func() { mustNil(t, h.s.Resume("b")) })
		h.s.At(T-env.Time(time.Millisecond), "send second", func() { mustNil(t, h.rec("a").e.Net.Send("b", []byte("second"))) })
		h.run(time.Second)
		got := h.rec("b").datagrams
		if len(got) != 2 || got[0].msg != "first" || got[1].msg != "second" {
			t.Fatalf("delivery order %+v", got)
		}
	})
	t.Run("stopping a released timer does not wedge the node", func(t *testing.T) {
		h := twoNodes(t, nil)
		b := h.rec("b")
		fired := false
		tm := b.e.Clock.AfterFunc(time.Millisecond, func() { fired = true })
		mustNil(t, h.s.Pause("b"))
		h.s.At(T, "resume then stop", func() {
			mustNil(t, h.s.Resume("b"))
			if !tm.Stop() {
				t.Error("Stop of a released timer returned false")
			}
		})
		h.s.At(T+1, "send", func() { mustNil(t, h.rec("a").e.Net.Send("b", []byte("x"))) })
		h.run(time.Second)
		if fired || len(b.datagrams) != 1 {
			t.Fatalf("fired=%v datagrams=%d", fired, len(b.datagrams))
		}
	})
	t.Run("a reply released at the same instant cancels its pending timeout", func(t *testing.T) {
		h := twoNodes(t, nil)
		mustNil(t, h.s.Clog("b", "a"))
		h.rec("a").request("b", "q", 200*time.Millisecond)
		h.s.At(T, "unclog", func() { mustNil(t, h.s.Unclog("b", "a")) })
		h.run(time.Second)
		reps := h.rec("a").replies
		if len(reps) != 1 || reps[0].err != nil || reps[0].at != T {
			t.Fatalf("replies %+v", reps)
		}
	})
}

func TestErrorMessagesNameTheSeed(t *testing.T) {
	s := sim.New(sim.Config{Seed: 31, MaxSteps: 1})
	s.AddInvariant("never", 1, func() error { return errors.New("nope") })
	s.At(1, "x", func() {})
	err := s.RunUntilQuiet()
	if err == nil || !strings.Contains(err.Error(), "seed 31") || !strings.Contains(err.Error(), `"never"`) {
		t.Fatalf("invariant error %v", err)
	}
	s2 := sim.New(sim.Config{Seed: 32, MaxSteps: 1})
	s2.At(1, "x", func() {})
	s2.At(2, "y", func() {})
	err = s2.RunUntilQuiet()
	if err == nil || !strings.Contains(err.Error(), "seed 32") || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("stall error %v", err)
	}
}

func TestInvalidNetConfigFailsTheRun(t *testing.T) {
	nc := sim.DefaultNetConfig()
	nc.Latency = sim.Range{Min: 5, Max: 1}
	s := sim.New(sim.Config{Seed: 1, Net: nc})
	if _, err := s.Step(); !errors.Is(err, env.ErrInvalidArgument) {
		t.Fatalf("Step = %v", err)
	}
	nc = sim.DefaultNetConfig()
	nc.DropPPM = sim.PPM + 1
	if err := sim.New(sim.Config{Seed: 1, Net: nc}).Err(); !errors.Is(err, env.ErrInvalidArgument) {
		t.Fatalf("Err = %v", err)
	}
}

func TestReplyFromACrashedServerIsDropped(t *testing.T) {
	h := twoNodes(t, nil)
	var pending env.Reply
	h.rec("b").onRequest = func(r *recorder, from env.NodeID, req []byte, reply env.Reply) { pending = reply }
	h.rec("a").request("b", "q", 100*time.Millisecond)
	h.run(10 * time.Millisecond)
	mustNil(t, h.s.Crash("b"))
	mustNil(t, h.s.Restart("b"))
	pending([]byte("from a dead incarnation"), nil)
	h.run(time.Second)
	reps := h.rec("a").replies
	if len(reps) != 1 || !errors.Is(reps[0].err, env.ErrTimeout) {
		t.Fatalf("replies %+v", reps)
	}
}

func TestEventLogKeepsTheLastLogCapRecords(t *testing.T) {
	s := sim.New(sim.Config{Seed: 1, LogCap: 3})
	for i := 1; i <= 10; i++ {
		s.At(env.Time(i), "act", func() {})
	}
	mustNil(t, s.RunUntilQuiet())
	if s.LogLen() != 10 || !s.LogTruncated() {
		t.Fatalf("LogLen %d truncated %v", s.LogLen(), s.LogTruncated())
	}
	tail := s.LogTail(5)
	if len(tail) != 3 || !strings.HasPrefix(tail[0], "t=8 ") || !strings.HasPrefix(tail[2], "t=10 ") {
		t.Fatalf("tail %q", tail)
	}
	var b strings.Builder
	mustNil(t, s.WriteLog(&b))
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 4 || lines[0] != "# log truncated: first 7 of 10 records dropped" || !strings.Contains(lines[3], "action/ran") {
		t.Fatalf("log %q", lines)
	}
	full := sim.New(sim.Config{Seed: 1})
	for i := 1; i <= 10; i++ {
		full.At(env.Time(i), "act", func() {})
	}
	mustNil(t, full.RunUntilQuiet())
	if full.Digests() != s.Digests() {
		t.Fatal("LogCap changed the event digest")
	}
}
