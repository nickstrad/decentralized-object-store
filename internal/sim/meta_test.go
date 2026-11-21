package sim_test

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtoy"
)

var updateGolden = flag.Bool("update-golden", false, "")

var metaSizes = []int{20, 50, 100}

func chaos(n int) func(seed int64) *sim.Sim {
	return simtoy.ChaosRun(simtoy.ChaosOptions{Nodes: n})
}

func TestReplaySameProcess(t *testing.T) {
	for _, n := range metaSizes {
		t.Run(fmt.Sprintf("nodes=%d", n), func(t *testing.T) {
			simtest.Sweep(t, func(t *testing.T, seed int64) {
				s := simtest.RequireDeterministic(t, seed, chaos(n))
				if !simtoy.Converged(s) {
					t.Fatalf("not converged at %d", s.Now())
				}
			})
		})
	}
}

const childEnv = "SIM_REPLAY_CHILD"

func TestReplayAcrossProcesses(t *testing.T) {
	if spec := os.Getenv(childEnv); spec != "" {
		var n int
		var seed int64
		if _, err := fmt.Sscanf(spec, "%d:%d", &n, &seed); err != nil {
			t.Fatalf("bad %s=%q: %v", childEnv, spec, err)
		}
		s := chaos(n)(seed)
		if err := s.Verify(); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("DIGESTS %s\n", s.Digests())
		return
	}
	if testing.Short() {
		t.Skip("re-executes the test binary")
	}
	seed := simtest.Seeds(t)[0]
	variants := [][]string{{"GOMAXPROCS=1", "GOGC=1"}, {"GOMAXPROCS=4", "GOGC=off"}}
	for _, n := range metaSizes {
		t.Run(fmt.Sprintf("nodes=%d", n), func(t *testing.T) {
			s := chaos(n)(seed)
			mustNil(t, s.Verify())
			want := s.Digests().String()
			for _, v := range variants {
				cmd := exec.Command(os.Args[0], "-test.run=^TestReplayAcrossProcesses$", "-test.count=1")
				cmd.Env = append(append(os.Environ(), fmt.Sprintf("%s=%d:%d", childEnv, n, seed)), v...)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("child %v: %v\n%s", v, err, out)
				}
				got := ""
				sc := bufio.NewScanner(strings.NewReader(string(out)))
				for sc.Scan() {
					if rest, ok := strings.CutPrefix(sc.Text(), "DIGESTS "); ok {
						got = rest
					}
				}
				if got != want {
					t.Fatalf("seed %d nodes %d: child %v digests %q, parent %q\n%s",
						seed, n, v, got, want, simtest.ReplayCommand(t, seed))
				}
			}
		})
	}
}

func TestGoldenDigests(t *testing.T) {
	path := filepath.Join("testdata", "golden.txt")
	type row struct {
		n    int
		seed int64
		want string
	}
	var rows []row
	if *updateGolden {
		var b strings.Builder
		b.WriteString("# nodes seed digests\n")
		for _, n := range metaSizes {
			for _, seed := range []int64{1, 2, 3} {
				s := chaos(n)(seed)
				mustNil(t, s.Verify())
				fmt.Fprintf(&b, "%d %d %s\n", n, seed, s.Digests())
			}
		}
		mustNil(t, os.MkdirAll("testdata", 0o755))
		mustNil(t, os.WriteFile(path, []byte(b.String()), 0o644))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden file (regenerate with -update-golden): %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.SplitN(line, " ", 3)
		if len(f) != 3 {
			t.Fatalf("bad golden line %q", line)
		}
		n, err1 := strconv.Atoi(f[0])
		seed, err2 := strconv.ParseInt(f[1], 10, 64)
		if err := errors.Join(err1, err2); err != nil {
			t.Fatalf("bad golden line %q: %v", line, err)
		}
		rows = append(rows, row{n, seed, f[2]})
	}
	if len(rows) != 9 {
		t.Fatalf("golden file has %d rows, want 9", len(rows))
	}
	for _, r := range rows {
		t.Run(fmt.Sprintf("nodes=%d/seed=%d", r.n, r.seed), func(t *testing.T) {
			s := chaos(r.n)(r.seed)
			mustNil(t, s.Verify())
			if got := s.Digests().String(); got != r.want {
				t.Fatalf("digests changed:\n got  %s\n want %s", got, r.want)
			}
		})
	}
}

func TestDifferentSeedsDiffer(t *testing.T) {
	for _, n := range metaSizes {
		t.Run(fmt.Sprintf("nodes=%d", n), func(t *testing.T) {
			a, b := chaos(n)(1).Digests(), chaos(n)(2).Digests()
			if a.Events == b.Events || a.State == b.State {
				t.Fatalf("seeds 1 and 2 share a digest: %s vs %s", a, b)
			}
		})
	}
}

type unseeded struct {
	env.Process
	e env.Env
}

func (u *unseeded) Unwrap() env.Process { return u.Process }

func (u *unseeded) OnDatagram(from env.NodeID, msg []byte) {
	if rand.IntN(4) == 0 {
		_ = u.e.Net.Send(from, msg)
	}
	u.Process.OnDatagram(from, msg)
}

type mapOrder struct {
	env.Process
	e     env.Env
	peers map[env.NodeID]bool
}

func (m *mapOrder) Unwrap() env.Process { return m.Process }

func (m *mapOrder) OnDatagram(from env.NodeID, msg []byte) {
	for id := range m.peers {
		if id != m.e.ID {
			_ = m.e.Net.Send(id, msg)
		}
		break
	}
	m.Process.OnDatagram(from, msg)
}

func requireCanaryCaught(t *testing.T, run func(seed int64) *sim.Sim, want error) {
	t.Helper()
	caught := false
	for seed := int64(1); seed <= 3 && !caught; seed++ {
		_, err := simtest.CheckDeterministic(seed, run)
		if err != nil {
			if !errors.Is(err, want) {
				t.Fatalf("harness failed for the wrong reason: %v", err)
			}
			caught = true
		}
	}
	if !caught {
		t.Fatal("planted bug was not caught by the harness")
	}
}

func TestCanaryUnseededDraw(t *testing.T) {
	run := simtoy.ChaosRun(simtoy.ChaosOptions{Nodes: 20, Wrap: func(e env.Env, p env.Process) env.Process {
		return &unseeded{Process: p, e: e}
	}})
	requireCanaryCaught(t, run, simtest.ErrNondeterministic)
}

func TestCanaryMapOrder(t *testing.T) {
	run := simtoy.ChaosRun(simtoy.ChaosOptions{Nodes: 20, Wrap: func(e env.Env, p env.Process) env.Process {
		peers := map[env.NodeID]bool{}
		for _, id := range sim.NodeIDs(20) {
			peers[id] = true
		}
		return &mapOrder{Process: p, e: e, peers: peers}
	}})
	requireCanaryCaught(t, run, simtest.ErrNondeterministic)
}

func TestCanaryDroppedInvariant(t *testing.T) {
	clean := simtoy.ChaosRun(simtoy.ChaosOptions{Nodes: 20})
	if _, err := simtest.CheckDeterministic(1, clean); err != nil {
		t.Fatalf("clean run failed: %v", err)
	}
	run := simtoy.ChaosRun(simtoy.ChaosOptions{Nodes: 20, Configure: sim.PlantDroppedInvariants})
	requireCanaryCaught(t, run, sim.ErrInvariantNotRun)
}
