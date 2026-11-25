package objctl_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/objctl"
	"github.com/nickstrad/object-store/internal/objctl/objctltest"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/storage"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
)

// TestNoFrameworkImports keeps the package free of the HTTP stack and of the
// command-line framework: those belong to httpclient and cmd/objctl.
func TestNoFrameworkImports(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(p, "net/http") || strings.Contains(p, "cobra") || strings.Contains(p, "pflag") || p == "flag" {
				t.Errorf("%s imports %s", name, p)
			}
		}
	}
}

type status struct {
	code int
	err  error
}

func (s status) Error() string   { return fmt.Sprintf("http %d: %v", s.code, s.err) }
func (s status) StatusCode() int { return s.code }
func (s status) Unwrap() error   { return s.err }

func TestExitCode(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, objctl.ExitOK},
		{fmt.Errorf("x: %w", objctl.ErrUsage), objctl.ExitUsage},
		{fmt.Errorf("local: %w", storage.ErrInvalid), objctl.ExitUsage},
		{status{400, storage.ErrInvalid}, objctl.ExitFailure},
		{status{404, errors.New("node: migration not coordinated here")}, objctl.ExitNotFound},
		{status{412, occ.ErrPreconditionFailed}, objctl.ExitPrecondition},
		{status{409, storage.ErrBucketExists}, objctl.ExitFailure},
		{fmt.Errorf("fake: %w", storage.ErrNoSuchBucket), objctl.ExitNotFound},
		{fmt.Errorf("fake: %w", occ.ErrPreconditionFailed), objctl.ExitPrecondition},
		{errors.New("boom"), objctl.ExitFailure},
	}
	for _, c := range cases {
		if got := objctl.ExitCode(c.err); got != c.want {
			t.Errorf("ExitCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func TestParseCondition(t *testing.T) {
	cases := []struct {
		match []string
		none  string
		set   bool
		want  storage.Condition
		bad   bool
	}{
		{want: storage.Condition{}},
		{none: "*", set: true, want: storage.Condition{IfNoneMatch: true}},
		{none: "", set: true, bad: true},
		{match: []string{"*"}, want: storage.Condition{IfMatch: "*"}},
		{match: []string{"*", "a"}, bad: true},
		{match: []string{"a"}, none: "*", set: true, bad: true},
		{match: []string{`"a"`, "b", `""`, `"`}, want: storage.Condition{IfMatchList: true, IfMatchAny: []string{"a", "b", "", `"`}}},
	}
	for _, c := range cases {
		got, err := objctl.ParseCondition(c.match, c.none, c.set)
		if c.bad {
			if !errors.Is(err, objctl.ErrUsage) {
				t.Errorf("ParseCondition(%q, %q) = %+v, want usage error", c.match, c.none, got)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("ParseCondition(%q, %q) = %+v, %v; want %+v", c.match, c.none, got, err, c.want)
		}
	}
}

func TestSplitObject(t *testing.T) {
	for arg, want := range map[string][2]string{"b/k": {"b", "k"}, "b/a/b/": {"b", "a/b/"}, "b//": {"b", "/"}, "b/..": {"b", ".."}} {
		b, k, err := objctl.SplitObject(arg)
		if err != nil || b != want[0] || k != want[1] {
			t.Errorf("SplitObject(%q) = %q %q %v", arg, b, k, err)
		}
	}
	for _, bad := range []string{"b", "b/", "/k", "B/k", ""} {
		if _, _, err := objctl.SplitObject(bad); !errors.Is(err, objctl.ErrUsage) {
			t.Errorf("SplitObject(%q) = %v, want usage error", bad, err)
		}
	}
}

func TestRunTextOutput(t *testing.T) {
	fake := objctltest.NewFake()
	var stdout, stderr bytes.Buffer
	e := objctl.Env{Stdin: strings.NewReader("hello"), Stdout: &stdout, Stderr: &stderr,
		NewService: func(string, objctl.ServiceOptions) (httpapi.Service, error) { return fake, nil }}
	steps := []struct {
		cmd  objctl.Command
		code int
		out  string
	}{
		{objctl.Command{Verb: objctl.VerbMakeBucket, Bucket: "b"}, 0, "created bucket b\n"},
		{objctl.Command{Verb: objctl.VerbPut, Bucket: "b", Key: "tab\tkey", ContentType: "text/plain"}, 0, `object        b/"tab\tkey"`},
		{objctl.Command{Verb: objctl.VerbList, Bucket: "b"}, 0, `"tab\tkey"`},
		{objctl.Command{Verb: objctl.VerbGet, Bucket: "b", Key: "tab\tkey"}, 0, "hello"},
		{objctl.Command{Verb: objctl.VerbGet, Bucket: "b", Key: "missing"}, 3, ""},
		{objctl.Command{Verb: objctl.VerbQuotaGet, Bucket: "b"}, 0, "bytes    5 used of unlimited"},
		{objctl.Command{Verb: objctl.VerbMigrateStatus, ID: "m"}, 3, ""},
	}
	for _, s := range steps {
		stdout.Reset()
		stderr.Reset()
		code := objctl.Run(context.Background(), withTimeout(s.cmd), e)
		if code != s.code || !strings.Contains(stdout.String(), s.out) {
			t.Errorf("%s: exit %d stdout %q stderr %q; want exit %d and %q", s.cmd.Verb, code, stdout.String(), stderr.String(), s.code, s.out)
		}
	}
}

// migrating is a service whose migration "m1" runs for two polls and then
// ends in the given state.
func withTimeout(c objctl.Command) objctl.Command {
	c.Timeout = time.Minute
	return c
}

type migrating struct {
	*objctltest.Fake
	polls int
	end   string
}

func (s *migrating) Migration(_ context.Context, id string) (storage.MigrationInfo, error) {
	s.polls++
	mi := storage.MigrationInfo{ID: id, Bucket: "b", From: "r0", To: "r1", State: "running", Step: s.polls}
	if s.polls > 2 {
		mi.State = s.end
	}
	return mi, nil
}

func TestMigrateStatusWait(t *testing.T) {
	for end, code := range map[string]int{"completed": objctl.ExitOK, "compensated": objctl.ExitFailure} {
		svc := &migrating{Fake: objctltest.NewFake(), end: end}
		var stdout, stderr bytes.Buffer
		e := objctl.Env{Stdout: &stdout, Stderr: &stderr, PollInterval: time.Millisecond,
			NewService: func(string, objctl.ServiceOptions) (httpapi.Service, error) { return svc, nil }}
		got := objctl.Run(context.Background(), objctl.Command{Verb: objctl.VerbMigrateStatus, ID: "m1", Wait: true, Globals: objctl.Globals{JSON: true, Timeout: time.Minute}}, e)
		if got != code || svc.polls != 3 || !strings.Contains(stdout.String(), `"State":"`+end+`"`) || strings.Count(stdout.String(), "\n") != 1 {
			t.Errorf("%s: exit %d after %d polls, stdout %q stderr %q", end, got, svc.polls, stdout.String(), stderr.String())
		}
	}
	svc := &migrating{Fake: objctltest.NewFake(), end: "running"}
	e := objctl.Env{Stdout: io.Discard, Stderr: io.Discard, PollInterval: time.Millisecond,
		NewService: func(string, objctl.ServiceOptions) (httpapi.Service, error) { return svc, nil }}
	if got := objctl.Run(context.Background(), objctl.Command{Verb: objctl.VerbMigrateStatus, ID: "m1", Wait: true, Globals: objctl.Globals{Timeout: 20 * time.Millisecond}}, e); got != objctl.ExitFailure {
		t.Errorf("never-ending migration: exit %d, want %d", got, objctl.ExitFailure)
	}
}
