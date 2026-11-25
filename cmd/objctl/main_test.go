package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nickstrad/object-store/internal/objctl"
	"github.com/nickstrad/object-store/internal/objctl/objctltest"
	"github.com/nickstrad/object-store/internal/sim/simtest"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
	"github.com/nickstrad/object-store/internal/storage/httpapi/httpapitest"
	"github.com/nickstrad/object-store/internal/storage/httpclient"
)

func noEnv(string) string { return "" }

// inProcess returns a Runner that executes the real cobra tree in-process
// against svc served by httpapi.NewHandler over an in-memory HTTP/1.1
// encoding, with relative file names resolved under dir.
func inProcess(svc httpapi.Service, dir string) objctltest.Runner {
	h := httpapi.NewHandler(svc, httpapi.Limits{})
	return func(args []string, stdin []byte) objctltest.Result {
		var stdout, stderr bytes.Buffer
		c := config{getenv: noEnv, Env: objctl.Env{
			Stdin: bytes.NewReader(stdin), Stdout: &stdout, Stderr: &stderr,
			Open:   func(name string) (io.ReadCloser, error) { return os.Open(filepath.Join(dir, name)) },
			Create: func(name string) (io.WriteCloser, error) { return os.Create(filepath.Join(dir, name)) },
			NewService: func(addr string, o objctl.ServiceOptions) (httpapi.Service, error) {
				return httpclient.New(addr, httpclient.Options{Transport: httpapitest.InProcess(h), Trace: o.Trace})
			},
		}}
		code := run(context.Background(), args, c)
		return objctltest.Result{Code: code, Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	}
}

// TestModelInProcess runs the seeded objctl model (generator and oracle shared
// with the tier R test in test/e2e) through the real command tree, client and
// HTTP handler against the in-memory fake service.
func TestModelInProcess(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		dir := t.TempDir()
		tr := objctltest.NewTranscript(t, "in-process model against the fake service")
		err := objctltest.RunModel(seed, inProcess(objctltest.NewFake(), dir), objctltest.ModelOptions{
			Globals: []string{"--addr", "inproc:1", "-v"}, Dir: dir, Steps: 150}, tr)
		if err != nil {
			t.Log(simtest.ReplayCommand(t, seed))
			t.Fatal(err)
		}
	})
}

func TestParse(t *testing.T) {
	env := func(k string) string {
		if k == objctl.AddrEnv {
			return "envhost:9"
		}
		return ""
	}
	cases := []struct {
		args []string
		want objctl.Command
		code int
	}{
		{args: []string{"put", "--if-match", `"e1"`, "--if-match", "e2", "-f", "x.bin", "b/dir/../k?#"},
			want: objctl.Command{Verb: objctl.VerbPut, Bucket: "b", Key: "dir/../k?#", File: "x.bin"}},
		{args: []string{"get", "b/k", "-o", "-", "-v"}, want: objctl.Command{Verb: objctl.VerbGet, Bucket: "b", Key: "k", Globals: objctl.Globals{Verbose: true}}},
		{args: []string{"get", "--json", "-o", "-", "b/k"}, code: objctl.ExitUsage},
		{args: []string{"migrate", "status", ""}, code: objctl.ExitUsage},
		{args: []string{"stat", "--", "b/-k"}, want: objctl.Command{Verb: objctl.VerbHead, Bucket: "b", Key: "-k"}},
		{args: []string{"tag", "set", "b/k", "n", "--", "-v"}, want: objctl.Command{Verb: objctl.VerbTagSet, Bucket: "b", Key: "k", Name: "n", Value: "-v"}},
		{args: []string{"--addr", "h:1", "quota", "set", "--bytes", "5", "b"}, want: objctl.Command{Verb: objctl.VerbQuotaSet, Bucket: "b", QuotaBytes: 5}},
		{args: []string{"get", "b"}, code: objctl.ExitUsage},
		{args: []string{"get", "B/k"}, code: objctl.ExitUsage},
		{args: []string{"put", "--if-none-match", "x", "b/k"}, code: objctl.ExitUsage},
		{args: []string{"put", "--if-match", "*", "--if-match", "e", "b/k"}, code: objctl.ExitUsage},
		{args: []string{"ls", "--limit", "10001", "b"}, code: objctl.ExitUsage},
		{args: []string{"health", "--timeout", "0s"}, code: objctl.ExitUsage},
		{args: []string{"acl"}, code: objctl.ExitUsage},
		{args: []string{"tag", "nope"}, code: objctl.ExitUsage},
		{args: []string{"rm", "b/k", "extra"}, code: objctl.ExitUsage},
		{args: []string{"--bogus"}, code: objctl.ExitUsage},
	}
	for _, c := range cases {
		got, ok, err := parse(c.args, env)
		if c.code == objctl.ExitUsage {
			if !errors.Is(err, objctl.ErrUsage) {
				t.Errorf("parse(%q) = %+v, %v; want a usage error", c.args, got, err)
			}
			continue
		}
		if err != nil || !ok {
			t.Errorf("parse(%q): ok=%v err=%v", c.args, ok, err)
			continue
		}
		w := c.want
		if w.Addr == "" {
			w.Addr = "envhost:9"
		}
		if c.args[0] == "--addr" {
			w.Addr = "h:1"
		}
		w.Timeout = objctl.DefaultTimeout
		w.JSON = strings.Contains(strings.Join(c.args, " "), "--json")
		if w.Verb == objctl.VerbPut {
			w.Cond.IfMatchList, w.Cond.IfMatchAny = true, []string{"e1", "e2"}
		}
		if !reflect.DeepEqual(got, w) {
			t.Errorf("parse(%q)\n got %+v\nwant %+v", c.args, got, w)
		}
	}
	for _, help := range [][]string{{"--help"}, {"put", "-h"}, {"help", "tag", "set"}, {"completion", "bash"}} {
		if _, ok, err := parse(help, noEnv); ok || err != nil {
			t.Errorf("parse(%q) = ok %v err %v; want neither", help, ok, err)
		}
	}
}

// FuzzArgv runs arbitrary argv (NUL-separated) through the real command tree
// against the in-memory fake service, with no network and no file system:
// nothing panics, and every argv that parses to a Command renders to a
// canonical argv that parses back to an equal Command.
func FuzzArgv(f *testing.F) {
	for _, s := range []string{
		"put\x00--if-match\x00\"e\"\x00b/k", "get\x00-o\x00f\x00--json\x00b/a/b", "tag\x00set\x00b/k\x00n\x00--\x00-v",
		"ls\x00--prefix=\x00--limit=3\x00b", "--addr=h:1\x00-v\x00health", "mb\x00--region\x00r1\x00--quota-bytes\x0010\x00b",
		"acl\x00set\x00--if-none-match=*\x00b\x00p\x00rw", "quota\x00set\x00--objects=4\x00b", "migrate\x00status\x00--wait\x00--timeout=1ms\x00id",
		"migrate\x00start\x00b\x00r2", "rm\x00--if-match=*\x00b/k", "--\x00put", "help\x00put", "__complete\x00put\x00", "completion\x00zsh",
		"put\x00--if-match=\x00b/k", "get\x00b/", "stat\x00b/..", "--timeout=-1s\x00members", "tag\x00ls\x00--\x00b/--",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		args := strings.Split(s, "\x00")
		if s == "" {
			args = nil
		}
		fake := objctltest.NewFake()
		var stdout, stderr bytes.Buffer
		c := config{getenv: noEnv, Env: objctl.Env{Stdin: strings.NewReader("stdin"), Stdout: &stdout, Stderr: &stderr,
			Open:       func(string) (io.ReadCloser, error) { return nil, os.ErrPermission },
			Create:     func(string) (io.WriteCloser, error) { return nil, os.ErrPermission },
			NewService: func(string, objctl.ServiceOptions) (httpapi.Service, error) { return fake, nil }}}
		code := run(context.Background(), args, c)
		if code < 0 || code > objctl.ExitPrecondition {
			t.Fatalf("exit %d", code)
		}
		cmd, ok, err := parse(args, noEnv)
		if err != nil {
			if code != objctl.ExitUsage {
				t.Fatalf("parse error %v but run exited %d", err, code)
			}
			return
		}
		if !ok {
			return
		}
		canon := argv(cmd)
		again, ok, err := parse(canon, noEnv)
		if err != nil || !ok {
			t.Fatalf("canonical %q of %q does not parse: ok=%v %v", canon, args, ok, err)
		}
		if !reflect.DeepEqual(again, cmd) {
			t.Fatalf("argv %q\n  parsed  %+v\ncanonical %q\n  parsed  %+v", args, cmd, canon, again)
		}
	})
}
