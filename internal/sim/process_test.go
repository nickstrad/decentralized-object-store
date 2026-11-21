package sim_test

import (
	"errors"
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
