package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/storage"
)

// Tier R limits and defaults. realPutSlots caps in-flight PUTs at 1 outside E9
// so race-built objnode processes do not exceed the fixed 1 s shard
// RequestTimeout under concurrent fsync load.
const (
	tierREnv        = "E2E_TIER_R"
	blocklistSuffix = ".e2e-blocklist"
	realStripe      = 64 << 10
	realCallTimeout = 60 * time.Second
	realPoll        = 100 * time.Millisecond
	realPutSlots    = 1
)

var realBounds = bounds{
	converge:   90 * time.Second,
	repair:     90 * time.Second,
	migrate:    120 * time.Second,
	catchUp:    90 * time.Second,
	refill:     120 * time.Second,
	readBatch:  8,
	writeBatch: 1,
}

func requireTierR(t *testing.T) {
	t.Helper()
	if os.Getenv(tierREnv) != "1" {
		t.Skip("tier R runs real processes only with " + tierREnv + "=1")
	}
}

var (
	binMu   sync.Mutex
	binDirs []string
	binPath = map[string]string{}
)

func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("e2e: no go.mod above the working directory")
		}
		dir = parent
	}
}

// buildBinary compiles pkg (with extra go build flags) once per (name, flags)
// combination for the lifetime of the test binary; TestMain deletes all
// temporary binary dirs.
func buildBinary(t testing.TB, name, pkg string, extra ...string) string {
	t.Helper()
	key := name + "|" + strings.Join(extra, " ")
	binMu.Lock()
	defer binMu.Unlock()
	if p, ok := binPath[key]; ok {
		return p
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "e2e-"+name+"-bin-")
	if err != nil {
		t.Fatal(err)
	}
	binDirs = append(binDirs, dir)
	out := filepath.Join(dir, name)
	args := append(append([]string{"build", "-o", out}, extra...), pkg)
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, b)
	}
	binPath[key] = out
	return out
}

// buildObjnode compiles ./cmd/objnode once per (race, tags) combination.
func buildObjnode(t testing.TB, race bool, tags string) string {
	t.Helper()
	var extra []string
	if race {
		extra = append(extra, "-race")
	}
	if tags != "" {
		extra = append(extra, "-tags", tags)
	}
	return buildBinary(t, "objnode", "./cmd/objnode", extra...)
}

func removeBinaries() {
	binMu.Lock()
	defer binMu.Unlock()
	for _, d := range binDirs {
		_ = os.RemoveAll(d)
	}
	binDirs = nil
}

func TestMain(m *testing.M) {
	code := m.Run()
	removeBinaries()
	os.Exit(code)
}

// allocDistinctPorts reserves n distinct loopback ports free on both TCP and
// UDP, keeping every probe socket open until all n are chosen so two nodes in
// one cluster can never receive the same port.
func allocDistinctPorts(n int) ([]int, error) {
	if n <= 0 {
		return nil, nil
	}
	var tcpLs []net.Listener
	var udpCs []net.PacketConn
	defer func() {
		for _, u := range udpCs {
			_ = u.Close()
		}
		for _, l := range tcpLs {
			_ = l.Close()
		}
	}()
	used := map[int]bool{}
	ports := make([]int, 0, n)
	attempts := 0
	for len(ports) < n && attempts < n*50 {
		attempts++
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		port := l.Addr().(*net.TCPAddr).Port
		if used[port] {
			_ = l.Close()
			continue
		}
		u, uerr := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
		if uerr != nil {
			_ = l.Close()
			continue
		}
		used[port] = true
		tcpLs = append(tcpLs, l)
		udpCs = append(udpCs, u)
		ports = append(ports, port)
	}
	if len(ports) < n {
		return nil, fmt.Errorf("e2e: could only allocate %d of %d distinct free ports", len(ports), n)
	}
	return ports, nil
}

type realProc struct {
	node     Node
	httpAddr string
	data     string
	log      string
	args     []string
	cmd      *exec.Cmd
	exited   chan struct{}
	state    *os.ProcessState
	starts   int
	ports    []int
}

// realCluster implements Cluster over real objnode OS processes on loopback,
// communicating through HTTP and injecting partitions via e2efault blocklists,
// crashes via SIGKILL, and bit-rot by mutating shard files on disk.
type realCluster struct {
	t       testing.TB
	bin     string
	dir     string
	procs   []*realProc
	ledger  *Ledger
	r       *rand.Rand
	t0      time.Time
	hc      *http.Client
	layout  storage.Layout
	regions []string
	blocked map[[2]int]bool
	calls   sync.WaitGroup
	startup time.Duration
	closed  bool
	slots   chan struct{}
}

type realOptions struct {
	nodes    int
	layout   storage.Layout
	putSlots int
	bin      string
	ports    []int
	onLaunch func(dir string, pids []int)
}

func defaultRealOptions() realOptions {
	return realOptions{nodes: 9, layout: storage.Layout{K: 4, M: 2, StripeSize: realStripe}}
}

func logTail(path string, maxLines int) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("<unable to read %s: %v>", path, err)
	}
	trimmed := strings.TrimRight(string(b), "\n")
	if trimmed == "" {
		return "<empty log>"
	}
	lines := strings.Split(trimmed, "\n")
	return strings.Join(lines[max(0, len(lines)-maxLines):], "\n")
}

// allStartedAndConverged requires every intended process to remain alive, report
// Ready with its own node ID on /_health, agree on metadata digest and Merkle
// root, and see all N peers alive in /_cluster/members.
func (c *realCluster) allStartedAndConverged() (bool, error) {
	want := map[string]bool{}
	for _, p := range c.procs {
		if !p.running() {
			code := -1
			if p.state != nil {
				code = p.state.ExitCode()
			}
			return false, failf("node %s (%s) exited with status %d during startup; log tail:\n%s", p.node.ID, p.node.Region, code, logTail(p.log, 15))
		}
		want[p.node.ID] = true
	}
	if len(want) != len(c.procs) || len(c.procs) == 0 {
		return false, failf("intended %d nodes but got %d distinct IDs", len(c.procs), len(want))
	}
	var digest, root string
	for i, p := range c.procs {
		h, err := c.Health(p.node)
		if err != nil || !h.Ready || h.MetaDigest == "" {
			return false, nil
		}
		if h.Node != p.node.ID {
			return false, failf("node %s HTTP endpoint %s reported identity %q", p.node.ID, p.httpAddr, h.Node)
		}
		if i == 0 {
			digest, root = h.MetaDigest, h.MetaRoot
		} else if h.MetaDigest != digest || h.MetaRoot != root {
			return false, nil
		}
		ms, err := c.Members(p.node)
		if err != nil {
			return false, nil
		}
		live := 0
		for _, m := range ms {
			if m.State == "alive" {
				if !want[m.ID] {
					return false, nil
				}
				live++
			}
		}
		if live != len(c.procs) {
			return false, nil
		}
	}
	return true, nil
}

func (c *realCluster) awaitStartup(bound time.Duration) (time.Duration, error) {
	start := time.Now()
	for {
		ok, err := c.allStartedAndConverged()
		if err != nil {
			return time.Since(start), err
		}
		if ok {
			return time.Since(start), nil
		}
		if time.Since(start) > bound {
			var tails []string
			for _, p := range c.procs {
				tails = append(tails, fmt.Sprintf("--- %s (%s, running=%v) ---\n%s", p.node.ID, p.node.Region, p.running(), logTail(p.log, 10)))
			}
			return time.Since(start), failf("cluster of %d nodes not ready and converged within %v:\n%s", len(c.procs), bound, strings.Join(tails, "\n"))
		}
		time.Sleep(realPoll)
	}
}

const realStartAttempts = 5

// startRealCluster launches a tier R cluster and retries up to
// realStartAttempts times on ephemeral port bind races ("address already in
// use"). When o.ports is pinned, bind errors fail immediately without retry.
func startRealCluster(t testing.TB, seed int64, o realOptions) (*realCluster, error) {
	t.Helper()
	if len(o.ports) > 0 {
		return startRealClusterOnce(t, seed, o)
	}
	var lastErr error
	for range realStartAttempts {
		c, err := startRealClusterOnce(t, seed, o)
		if err == nil {
			return c, nil
		}
		if !strings.Contains(err.Error(), "address already in use") {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

func startRealClusterOnce(t testing.TB, seed int64, o realOptions) (*realCluster, error) {
	t.Helper()
	if o.nodes < 1 {
		return nil, failf("real cluster requires at least 1 node, got %d", o.nodes)
	}
	bin := o.bin
	if bin == "" {
		bin = buildObjnode(t, true, "e2efault")
	}
	ports := o.ports
	if len(ports) == 0 {
		var err error
		ports, err = allocDistinctPorts(2 * o.nodes)
		if err != nil {
			return nil, err
		}
	} else if len(ports) != 2*o.nodes {
		return nil, failf("real cluster with %d nodes needs %d ports, got %d", o.nodes, 2*o.nodes, len(ports))
	}
	dir, err := os.MkdirTemp("", "e2e-tier-r-")
	if err != nil {
		return nil, err
	}
	slots := o.putSlots
	if slots <= 0 {
		slots = realPutSlots
	}
	c := &realCluster{t: t, bin: bin, dir: dir, ledger: newLedger(), r: rand.New(rand.NewPCG(uint64(seed), 7)),
		hc: &http.Client{Timeout: realCallTimeout}, layout: o.layout, blocked: map[[2]int]bool{}, slots: make(chan struct{}, slots)}
	cleanup := true
	defer func() {
		if cleanup {
			c.close()
		}
	}()
	for r := range 3 {
		c.regions = append(c.regions, fmt.Sprintf("r%d", r))
	}
	ids := make([]string, o.nodes)
	for i := range o.nodes {
		ids[i] = fmt.Sprintf("127.0.0.1:%d", ports[i])
	}
	seedCount := min(3, len(ids))
	for i, id := range ids {
		hp := ports[o.nodes+i]
		p := &realProc{node: Node{ID: id, Region: c.regions[i%3]}, httpAddr: fmt.Sprintf("127.0.0.1:%d", hp),
			data: filepath.Join(dir, fmt.Sprintf("node-%d", i)), log: filepath.Join(dir, fmt.Sprintf("node-%d.log", i))}
		p.ports = []int{ports[i], hp}
		p.args = []string{"-id", id, "-region", p.node.Region, "-listen", id, "-http", p.httpAddr, "-data", p.data,
			"-seeds", strings.Join(ids[:seedCount], ","), "-k", strconv.Itoa(o.layout.K), "-m", strconv.Itoa(o.layout.M),
			"-stripe", strconv.Itoa(o.layout.StripeSize), "-shutdown-timeout", "10s"}
		c.procs = append(c.procs, p)
	}
	c.t0 = time.Now()
	var pids []int
	for _, p := range c.procs {
		if err := c.start(p); err != nil {
			if o.onLaunch != nil {
				o.onLaunch(c.dir, pids)
			}
			return nil, failf("launch node %s (%s): %v", p.node.ID, p.node.Region, err)
		}
		pids = append(pids, p.cmd.Process.Pid)
	}
	if o.onLaunch != nil {
		o.onLaunch(c.dir, pids)
	}
	d, err := c.awaitStartup(realBounds.converge)
	if err != nil {
		return nil, err
	}
	c.startup = d
	cleanup = false
	t.Cleanup(c.close)
	return c, nil
}

func newRealCluster(t testing.TB, seed int64, o realOptions) *realCluster {
	t.Helper()
	c, err := startRealCluster(t, seed, o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (c *realCluster) start(p *realProc) error {
	logf, err := os.OpenFile(p.log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(c.bin, p.args...)
	cmd.Stdout, cmd.Stderr = logf, logf
	// Pdeathsig ensures child objnode processes cannot outlive a crashed test runner.
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		return errors.Join(err, logf.Close())
	}
	p.cmd, p.exited = cmd, make(chan struct{})
	p.starts++
	exited := p.exited
	go func() {
		_ = cmd.Wait()
		p.state = cmd.ProcessState
		_ = logf.Close()
		close(exited)
	}()
	return nil
}

func (p *realProc) running() bool {
	if p.exited == nil {
		return false
	}
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

func (p *realProc) signal(sig os.Signal) error {
	if !p.running() {
		return nil
	}
	if err := p.cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func (p *realProc) kill() error {
	if err := p.signal(syscall.SIGKILL); err != nil {
		return err
	}
	if p.exited != nil {
		<-p.exited
	}
	return nil
}

func (c *realCluster) close() {
	if c.closed {
		return
	}
	c.closed = true
	for _, p := range c.procs {
		_ = p.kill()
	}
	c.calls.Wait()
	if c.t.Failed() {
		for _, p := range c.procs {
			b, _ := os.ReadFile(p.log)
			lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
			c.t.Logf("log tail of %s (%s):\n%s", p.node.ID, p.node.Region, strings.Join(lines[max(0, len(lines)-15):], "\n"))
		}
	}
	if err := os.RemoveAll(c.dir); err != nil {
		c.t.Errorf("remove %s: %v", c.dir, err)
	}
	c.hc.CloseIdleConnections()
}

func (c *realCluster) proc(n Node) *realProc {
	for _, p := range c.procs {
		if p.node.ID == n.ID {
			return p
		}
	}
	return nil
}

func (c *realCluster) Tier() string           { return "R" }
func (c *realCluster) Regions() []string      { return slices.Clone(c.regions) }
func (c *realCluster) Layout() storage.Layout { return c.layout }
func (c *realCluster) Rand() *rand.Rand       { return c.r }
func (c *realCluster) Now() time.Duration     { return time.Since(c.t0) }
func (c *realCluster) Ledger() *Ledger        { return c.ledger }
func (c *realCluster) Up(n Node) bool         { p := c.proc(n); return p != nil && p.running() }

func (c *realCluster) Nodes() []Node {
	var out []Node
	for _, p := range c.procs {
		out = append(out, p.node)
	}
	return out
}

type httpError struct {
	status int
	msg    string
	is     []error
}

func (e *httpError) Error() string   { return fmt.Sprintf("http %d: %s", e.status, e.msg) }
func (e *httpError) Unwrap() []error { return e.is }

// statusError maps HTTP error responses back to the repository's sentinel
// errors so shared scenarios can use errors.Is identically on both tiers.
func statusError(status int, body []byte) error {
	var eb struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &eb)
	msg := eb.Error
	if msg == "" {
		msg = strings.TrimSpace(string(body))
	}
	e := &httpError{status: status, msg: msg}
	switch status {
	case http.StatusNotFound:
		e.is = []error{occ.ErrNotFound, storage.ErrNotFound}
		if strings.Contains(msg, storage.ErrNoSuchBucket.Error()) {
			e.is = append(e.is, storage.ErrNoSuchBucket)
		}
	case http.StatusPreconditionFailed:
		e.is = []error{occ.ErrPreconditionFailed}
	case http.StatusServiceUnavailable:
		e.is = []error{storage.ErrUnavailable}
		if strings.Contains(msg, storage.ErrBusy.Error()) {
			e.is = []error{storage.ErrBusy}
		}
	case http.StatusGatewayTimeout:
		e.is = []error{env.ErrTimeout}
	case http.StatusInsufficientStorage:
		e.is = []error{occ.ErrQuotaExceeded}
	case http.StatusConflict:
		e.is = []error{storage.ErrExists}
	}
	return e
}

func condHeader(h http.Header, cond storage.Condition) {
	switch {
	case cond.IfNoneMatch:
		h.Set("If-None-Match", "*")
	case cond.IfMatch == "*":
		h.Set("If-Match", "*")
	case cond.IfMatchList:
		var tags []string
		for _, e := range cond.IfMatchAny {
			tags = append(tags, `"`+e+`"`)
		}
		h.Set("If-Match", strings.Join(tags, ", "))
	case cond.IfMatch != "":
		h.Set("If-Match", `"`+cond.IfMatch+`"`)
	}
}

func (c *realCluster) do(at Node, method, path string, q url.Values, body []byte, cond storage.Condition) (*http.Response, []byte, error) {
	p := c.proc(at)
	if p == nil {
		return nil, nil, fmt.Errorf("e2e: no node %s", at.ID)
	}
	u := "http://" + p.httpAddr + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	ctx, cancel := context.WithTimeout(context.Background(), realCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	condHeader(req.Header, cond)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("e2e: %s %s: %v: %w", method, path, err, env.ErrUnreachable)
	}
	b, rerr := io.ReadAll(resp.Body)
	cerr := resp.Body.Close()
	if err := errors.Join(rerr, cerr); err != nil {
		return resp, nil, fmt.Errorf("e2e: %s %s: read body: %v: %w", method, path, err, env.ErrUnreachable)
	}
	if resp.StatusCode >= 300 {
		return resp, b, statusError(resp.StatusCode, b)
	}
	return resp, b, nil
}

func (c *realCluster) async(k *Call, fn func() error) *Call {
	k.wait = make(chan struct{})
	k.Began = c.Now()
	c.calls.Add(1)
	go func() {
		defer c.calls.Done()
		k.Err = fn()
		k.Ended = c.Now()
		k.done = true
		c.ledger.settle(k)
		close(k.wait)
	}()
	return k
}

func objPath(bucket, key string) string { return "/" + bucket + "/" + url.PathEscape(key) }

func objectInfo(resp *http.Response, bucket, key string, size int) storage.ObjectInfo {
	return storage.ObjectInfo{Bucket: bucket, Key: key, Size: int64(size), ETag: strings.Trim(resp.Header.Get("ETag"), `"`),
		Version: resp.Header.Get("X-Object-Version"), ContentSHA: resp.Header.Get("X-Content-Sha256"), ContentType: resp.Header.Get("Content-Type")}
}

func (c *realCluster) jsonCall(k *Call, method, path string, q url.Values, body []byte, cond storage.Condition, out any) *Call {
	return c.async(k, func() error {
		_, b, err := c.do(k.At, method, path, q, body, cond)
		if err != nil {
			return err
		}
		if out == nil || len(b) == 0 {
			return nil
		}
		return json.Unmarshal(b, out)
	})
}

func (c *realCluster) CreateBucket(at Node, spec storage.BucketSpec) *Call {
	q := url.Values{}
	if spec.Region != "" {
		q.Set("region", spec.Region)
	}
	if spec.QuotaBytes > 0 {
		q.Set("quota-bytes", strconv.FormatUint(spec.QuotaBytes, 10))
	}
	if spec.QuotaObjects > 0 {
		q.Set("quota-objects", strconv.FormatUint(spec.QuotaObjects, 10))
	}
	return c.jsonCall(&Call{Op: "create", At: at, Bucket: spec.Name}, http.MethodPut, "/"+spec.Name, q, nil, storage.Condition{}, nil)
}

func (c *realCluster) Put(at Node, bucket, key string, data []byte, cond storage.Condition) *Call {
	k := &Call{Op: "put", At: at, Bucket: bucket, Key: key}
	c.ledger.attempt(k, data)
	return c.async(k, func() error {
		c.slots <- struct{}{}
		defer func() { <-c.slots }()
		k.Began = c.Now()
		_, b, err := c.do(at, http.MethodPut, objPath(bucket, key), nil, data, cond)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, &k.Info)
	})
}

func (c *realCluster) Get(at Node, bucket, key string) *Call {
	k := &Call{Op: "get", At: at, Bucket: bucket, Key: key}
	return c.async(k, func() error {
		resp, b, err := c.do(at, http.MethodGet, objPath(bucket, key), nil, nil, storage.Condition{})
		if err != nil {
			return err
		}
		k.Data = b
		k.Info = objectInfo(resp, bucket, key, len(b))
		if cl := resp.Header.Get("Content-Length"); cl != "" && cl != strconv.Itoa(len(b)) {
			return fmt.Errorf("e2e: get %s/%s: %d bytes for a Content-Length of %s", bucket, key, len(b), cl)
		}
		return nil
	})
}

func (c *realCluster) Delete(at Node, bucket, key string, cond storage.Condition) *Call {
	k := &Call{Op: "delete", At: at, Bucket: bucket, Key: key}
	return c.async(k, func() error {
		resp, _, err := c.do(at, http.MethodDelete, objPath(bucket, key), nil, nil, cond)
		if err != nil {
			return err
		}
		k.Info = objectInfo(resp, bucket, key, 0)
		return nil
	})
}

func (c *realCluster) List(at Node, bucket string) *Call {
	k := &Call{Op: "list", At: at, Bucket: bucket}
	return c.jsonCall(k, http.MethodGet, "/"+bucket, nil, nil, storage.Condition{}, &k.Infos)
}

func (c *realCluster) PutTag(at Node, bucket, key, name, value string, cond storage.Condition) *Call {
	k := &Call{Op: "tag", At: at, Bucket: bucket, Key: key}
	return c.jsonCall(k, http.MethodPut, objPath(bucket, key), url.Values{"tag": {name}}, []byte(value), cond, &k.Tag)
}

func (c *realCluster) Tags(at Node, bucket, key string) *Call {
	k := &Call{Op: "tags", At: at, Bucket: bucket, Key: key}
	return c.jsonCall(k, http.MethodGet, objPath(bucket, key), url.Values{"tags": {""}}, nil, storage.Condition{}, &k.Tags)
}

func (c *realCluster) PutACL(at Node, bucket, principal, perm string, cond storage.Condition) *Call {
	k := &Call{Op: "acl", At: at, Bucket: bucket, Key: principal}
	return c.jsonCall(k, http.MethodPut, "/"+bucket, url.Values{"acl": {principal}}, []byte(perm), cond, &k.ACL)
}

func (c *realCluster) ACL(at Node, bucket, principal string) *Call {
	k := &Call{Op: "acl-get", At: at, Bucket: bucket, Key: principal}
	return c.jsonCall(k, http.MethodGet, "/"+bucket, url.Values{"acl": {principal}}, nil, storage.Condition{}, &k.ACL)
}

func (c *realCluster) SetQuota(at Node, bucket string, bytes, objects uint64) *Call {
	k := &Call{Op: "quota-set", At: at, Bucket: bucket}
	body, _ := json.Marshal(map[string]uint64{"bytes": bytes, "objects": objects})
	return c.jsonCall(k, http.MethodPut, "/"+bucket, url.Values{"quota": {""}}, body, storage.Condition{}, &k.Quota)
}

func (c *realCluster) Quota(at Node, bucket string) *Call {
	k := &Call{Op: "quota", At: at, Bucket: bucket}
	return c.jsonCall(k, http.MethodGet, "/"+bucket, url.Values{"quota": {""}}, nil, storage.Condition{}, &k.Quota)
}

func (c *realCluster) StartMigration(at Node, bucket, to string) *Call {
	k := &Call{Op: "migrate", At: at, Bucket: bucket}
	body, _ := json.Marshal(map[string]string{"bucket": bucket, "to": to})
	return c.jsonCall(k, http.MethodPost, "/_migrations", nil, body, storage.Condition{}, &k.Mig)
}

func (c *realCluster) Migration(at Node, id string) *Call {
	k := &Call{Op: "migration", At: at, Key: id}
	return c.jsonCall(k, http.MethodGet, "/_migrations/"+url.PathEscape(id), nil, nil, storage.Condition{}, &k.Mig)
}

func (c *realCluster) Wait(calls ...*Call) error {
	for _, k := range calls {
		if k.wait != nil {
			<-k.wait
		}
	}
	return nil
}

func (c *realCluster) Sleep(d time.Duration) error {
	time.Sleep(d)
	return nil
}

func (c *realCluster) Await(what string, bound time.Duration, cond func() (bool, error)) (time.Duration, error) {
	start := time.Now()
	var last error
	for {
		ok, err := cond()
		if ok && err == nil {
			return time.Since(start), nil
		}
		last = err
		if time.Since(start) > bound {
			return time.Since(start), failf("%s not within %v (last error %v)", what, bound, last)
		}
		time.Sleep(realPoll)
	}
}

// writeBlocklists atomically updates or removes each node's .e2e-blocklist file
// so e2efault-built objnode processes apply the current partition topology.
func (c *realCluster) writeBlocklists() error {
	for i, p := range c.procs {
		var peers []string
		for j, q := range c.procs {
			if c.blocked[[2]int{i, j}] {
				peers = append(peers, q.node.ID)
			}
		}
		path := p.data + blocklistSuffix
		if len(peers) == 0 {
			if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			continue
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, []byte(strings.Join(peers, "\n")+"\n"), 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, path); err != nil {
			return err
		}
	}
	return nil
}

func (c *realCluster) index(n Node) int {
	for i, p := range c.procs {
		if p.node.ID == n.ID {
			return i
		}
	}
	return -1
}

func (c *realCluster) BlockOneWay(from, to []Node) error {
	for _, a := range from {
		for _, b := range to {
			c.blocked[[2]int{c.index(a), c.index(b)}] = true
		}
	}
	return c.writeBlocklists()
}

func (c *realCluster) Partition(side, other []Node) error {
	for _, a := range side {
		for _, b := range other {
			c.blocked[[2]int{c.index(a), c.index(b)}] = true
			c.blocked[[2]int{c.index(b), c.index(a)}] = true
		}
	}
	return c.writeBlocklists()
}

func (c *realCluster) PartitionRegion(r string) error {
	return c.Partition(inRegion(c, r), outside(c, r))
}

func (c *realCluster) Heal() error {
	c.blocked = map[[2]int]bool{}
	return c.writeBlocklists()
}

func (c *realCluster) Crash(n Node) error {
	p := c.proc(n)
	if p == nil {
		return fmt.Errorf("e2e: crash %s: no such node", n.ID)
	}
	return p.kill()
}

func (c *realCluster) Restart(n Node) error {
	p := c.proc(n)
	if p == nil {
		return fmt.Errorf("e2e: restart %s: no such node", n.ID)
	}
	if p.running() {
		return fmt.Errorf("e2e: restart %s: still running", n.ID)
	}
	return c.start(p)
}

func (c *realCluster) Wipe(n Node) error {
	p := c.proc(n)
	if p == nil {
		return fmt.Errorf("e2e: wipe %s: no such node", n.ID)
	}
	if err := p.kill(); err != nil {
		return err
	}
	if err := os.RemoveAll(p.data); err != nil {
		return err
	}
	return c.start(p)
}

func (c *realCluster) Shards(n Node) ([]ShardFile, error) {
	p := c.proc(n)
	if p == nil {
		return nil, fmt.Errorf("e2e: shards of %s: no such node", n.ID)
	}
	root := filepath.Join(p.data, "shards")
	var out []ShardFile
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(p.data, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		out = append(out, ShardFile{Name: filepath.ToSlash(rel), Blob: b})
		return nil
	})
	return out, err
}

// Rot flips a byte inside the shard file's payload and atomically replaces the
// file on disk to simulate silent bit-rot on tier R.
func (c *realCluster) Rot(n Node, name string) error {
	p := c.proc(n)
	if p == nil {
		return fmt.Errorf("e2e: rot on %s: no such node", n.ID)
	}
	path := filepath.Join(p.data, filepath.FromSlash(name))
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return fmt.Errorf("e2e: rot %s: empty file", name)
	}
	at := len(b) / 2
	if len(b) > storage.HeaderSize {
		at = storage.HeaderSize + (len(b)-storage.HeaderSize)/2
	}
	b[at] ^= 0x5a
	tmp := filepath.Join(c.dir, "rot.tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *realCluster) Health(n Node) (storage.Health, error) {
	var h storage.Health
	_, b, err := c.do(n, http.MethodGet, "/_health", nil, nil, storage.Condition{})
	if err != nil {
		return h, err
	}
	return h, json.Unmarshal(b, &h)
}

func (c *realCluster) Members(n Node) ([]storage.MemberInfo, error) {
	var ms []storage.MemberInfo
	_, b, err := c.do(n, http.MethodGet, "/_cluster/members", nil, nil, storage.Condition{})
	if err != nil {
		return nil, err
	}
	return ms, json.Unmarshal(b, &ms)
}

func (c *realCluster) Converged() (bool, error) {
	ups := upNodes(c)
	if len(ups) == 0 {
		return false, nil
	}
	want := map[string]bool{}
	for _, n := range ups {
		want[n.ID] = true
	}
	var digest, root string
	for i, n := range ups {
		h, err := c.Health(n)
		if err != nil || !h.Ready || h.MetaDigest == "" {
			return false, nil
		}
		if i == 0 {
			digest, root = h.MetaDigest, h.MetaRoot
		} else if h.MetaDigest != digest || h.MetaRoot != root {
			return false, nil
		}
		ms, err := c.Members(n)
		if err != nil {
			return false, nil
		}
		live := 0
		for _, m := range ms {
			if m.State == "alive" {
				if !want[m.ID] {
					return false, nil
				}
				live++
			}
		}
		if live != len(ups) {
			return false, nil
		}
	}
	return true, nil
}

func (c *realCluster) logOf(n Node) string {
	p := c.proc(n)
	if p == nil {
		return ""
	}
	b, _ := os.ReadFile(p.log)
	return string(b)
}
