package occ_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

type skewedPair struct {
	s   *sim.Sim
	w   *nodeWorld
	ids []env.NodeID
}

func newSkewedPair(t *testing.T, seed int64, wrap func(env.Env) env.Env) skewedPair {
	t.Helper()
	w := &nodeWorld{or: newOracle(), logs: map[env.NodeID][]string{}}
	s := newSim(t, seed)
	ids := []env.NodeID{"n1", "n2"}
	for i, id := range ids {
		spec := sim.NodeSpec{ID: id, Region: "r"}
		if i == 0 {
			spec.Skew = -5 * time.Second
		}
		if err := s.AddNode(spec, loggedFactory(ids, w, wrap)); err != nil {
			t.Fatal(err)
		}
	}
	run(t, s, 10*time.Millisecond)
	return skewedPair{s: s, w: w, ids: ids}
}

func (p skewedPair) node(i int) *occ.Node { return occNode(p.s, p.ids[i]) }

func (p skewedPair) named(id env.NodeID, v occ.Version) bool {
	for _, line := range p.w.logs[id] {
		if strings.Contains(line, "loser="+v.ID().String()) {
			return true
		}
	}
	return false
}

func deliverLoser(t *testing.T, p skewedPair) (v1, v2 occ.Version) {
	t.Helper()
	n1, n2 := p.ids[0], p.ids[1]
	if err := p.s.Partition([]env.NodeID{n1}, []env.NodeID{n2}); err != nil {
		t.Fatal(err)
	}
	run(t, p.s, time.Second)
	v2 = ackedPut(t, p.w, p.node(1), "k", "v2", occ.IfNoneMatch())
	run(t, p.s, 100*time.Millisecond)
	p.s.HealNet()
	if err := p.s.PartitionOneWay([]env.NodeID{n2}, []env.NodeID{n1}); err != nil {
		t.Fatal(err)
	}
	deltas := p.node(1).Stats().Deltas
	v1 = ackedPut(t, p.w, p.node(0), "k", "v1", occ.IfNoneMatch())
	if !lwwLess(v1, v2) {
		t.Fatal("setup: v1 must lose to v2")
	}
	run(t, p.s, 50*time.Millisecond)
	if p.node(1).Stats().Deltas == deltas {
		t.Fatal("setup: v1 did not reach n2")
	}
	d := occ.NewDoc()
	d.MergeVersion("k", v1)
	p.w.or.reach(string(n2), d)
	if got, _ := p.node(0).Record("k").Version(); got.ID() != v1.ID() {
		t.Fatal("setup: n1 learned v2 before its fate")
	}
	if got, _ := p.node(1).Record("k").Version(); got.ID() != v2.ID() {
		t.Fatal("setup: n2 must keep v2")
	}
	return v1, v2
}

func incomingLoserScenario(t *testing.T, fate string) error {
	p := newSkewedPair(t, 21, nil)
	v1, _ := deliverLoser(t, p)
	n1 := p.ids[0]
	p.w.or.diskLost(string(n1), p.node(0).Doc())
	switch fate {
	case "crash for good":
		if err := p.s.Crash(n1); err != nil {
			t.Fatal(err)
		}
	case "wipe and rejoin":
		if err := p.s.WipeRejoin(n1); err != nil {
			t.Fatal(err)
		}
	case "state blob rot":
		if err := p.s.Crash(n1); err != nil {
			t.Fatal(err)
		}
		if err := p.s.BitRot(n1, occ.StateBlob); err != nil {
			t.Fatal(err)
		}
		if err := p.s.Restart(n1); err != nil {
			t.Fatal(err)
		}
	}
	p.s.HealNet()
	run(t, p.s, 3*time.Second)
	docs := []*occ.Doc{p.node(1).Doc()}
	if fate != "crash for good" {
		if !p.node(0).Doc().Equal(docs[0]) {
			t.Fatal("the replicas did not converge after the heal")
		}
		docs = append(docs, p.node(0).Doc())
	}
	if err := p.w.or.check(docs, true); err != nil {
		return err
	}
	if !p.named(p.ids[1], v1) {
		return fmt.Errorf("no surviving node's log names the lost version %s", short(v1.ID()))
	}
	return nil
}

var writerFates = []string{"crash for good", "wipe and rejoin", "state blob rot"}

func TestIncomingLoserRecordedWhenWriterIsLost(t *testing.T) {
	for _, fate := range writerFates {
		t.Run(fate, func(t *testing.T) {
			if err := incomingLoserScenario(t, fate); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCanaryIncomingLoserSilent(t *testing.T) {
	restore := occ.SetCanaryIncomingLoserSilent(true)
	defer restore()
	for _, fate := range writerFates {
		t.Run(fate, func(t *testing.T) {
			if incomingLoserScenario(t, fate) == nil {
				t.Fatal("with incoming losers rejected silently the suite must see an acknowledged write vanish with no record")
			}
		})
	}
}

func TestDiskLossBeforeReachIsTheOnlyUnrecordedLoss(t *testing.T) {
	p := newSkewedPair(t, 22, nil)
	n1, n2 := p.ids[0], p.ids[1]
	if err := p.s.Partition([]env.NodeID{n1}, []env.NodeID{n2}); err != nil {
		t.Fatal(err)
	}
	run(t, p.s, time.Second)
	v1 := ackedPut(t, p.w, p.node(0), "k", "v1", occ.IfNoneMatch())
	ackedPut(t, p.w, p.node(1), "j", "j", occ.IfNoneMatch())
	run(t, p.s, time.Second)
	before := p.node(0).Doc()
	if err := p.s.WipeRejoin(n1); err != nil {
		t.Fatal(err)
	}
	p.s.HealNet()
	run(t, p.s, 3*time.Second)
	docs := []*occ.Doc{p.node(0).Doc(), p.node(1).Doc()}
	if !docs[0].Equal(docs[1]) || docs[0].Record("k").Present() {
		t.Fatal("setup: v1 must be gone from every node")
	}
	for _, id := range p.ids {
		if occNode(p.s, id).LWWDiscards() != 0 || len(p.w.logs[id]) != 0 {
			t.Fatal("a version no other node ever received was recorded as a concurrency loss")
		}
	}
	err := p.w.or.check(docs, true)
	if err == nil || !strings.Contains(err.Error(), short(v1.ID())) {
		t.Fatalf("before the disk loss is declared the oracle must flag v1 and only v1: %v", err)
	}
	p.w.or.diskLost(string(n1), before)
	if err := p.w.or.check(docs, true); err != nil {
		t.Fatal(err)
	}
	if got := p.w.or.tally(docs); got["lost-with-disk"] != 1 || got["current"] != 1 {
		t.Fatalf("outcome %v", got)
	}
	t.Run("a holder that survives voids the allowance", func(t *testing.T) {
		or := newOracle()
		v := version("n1", 1, occ.VersionVector{}, false, "x", st(1, 0, "n1"))
		or.record("k", v, occ.Record{})
		d := occ.NewDoc()
		d.MergeVersion("k", v)
		or.reach("n2", d)
		or.diskLost("n1", d)
		if or.check(nil, true) == nil {
			t.Fatal("a version that reached n2 and is gone with no record was allowed")
		}
		or.diskLost("n2", d)
		if or.check(nil, true) != nil {
			t.Fatal("every holder lost its disk: the oracle must allow it")
		}
		or.reach("n2", d)
		if or.check(nil, true) == nil {
			t.Fatal("n2's next incarnation received it: a later silent drop there must be flagged")
		}
	})
}

type countingDisk struct {
	env.Disk
	allow int
	armed bool
}

func (d *countingDisk) Write(name string, data []byte) error {
	if d.armed {
		if d.allow == 0 {
			return env.ErrIO
		}
		d.allow--
	}
	return d.Disk.Write(name, data)
}

func TestNodeReplaysOutboxAfterRestart(t *testing.T) {
	disks := map[env.NodeID]*countingDisk{}
	p := newSkewedPair(t, 23, func(e env.Env) env.Env {
		d := &countingDisk{Disk: e.Disk}
		disks[e.ID] = d
		return e.WithDisk(d)
	})
	n1, n2 := p.ids[0], p.ids[1]
	if err := p.s.Partition([]env.NodeID{n1}, []env.NodeID{n2}); err != nil {
		t.Fatal(err)
	}
	run(t, p.s, time.Second)
	ackedPut(t, p.w, p.node(1), "k", "v2", occ.IfNoneMatch())
	p.s.HealNet()
	if err := p.s.PartitionOneWay([]env.NodeID{n2}, []env.NodeID{n1}); err != nil {
		t.Fatal(err)
	}
	disks[n2].allow, disks[n2].armed = 1, true
	v1 := ackedPut(t, p.w, p.node(0), "k", "v1", occ.IfNoneMatch())
	run(t, p.s, 50*time.Millisecond)
	if p.node(1).LWWDiscards() != 1 || len(p.w.logs[n2]) != 1 || !p.named(n2, v1) {
		t.Fatal("setup: n2 must record v1 once")
	}
	if err := p.s.Crash(n2); err != nil {
		t.Fatal(err)
	}
	if err := p.s.Restart(n2); err != nil {
		t.Fatal(err)
	}
	run(t, p.s, 10*time.Millisecond)
	n := p.node(1)
	if !n.Ready() || n.LWWDiscards() != 1 || len(n.DiscardTail()) != 1 || n.DiscardTail()[0].Loser.ID() != v1.ID() {
		t.Fatal("the restarted node lost its counter or its durable tail")
	}
	if len(p.w.logs[n2]) != 2 || p.w.logs[n2][1] != p.w.logs[n2][0] {
		t.Fatalf("an undrained outbox must be emitted again on restart: %v", p.w.logs[n2])
	}
	if err := p.s.Crash(n2); err != nil {
		t.Fatal(err)
	}
	if err := p.s.Restart(n2); err != nil {
		t.Fatal(err)
	}
	run(t, p.s, 10*time.Millisecond)
	if len(p.w.logs[n2]) != 2 {
		t.Fatal("a drained outbox was emitted again")
	}
}
