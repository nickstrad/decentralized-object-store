package objctltest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode"
	"unicode/utf8"
)

// transcriptEnv names a file that every Transcript also appends to.
const transcriptEnv = "OBJCTL_TRANSCRIPT"

// Result is the outcome of one objctl invocation.
type Result struct {
	Code   int
	Stdout []byte
	Stderr []byte
}

// Runner runs objctl with args (not including the program name) and stdin.
type Runner func(args []string, stdin []byte) Result

// Transcript records every objctl step of a test in a form a person can read:
// the exact command line, stdin size, exit code, stdout and stderr (which
// holds the --verbose request/response trace), and any oracle verdict. Each
// step goes to t.Log and, when OBJCTL_TRANSCRIPT is set, to that file.
type Transcript struct {
	tb   testing.TB
	mu   sync.Mutex
	file *os.File
	n    int
}

// NewTranscript starts a transcript for tb under the given heading.
func NewTranscript(tb testing.TB, heading string) *Transcript {
	tr := &Transcript{tb: tb}
	if path := os.Getenv(transcriptEnv); path != "" {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			tb.Fatalf("transcript %s: %v", path, err)
		}
		tr.file = f
		tb.Cleanup(func() { _ = f.Close() })
	}
	tr.write(fmt.Sprintf("=== %s: %s", tb.Name(), heading))
	return tr
}

func (tr *Transcript) write(block string) {
	tr.tb.Helper()
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.tb.Log("\n" + block)
	if tr.file != nil {
		_, _ = tr.file.WriteString(block + "\n")
	}
}

// Note adds a free-form line.
func (tr *Transcript) Note(format string, a ...any) {
	tr.tb.Helper()
	tr.write("# " + fmt.Sprintf(format, a...))
}

// Step records one invocation with an optional expectation and verdict.
func (tr *Transcript) Step(args []string, stdin []byte, r Result, expect, verdict string) {
	tr.tb.Helper()
	tr.mu.Lock()
	tr.n++
	n := tr.n
	tr.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "[%d] $ objctl %s", n, CommandLine(args))
	if stdin != nil {
		fmt.Fprintf(&b, "   < stdin %s", showBytes(stdin, 60))
	}
	b.WriteString("\n")
	for _, line := range strings.Split(strings.TrimRight(string(r.Stderr), "\n"), "\n") {
		if line != "" {
			fmt.Fprintf(&b, "    %s\n", line)
		}
	}
	fmt.Fprintf(&b, "    exit %d", r.Code)
	if len(r.Stdout) > 0 {
		fmt.Fprintf(&b, "   stdout %s", showBytes(r.Stdout, 400))
	}
	if expect != "" {
		fmt.Fprintf(&b, "\n    oracle: %s => %s", expect, verdict)
	}
	tr.write(b.String())
}

var plainArg = regexp.MustCompile(`^[A-Za-z0-9_./:=@%+,-]+$`)

// CommandLine renders args as a POSIX shell would need them typed.
func CommandLine(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = shellQuote(a)
	}
	return strings.Join(out, " ")
}

// shellQuote quotes one argument for a POSIX shell: bare when safe, in single
// quotes when printable, and as $'...' with escapes otherwise.
func shellQuote(a string) string {
	if plainArg.MatchString(a) {
		return a
	}
	printable := utf8.ValidString(a)
	for _, r := range a {
		if !unicode.IsPrint(r) && r != ' ' {
			printable = false
		}
	}
	if printable {
		return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	q := strconv.Quote(a)
	return "$'" + strings.ReplaceAll(q[1:len(q)-1], "'", `\'`) + "'"
}

// showBytes renders bytes for the transcript: quoted, cut to max bytes with the
// total size and SHA-256 prefix noted when longer.
func showBytes(b []byte, max int) string {
	if len(b) <= max {
		return strconv.Quote(string(b))
	}
	sum := sha256.Sum256(b)
	return fmt.Sprintf("%s... (%d bytes, sha256 %s)", strconv.Quote(string(b[:max])), len(b), hex.EncodeToString(sum[:])[:12])
}
