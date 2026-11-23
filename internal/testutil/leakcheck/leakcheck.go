// Package leakcheck detects leaked goroutines in tests that exercise real
// sockets and background workers outside testing/synctest.
//
// Callers capture a Snapshot before work begins and call Snapshot.Leaked (or
// wrap TestMain with Main) to fail if any non-benign goroutine survives past
// the settle window.
package leakcheck

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ErrLeak is returned by Snapshot.Leaked when goroutines started after the
// snapshot remain running past the settle deadline.
var ErrLeak = errors.New("leakcheck: goroutines leaked")

// Snapshot records the goroutine IDs present when Take was called.
type Snapshot map[uint64]bool

type goroutine struct {
	id    uint64
	stack string
}

// stacks captures and parses all live goroutine stacks from the Go runtime.
func stacks() []goroutine {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]byte, 2*len(buf))
	}
	var out []goroutine
	for _, block := range bytes.Split(buf, []byte("\n\n")) {
		head, _, _ := bytes.Cut(block, []byte("\n"))
		f := strings.Fields(string(head))
		if len(f) < 2 || f[0] != "goroutine" {
			continue
		}
		id, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		out = append(out, goroutine{id: id, stack: string(block)})
	}
	return out
}

// Take returns a Snapshot of every goroutine running at the call site.
func Take() Snapshot {
	s := Snapshot{}
	for _, g := range stacks() {
		s[g.id] = true
	}
	return s
}

var ignored = []string{
	"testing.(*T).Run",
	"testing.(*M).",
	"testing.tRunner",
	"testing.runFuzzing",
	"os/signal.signal_recv",
	"os/signal.loop",
	"runtime.ensureSigM",
	"internal/poll.runtime_pollWait",
}

// benign reports whether a goroutine stack belongs to the caller itself or a
// long-lived runtime/testing/signal helper outside project and net/http code.
func benign(stack string, self uint64) bool {
	if strings.HasPrefix(stack, "goroutine "+strconv.FormatUint(self, 10)+" ") {
		return true
	}
	if !strings.Contains(stack, "github.com/nickstrad/object-store/") && !strings.Contains(stack, "net/http.") {
		for _, p := range ignored {
			if strings.Contains(stack, p) {
				return true
			}
		}
	}
	return false
}

func selfID() uint64 {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]
	f := strings.Fields(string(buf))
	if len(f) < 2 {
		return 0
	}
	id, _ := strconv.ParseUint(f[1], 10, 64)
	return id
}

func (s Snapshot) leaked() []goroutine {
	self := selfID()
	var out []goroutine
	for _, g := range stacks() {
		if s[g.id] || benign(g.stack, self) {
			continue
		}
		out = append(out, g)
	}
	return out
}

// Leaked polls until no unexpected goroutine started after s remains, or until
// settle elapses. It returns nil when clean, or an error wrapping ErrLeak with
// the surviving stacks.
func (s Snapshot) Leaked(settle time.Duration) error {
	deadline := time.Now().Add(settle)
	for {
		left := s.leaked()
		if len(left) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			var b strings.Builder
			for _, g := range left {
				b.WriteString(g.stack)
				b.WriteString("\n\n")
			}
			return fmt.Errorf("%w: %d still running after %v:\n%s", ErrLeak, len(left), settle, b.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Main runs m and then verifies that no goroutines leaked beyond settle.
// If any goroutine remains, it prints the stacks to stderr and forces a
// non-zero exit code.
func Main(m *testing.M, settle time.Duration) int {
	before := Take()
	code := m.Run()
	if err := before.Leaked(settle); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if code == 0 {
			code = 1
		}
	}
	return code
}
