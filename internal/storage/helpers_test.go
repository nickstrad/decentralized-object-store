package storage_test

import (
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

type idleProcess struct{}

func (idleProcess) Start()                                  {}
func (idleProcess) OnDatagram(env.NodeID, []byte)           {}
func (idleProcess) OnRequest(env.NodeID, []byte, env.Reply) {}
func (idleProcess) EncodeState() []byte                     { return nil }

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
