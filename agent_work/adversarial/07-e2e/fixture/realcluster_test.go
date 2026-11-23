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

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

const (
	tierREnv        = "E2E_TIER_R"
	blocklistSuffix = ".e2e-blocklist"
	realStripe      = 64 << 10
	realCallTimeout = 60 * time.Second
	realPoll        = 100 * time.Millisecond
	realPutSlots    = 4
)

var realBounds = bounds{
	converge:   90 * time.Second,
	repair:     90 * time.Second,
	migrate:    120 * time.Second,
	catchUp:    90 * time.Second,
	refill:     120 * time.Second,
	readBatch:  8,
	writeBatch: 4,
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
		if _, err := os.Stat(filepath.Join(dir, "cmd", "objnode", "main.go")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("e2e: no go.mod above the working directory")
		}
		dir = parent
	}
}

func buildObjnode(t testing.TB, race bool, tags string) string {
	t.Helper()
	key := fmt.Sprintf("%v|%s", race, tags)
	binMu.Lock()
	defer binMu.Unlock()
	if p, ok := binPath[key]; ok {
		return p
	}
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp("", "e2e-objnode-bin-")
	if err != nil {
		t.Fatal(err)
	}
	binDirs = append(binDirs, dir)
	out := filepath.Join(dir, "objnode")
	args := []string{"build", "-o", out}
	if race {
		args = append(args, "-race")
	}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	cmd := exec.Command("go", append(args, "./cmd/objnode")...)
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build objnode: %v\n%s", err, b)
	}
	binPath[key] = out
	return out
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

func freePort(t testing.TB) int {
	t.Helper()
	for range 50 {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		u, uerr := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", port))
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		if uerr == nil {
			if err := u.Close(); err != nil {
				t.Fatal(err)
			}
			return port
		}
	}
	t.Fatal("no free port")
	return 0
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
	nodes  int
	layout storage.Layout
}

func defaultRealOptions() realOptions {
	return realOptions{nodes: 9, layout: storage.Layout{K: 4, M: 2, StripeSize: realStripe}}
}

func newRealCluster(t testing.TB, seed int64, o realOptions) *realCluster {
	t.Helper()
	dir, err := os.MkdirTemp("", "e2e-tier-r-")
	if err != nil {
		t.Fatal(err)
	}
	c := &realCluster{t: t, bin: buildObjnode(t, true, "e2efault"), dir: dir, ledger: newLedger(), r: rand.New(rand.NewPCG(uint64(seed), 7)),
		hc: &http.Client{Timeout: realCallTimeout}, layout: o.layout, blocked: map[[2]int]bool{}, slots: make(chan struct{}, realPutSlots)}
	t.Cleanup(c.close)
	for r := range 3 {
		c.regions = append(c.regions, fmt.Sprintf("r%d", r))
	}
	var ids []string
	for range o.nodes {
		ids = append(ids, fmt.Sprintf("127.0.0.1:%d", freePort(t)))
	}
	for i, id := range ids {
		hp := freePort(t)
		p := &realProc{node: Node{ID: id, Region: c.regions[i%3]}, httpAddr: fmt.Sprintf("127.0.0.1:%d", hp),
			data: filepath.Join(dir, fmt.Sprintf("node-%d", i)), log: filepath.Join(dir, fmt.Sprintf("node-%d.log", i))}
		port, _ := strconv.Atoi(strings.TrimPrefix(id, "127.0.0.1:"))
		p.ports = []int{port, hp}
		p.args = []string{"-id", id, "-region", p.node.Region, "-listen", id, "-http", p.httpAddr, "-data", p.data,
			"-seeds", strings.Join(ids[:3], ","), "-k", strconv.Itoa(o.layout.K), "-m", strconv.Itoa(o.layout.M),
			"-stripe", strconv.Itoa(o.layout.StripeSize), "-shutdown-timeout", "10s"}
		c.procs = append(c.procs, p)
	}
	c.t0 = time.Now()
	for _, p := range c.procs {
		if err := c.start(p); err != nil {
			t.Fatal(err)
		}
	}
	d, err := c.Await("cluster formed", realBounds.converge, c.Converged)
	if err != nil {
		t.Fatal(err)
	}
	c.startup = d
	return c
}

func (c *realCluster) start(p *realProc) error {
	logf, err := os.OpenFile(p.log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	cmd := exec.Command(c.bin, p.args...)
	cmd.Stdout, cmd.Stderr = logf, logf
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
