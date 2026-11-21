package simtest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

// TailLines is the maximum number of trailing event-log records printed by Report.
const TailLines = 20

// ErrNondeterministic is returned by CheckDeterministic when two runs from the
// same seed produce different event or state digests.
var ErrNondeterministic = errors.New("simtest: same seed gave different digests")

// CheckDeterministic runs run(seed) twice and compares both event and state
// digests before calling Verify on the first run. It returns the first *sim.Sim
// along with ErrNondeterministic or any verification error.
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

// RequireDeterministic verifies determinism and invariants for seed via
// CheckDeterministic, fails t on error, and registers a Cleanup hook that calls
// Report if t fails either now or during later assertions.
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

// RunPattern converts a Go subtest name ("A/B/C") into an anchored -run regex
// ("^A$/^B$/^C$") with regex metacharacters escaped.
func RunPattern(name string) string {
	parts := strings.Split(name, "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	return strings.Join(parts, "/")
}

// ReplayCommand returns a single-line "REPLAY: go test ..." command that
// re-runs t with the given seed from the module root.
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

// Report logs a diagnostic summary for a failed simulation: seed, error,
// digests, step count, virtual time, up to TailLines trailing event-log lines,
// any PanicError stack, an optional full log artifact path (when DST_ARTIFACTS
// is set), a PIN line, and ReplayCommand as the final line.
func Report(t testing.TB, s *sim.Sim) {
	t.Helper()
	if s == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "simulation failed\nseed: %d\nerror: %v\ndigests: %s\nsteps: %d, virtual time: %d\n",
		s.Seed(), s.Err(), s.Digests(), s.Steps(), s.Now())
	tail := s.LogTail(TailLines)
	fmt.Fprintf(&b, "event log tail (%d of %d):\n", len(tail), s.LogLen())
	for _, line := range tail {
		b.WriteString("  " + line + "\n")
	}
	var pe *sim.PanicError
	if errors.As(s.Err(), &pe) && len(pe.Stack) > 0 {
		fmt.Fprintf(&b, "panic stack:\n%s\n", strings.TrimRight(string(pe.Stack), "\n"))
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
