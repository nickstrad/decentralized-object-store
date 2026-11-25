package e2e

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/objctl/objctltest"
	"github.com/nickstrad/object-store/internal/sim/simtest"
	"github.com/nickstrad/object-store/internal/storage"
)

// buildObjctl compiles ./cmd/objctl once for the lifetime of the test binary.
func buildObjctl(t testing.TB) string {
	t.Helper()
	return buildBinary(t, "objctl", "./cmd/objctl")
}

// objctlRunner runs the objctl binary with dir as its working directory and
// OBJCTL_ADDR cleared, so every address on the transcript is explicit.
func objctlRunner(t testing.TB, bin, dir string) objctltest.Runner {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "OBJCTL_ADDR=") {
			env = append(env, kv)
		}
	}
	return func(args []string, stdin []byte) objctltest.Result {
		ctx, cancel := context.WithTimeout(context.Background(), 2*realCallTimeout)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir, cmd.Env = dir, env
		cmd.Stdin = bytes.NewReader(stdin)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		var ee *exec.ExitError
		if err != nil && !errors.As(err, &ee) {
			t.Fatalf("run objctl %q: %v", args, err)
		}
		return objctltest.Result{Code: cmd.ProcessState.ExitCode(), Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	}
}

// cliRealOptions is a three-node cluster, one node per region, with a 2+1
// layout so every stripe spans all three nodes.
func cliRealOptions() realOptions {
	return realOptions{nodes: 3, layout: storage.Layout{K: 2, M: 1, StripeSize: realStripe}}
}

type cliSession struct {
	t   *testing.T
	run objctltest.Runner
	tr  *objctltest.Transcript
}

// do runs objctl --addr addr -v args, records the step, and fails the test
// unless the exit code is want.
func (s *cliSession) do(addr string, stdin []byte, want int, args ...string) objctltest.Result {
	s.t.Helper()
	full := append([]string{"--addr", addr, "-v"}, args...)
	r := s.run(full, stdin)
	verdict := "ok"
	if r.Code != want {
		verdict = "MISMATCH"
	}
	s.tr.Step(full, stdin, r, fmt.Sprintf("exit %d", want), verdict)
	if r.Code != want {
		s.t.Fatalf("objctl %s: exit %d, want %d\nstderr: %s", objctltest.CommandLine(full), r.Code, want, r.Stderr)
	}
	return r
}

func decodeOut[T any](t *testing.T, r objctltest.Result) T {
	t.Helper()
	v, err := objctltest.Decode[T](r.Stdout)
	if err != nil {
		t.Fatalf("stdout %q: %v", r.Stdout, err)
	}
	return v
}

// TestObjctlHappyPathTierR drives the objctl binary through a scripted session
// against three real objnode processes: health and membership, bucket
// creation, puts from a file and from stdin, reads through other nodes, head,
// prefix listing, tags, ACLs, quotas, conditional writes and deletes, and the
// not-found exit after a delete.
func TestObjctlHappyPathTierR(t *testing.T) {
	requireTierR(t)
	bin := buildObjctl(t)
	c := newRealCluster(t, 31, cliRealOptions())
	dir := t.TempDir()
	s := &cliSession{t: t, run: objctlRunner(t, bin, dir), tr: objctltest.NewTranscript(t, "happy path against 3 objnode processes")}
	n0, n1, n2 := c.procs[0].httpAddr, c.procs[1].httpAddr, c.procs[2].httpAddr

	h := decodeOut[storage.Health](t, s.do(n0, nil, 0, "health", "--json"))
	if !h.Ready || h.Node != c.procs[0].node.ID {
		t.Fatalf("health %+v", h)
	}
	if out := s.do(n1, nil, 0, "health"); !bytes.Contains(out.Stdout, []byte("ready         true")) {
		t.Fatalf("text health %q", out.Stdout)
	}
	ms := decodeOut[[]storage.MemberInfo](t, s.do(n0, nil, 0, "members", "--json"))
	alive := 0
	for _, m := range ms {
		if m.State == "alive" {
			alive++
		}
	}
	if alive != 3 {
		t.Fatalf("members %+v, want 3 alive", ms)
	}

	spec := decodeOut[storage.BucketSpec](t, s.do(n0, nil, 0, "mb", "--json", "hp"))
	if spec != (storage.BucketSpec{Name: "hp"}) {
		t.Fatalf("mb %+v", spec)
	}
	s.do(n0, nil, 1, "mb", "hp")

	readme := []byte("objctl happy path\n")
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), readme, 0o644); err != nil {
		t.Fatal(err)
	}
	const readmeKey, binKey = "docs/readme.txt", "docs/a b?#%.bin"
	put1 := decodeOut[storage.ObjectInfo](t, s.do(n0, nil, 0, "put", "--json", "--content-type", "text/plain", "-f", "readme.txt", "hp/"+readmeKey))
	if put1.Key != readmeKey || put1.Size != int64(len(readme)) || put1.ETag == "" || put1.ContentType != "text/plain" {
		t.Fatalf("put %+v", put1)
	}
	blob := randomBytes(c.Rand(), realStripe+4093)
	put2 := decodeOut[storage.ObjectInfo](t, s.do(n0, blob, 0, "put", "--json", "hp/"+binKey))
	if put2.Key != binKey || put2.Size != int64(len(blob)) {
		t.Fatalf("put %+v", put2)
	}

	if _, err := c.Await("cluster converged after the puts", realBounds.converge, c.Converged); err != nil {
		t.Fatal(err)
	}
	if got := s.do(n1, nil, 0, "get", "hp/"+readmeKey); !bytes.Equal(got.Stdout, readme) {
		t.Fatalf("get via node 1 returned %q", got.Stdout)
	}
	gi := decodeOut[storage.ObjectInfo](t, s.do(n2, nil, 0, "get", "--json", "-o", "blob.out", "hp/"+binKey))
	if b, err := os.ReadFile(filepath.Join(dir, "blob.out")); err != nil || !bytes.Equal(b, blob) || gi.ETag != put2.ETag || gi.ContentType != "application/octet-stream" {
		t.Fatalf("get via node 2: %+v, file matches=%v (%v)", gi, bytes.Equal(b, blob), err)
	}
	hi := decodeOut[storage.ObjectInfo](t, s.do(n0, nil, 0, "stat", "--json", "hp/"+readmeKey))
	if hi != put1 {
		t.Fatalf("head %+v, want %+v", hi, put1)
	}

	ls := decodeOut[[]storage.ObjectInfo](t, s.do(n0, nil, 0, "ls", "--json", "--prefix", "docs/", "hp"))
	if len(ls) != 2 {
		t.Fatalf("ls docs/ = %+v", ls)
	}
	if none := decodeOut[[]storage.ObjectInfo](t, s.do(n0, nil, 0, "ls", "--json", "--prefix", "nope", "hp")); len(none) != 0 {
		t.Fatalf("ls nope = %+v", none)
	}
	if text := s.do(n0, nil, 0, "ls", "hp"); !bytes.Contains(text.Stdout, []byte(binKey)) {
		t.Fatalf("text ls %q", text.Stdout)
	}

	ti := decodeOut[storage.TagInfo](t, s.do(n0, nil, 0, "tag", "set", "--json", "hp/"+readmeKey, "lang", "en"))
	if ti.Name != "lang" || ti.Value != "en" || ti.ETag == "" {
		t.Fatalf("tag set %+v", ti)
	}
	if tags := decodeOut[[]storage.TagInfo](t, s.do(n0, nil, 0, "tag", "ls", "--json", "hp/"+readmeKey)); len(tags) != 1 || tags[0] != ti {
		t.Fatalf("tag ls %+v", tags)
	}

	ai := decodeOut[storage.ACLInfo](t, s.do(n0, nil, 0, "acl", "set", "--json", "hp", "alice", "wr"))
	if ai.Principal != "alice" || ai.Perm != "rw" || ai.ETag == "" {
		t.Fatalf("acl set %+v", ai)
	}
	if got := decodeOut[storage.ACLInfo](t, s.do(n0, nil, 0, "acl", "get", "--json", "hp", "alice")); got != ai {
		t.Fatalf("acl get %+v, want %+v", got, ai)
	}
	s.do(n0, nil, 3, "acl", "get", "hp", "nobody")

	wantQ := storage.QuotaInfo{Bucket: "hp", LimitBytes: 1 << 20, LimitObjects: 10, UsedBytes: uint64(len(readme) + len(blob)), UsedObjects: 2}
	if q := decodeOut[storage.QuotaInfo](t, s.do(n0, nil, 0, "quota", "set", "--json", "--bytes", "1048576", "--objects", "10", "hp")); q != wantQ {
		t.Fatalf("quota set %+v, want %+v", q, wantQ)
	}
	if q := decodeOut[storage.QuotaInfo](t, s.do(n0, nil, 0, "quota", "get", "--json", "hp")); q != wantQ {
		t.Fatalf("quota get %+v, want %+v", q, wantQ)
	}

	s.do(n0, []byte("again"), 4, "put", "--if-none-match", "*", "hp/"+readmeKey)
	v2 := []byte("objctl happy path, second version\n")
	put3 := decodeOut[storage.ObjectInfo](t, s.do(n0, v2, 0, "put", "--json", "--if-match", put1.ETag, "--content-type", "text/plain", "hp/"+readmeKey))
	if put3.ETag == put1.ETag || put3.Size != int64(len(v2)) {
		t.Fatalf("conditional put %+v", put3)
	}
	s.do(n0, nil, 4, "rm", "--if-match", put1.ETag, "hp/"+readmeKey)
	del := decodeOut[storage.ObjectInfo](t, s.do(n0, nil, 0, "rm", "--json", "--if-match", `"`+put3.ETag+`"`, "hp/"+readmeKey))
	if !del.Deleted || del.Key != readmeKey {
		t.Fatalf("rm %+v", del)
	}
	s.do(n0, nil, 3, "get", "hp/"+readmeKey)
	s.do(n0, nil, 3, "rm", "hp/"+readmeKey)
	s.do(n0, nil, 3, "ls", "ghost")
	s.do(n0, nil, 2, "get", "hp")
}

// TestObjctlModelTierR runs the seeded objctl model (the generator and oracle
// TestModelInProcess in cmd/objctl uses against a fake) through the objctl
// binary against three real objnode processes. Every invocation goes to node
// 0: metadata is local-first and reaches other nodes by gossip, so a read on
// another node may lag a write (the existing scenarios await convergence for
// cross-node reads); pinning one node keeps the sequential oracle exact.
func TestObjctlModelTierR(t *testing.T) {
	requireTierR(t)
	bin := buildObjctl(t)
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		c := newRealCluster(t, seed, cliRealOptions())
		dir := t.TempDir()
		tr := objctltest.NewTranscript(t, "model against 3 objnode processes, pinned to node 0")
		start := time.Now()
		err := objctltest.RunModel(seed, objctlRunner(t, bin, dir), objctltest.ModelOptions{
			Globals: []string{"--addr", c.procs[0].httpAddr, "-v"}, Dir: dir, Steps: 300}, tr)
		logMeasure(t, "objctl/model_seconds", time.Since(start).Seconds())
		if err != nil {
			t.Log(simtest.ReplayCommand(t, seed) + " (with " + tierREnv + "=1)")
			t.Fatal(err)
		}
	})
}
