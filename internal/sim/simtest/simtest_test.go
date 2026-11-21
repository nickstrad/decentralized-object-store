package simtest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtoy"
)

func withSeedFlag(t *testing.T, v seedValue) {
	old := seedFlag
	seedFlag = v
	t.Cleanup(func() { seedFlag = old })
}

func clearEnv(t *testing.T) {
	for _, k := range []string{"DST_SEED", "DST_SEEDS", "DST_SEED_BASE", "DST_ARTIFACTS"} {
		t.Setenv(k, "")
	}
}

func TestSeeds(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		flag seedValue
		want []int64
	}{
		{"the default is a small fixed sweep", nil, seedValue{}, []int64{1, 2, 3}},
		{"DST_SEEDS and DST_SEED_BASE move the sweep", map[string]string{"DST_SEEDS": "2", "DST_SEED_BASE": "10"}, seedValue{}, []int64{10, 11}},
		{"DST_SEED selects one seed", map[string]string{"DST_SEED": "77", "DST_SEEDS": "50"}, seedValue{}, []int64{77}},
		{"the -seed flag wins over DST_SEED", map[string]string{"DST_SEED": "77"}, seedValue{set: true, v: -5}, []int64{-5}},
		{"a zero count gives no swept seeds", map[string]string{"DST_SEEDS": "0"}, seedValue{}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			withSeedFlag(t, tc.flag)
			got := Seeds(t)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("Seeds = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSeedErrors(t *testing.T) {
	clearEnv(t)
	withSeedFlag(t, seedValue{})
	t.Setenv("DST_SEED", "nope")
	if _, _, err := Seed(); err == nil {
		t.Fatal("bad DST_SEED accepted")
	}
	var v seedValue
	if err := v.Set("x"); err == nil {
		t.Fatal("bad -seed accepted")
	}
	if v.String() != "" {
		t.Fatalf("unset flag renders %q", v.String())
	}
	mustOK(t, v.Set("12"))
	if v.String() != "12" {
		t.Fatalf("flag renders %q", v.String())
	}
	if _, err := envInt("DST_SEEDS_TEST_UNSET", 4); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DST_SEEDS", "many")
	if _, err := envInt("DST_SEEDS", 3); err == nil {
		t.Fatal("bad DST_SEEDS accepted")
	}
}

func mustOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestPinnedSeeds(t *testing.T) {
	dir := t.TempDir()
	if got, err := PinnedSeeds(dir); err != nil || got != nil {
		t.Fatalf("missing file: %v %v", got, err)
	}
	mustOK(t, os.WriteFile(filepath.Join(dir, SeedsFile), []byte("# header\n\n42 TestX stalled after heal\n-7 note\n42 again\n"), 0o644))
	got, err := PinnedSeeds(dir)
	if err != nil || fmt.Sprint(got) != "[42 -7 42]" {
		t.Fatalf("PinnedSeeds = %v %v", got, err)
	}
	mustOK(t, os.WriteFile(filepath.Join(dir, SeedsFile), []byte("42\tTestX tab-separated note\n7\n\t8\t\n"), 0o644))
	got, err = PinnedSeeds(dir)
	if err != nil || fmt.Sprint(got) != "[42 7 8]" {
		t.Fatalf("PinnedSeeds with tabs = %v %v", got, err)
	}
	mustOK(t, os.WriteFile(filepath.Join(dir, SeedsFile), []byte("abc\n"), 0o644))
	if _, err := PinnedSeeds(dir); err == nil {
		t.Fatal("bad line accepted")
	}
	mustOK(t, os.Remove(filepath.Join(dir, SeedsFile)))
	mustOK(t, os.Mkdir(filepath.Join(dir, SeedsFile), 0o755))
	if _, err := PinnedSeeds(dir); err == nil {
		t.Fatal("unreadable file accepted")
	}
}

func TestSeedsIncludesPinnedFirst(t *testing.T) {
	clearEnv(t)
	withSeedFlag(t, seedValue{})
	dir := t.TempDir()
	mustOK(t, os.MkdirAll(filepath.Join(dir, "testdata"), 0o755))
	mustOK(t, os.WriteFile(filepath.Join(dir, "testdata", SeedsFile), []byte("99 pinned\n2 dup\n"), 0o644))
	t.Chdir(dir)
	if got := Seeds(t); fmt.Sprint(got) != "[99 2 1 3]" {
		t.Fatalf("Seeds = %v", got)
	}
}

func TestSweepRunsOneSubtestPerSeed(t *testing.T) {
	clearEnv(t)
	withSeedFlag(t, seedValue{})
	t.Setenv("DST_SEEDS", "2")
	var got []string
	Sweep(t, func(t *testing.T, seed int64) { got = append(got, t.Name()) })
	if fmt.Sprint(got) != "[TestSweepRunsOneSubtestPerSeed/seed=1 TestSweepRunsOneSubtestPerSeed/seed=2]" {
		t.Fatalf("subtests %v", got)
	}
}

func TestReplayCommand(t *testing.T) {
	if got := RunPattern("TestX/nodes=20/seed=5"); got != "^TestX$/^nodes=20$/^seed=5$" {
		t.Fatalf("RunPattern = %q", got)
	}
	if got := RunPattern("TestA.B"); got != `^TestA\.B$` {
		t.Fatalf("RunPattern = %q", got)
	}
	want := "REPLAY: go test ./internal/sim/simtest -run '^TestReplayCommand$' -seed=9 -count=1"
	if got := ReplayCommand(t, 9); got != want {
		t.Fatalf("ReplayCommand = %q", got)
	}
	t.Chdir(t.TempDir())
	if got := ReplayCommand(t, 9); !strings.HasPrefix(got, "REPLAY: go test . ") {
		t.Fatalf("outside a module: %q", got)
	}
}

func toyRun(seed int64) *sim.Sim {
	s := sim.New(sim.Config{Seed: seed})
	ids := sim.NodeIDs(4)
	if err := s.AddCluster(ids, 2, simtoy.Factory(simtoy.Config{Peers: ids})); err != nil {
		s.Fail(err)
		return s
	}
	s.Fail(s.RunUntil(env.Time(2e9)))
	return s
}

func TestCheckDeterministic(t *testing.T) {
	if _, err := CheckDeterministic(3, toyRun); err != nil {
		t.Fatalf("deterministic run: %v", err)
	}
	calls := 0
	flaky := func(seed int64) *sim.Sim {
		calls++
		return toyRun(seed + int64(calls))
	}
	if _, err := CheckDeterministic(3, flaky); !errors.Is(err, ErrNondeterministic) {
		t.Fatalf("flaky run: %v", err)
	}
	boom := errors.New("boom")
	failing := func(seed int64) *sim.Sim {
		s := toyRun(seed)
		s.Fail(boom)
		return s
	}
	if _, err := CheckDeterministic(3, failing); !errors.Is(err, boom) {
		t.Fatalf("failing run: %v", err)
	}
}

type captureTB struct {
	testing.TB
	name     string
	logs     []string
	failed   bool
	cleanups []func()
}

func (c *captureTB) Helper()                      {}
func (c *captureTB) Name() string                 { return c.name }
func (c *captureTB) Log(args ...any)              { c.logs = append(c.logs, fmt.Sprint(args...)) }
func (c *captureTB) Fatal(args ...any)            { c.failed = true; c.Log(args...) }
func (c *captureTB) Failed() bool                 { return c.failed }
func (c *captureTB) Cleanup(fn func())            { c.cleanups = append(c.cleanups, fn) }
func (c *captureTB) Fatalf(f string, args ...any) { c.Fatal(fmt.Sprintf(f, args...)) }

func TestRequireDeterministicReportsAFailure(t *testing.T) {
	clearEnv(t)
	dir := t.TempDir()
	t.Setenv("DST_ARTIFACTS", dir)
	c := &captureTB{name: "TestFake/seed=4"}
	s := RequireDeterministic(c, 4, func(seed int64) *sim.Sim {
		s := toyRun(seed)
		s.Fail(errors.New("planted failure"))
		return s
	})
	if !c.failed || s == nil {
		t.Fatal("RequireDeterministic did not fail")
	}
	for _, fn := range c.cleanups {
		fn()
	}
	out := strings.Join(c.logs, "\n")
	for _, want := range []string{"planted failure", "seed: 4", "digests: events=", "event log tail", "artifact: " + dir,
		"PIN: echo '4 TestFake/seed=4' >> testdata/seeds.txt",
		"REPLAY: go test ./internal/sim/simtest -run '^TestFake$/^seed=4$' -seed=4 -count=1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report lacks %q:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "-count=1") {
		t.Fatalf("REPLAY is not the last line:\n%s", out)
	}
	files, err := filepath.Glob(filepath.Join(dir, "*.eventlog.txt"))
	if err != nil || len(files) != 1 {
		t.Fatalf("artifacts %v %v", files, err)
	}
	data, err := os.ReadFile(files[0])
	if err != nil || uint64(strings.Count(string(data), "\n")) != s.LogLen() {
		t.Fatalf("artifact has %d lines, log %d (%v)", strings.Count(string(data), "\n"), s.LogLen(), err)
	}
}

func TestRequireDeterministicPassesQuietly(t *testing.T) {
	c := &captureTB{name: "TestFake"}
	RequireDeterministic(c, 2, toyRun)
	for _, fn := range c.cleanups {
		fn()
	}
	if c.failed || len(c.logs) != 0 {
		t.Fatalf("passing run reported: %v", c.logs)
	}
	Report(c, nil)
}

func TestArtifactErrorIsReported(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "file")
	mustOK(t, os.WriteFile(blocker, nil, 0o644))
	t.Setenv("DST_ARTIFACTS", filepath.Join(blocker, "sub"))
	c := &captureTB{name: "TestFake"}
	Report(c, toyRun(1))
	if !strings.Contains(strings.Join(c.logs, "\n"), "artifact: simtest: artifacts:") {
		t.Fatalf("artifact error not reported: %v", c.logs)
	}
}

func TestCluster(t *testing.T) {
	s := sim.New(sim.Config{Seed: 1})
	var got []env.NodeID
	ids := Cluster(t, s, 5, 2, func(ids []env.NodeID) sim.Factory {
		got = ids
		return simtoy.Factory(simtoy.Config{Peers: ids})
	})
	if len(ids) != 5 || len(got) != 5 || s.Region(ids[1]) != "r1" || s.Region(ids[2]) != "r0" {
		t.Fatalf("Cluster = %v regions %v", ids, s.Regions())
	}
}

type panicky struct{ env.Process }

func (p *panicky) Start() { explodeInStart() }

func explodeInStart() { panic("planted node panic") }

func TestReportShowsThePanicStack(t *testing.T) {
	clearEnv(t)
	c := &captureTB{name: "TestFake/seed=5"}
	RequireDeterministic(c, 5, func(seed int64) *sim.Sim {
		s := sim.New(sim.Config{Seed: seed})
		if err := s.AddNode(sim.NodeSpec{ID: "a"}, func(e env.Env) env.Process { return &panicky{} }); err != nil {
			s.Fail(err)
		}
		s.Fail(s.RunUntil(env.Time(1e9)))
		return s
	})
	for _, fn := range c.cleanups {
		fn()
	}
	out := strings.Join(c.logs, "\n")
	for _, want := range []string{"planted node panic", "panic stack:", "simtest.explodeInStart", "simtest_test.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("report lacks %q:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "-count=1") {
		t.Fatalf("REPLAY is not the last line:\n%s", out)
	}
}

func TestReportTailHeaderCountsThePrintedLines(t *testing.T) {
	clearEnv(t)
	s := sim.New(sim.Config{Seed: 1, LogCap: 5})
	for i := 1; i <= 30; i++ {
		s.At(env.Time(i), "act", func() {})
	}
	mustOK(t, s.RunUntilQuiet())
	c := &captureTB{name: "TestFake"}
	Report(c, s)
	out := strings.Join(c.logs, "\n")
	if !strings.Contains(out, "event log tail (5 of 30):") || strings.Count(out, "action/ran") != 5 {
		t.Fatalf("tail header or lines wrong:\n%s", out)
	}
}
