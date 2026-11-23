package sim_test

import (
	"errors"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/sim"
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

// TestDatagramFaults proves each datagram network fault in isolation: latency,
// inter-region delay, drop, duplication, reordering, partitions, isolation,
// link loss, and HealNet recovery.
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

// TestClogHoldsTrafficUntilRelease proves that Clog buffers datagrams and
// requests until Unclog releases them in order at the release timestamp.
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

// TestHealNetReleasesClogs proves that HealNet releases traffic held on clogged links.
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

// TestMessagesAreCopied proves that mutating a sent slice after Send does not
// affect the bytes delivered to the peer.
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

// TestSizeLimits proves MaxDatagram and MaxMessage enforcement for datagrams,
// requests, and replies.
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

// TestSendErrors proves ErrUnreachable for unknown targets, ErrClosed on a
// crashed handle, and silence for Request callbacks on a closed incarnation.
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

// TestRequestResponse proves every request/response outcome: normal reply,
// lost request, lost response, refused connection, silent server timeout,
// wire error code mapping, discarded late reply, and invalid timeout.
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

// TestRequestToUnknownNodeIsUnreachable proves that requesting a nonexistent
// node fails asynchronously with ErrUnreachable.
func TestRequestToUnknownNodeIsUnreachable(t *testing.T) {
	h := twoNodes(t, nil)
	h.rec("a").request("ghost", "q", time.Second)
	h.run(time.Second)
	reps := h.rec("a").replies
	if len(reps) != 1 || !errors.Is(reps[0].err, env.ErrUnreachable) {
		t.Fatalf("replies %+v", reps)
	}
}

// TestAsyncReplyAfterFanOut proves that a server may defer Reply until a
// downstream Request finishes and return the combined result.
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

// TestReplyTwiceIsAProtocolBug proves that invoking Reply a second time fails
// the run with a *PanicError naming the offending node.
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

// TestRequestsOnOneConnectionStayInOrder proves that requests and replies on
// one directed link stay strictly FIFO despite wide random latency.
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

// TestNetworkArgumentErrors proves that topology fault methods return
// ErrUnknownNode, ErrUnknownRegion, or ErrInvalidArgument on bad inputs.
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

// TestUnknownServerErrorArrivesAsErrRemote proves that an unregistered server
// error crosses the wire as CodeRemote and decodes to *env.RemoteError.
func TestUnknownServerErrorArrivesAsErrRemote(t *testing.T) {
	h := twoNodes(t, nil)
	h.rec("b").onRequest = func(r *recorder, from env.NodeID, req []byte, reply env.Reply) {
		reply([]byte("ignored"), errors.New("disk on fire"))
	}
	h.rec("a").request("b", "q", time.Second)
	h.run(time.Second)
	reps := h.rec("a").replies
	if len(reps) != 1 || !errors.Is(reps[0].err, env.ErrRemote) || errors.Is(reps[0].err, env.ErrNotFound) || reps[0].reply != "" {
		t.Fatalf("replies %+v", reps)
	}
	var re *env.RemoteError
	if !errors.As(reps[0].err, &re) || re.Code != env.CodeRemote || re.Msg != "disk on fire" {
		t.Fatalf("remote error %#v", reps[0].err)
	}
}

type mutator struct{ e env.Env }

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

func (m *mutator) Start()                              {}
func (m *mutator) OnDatagram(_ env.NodeID, msg []byte) { zero(msg) }
func (m *mutator) EncodeState() []byte                 { return nil }
func (m *mutator) OnRequest(_ env.NodeID, req []byte, reply env.Reply) {
	zero(req)
	reply([]byte("pong"), nil)
}

// TestEventLogHashesThePayloadAsItCrossedTheWire proves that network payloads
// are hashed before handlers execute, even if handlers zero their input slices.
func TestEventLogHashesThePayloadAsItCrossedTheWire(t *testing.T) {
	s := sim.New(sim.Config{Seed: 1, Net: fixedNet()})
	procs := map[env.NodeID]*mutator{}
	for _, id := range []env.NodeID{"a", "b"} {
		mustNil(t, s.AddNode(sim.NodeSpec{ID: id, Region: "r"}, func(e env.Env) env.Process {
			m := &mutator{e: e}
			procs[e.ID] = m
			return m
		}))
	}
	mustNil(t, s.RunUntil(0))
	mustNil(t, procs["a"].e.Net.Send("b", []byte("hello")))
	procs["a"].e.Net.Request("b", []byte("ping"), time.Second, func(reply []byte, err error) {
		mustNil(t, err)
		zero(reply)
	})
	mustNil(t, s.RunUntilQuiet())
	want := [][2]string{{"datagram/ran", "hello"}, {"request/ran", "ping"}, {"response/ran", "pong"}}
	seen := 0
	for _, line := range s.LogTail(100) {
		for _, kp := range want {
			kind, payload := kp[0], kp[1]
			if !strings.Contains(line, " "+kind+" ") {
				continue
			}
			h := fnv.New64a()
			_, _ = h.Write([]byte(payload))
			if !strings.Contains(line, fmt.Sprintf("hash=%016x", h.Sum64())) {
				t.Fatalf("%s logged with the handler's bytes, not %q: %s", kind, payload, line)
			}
			seen++
		}
	}
	if seen != len(want) {
		t.Fatalf("found %d of %d payload records in %q", seen, len(want), s.LogTail(100))
	}
}
