package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

func runArgs(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	return runWith(scenarios(), args...)
}

func runWith(all []sim.Scenario, args ...string) (int, string, string) {
	return runAs("objsim", all, args...)
}

func runAs(prog string, all []sim.Scenario, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(prog, args, all, &out, &errb)
	return code, out.String(), errb.String()
}

// TestToyScenarioReplaysBySeed proves that running a scenario twice with the
// same seed prints identical digests and that a different seed changes output.
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

// TestCommandLine proves -list, -h help output, unknown-scenario exit 2, and
// invalid-flag exit 2.
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

// TestFailingScenarioExitsNonZero proves that a scenario failing Verify exits
// with status 1 and prints its error to stderr.
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

func failing() []sim.Scenario {
	return []sim.Scenario{{Name: "explodes", Run: func(seed int64) *sim.Sim {
		s := sim.New(sim.Config{Seed: seed})
		for i := 0; i < 30; i++ {
			s.At(env.Time(i), "tick", func() {})
		}
		s.At(40, "boom", func() { panic("cli-replay-fixture") })
		_ = s.RunUntilQuiet()
		return s
	}}}
}

// TestFailingScenarioPrintsTheLogTailAndReplay proves that a failing run prints
// digests to stdout and the error, 20-line log tail, panic stack, and one final
// shell-quoted REPLAY line to stderr across binary and go-run invocations.
func TestFailingScenarioPrintsTheLogTailAndReplay(t *testing.T) {
	tests := []struct {
		name string
		prog string
		args []string
		want string
	}{
		{"an installed binary replays itself", "/usr/local/bin/objsim", []string{"-scenario", "explodes", "-seed", "23"},
			"REPLAY: /usr/local/bin/objsim -scenario explodes -seed 23"},
		{"equals-form flags replay in the same shape", "./objsim", []string{"-scenario=explodes", "-seed=23"},
			"REPLAY: ./objsim -scenario explodes -seed 23"},
		{"a go run binary replays through go run", "/tmp/go-build123/b001/exe/objsim", []string{"-scenario=explodes", "-seed=23"},
			"REPLAY: go run ./cmd/objsim -scenario explodes -seed 23"},
		{"the default seed is spelled out", "objsim", []string{"-scenario=explodes"},
			"REPLAY: objsim -scenario explodes -seed 1"},
		{"a path with a space is quoted", "/opt/my tools/objsim", []string{"-scenario=explodes", "-seed=23"},
			"REPLAY: '/opt/my tools/objsim' -scenario explodes -seed 23"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := runAs(tc.prog, failing(), tc.args...)
			if code != 1 {
				t.Fatalf("exit %d", code)
			}
			if !strings.Contains(out, "events=") || !strings.Contains(out, "state=") {
				t.Fatalf("stdout lacks digests: %s", out)
			}
			for _, want := range []string{"cli-replay-fixture", "event log tail (20 of 31):", "\n  t=40 seq=", "panic stack:"} {
				if !strings.Contains(errOut, want) {
					t.Fatalf("stderr lacks %q:\n%s", want, errOut)
				}
			}
			lines := strings.Split(strings.TrimRight(errOut, "\n"), "\n")
			if last := lines[len(lines)-1]; last != tc.want {
				t.Fatalf("last line %q, want %q\n%s", last, tc.want, errOut)
			}
			if strings.Count(errOut, "REPLAY:") != 1 {
				t.Fatalf("REPLAY printed more than once:\n%s", errOut)
			}
		})
	}
}

// TestPassingScenarioPrintsNoReplay proves that a passing run exits 0 and emits
// no REPLAY line.
func TestPassingScenarioPrintsNoReplay(t *testing.T) {
	code, out, errOut := runArgs(t, "-scenario=toy-chaos-20", "-seed=7")
	if code != 0 || strings.Contains(out+errOut, "REPLAY:") {
		t.Fatalf("exit %d out %q err %q", code, out, errOut)
	}
}
