package main

import (
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtoy"
)

func scenarios() []sim.Scenario {
	var out []sim.Scenario
	out = append(out, simtoy.Scenarios()...)
	return out
}
