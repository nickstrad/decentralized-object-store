package storage_test

import (
	"testing"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/sim"
	"pgregory.net/rapid"
)

type idleProcess struct{}

func (idleProcess) Start()                                  {}
func (idleProcess) OnDatagram(env.NodeID, []byte)           {}
func (idleProcess) OnRequest(env.NodeID, []byte, env.Reply) {}
func (idleProcess) EncodeState() []byte                     { return nil }

// simDisk starts a single-node simulator and returns its simulation, node ID,
// and disk handle for ChunkStore unit tests.
func simDisk(t testing.TB) (*sim.Sim, env.NodeID, env.Disk) {
	t.Helper()
	s := sim.New(sim.Config{Seed: 1})
	var disk env.Disk
	id := env.NodeID("n000")
	err := s.AddNode(sim.NodeSpec{ID: id, Region: "r0"}, func(e env.Env) env.Process {
		disk = e.Disk
		return idleProcess{}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RunUntilQuiet(); err != nil {
		t.Fatal(err)
	}
	if disk == nil {
		t.Fatal("factory did not run")
	}
	return s, id, disk
}

// restartableDisk wraps a single-node simulator so crash tests can drop
// unsynced writes and obtain the restarted incarnation's disk handle.
type restartableDisk struct {
	s    *sim.Sim
	id   env.NodeID
	disk env.Disk
}

func newRestartableDisk(t rapid.TB) *restartableDisk {
	t.Helper()
	rd := &restartableDisk{s: sim.New(sim.Config{Seed: 1}), id: "n000"}
	err := rd.s.AddNode(sim.NodeSpec{ID: rd.id, Region: "r0"}, func(e env.Env) env.Process {
		rd.disk = e.Disk
		return idleProcess{}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := rd.s.RunUntilQuiet(); err != nil {
		t.Fatal(err)
	}
	return rd
}

func (rd *restartableDisk) crash(t rapid.TB) {
	t.Helper()
	old := rd.disk
	if err := rd.s.Crash(rd.id); err != nil {
		t.Fatal(err)
	}
	if err := rd.s.Restart(rd.id); err != nil {
		t.Fatal(err)
	}
	if err := rd.s.RunUntilQuiet(); err != nil {
		t.Fatal(err)
	}
	if rd.disk == old {
		t.Fatal("restart did not hand out a new disk")
	}
}
