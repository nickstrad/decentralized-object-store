package simtest

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

const (
	DefaultSeeds    = 3
	DefaultSeedBase = 1
	TailLines       = 20
	SeedsFile       = "seeds.txt"
)

var ErrNondeterministic = errors.New("simtest: same seed gave different digests")

type seedValue struct {
	set bool
	v   int64
}

func (s *seedValue) String() string {
	if s == nil || !s.set {
		return ""
	}
	return strconv.FormatInt(s.v, 10)
}

func (s *seedValue) Set(v string) error {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fmt.Errorf("simtest: -seed %q: %w", v, err)
	}
	s.v, s.set = n, true
	return nil
}

var seedFlag seedValue

func init() {
	flag.Var(&seedFlag, "seed", "")
}

func Seed() (int64, bool, error) {
	if seedFlag.set {
		return seedFlag.v, true, nil
	}
	v := os.Getenv("DST_SEED")
	if v == "" {
		return 0, false, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("simtest: DST_SEED=%q: %w", v, err)
	}
	return n, true, nil
}

func envInt(name string, def int64) (int64, error) {
	v := os.Getenv(name)
	if v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("simtest: %s=%q: %w", name, v, err)
	}
	return n, nil
}

func PinnedSeeds(dir string) ([]int64, error) {
	data, err := os.ReadFile(filepath.Join(dir, SeedsFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("simtest: pinned seeds: %w", err)
	}
	var out []int64
	for i, raw := range strings.Split(string(data), "\n") {
		text := strings.TrimSpace(raw)
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		field, _, _ := strings.Cut(text, " ")
		n, err := strconv.ParseInt(field, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("simtest: %s line %d: %w", SeedsFile, i+1, err)
		}
		out = append(out, n)
	}
	return out, nil
}

func Seeds(t testing.TB) []int64 {
	t.Helper()
	if seed, ok, err := Seed(); err != nil {
		t.Fatal(err)
	} else if ok {
		return []int64{seed}
	}
	count, err1 := envInt("DST_SEEDS", DefaultSeeds)
	base, err2 := envInt("DST_SEED_BASE", DefaultSeedBase)
	pinned, err3 := PinnedSeeds("testdata")
	if err := errors.Join(err1, err2, err3); err != nil {
		t.Fatal(err)
	}
	seen := map[int64]bool{}
	var out []int64
	for _, s := range pinned {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for i := int64(0); i < count; i++ {
		s := base + i
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func Sweep(t *testing.T, fn func(t *testing.T, seed int64)) {
	t.Helper()
	for _, seed := range Seeds(t) {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) { fn(t, seed) })
	}
}

func CheckDeterministic(seed int64, run func(seed int64) *sim.Sim) (*sim.Sim, error) {
	a := run(seed)
	b := run(seed)
	da, db := a.Digests(), b.Digests()
	if da != db {
		return a, fmt.Errorf("%w: seed %d: first run %s, second run %s", ErrNondeterministic, seed, da, db)
	}
	if err := a.Verify(); err != nil {
		return a, err
	}
	return a, nil
}

func RequireDeterministic(t testing.TB, seed int64, run func(seed int64) *sim.Sim) *sim.Sim {
	t.Helper()
	s, err := CheckDeterministic(seed, run)
	t.Cleanup(func() {
		if t.Failed() {
			Report(t, s)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func moduleRoot(dir string) (string, bool) {
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func packagePath() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	root, ok := moduleRoot(wd)
	if !ok {
		return "."
	}
	rel, err := filepath.Rel(root, wd)
	if err != nil || rel == "." {
		return "."
	}
	return "./" + filepath.ToSlash(rel)
}

func RunPattern(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	return strings.Join(parts, "/")
}

func ReplayCommand(t testing.TB, seed int64) string {
	return fmt.Sprintf("REPLAY: go test %s -run '%s' -seed=%d -count=1", packagePath(), RunPattern(t.Name()), seed)
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_.=-]+`)

func writeArtifact(t testing.TB, s *sim.Sim) (string, error) {
	dir := os.Getenv("DST_ARTIFACTS")
	if dir == "" {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("simtest: artifacts: %w", err)
	}
	name := unsafeName.ReplaceAllString(t.Name(), "_") + fmt.Sprintf("-seed%d.eventlog.txt", s.Seed())
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		return "", fmt.Errorf("simtest: artifacts: %w", err)
	}
	werr := s.WriteLog(f)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		return "", fmt.Errorf("simtest: artifacts: %w", err)
	}
	return path, nil
}

func Report(t testing.TB, s *sim.Sim) {
	t.Helper()
	if s == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "simulation failed\nseed: %d\nerror: %v\ndigests: %s\nsteps: %d, virtual time: %d\n",
		s.Seed(), s.Err(), s.Digests(), s.Steps(), s.Now())
	fmt.Fprintf(&b, "event log tail (%d of %d):\n", min(TailLines, int(s.LogLen())), s.LogLen())
	for _, line := range s.LogTail(TailLines) {
		b.WriteString("  " + line + "\n")
	}
	if path, err := writeArtifact(t, s); err != nil {
		fmt.Fprintf(&b, "artifact: %v\n", err)
	} else if path != "" {
		fmt.Fprintf(&b, "artifact: %s\n", path)
	}
	fmt.Fprintf(&b, "PIN: echo '%d %s' >> testdata/%s\n", s.Seed(), t.Name(), SeedsFile)
	b.WriteString(ReplayCommand(t, s.Seed()))
	t.Log(b.String())
}

func Cluster(t testing.TB, s *sim.Sim, n, regions int, factory func(ids []env.NodeID) sim.Factory) []env.NodeID {
	t.Helper()
	ids := sim.NodeIDs(n)
	if err := s.AddCluster(ids, regions, factory(ids)); err != nil {
		t.Fatal(err)
	}
	return ids
}
