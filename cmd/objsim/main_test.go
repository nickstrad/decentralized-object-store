package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

func runArgs(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return runWith(scenarios(), args...)
}

func runWith(all []sim.Scenario, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, all, &out, &errb)
	return code, out.String(), errb.String()
}

func TestToyScenarioReplaysBySeed(t *testing.T) {
	c1, a, e1 := runArgs(t, "-scenario=toy-chaos-20", "-seed=7")
	c2, b, e2 := runArgs(t, "-scenario=toy-chaos-20", "-seed=7")
	if c1 != 0 || c2 != 0 {
		t.Fatalf("exit %d %d: %s %s", c1, c2, e1, e2)
	}
	if a != b {
		t.Fatalf("same seed, different output:\n%s\n%s", a, b)
	}
	if !strings.Contains(a, "events=") || !strings.Contains(a, "state=") {
		t.Fatalf("output lacks digests: %s", a)
	}
	_, c, _ := runArgs(t, "-scenario=toy-chaos-20", "-seed=8")
	if c == a {
		t.Fatal("different seeds gave the same output")
	}
}

func TestCommandLine(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
		wantErr  string
	}{
		{"list prints every scenario", []string{"-list"}, 0, "toy-chaos-100\n", ""},
		{"help prints the embedded help", []string{"-h"}, 0, "", help},
		{"an unknown scenario is a usage error", []string{"-scenario=nope"}, 2, "", "unknown scenario"},
		{"a bad flag is a usage error", []string{"-seed=x"}, 2, "", "invalid value"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := runArgs(t, tc.args...)
			if code != tc.wantCode || !strings.Contains(out, tc.wantOut) || !strings.Contains(errOut, tc.wantErr) {
				t.Fatalf("code %d out %q err %q", code, out, errOut)
			}
		})
	}
}

func TestFailingScenarioExitsNonZero(t *testing.T) {
	bad := []sim.Scenario{{Name: "bad", Run: func(seed int64) *sim.Sim {
		s := sim.New(sim.Config{Seed: seed})
		s.Fail(errors.New("planted"))
		return s
	}}}
	code, _, errOut := runWith(bad, "-scenario=bad")
	if code != 1 || !strings.Contains(errOut, "planted") {
		t.Fatalf("code %d err %q", code, errOut)
	}
}
