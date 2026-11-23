// Package e2e hosts the end-to-end test suite for the object store and metadata
// sync engine.
//
// Shared scenarios run against real packages on two tiers: deterministic
// simulated clusters (tier S, run by default through go test) and real objnode
// OS processes on loopback (tier R, enabled with E2E_TIER_R=1). This file keeps
// the directory a valid Go package for go build and go vet.
package e2e
