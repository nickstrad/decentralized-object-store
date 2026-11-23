package occ_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/sim"
)

func newSim(t testing.TB, seed int64) *sim.Sim {
	t.Helper()
	codes, err := occ.Codes()
	if err != nil {
		t.Fatal(err)
	}
	return sim.New(sim.Config{Seed: seed, Codes: codes, Net: sim.DefaultNetConfig()})
}

func occNode(s *sim.Sim, id env.NodeID) *occ.Node {
	p := s.Process(id)
	if p == nil {
		return nil
	}
	if u, ok := p.(interface{ Unwrap() *occ.Node }); ok {
		return u.Unwrap()
	}
	n, _ := p.(*occ.Node)
	return n
}

func run(t testing.TB, s *sim.Sim, d time.Duration) {
	t.Helper()
	if err := s.RunFor(d); err != nil {
		t.Fatal(err)
	}
}

type probe struct {
	e       env.Env
	replies []probeReply
}

type probeReply struct {
	data []byte
	err  error
}

func (p *probe) Start()                                        {}
func (p *probe) OnDatagram(env.NodeID, []byte)                 {}
func (p *probe) OnRequest(_ env.NodeID, _ []byte, r env.Reply) { r(nil, nil) }
func (p *probe) EncodeState() []byte                           { return []byte{byte(len(p.replies))} }

func (p *probe) ask(to env.NodeID, req []byte) {
	p.e.Net.Request(to, req, time.Second, func(reply []byte, err error) {
		p.replies = append(p.replies, probeReply{reply, err})
	})
}

func addProbe(t *testing.T, s *sim.Sim) *probe {
	p := &probe{}
	if err := s.AddNode(sim.NodeSpec{ID: "probe", Region: "r"}, func(e env.Env) env.Process { p.e = e; return p }); err != nil {
		t.Fatal(err)
	}
	return p
}

type nodeWorld struct {
	or   *oracle
	logs map[env.NodeID][]string
}

func loggedFactory(ids []env.NodeID, w *nodeWorld, wrap func(env.Env) env.Env) sim.Factory {
	return func(e env.Env) env.Process {
		if wrap != nil {
			e = wrap(e)
		}
		id := e.ID
		cfg := occ.Config{Membership: occ.StaticMembership(ids)}
		if w != nil {
			cfg.OnDiscard = w.or.discard
			cfg.Log = func(line string) { w.logs[id] = append(w.logs[id], line) }
		}
		return occ.NewNode(e, cfg)
	}
}

func twoNodesWith(t *testing.T, seed int64, w *nodeWorld, wrap func(env.Env) env.Env) (*sim.Sim, []env.NodeID) {
	s := newSim(t, seed)
	ids := []env.NodeID{"n1", "n2"}
	for _, id := range ids {
		if err := s.AddNode(sim.NodeSpec{ID: id, Region: "r"}, loggedFactory(ids, w, wrap)); err != nil {
			t.Fatal(err)
		}
	}
	run(t, s, 10*time.Millisecond)
	return s, ids
}

func twoNodes(t *testing.T, seed int64) (*sim.Sim, []env.NodeID) {
	return twoNodesWith(t, seed, nil, nil)
}

func ackedPut(t *testing.T, w *nodeWorld, n *occ.Node, key, val string, p occ.Precondition) occ.Version {
	t.Helper()
	held := n.Record(key)
	v, err := n.Put(key, []byte(val), p)
	if err != nil {
		t.Fatal(err)
	}
	if w != nil {
		w.or.record(key, v, held)
	}
	return v
}

// TestNodePersistsAndReloadsItsEpoch proves that a crashed and restarted node
// reloads its acknowledged writes, actor epoch, and dot counter without taking
// a fresh epoch.
func TestNodePersistsAndReloadsItsEpoch(t *testing.T) {
	s, ids := twoNodes(t, 1)
	n := occNode(s, ids[0])
	before := n.Actor()
	v, err := n.Put("k", []byte("v"), occ.IfNoneMatch())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Crash(ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.Restart(ids[0]); err != nil {
		t.Fatal(err)
	}
	run(t, s, 10*time.Millisecond)
	n = occNode(s, ids[0])
	got, ok := n.Record("k").Version()
	if !n.Ready() || n.Actor() != before || !ok || got.ID() != v.ID() {
		t.Fatal("a crash lost the acknowledged write or the epoch")
	}
	if n.Stats().Fresh != 0 {
		t.Fatal("a restart with an intact disk must not take a fresh epoch")
	}
	w, err := n.Put("k", []byte("w"), occ.IfMatch(v.ETag()))
	if err != nil || w.Dot().Counter <= v.Dot().Counter {
		t.Fatal("the dot counter did not survive the restart")
	}
}

func wipeScenario(t *testing.T) error {
	w := &nodeWorld{or: newOracle(), logs: map[env.NodeID][]string{}}
	s, ids := twoNodesWith(t, 3, w, nil)
	a := occNode(s, ids[0])
	first := a.Actor()
	ackedPut(t, w, a, "k", "v1", occ.IfNoneMatch())
	v2 := ackedPut(t, w, a, "k", "v2", occ.IfMatch(a.Record("k").ETag()))
	run(t, s, 3*time.Second)
	if got, _ := occNode(s, ids[1]).Record("k").Version(); got.ID() != v2.ID() {
		t.Fatal("v2 did not replicate before the wipe")
	}
	if err := s.Isolate(ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.WipeRejoin(ids[0]); err != nil {
		t.Fatal(err)
	}
	run(t, s, 10*time.Millisecond)
	a = occNode(s, ids[0])
	if a.Stats().Fresh != 1 {
		t.Fatal("a wiped node must start fresh")
	}
	if a.Actor() == first {
		t.Log("the wiped node reuses its old actor")
	}
	ackedPut(t, w, a, "k", "after-wipe-1", occ.IfNoneMatch())
	ackedPut(t, w, a, "k", "after-wipe-2", occ.IfMatch(a.Record("k").ETag()))
	ackedPut(t, w, a, "k", "after-wipe-3", occ.IfMatch(a.Record("k").ETag()))
	s.HealNet()
	run(t, s, 5*time.Second)
	b := occNode(s, ids[1])
	docs := []*occ.Doc{a.Doc(), b.Doc()}
	if !docs[0].Equal(docs[1]) {
		t.Fatal("the replicas did not converge after the heal")
	}
	if err := w.or.check(docs, true); err != nil {
		return err
	}
	if b.LWWDiscards() != 1 || len(w.logs[ids[1]]) != 1 || b.DiscardTail()[0].Loser.ID() != v2.ID() {
		t.Fatalf("the discard of v2 must be on b's record: %d %v", b.LWWDiscards(), w.logs)
	}
	return nil
}

// TestWipedNodeTakesAFreshEpoch proves that a wiped node draws a new epoch so
// its post-wipe writes do not appear causal over pre-wipe writes held by peers,
// and verifies that enabling hazardReuseEpoch makes the oracle catch the loss.
func TestWipedNodeTakesAFreshEpoch(t *testing.T) {
	if err := wipeScenario(t); err != nil {
		t.Fatalf("with epochs, the write lost to the wiped node's new writes must be on the record: %v", err)
	}
	restore := occ.SetHazardReuseEpoch(true)
	defer restore()
	if wipeScenario(t) == nil {
		t.Fatal("with the epoch switched off the reused actor must make the discard look causal and lose v2 with no record; the test cannot see the hazard")
	}
}

// TestCorruptStateTakesAFreshEpoch proves that bit-rot in StateBlob is detected
// on restart, counted in Stats.Corrupt, and answered with a fresh actor epoch.
func TestCorruptStateTakesAFreshEpoch(t *testing.T) {
	s, ids := twoNodes(t, 4)
	n := occNode(s, ids[0])
	first := n.Actor()
	if _, err := n.Put("k", []byte("v"), occ.IfNoneMatch()); err != nil {
		t.Fatal(err)
	}
	if err := s.Crash(ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.BitRot(ids[0], occ.StateBlob); err != nil {
		t.Fatal(err)
	}
	if err := s.Restart(ids[0]); err != nil {
		t.Fatal(err)
	}
	run(t, s, 10*time.Millisecond)
	n = occNode(s, ids[0])
	if n.Stats().Corrupt != 1 || n.Actor() == first || n.Actor().Node != ids[0] {
		t.Fatal("a corrupt state blob must be detected and answered with a fresh epoch")
	}
}

// TestNodeRetriesLoadAfterReadErrors proves that transient StateBlob read errors
// keep the node ErrNotReady until retries succeed and reload its own state.
func TestNodeRetriesLoadAfterReadErrors(t *testing.T) {
	s, ids := twoNodes(t, 5)
	n := occNode(s, ids[0])
	v, err := n.Put("k", []byte("v"), occ.IfNoneMatch())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Crash(ids[0]); err != nil {
		t.Fatal(err)
	}
	if err := s.FailReads(ids[0], occ.StateBlob, 3); err != nil {
		t.Fatal(err)
	}
	if err := s.Restart(ids[0]); err != nil {
		t.Fatal(err)
	}
	run(t, s, time.Millisecond)
	n = occNode(s, ids[0])
	if _, err := n.Reader(); n.Ready() || n.Doc() != nil || n.Digest() != ([32]byte{}) || !errors.Is(err, occ.ErrNotReady) {
		t.Fatal("a node whose state cannot be read must not serve")
	}
	if n.Record("k").Present() || n.Actor() != (occ.Actor{}) || n.LWWDiscards() != 0 || n.DiscardTail() != nil || n.AheadStamps() != 0 {
		t.Fatal("a node that is not ready reports nothing")
	}
	for _, err := range []error{
		func() error { _, _, err := n.Get("k"); return err }(),
		func() error { _, err := n.Put("k", nil, occ.IfNoneMatch()); return err }(),
		func() error { _, err := n.Delete("k", occ.IfNoneMatch()); return err }(),
		func() error { _, err := n.SetScalar("k", nil); return err }(),
		func() error { _, err := n.UpdateQuota("k", 1, occ.AddUsage(1, 0, occ.Usage{})); return err }(),
		func() error {
			_, _, err := n.RecountQuota("k", 1, func(string) (occ.Usage, error) { return occ.Usage{}, nil })
			return err
		}(),
	} {
		if !errors.Is(err, occ.ErrNotReady) {
			t.Fatalf("err = %v, want ErrNotReady", err)
		}
	}
	run(t, s, time.Second)
	n = occNode(s, ids[0])
	got, _ := n.Record("k").Version()
	if !n.Ready() || n.Stats().LoadErrors != 3 || got.ID() != v.ID() || n.Stats().Fresh != 0 || n.AheadStamps() != 0 {
		t.Fatalf("after the read errors cleared the node must load its own state (load errors %d)", n.Stats().LoadErrors)
	}
}

// TestNodeWireProtocol proves wire request/reply handling, error-code framing,
// Client remote operations, and malformed datagram counting.
func TestNodeWireProtocol(t *testing.T) {
	s, _ := twoNodes(t, 6)
	p := addProbe(t, s)
	if err := s.Pause("n1"); err != nil {
		t.Fatal(err)
	}
	run(t, s, 10*time.Millisecond)
	if err := s.Resume("n1"); err != nil {
		t.Fatal(err)
	}
	run(t, s, 10*time.Millisecond)
	n := occNode(s, "n1")
	if _, err := n.Put("k", []byte("v"), occ.IfNoneMatch()); err != nil {
		t.Fatal(err)
	}
	good := n.Doc().Encode()
	reqs := []struct {
		name string
		req  []byte
		want error
	}{
		{"empty request", nil, occ.ErrMalformed},
		{"unknown kind", []byte{99}, occ.ErrMalformed},
		{"short digest", []byte{occ.MsgDigest, 1}, occ.ErrMalformed},
		{"malformed push", []byte{occ.MsgPush, 1, 2}, occ.ErrMalformed},
		{"malformed op", []byte{occ.MsgOp, 9}, occ.ErrMalformed},
		{"op with a bad precondition", append(occ.EncodeOpRequest(occ.OpGet, "k", occ.Unconditional(), nil)[:4], 7), occ.ErrMalformed},
		{"delete carrying a value", occ.EncodeOpRequest(occ.OpDelete, "k", occ.Unconditional(), []byte("x")), occ.ErrMalformed},
		{"get of a missing key", occ.EncodeOpRequest(occ.OpGet, "missing", occ.Unconditional(), nil), occ.ErrNotFound},
		{"stale put", occ.EncodeOpRequest(occ.OpPut, "k", occ.IfMatch(occ.ETag{1}), []byte("x")), occ.ErrPreconditionFailed},
		{"create of an existing key", occ.EncodeOpRequest(occ.OpPut, "k", occ.IfNoneMatch(), []byte("x")), occ.ErrPreconditionFailed},
		{"push of a valid state", append([]byte{occ.MsgPush}, good...), nil},
		{"digest of an equal state", append([]byte{occ.MsgDigest}, digestOf(good)...), nil},
		{"blind put", occ.EncodeOpRequest(occ.OpPut, "k", occ.Unconditional(), []byte("x")), nil},
		{"get", occ.EncodeOpRequest(occ.OpGet, "k", occ.Unconditional(), nil), nil},
	}
	for _, r := range reqs {
		p.ask("n1", r.req)
		run(t, s, 50*time.Millisecond)
		got := p.replies[len(p.replies)-1]
		if r.want == nil && got.err != nil || r.want != nil && (!errors.Is(got.err, r.want) || !errors.Is(got.err, env.ErrRemote)) {
			t.Fatalf("%s: err = %v, want %v", r.name, got.err, r.want)
		}
	}
	res, err := occ.DecodeResult(p.replies[len(p.replies)-1].data)
	if err != nil || string(res.Value) != "x" || res.ETag != n.Record("k").ETag() {
		t.Fatalf("get result: %v", err)
	}
	cl := occ.NewClient(p.e.Net, time.Second)
	var delRes occ.Result
	var delErr error
	cl.Delete("n1", "k", occ.IfMatch(n.Record("k").ETag()), func(r occ.Result, err error) { delRes, delErr = r, err })
	run(t, s, 50*time.Millisecond)
	tomb, _ := n.Record("k").Version()
	if delErr != nil || delRes.ETag != tomb.ETag() || delRes.Version != tomb.ID() || !tomb.Tombstone() {
		t.Fatalf("client delete: %v", delErr)
	}
	before := n.Stats().Malformed
	for _, msg := range [][]byte{nil, {99}, {occ.MsgDelta, 1}} {
		if err := p.e.Net.Send("n1", msg); err != nil {
			t.Fatal(err)
		}
	}
	run(t, s, 50*time.Millisecond)
	if got := n.Stats().Malformed - before; got != 3 {
		t.Fatalf("malformed datagrams counted %d", got)
	}
}

func digestOf(b []byte) []byte {
	d, err := occ.Decode(b)
	if err != nil {
		return nil
	}
	x := d.Digest()
	return x[:]
}

type flakyDisk struct {
	env.Disk
	failWrite, failSync bool
}

func (d *flakyDisk) Write(name string, data []byte) error {
	if d.failWrite {
		return env.ErrIO
	}
	return d.Disk.Write(name, data)
}

func (d *flakyDisk) Sync(name string) error {
	if d.failSync {
		return env.ErrIO
	}
	return d.Disk.Sync(name)
}

type nodeView struct {
	doc      string
	etag     occ.ETag
	actor    occ.Actor
	discards uint64
	tail     int
	logs     int
}

func viewOf(n *occ.Node, w *nodeWorld) nodeView {
	logs := w.or.logged
	for _, l := range w.logs {
		logs += len(l)
	}
	return nodeView{string(n.Doc().Encode()), n.Record("k").ETag(), n.Actor(), n.LWWDiscards(), len(n.DiscardTail()), logs}
}

// TestNodeFailedPersistLeavesNoTrace proves that Disk.Write or Disk.Sync errors
// during Node writes or inbound pushes roll back all state, emit no discards,
// replicate nothing, and allow later writes on the pre-failure ETag after recovery.
func TestNodeFailedPersistLeavesNoTrace(t *testing.T) {
	ops := []struct {
		name string
		do   func(s *sim.Sim, p *probe, n *occ.Node) error
	}{
		{"put", func(_ *sim.Sim, _ *probe, n *occ.Node) error {
			_, err := n.Put("k", []byte("new"), occ.IfMatch(n.Record("k").ETag()))
			return err
		}},
		{"delete", func(_ *sim.Sim, _ *probe, n *occ.Node) error {
			_, err := n.Delete("k", occ.IfMatch(n.Record("k").ETag()))
			return err
		}},
		{"set scalar", func(_ *sim.Sim, _ *probe, n *occ.Node) error {
			_, err := n.SetRoute("b", "r")
			return err
		}},
		{"update quota", func(_ *sim.Sim, _ *probe, n *occ.Node) error {
			_, err := n.UpdateQuota(occ.QuotaKey("b"), 2, occ.AddUsage(1, 1, occ.Usage{}))
			return err
		}},
		{"inbound push carrying a discard", func(s *sim.Sim, p *probe, n *occ.Node) error {
			d := occ.NewDoc()
			d.MergeVersion("k", version("zz", 1, occ.VersionVector{}, false, "theirs", st(int64(s.LocalTime("n1"))+int64(time.Second), 0, "zz")))
			p.ask("n1", append([]byte{occ.MsgPush}, d.Encode()...))
			if err := s.RunFor(50 * time.Millisecond); err != nil {
				return err
			}
			return p.replies[len(p.replies)-1].err
		}},
	}
	for _, mode := range []string{"write", "sync"} {
		for _, op := range ops {
			t.Run(mode+" fails during "+op.name, func(t *testing.T) {
				w := &nodeWorld{or: newOracle(), logs: map[env.NodeID][]string{}}
				disks := map[env.NodeID]*flakyDisk{}
				s, ids := twoNodesWith(t, 12, w, func(e env.Env) env.Env {
					d := &flakyDisk{Disk: e.Disk}
					disks[e.ID] = d
					return e.WithDisk(d)
				})
				p := addProbe(t, s)
				n := occNode(s, ids[0])
				other := occNode(s, ids[1])
				ackedPut(t, w, n, "k", "v0", occ.IfNoneMatch())
				run(t, s, 2*time.Second)
				before := viewOf(n, w)
				if !n.Doc().Equal(other.Doc()) {
					t.Fatal("setup: the replicas did not converge")
				}
				disks[ids[0]].failWrite, disks[ids[0]].failSync = mode == "write", mode == "sync"
				err := op.do(s, p, n)
				if !errors.Is(err, env.ErrIO) {
					t.Fatalf("err = %v, want the disk error", err)
				}
				if got := viewOf(n, w); got != before {
					t.Fatalf("a failed persist left a trace on the node:\nbefore %+v\nafter  %+v", before, got)
				}
				if _, err := n.Put("k", []byte("chained"), occ.IfMatch(occ.ETag{0xee})); !errors.Is(err, occ.ErrPreconditionFailed) {
					t.Fatal(err)
				}
				run(t, s, 2*time.Second)
				if string(other.Doc().Encode()) != before.doc {
					t.Fatal("the failed write replicated")
				}
				if got := viewOf(n, w); got.doc != before.doc {
					t.Fatal("the node took state while its disk was failing")
				}
				disks[ids[0]].failWrite, disks[ids[0]].failSync = false, false
				if _, err := n.Put("k", []byte("after"), occ.IfMatch(before.etag)); err != nil {
					t.Fatalf("a conditional write on the pre-failure etag after recovery: %v", err)
				}
			})
		}
	}
}

// TestPeerChoiceIgnoresMembershipOrder proves that anti-entropy peer selection
// produces identical event digests regardless of Membership ordering or duplicates.
func TestPeerChoiceIgnoresMembershipOrder(t *testing.T) {
	runWith := func(order []env.NodeID) [32]byte {
		s := newSim(t, 21)
		ids := []env.NodeID{"n1", "n2", "n3"}
		for _, id := range ids {
			if err := s.AddNode(sim.NodeSpec{ID: id, Region: "r"}, occ.NewFactory(occ.Config{Membership: occ.StaticMembership(order)})); err != nil {
				t.Fatal(err)
			}
		}
		run(t, s, 10*time.Millisecond)
		if _, err := occNode(s, "n1").Put("k", []byte("v"), occ.IfNoneMatch()); err != nil {
			t.Fatal(err)
		}
		run(t, s, 3*time.Second)
		return s.Digests().Events
	}
	if runWith([]env.NodeID{"n1", "n2", "n3"}) != runWith([]env.NodeID{"n3", "n1", "n2", "n3"}) {
		t.Fatal("the peer drawn depends on the order the membership lists peers in")
	}
}
