package node_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

type crashBetweenFlipWrites struct {
	saga.Step
	crashed *bool
	fn      func(c saga.Call) error
}

func (s crashBetweenFlipWrites) Do(c saga.Call, done func([]byte, error)) {
	if !*s.crashed {
		*s.crashed = true
		if err := s.fn(c); err != nil {
			done(nil, err)
			return
		}
		done(nil, fmt.Errorf("test: coordinator crashed between the flip record and the route: %w", errors.ErrUnsupported))
		return
	}
	s.Step.Do(c, done)
}

func TestFlipAfterACrashBetweenItsWritesLeavesTheBucketWritable(t *testing.T) {
	cfg := partitionConfig()
	cfg.Layout = smallLayout()
	cfg.ViewSettle = 2 * time.Second
	var h *harness
	coord := env.NodeID("")
	crashed := false
	cfg.WrapMigration = func(d saga.Definition) saga.Definition {
		steps := append([]saga.Step(nil), d.Steps...)
		steps[saga.MigrationPivot] = crashBetweenFlipWrites{Step: steps[saga.MigrationPivot], crashed: &crashed, fn: func(c saga.Call) error {
			n := h.node(coord)
			flip := node.FlipKeyForTest("fl")
			if _, err := n.OCC().SetScalar(flip, []byte(string(c.Key.Saga)+"|r1")); err != nil {
				return err
			}
			n.RecordMeta(flip)
			if _, err := n.OCC().SetRoute("fl", "r2"); err != nil {
				return err
			}
			n.RecordMeta(occ.RouteKey("fl"))
			return nil
		}}
		d.Steps = steps
		return d
	}
	var err error
	h, err = newHarness(1, mtNodes, cfg, sim.DefaultNetConfig())
	if err != nil {
		t.Fatal(err)
	}
	coord = h.ids[1]
	if err := h.s.RunFor(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.node(coord).CreateBucket(storage.BucketSpec{Name: "fl", Region: "r0"}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	before, err := h.syncPut(t, coord, storage.PutRequest{Bucket: "fl", Key: "before", Data: []byte("v")})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	mi, err := h.node(coord).StartMigration("fl", "r1")
	if err != nil {
		t.Fatal(err)
	}
	var st storage.MigrationInfo
	if err := h.s.RunUntilConverged(h.s.Now().Add(5*time.Minute), func() bool {
		st, err = h.node(coord).Migration(mi.ID)
		return err == nil && st.State != saga.StateRunning.String() && st.State != saga.StateCompensating.String()
	}); err != nil {
		t.Fatalf("the migration never ended: %v (%+v)", err, st)
	}
	if !crashed {
		t.Fatal("the flip step never ran; the scenario did not reach the crash")
	}
	if st.State != saga.StateCompleted.String() {
		t.Fatalf("the migration ended %s at step %d (%s), want completed", st.State, st.Step, st.Error)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(30*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	for _, n := range h.up() {
		rd, err := n.OCC().Reader()
		if err != nil {
			t.Fatal(err)
		}
		route, err := rd.Route("fl")
		if err != nil || route != "r1" {
			t.Fatalf("%s routes the bucket to %q (%v), want r1", n.ID(), route, err)
		}
		owner, _, err := rd.Get(node.FlipKeyForTest("fl"))
		if err != nil || string(owner) != mi.ID+"|r1" {
			t.Fatalf("%s records flip owner %q (%v), want %s|r1", n.ID(), owner, err, mi.ID)
		}
	}
	for _, id := range []env.NodeID{coord, h.ids[0], h.ids[5]} {
		if _, err := h.syncPut(t, id, storage.PutRequest{Bucket: "fl", Key: "after-" + string(id), Data: []byte("w")}); err != nil {
			t.Fatalf("put through %s after the migration: %v", id, err)
		}
	}
	if _, got, err := h.syncGet(t, h.ids[4], "fl", "before"); err != nil || string(got) != "v" {
		t.Fatalf("the object written before the migration (%s): %q %v", before.Version, got, err)
	}
}
