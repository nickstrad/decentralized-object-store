package sim_test

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

func sendN(t *testing.T, h *harness, from, to env.NodeID, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		mustNil(t, h.rec(from).e.Net.Send(to, []byte(strconv.Itoa(i))))
	}
}

func threeNodes(t *testing.T, mut func(*sim.Config)) *harness {
	cfg := sim.Config{Seed: 1, Net: fixedNet()}
	if mut != nil {
		mut(&cfg)
	}
	return newHarness(t, cfg, spec("a", "r1"), spec("b", "r1"), spec("c", "r2"))
}

func TestDatagramFaults(t *testing.T) {
	tests := []struct {
		name  string
		cfg   func(*sim.Config)
		fault func(h *harness)
		from  env.NodeID
		to    env.NodeID
		n     int
		check func(t *testing.T, got []gotDatagram)
	}{
		{"a healthy link delivers every datagram after its latency", nil, nil, "a", "b", 5,
			func(t *testing.T, got []gotDatagram) {
				if len(got) != 5 || got[0].at != env.Time(time.Millisecond) || got[0].from != "a" {
					t.Fatalf("got %+v", got)
				}
			}},
		{"inter-region traffic uses the inter-region latency", nil, nil, "a", "c", 1,
			func(t *testing.T, got []gotDatagram) {
				if len(got) != 1 || got[0].at != env.Time(10*time.Millisecond) {
					t.Fatalf("got %+v", got)
				}
			}},
		{"drop at one million ppm drops everything", func(c *sim.Config) { c.Net.DropPPM = 1_000_000 }, nil, "a", "b", 20,
			func(t *testing.T, got []gotDatagram) {
				if len(got) != 0 {
					t.Fatalf("got %d datagrams", len(got))
				}
			}},
		{"duplicate at one million ppm delivers every datagram twice", func(c *sim.Config) { c.Net.DuplicatePPM = 1_000_000 }, nil, "a", "b", 10,
			func(t *testing.T, got []gotDatagram) {
				if len(got) != 20 {
					t.Fatalf("got %d datagrams", len(got))
				}
				count := map[string]int{}
				for _, d := range got {
					count[d.msg]++
				}
				for i := 0; i < 10; i++ {
					if count[strconv.Itoa(i)] != 2 {
						t.Fatalf("message %d delivered %d times", i, count[strconv.Itoa(i)])
					}
				}
			}},
		{"random latency reorders datagrams", func(c *sim.Config) {
			c.Net.Latency = sim.Range{Min: time.Millisecond, Max: 100 * time.Millisecond}
		}, nil, "a", "b", 50,
			func(t *testing.T, got []gotDatagram) {
				if len(got) != 50 {
					t.Fatalf("got %d", len(got))
				}
				inversions := 0
				for i := 1; i < len(got); i++ {
					x, _ := strconv.Atoi(got[i-1].msg)
					y, _ := strconv.Atoi(got[i].msg)
					if y < x {
						inversions++
					}
				}
				if inversions == 0 {
					t.Fatal("no reordering")
				}
			}},
		{"an asymmetric partition drops the blocked direction", nil, func(h *harness) { mustNil(t, h.s.PartitionOneWay([]env.NodeID{"a"}, []env.NodeID{"b"})) }, "a", "b", 5,
			func(t *testing.T, got []gotDatagram) {
				if len(got) != 0 {
					t.Fatalf("got %d", len(got))
				}
			}},
		{"an asymmetric partition lets the other direction flow", nil, func(h *harness) { mustNil(t, h.s.PartitionOneWay([]env.NodeID{"a"}, []env.NodeID{"b"})) }, "b", "a", 5,
			func(t *testing.T, got []gotDatagram) {
				if len(got) != 5 {
					t.Fatalf("got %d", len(got))
				}
			}},
		{"a symmetric partition drops both directions", nil, func(h *harness) {
			mustNil(t, h.s.Partition([]env.NodeID{"a"}, []env.NodeID{"b"}))
			sendN(t, h, "b", "a", 3)
		}, "a", "b", 5,
			func(t *testing.T, got []gotDatagram) {
				if len(got) != 0 {
					t.Fatalf("got %d", len(got))
				}
			}},
		{"a region partition cuts the region off", nil, func(h *harness) { mustNil(t, h.s.PartitionRegion("r2")) }, "a", "c", 5,
			func(t *testing.T, got []gotDatagram) {
				if len(got) != 0 {
					t.Fatalf("got %d", len(got))
				}
			}},
		{"a region partition leaves traffic inside the other side", nil, func(h *harness) { mustNil(t, h.s.PartitionRegion("r2")) }, "a", "b", 5,
			func(t *testing.T, got []gotDatagram) {
				if len(got) != 5 {
					t.Fatalf("got %d", len(got))
				}
			}},
		{"an isolated node hears nothing", nil, func(h *harness) { mustNil(t, h.s.Isolate("b")) }, "a", "b", 5,
			func(t *testing.T, got []gotDatagram) {
				if len(got) != 0 {
					t.Fatalf("got %d", len(got))
				}
			}},
		{"link loss at one million ppm drops the link", nil, func(h *harness) { mustNil(t, h.s.SetLinkLoss("a", "b", 1_000_000)) }, "a", "b", 10,
			func(t *testing.T, got []gotDatagram) {
				if len(got) != 0 {
					t.Fatalf("got %d", len(got))
				}
			}},
		{"link loss at half a million ppm drops about half", nil, func(h *harness) { mustNil(t, h.s.SetLinkLoss("a", "b", 500_000)) }, "a", "b", 2000,
			func(t *testing.T, got []gotDatagram) {
				if len(got) < 850 || len(got) > 1150 {
					t.Fatalf("got %d of 2000", len(got))
				}
			}},
		{"heal restores a partitioned link", nil, func(h *harness) {
			mustNil(t, h.s.Partition([]env.NodeID{"a"}, []env.NodeID{"b"}))
			mustNil(t, h.s.SetLinkLoss("a", "b", 1_000_000))
			h.s.HealNet()
		}, "a", "b", 5,
			func(t *testing.T, got []gotDatagram) {
				if len(got) != 5 {
					t.Fatalf("got %d", len(got))
				}
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := threeNodes(t, tc.cfg)
			if tc.fault != nil {
				tc.fault(h)
			}
			sendN(t, h, tc.from, tc.to, tc.n)
			h.run(time.Second)
			tc.check(t, h.rec(tc.to).datagrams)
		})
	}
}

func TestClogHoldsTrafficUntilRelease(t *testing.T) {
	h := twoNodes(t, nil)
	mustNil(t, h.s.Clog("a", "b"))
	sendN(t, h, "a", "b", 3)
	h.rec("a").request("b", "q", time.Hour)
	h.run(time.Second)
	if n := len(h.rec("b").datagrams) + len(h.rec("b").requests); n != 0 {
		t.Fatalf("clogged link delivered %d", n)
	}
	mustNil(t, h.s.Unclog("a", "b"))
	h.run(time.Second)
	got := h.rec("b").datagrams
	if len(got) != 3 || got[0].at != env.Time(time.Second) || got[0].msg != "0" || got[2].msg != "2" {
		t.Fatalf("after release got %+v", got)
	}
	if len(h.rec("a").replies) != 1 || h.rec("a").replies[0].err != nil {
		t.Fatalf("request after release: %+v", h.rec("a").replies)
	}
}

func TestHealNetReleasesClogs(t *testing.T) {
	h := twoNodes(t, nil)
	mustNil(t, h.s.Clog("a", "b"))
	sendN(t, h, "a", "b", 2)
	h.run(time.Second)
	h.s.HealNet()
	h.run(time.Second)
	if len(h.rec("b").datagrams) != 2 {
		t.Fatalf("got %d", len(h.rec("b").datagrams))
	}
}

func TestMessagesAreCopied(t *testing.T) {
	h := twoNodes(t, nil)
	buf := []byte("hello")
	mustNil(t, h.rec("a").e.Net.Send("b", buf))
	buf[0] = 'J'
	h.run(time.Second)
	if h.rec("b").datagrams[0].msg != "hello" {
		t.Fatalf("receiver saw sender's later write: %q", h.rec("b").datagrams[0].msg)
	}
}

func TestSizeLimits(t *testing.T) {
	h := twoNodes(t, func(c *sim.Config) { c.Net.MaxDatagram = 8; c.Net.MaxMessage = 16 })
	net := h.rec("a").e.Net
	if err := net.Send("b", make([]byte, 9)); !errors.Is(err, env.ErrTooLarge) {
		t.Fatalf("oversize Send = %v", err)
	}
	mustNil(t, net.Send("b", make([]byte, 8)))
	h.rec("a").request("b", strings.Repeat("x", 17), time.Second)
	h.rec("b").onRequest = func(r *recorder, from env.NodeID, req []byte, reply env.Reply) {
		reply(make([]byte, 100), nil)
	}
	h.rec("a").request("b", "small", time.Second)
	h.run(2 * time.Second)
	if len(h.rec("b").datagrams) != 1 {
		t.Fatalf("delivered %d datagrams", len(h.rec("b").datagrams))
	}
	reps := h.rec("a").replies
	if len(reps) != 2 || !errors.Is(reps[0].err, env.ErrTooLarge) || !errors.Is(reps[1].err, env.ErrTooLarge) {
		t.Fatalf("replies %+v", reps)
	}
	if len(h.rec("b").requests) != 1 {
		t.Fatalf("server saw %v", h.rec("b").requests)
	}
}

func TestSendErrors(t *testing.T) {
	h := twoNodes(t, nil)
	if err := h.rec("a").e.Net.Send("nobody", []byte("x")); !errors.Is(err, env.ErrUnreachable) {
		t.Fatalf("Send to unknown = %v", err)
	}
	old := h.rec("a")
	mustNil(t, h.s.Crash("a"))
	if err := old.e.Net.Send("b", []byte("x")); !errors.Is(err, env.ErrClosed) {
		t.Fatalf("Send from crashed incarnation = %v", err)
	}
	called := false
	old.e.Net.Request("b", []byte("x"), time.Second, func([]byte, error) { called = true })
	h.run(2 * time.Second)
	if called {
		t.Fatal("callback of a crashed incarnation ran")
	}
}

func TestRequestResponse(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(h *harness)
		timeout    time.Duration
		wantReply  string
		wantErr    error
		wantAt     env.Time
		serverSeen int
	}{
		{"a request gets its reply after a round trip", nil, time.Second, "echo:q", nil, env.Time(2 * time.Millisecond), 1},
		{"a lost request times out", func(h *harness) {
			mustNil(t, h.s.PartitionOneWay([]env.NodeID{"a"}, []env.NodeID{"b"}))
		}, time.Second, "", env.ErrTimeout, env.Time(time.Second), 0},
		{"a lost response times out the same way although the server ran", func(h *harness) {
			mustNil(t, h.s.PartitionOneWay([]env.NodeID{"b"}, []env.NodeID{"a"}))
		}, time.Second, "", env.ErrTimeout, env.Time(time.Second), 1},
		{"a crashed server refuses the connection", func(h *harness) {
			mustNil(t, h.s.Crash("b"))
		}, time.Second, "", env.ErrUnreachable, env.Time(2 * time.Millisecond), 0},
		{"a server that never replies causes a timeout", func(h *harness) {
			h.rec("b").onRequest = func(*recorder, env.NodeID, []byte, env.Reply) {}
		}, 300 * time.Millisecond, "", env.ErrTimeout, env.Time(300 * time.Millisecond), 1},
		{"a server error crosses as a code and maps back to its sentinel", func(h *harness) {
			h.rec("b").onRequest = func(r *recorder, from env.NodeID, req []byte, reply env.Reply) {
				reply(nil, fmt.Errorf("lookup %s: %w", req, env.ErrNotFound))
			}
		}, time.Second, "", env.ErrNotFound, env.Time(2 * time.Millisecond), 1},
		{"a reply after the timeout is discarded", func(h *harness) {
			h.rec("b").onRequest = func(r *recorder, from env.NodeID, req []byte, reply env.Reply) {
				r.e.Clock.AfterFunc(time.Second, func() { reply([]byte("late"), nil) })
			}
		}, 100 * time.Millisecond, "", env.ErrTimeout, env.Time(100 * time.Millisecond), 1},
		{"a non-positive timeout is an invalid argument", nil, 0, "", env.ErrInvalidArgument, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := twoNodes(t, nil)
			if tc.setup != nil {
				tc.setup(h)
			}
			h.rec("a").request("b", "q", tc.timeout)
			h.run(5 * time.Second)
			reps := h.rec("a").replies
			if len(reps) != 1 {
				t.Fatalf("callback ran %d times: %+v", len(reps), reps)
			}
			r := reps[0]
			if tc.wantErr == nil && r.err != nil || tc.wantErr != nil && !errors.Is(r.err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", r.err, tc.wantErr)
			}
			if r.reply != tc.wantReply || r.at != tc.wantAt {
				t.Fatalf("reply %q at %d, want %q at %d", r.reply, r.at, tc.wantReply, tc.wantAt)
			}
			if tc.serverSeen >= 0 && len(h.recs["b"]) > 0 && len(h.rec("b").requests) != tc.serverSeen {
				t.Fatalf("server saw %d requests, want %d", len(h.rec("b").requests), tc.serverSeen)
			}
		})
	}
}

func TestRequestToUnknownNodeIsUnreachable(t *testing.T) {
	h := twoNodes(t, nil)
	h.rec("a").request("ghost", "q", time.Second)
	h.run(time.Second)
	reps := h.rec("a").replies
	if len(reps) != 1 || !errors.Is(reps[0].err, env.ErrUnreachable) {
		t.Fatalf("replies %+v", reps)
	}
}

func TestAsyncReplyAfterFanOut(t *testing.T) {
	h := threeNodes(t, nil)
	h.rec("b").onRequest = func(r *recorder, from env.NodeID, req []byte, reply env.Reply) {
		r.e.Net.Request("c", []byte("inner"), time.Second, func(inner []byte, err error) {
			if err != nil {
				reply(nil, err)
				return
			}
			reply(append([]byte("outer+"), inner...), nil)
		})
	}
	h.rec("a").request("b", "q", time.Second)
	h.run(time.Second)
	reps := h.rec("a").replies
	if len(reps) != 1 || reps[0].err != nil || reps[0].reply != "outer+echo:inner" {
		t.Fatalf("replies %+v", reps)
	}
	if reps[0].at != env.Time(22*time.Millisecond) {
		t.Fatalf("reply at %d", reps[0].at)
	}
}

func TestReplyTwiceIsAProtocolBug(t *testing.T) {
	h := twoNodes(t, nil)
	h.rec("b").onRequest = func(r *recorder, from env.NodeID, req []byte, reply env.Reply) {
		reply(nil, nil)
		reply(nil, nil)
	}
	h.rec("a").request("b", "q", time.Second)
	err := h.s.RunUntil(env.Time(time.Second))
	var pe *sim.PanicError
	if !errors.As(err, &pe) || pe.Node != "b" {
		t.Fatalf("err = %v", err)
	}
}

func TestRequestsOnOneConnectionStayInOrder(t *testing.T) {
	h := twoNodes(t, func(c *sim.Config) {
		c.Net.Latency = sim.Range{Min: time.Millisecond, Max: 100 * time.Millisecond}
	})
	for i := 0; i < 50; i++ {
		h.rec("a").request("b", strconv.Itoa(i), time.Hour)
	}
	h.run(10 * time.Second)
	reqs := h.rec("b").requests
	reps := h.rec("a").replies
	if len(reqs) != 50 || len(reps) != 50 {
		t.Fatalf("got %d requests, %d replies", len(reqs), len(reps))
	}
	for i := 0; i < 50; i++ {
		if reqs[i] != strconv.Itoa(i) || reps[i].reply != "echo:"+strconv.Itoa(i) {
			t.Fatalf("out of order at %d: %q %q", i, reqs[i], reps[i].reply)
		}
	}
}

func TestNetworkArgumentErrors(t *testing.T) {
	h := twoNodes(t, nil)
	tests := []struct {
		name string
		err  error
		want error
	}{
		{"loss for an unknown node", h.s.SetLinkLoss("a", "zz", 5), sim.ErrUnknownNode},
		{"loss above one million ppm", h.s.SetLinkLoss("a", "b", 1_000_001), env.ErrInvalidArgument},
		{"clog an unknown node", h.s.Clog("zz", "a"), sim.ErrUnknownNode},
		{"unclog an unknown node", h.s.Unclog("a", "zz"), sim.ErrUnknownNode},
		{"isolate an unknown node", h.s.Isolate("zz"), sim.ErrUnknownNode},
		{"partition an unknown region", h.s.PartitionRegion("nowhere"), sim.ErrUnknownRegion},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !errors.Is(tc.err, tc.want) {
				t.Fatalf("err = %v, want %v", tc.err, tc.want)
			}
		})
	}
}
