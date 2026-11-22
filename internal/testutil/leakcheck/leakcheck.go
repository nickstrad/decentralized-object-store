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

var ErrLeak = errors.New("leakcheck: goroutines leaked")

type Snapshot map[uint64]bool

type goroutine struct {
	id    uint64
	stack string
}

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

func benign(stack string, self uint64) bool {
	if strings.HasPrefix(stack, "goroutine "+strconv.FormatUint(self, 10)+" ") {
		return true
	}
	if !strings.Contains(stack, "github.com/nickstrad/decentralized-object-store/") && !strings.Contains(stack, "net/http.") {
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
