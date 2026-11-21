package occ_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
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

func twoNodes(t *testing.T, seed int64) (*sim.Sim, []env.NodeID) {
	s := newSim(t, seed)
	ids := []env.NodeID{"n1", "n2"}
	f := occ.NewFactory(occ.Config{Membership: occ.StaticMembership(ids)})
	for _, id := range ids {
		if err := s.AddNode(sim.NodeSpec{ID: id, Region: "r"}, f); err != nil {
			t.Fatal(err)
		}
	}
	run(t, s, 10*time.Millisecond)
	return s, ids
}

func TestNodePersistsAndReloadsItsEpoch(t *testing.T) {
	s, ids := twoNodes(t, 1)
	n := occNode(s, ids[0])
	before := n.Store().Actor()
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
	if !n.Ready() || n.Store().Actor() != before || !n.Store().Record("k").Covers(v) {
		t.Fatal("a crash lost the acknowledged write or the epoch")
	}
	if n.Stats().Fresh != 0 {
		t.Fatal("a restart with an intact disk must not take a fresh epoch")
	}
}

func wipeScenario(t *testing.T) (lost bool) {
	s, ids := twoNodes(t, 3)
	a := occNode(s, ids[0])
	first := a.Store().Actor()
	if _, err := a.Put("k", []byte("v1"), occ.IfNoneMatch()); err != nil {
		t.Fatal(err)
	}
	v2, err := a.Put("k", []byte("v2"), occ.IfMatch(a.Store().Record("k").ETag()))
	if err != nil {
		t.Fatal(err)
	}
	run(t, s, 3*time.Second)
	if !occNode(s, ids[1]).Store().Record("k").Covers(v2) {
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
	if hazard := a.Store().Actor() == first; hazard {
		t.Log("the wiped node reuses its old actor")
	}
	v3, err := a.Put("k", []byte("after-wipe"), occ.IfNoneMatch())
	if err != nil {
		t.Fatal(err)
	}
	s.HealNet()
	run(t, s, 5*time.Second)
	for _, id := range ids {
		r := occNode(s, id).Store().Record("k")
		if !slices.Contains(r.IDs(), v3.ID()) {
			return true
		}
		if !r.Covers(v2) {
			t.Fatal("v2 lost")
		}
	}
	if r := occNode(s, ids[1]).Store().Record("k"); r.Len() != 2 {
		t.Fatalf("the post-wipe write should be a sibling of v2, got %d siblings", r.Len())
	}
	return false
}

func TestWipedNodeTakesAFreshEpoch(t *testing.T) {
	if wipeScenario(t) {
		t.Fatal("a write made after a wipe was silently dominated by the node's own older write")
	}
	restore := occ.SetHazardReuseEpoch(true)
	defer restore()
	if !wipeScenario(t) {
		t.Fatal("with the epoch switched off the classic reuse hazard must lose the post-wipe write; the test cannot see the hazard")
	}
}

func TestCorruptStateTakesAFreshEpoch(t *testing.T) {
	s, ids := twoNodes(t, 4)
	n := occNode(s, ids[0])
	first := n.Store().Actor()
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
	if n.Stats().Corrupt != 1 || n.Store().Actor() == first || n.Store().Actor().Node != ids[0] {
		t.Fatal("a corrupt state blob must be detected and answered with a fresh epoch")
	}
}

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
	if n.Ready() || n.Store() != nil || n.Digest() != ([32]byte{}) {
		t.Fatal("a node whose state cannot be read must not serve")
	}
	for _, err := range []error{
		func() error { _, _, err := n.Get("k"); return err }(),
		func() error { _, err := n.Put("k", nil, occ.IfNoneMatch()); return err }(),
		func() error { _, err := n.Delete("k", occ.IfNoneMatch()); return err }(),
		func() error { _, err := n.SetScalar("k", nil); return err }(),
		func() error { _, err := n.UpdateQuota("k", 1, nil, occ.AddUsage(1, 0, occ.Usage{})); return err }(),
	} {
		if !errors.Is(err, occ.ErrNotReady) {
			t.Fatalf("err = %v, want ErrNotReady", err)
		}
	}
	run(t, s, time.Second)
	n = occNode(s, ids[0])
	if !n.Ready() || n.Stats().LoadErrors != 3 || !n.Store().Record("k").Covers(v) || n.Stats().Fresh != 0 {
		t.Fatalf("after the read errors cleared the node must load its own state (load errors %d)", n.Stats().LoadErrors)
	}
}

func TestNodeWireProtocol(t *testing.T) {
	s := newSim(t, 6)
	ids := []env.NodeID{"n1", "n2"}
	f := occ.NewFactory(occ.Config{Membership: occ.StaticMembership(ids)})
	for _, id := range ids {
		if err := s.AddNode(sim.NodeSpec{ID: id, Region: "r"}, f); err != nil {
			t.Fatal(err)
		}
	}
	p := &probe{}
	if err := s.AddNode(sim.NodeSpec{ID: "probe", Region: "r"}, func(e env.Env) env.Process { p.e = e; return p }); err != nil {
		t.Fatal(err)
	}
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
	good := n.Store().Doc().Encode()
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
		{"get of a conflicted key", occ.EncodeOpRequest(occ.OpGet, "k", occ.Unconditional(), nil), occ.ErrConflict},
	}
	for _, r := range reqs {
		p.ask("n1", r.req)
		run(t, s, 50*time.Millisecond)
		got := p.replies[len(p.replies)-1]
		if r.want == nil && got.err != nil || r.want != nil && (!errors.Is(got.err, r.want) || !errors.Is(got.err, env.ErrRemote)) {
			t.Fatalf("%s: err = %v, want %v", r.name, got.err, r.want)
		}
	}
	cl := occ.NewClient(p.e.Net, time.Second)
	var delRes occ.Result
	var delErr error
	cl.Delete("n1", "k", occ.IfMatch(n.Store().Record("k").ETag()), func(r occ.Result, err error) { delRes, delErr = r, err })
	run(t, s, 50*time.Millisecond)
	if delErr != nil || delRes.ETag != n.Store().Record("k").ETag() || !n.Store().Record("k").Siblings()[0].Tombstone() {
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
