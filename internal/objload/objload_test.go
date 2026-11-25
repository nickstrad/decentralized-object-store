package objload

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/objctl/objctltest"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/storage"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
	"github.com/nickstrad/object-store/internal/storage/httpapi/httpapitest"
	"github.com/nickstrad/object-store/internal/storage/httpclient"
)

func TestDefaultsValid(t *testing.T) {
	if err := Defaults().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestConfigValidate(t *testing.T) {
	for name, mut := range map[string]func(*Config){
		"no address":    func(c *Config) { c.Addrs = nil },
		"empty address": func(c *Config) { c.Addrs = []string{"a:1", " "} },
		"zero rate":     func(c *Config) { c.Rate = 0 },
		"NaN rate":      func(c *Config) { c.Rate = math.NaN() },
		"huge rate":     func(c *Config) { c.Rate = 1e6 },
		"neg duration":  func(c *Config) { c.Duration = -time.Second },
		"no workers":    func(c *Config) { c.Workers = 0 },
		"many workers":  func(c *Config) { c.Workers = 5000 },
		"profile":       func(c *Config) { c.Profile = "heavy" },
		"no keys":       func(c *Config) { c.Keys = 0 },
		"no buckets":    func(c *Config) { c.Buckets = 0 },
		"bucket prefix": func(c *Config) { c.BucketPrefix = "Bad_Prefix" },
		"size order":    func(c *Config) { c.MinSize, c.MaxSize = 10, 5 },
		"size max":      func(c *Config) { c.MaxSize = httpapi.DefaultMaxObjectSize + 1 },
		"neg size":      func(c *Config) { c.MinSize = -1 },
		"timeout":       func(c *Config) { c.Timeout = 0 },
		"report":        func(c *Config) { c.Report = -1 },
	} {
		c := Defaults()
		mut(&c)
		if err := c.Validate(); !errors.Is(err, ErrConfig) {
			t.Errorf("%s: Validate() = %v, want ErrConfig", name, err)
		}
	}
}

func TestParseSize(t *testing.T) {
	for in, want := range map[string][2]int{
		"0": {0, 0}, "100": {100, 100}, "1K-16K": {1024, 16384}, "2kib-1MiB": {2048, 1 << 20}, "512B-1k": {512, 1024}, " 3 - 4 ": {3, 4},
	} {
		lo, hi, err := ParseSize(in)
		if err != nil || lo != want[0] || hi != want[1] {
			t.Errorf("ParseSize(%q) = %d, %d, %v; want %v", in, lo, hi, err, want)
		}
	}
	for _, in := range []string{"", "x", "-1", "5-3", "1-x", "64M", "1.5K"} {
		if _, _, err := ParseSize(in); !errors.Is(err, ErrConfig) {
			t.Errorf("ParseSize(%q) err = %v, want ErrConfig", in, err)
		}
	}
}

func TestChooserPicksExactWeights(t *testing.T) {
	for name, mix := range Profiles {
		ch, err := NewChooser(mix)
		if err != nil {
			t.Fatal(name, err)
		}
		var got Mix
		for r := range ch.Total() {
			got[ch.Pick(r)]++
		}
		if got != mix {
			t.Errorf("%s: picks over [0,%d) = %v, want %v", name, ch.Total(), got, mix)
		}
	}
	if _, err := NewChooser(Mix{}); !errors.Is(err, ErrConfig) {
		t.Errorf("empty mix: %v", err)
	}
	if _, err := NewChooser(Mix{OpGet: 1, OpPut: -1}); !errors.Is(err, ErrConfig) {
		t.Errorf("negative weight: %v", err)
	}
}

func TestPlannerRespectsMix(t *testing.T) {
	cfg := Defaults()
	cfg.Profile, cfg.Seed = "mixed", 7
	p, err := NewPlanner(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const n = 200000
	var got [NumOps]int
	for range n {
		got[p.Next().Op]++
	}
	mix := Profiles["mixed"]
	total := 0
	for _, w := range mix {
		total += w
	}
	for op, w := range mix {
		want := float64(n) * float64(w) / float64(total)
		if math.Abs(float64(got[op])-want) > 0.05*want+50 {
			t.Errorf("%v: %d draws, want about %.0f", Op(op), got[op], want)
		}
	}
}

func TestPlannerDeterministic(t *testing.T) {
	cfg := Defaults()
	cfg.Addrs = []string{"a:1", "b:1", "c:1"}
	cfg.Profile, cfg.Keys, cfg.Buckets, cfg.MinSize, cfg.MaxSize = "mixed", 10, 3, 5, 9
	plan := func(seed uint64) []Request {
		c := cfg
		c.Seed = seed
		p, err := NewPlanner(c)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]Request, 1000)
		for i := range out {
			out[i] = p.Next()
		}
		return out
	}
	a, b, other := plan(42), plan(42), plan(43)
	same := 0
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("request %d differs for one seed: %+v vs %+v", i, a[i], b[i])
		}
		if a[i] == other[i] {
			same++
		}
		r := a[i]
		if r.Seq != int64(i) || r.Addr != i%3 {
			t.Fatalf("request %d: seq %d addr %d", i, r.Seq, r.Addr)
		}
		if r.Size < 5 || r.Size > 9 {
			t.Fatalf("request %d: size %d outside 5-9", i, r.Size)
		}
		if r.Bucket != "objload-0" && r.Bucket != "objload-1" && r.Bucket != "objload-2" {
			t.Fatalf("request %d: bucket %q", i, r.Bucket)
		}
		if missing := strings.HasPrefix(r.Key, "missing/"); missing != (r.Op == OpGetMissing) {
			t.Fatalf("request %d: op %v key %q", i, r.Op, r.Key)
		}
		if r.Op != OpGetMissing && (len(r.Key) != len("key-000000") || r.Key > "key-000009") {
			t.Fatalf("request %d: key %q outside the key space", i, r.Key)
		}
	}
	if same > 10 {
		t.Errorf("seeds 42 and 43 agree on %d of 1000 requests", same)
	}
	if _, err := NewPlanner(Config{}); !errors.Is(err, ErrConfig) {
		t.Errorf("NewPlanner(Config{}) = %v", err)
	}
}

func TestPayload(t *testing.T) {
	for _, n := range []int{0, 1, 7, 8, 9, 1000} {
		a, b := Payload(3, n), Payload(3, n)
		if len(a) != n || !bytes.Equal(a, b) {
			t.Fatalf("Payload(3, %d) not deterministic or wrong length", n)
		}
	}
	if bytes.Equal(Payload(3, 64), Payload(4, 64)) {
		t.Error("different seeds give the same payload")
	}
}

func TestPacer(t *testing.T) {
	t0 := time.Unix(1000, 0)
	p := NewPacer(t0, 4, time.Second)
	for i := range int64(4) {
		due, skipped := p.Next(t0)
		if want := t0.Add(time.Duration(i) * 250 * time.Millisecond); !due.Equal(want) || skipped != 0 {
			t.Fatalf("request %d: due %v skipped %d, want %v", i, due.Sub(t0), skipped, want.Sub(t0))
		}
	}
	due, skipped := p.Next(t0.Add(1500 * time.Millisecond))
	if !due.Equal(t0.Add(time.Second)) || skipped != 0 {
		t.Fatalf("0.5s late: due %v skipped %d", due.Sub(t0), skipped)
	}
	due, skipped = p.Next(t0.Add(10 * time.Second))
	if !due.Equal(t0.Add(9*time.Second)) || skipped != 31 {
		t.Fatalf("after a 10s stall: due %v skipped %d, want 9s and 31", due.Sub(t0), skipped)
	}
	frac := NewPacer(t0, 3, time.Second)
	if got := frac.Due(3).Sub(t0); got != time.Second {
		t.Errorf("rate 3, request 3: due %v", got)
	}
	if got := frac.Due(1).Sub(t0); got != 333333333*time.Nanosecond {
		t.Errorf("rate 3, request 1: due %v", got)
	}
	slow := NewPacer(t0, 0.5, time.Second)
	if got := slow.Due(1).Sub(t0); got != 2*time.Second {
		t.Errorf("rate 0.5, request 1: due %v", got)
	}
}

type statusErr int

func (s statusErr) Error() string   { return fmt.Sprintf("status %d", int(s)) }
func (s statusErr) StatusCode() int { return int(s) }

func TestClassify(t *testing.T) {
	ctxErr := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 0)
		defer cancel()
		<-ctx.Done()
		return ctx.Err()
	}()
	for _, tc := range []struct {
		err  error
		want Outcome
	}{
		{nil, Outcome{C2xx, 200, false}},
		{&httpclient.HTTPError{Status: 404}, Outcome{C4xx, 404, false}},
		{fmt.Errorf("wrapped: %w", statusErr(412)), Outcome{C4xx, 412, false}},
		{statusErr(409), Outcome{C4xx, 409, true}},
		{statusErr(503), Outcome{C5xx, 503, true}},
		{statusErr(301), Outcome{C3xx, 301, true}},
		{statusErr(204), Outcome{C2xx, 204, false}},
		{statusErr(0), Outcome{C5xx, 0, true}},
		{ctxErr, Outcome{CTimeout, 0, true}},
		{env.ErrTimeout, Outcome{CTimeout, 0, true}},
		{fmt.Errorf("dial: %w", env.ErrUnreachable), Outcome{CNet, 0, true}},
		{context.Canceled, Outcome{CNet, 0, true}},
		{storage.ErrNotFound, Outcome{C4xx, 404, false}},
		{occ.ErrPreconditionFailed, Outcome{C4xx, 412, false}},
		{errors.New("boom"), Outcome{C5xx, 500, true}},
	} {
		if got := Classify(tc.err); got != tc.want {
			t.Errorf("Classify(%v) = %+v, want %+v", tc.err, got, tc.want)
		}
	}
}

func TestStrings(t *testing.T) {
	if OpGet.String() != "get" || OpMembers.String() != "members" || Op(-1).String() != "op(-1)" || NumOps.String() != "op(12)" {
		t.Error("Op.String")
	}
	if C2xx.String() != "2xx" || CNet.String() != "net" || NumClasses.String() != "class(6)" {
		t.Error("Class.String")
	}
	if err := Execute(context.Background(), objctltest.NewFake(), Request{Op: NumOps}); !errors.Is(err, ErrConfig) {
		t.Errorf("Execute(unknown op) = %v", err)
	}
}

func TestRecorderAndReport(t *testing.T) {
	var r Recorder
	r.Record(OpGet, Outcome{C2xx, 200, false}, 2*time.Millisecond)
	r.Record(OpGet, Outcome{C4xx, 404, false}, 4*time.Millisecond)
	r.Record(OpPut, Outcome{C5xx, 503, true}, 9*time.Millisecond)
	r.Record(OpPut, Outcome{CNet, 0, true}, time.Millisecond)
	r.Miss(0)
	r.Miss(3)
	iv := r.TakeInterval()
	if iv.Requests != 4 || iv.Class(C2xx) != 1 || iv.Class(C4xx) != 1 || iv.Class(CNet) != 1 || iv.Op(OpPut) != 2 ||
		iv.Failed != 2 || iv.Missed != 3 || iv.LatencyMax != 9*time.Millisecond || iv.MeanLatency() != 4*time.Millisecond {
		t.Fatalf("interval counts %+v", iv)
	}
	if again := r.TakeInterval(); again.Requests != 0 || again.Missed != 0 {
		t.Fatalf("interval not reset: %+v", again)
	}
	line := StatusLine(5*time.Second, 2*time.Second, iv, 20)
	for _, want := range []string{"5s", "2.0 req/s (target 20)", "2xx 1", "4xx 1", "5xx 1", "net 1", "MISSED 3", "mean 4.0ms max 9.0ms"} {
		if !strings.Contains(line, want) {
			t.Errorf("status line %q lacks %q", line, want)
		}
	}
	if strings.Contains(line, "timeout") {
		t.Errorf("status line %q shows an empty timeout class", line)
	}
	total := r.Total()
	total.ByStatus[999] = 1
	if r.Total().ByStatus[999] != 0 {
		t.Error("Total shares its status map")
	}
	s := Summary{Counts: r.Total(), Elapsed: 2 * time.Second, Target: 4, Seed: 9}
	var b bytes.Buffer
	s.Write(&b)
	out := b.String()
	for _, want := range []string{"4 requests in 2.0s, 2.0 req/s achieved of 4 req/s target (50%), seed 9", "get", "put",
		"404 Not Found: 1, 503 Service Unavailable: 1", "failures: 2", "missed: 3"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
	if !s.Succeeded() || (Summary{}).Succeeded() || (Summary{}).Achieved() != 0 || (&Counts{}).MeanLatency() != 0 {
		t.Error("Succeeded/Achieved/MeanLatency on edge cases")
	}
	var empty bytes.Buffer
	Summary{}.Write(&empty)
	if !strings.Contains(empty.String(), "0 requests") || strings.Contains(empty.String(), "non-2xx") {
		t.Errorf("empty summary:\n%s", empty.String())
	}
}

func inProcess(t *testing.T, svc httpapi.Service, n int, tr http.RoundTripper) []httpapi.Service {
	t.Helper()
	if tr == nil {
		tr = httpapitest.InProcess(httpapi.NewHandler(svc, httpapi.Limits{}))
	}
	out := make([]httpapi.Service, n)
	for i := range out {
		c, err := httpclient.New(fmt.Sprintf("node-%d:8080", i), httpclient.Options{Transport: tr})
		if err != nil {
			t.Fatal(err)
		}
		out[i] = c
	}
	return out
}

func TestRunMixedThroughHandler(t *testing.T) {
	cfg := Defaults()
	cfg.Addrs = []string{"node-0:8080", "node-1:8080"}
	cfg.Profile, cfg.Rate, cfg.Duration, cfg.Report, cfg.Seed = "mixed", 2000, 400*time.Millisecond, 100*time.Millisecond, 1
	cfg.Keys, cfg.MinSize, cfg.MaxSize = 20, 0, 256
	fake := objctltest.NewFake()
	var out bytes.Buffer
	s, err := Run(context.Background(), cfg, inProcess(t, fake, 2, nil), &out)
	if err != nil {
		t.Fatal(err)
	}
	if s.Requests < 100 || !s.Succeeded() {
		t.Fatalf("only %d requests, %d 2xx:\n%s", s.Requests, s.Class(C2xx), out.String())
	}
	if s.Failed != 0 || s.ByStatus[404] == 0 || s.ByStatus[412] == 0 {
		t.Errorf("failed %d, statuses %v", s.Failed, s.ByStatus)
	}
	for _, op := range []Op{OpGet, OpPut, OpTag, OpCreate, OpStalePut, OpGetMissing} {
		if s.Op(op) == 0 {
			t.Errorf("no %v request", op)
		}
	}
	if s.ByOp[OpGetMissing][C2xx] != 0 || s.ByOp[OpStalePut][C2xx] != 0 {
		t.Errorf("get-missing or stale-put succeeded: %v %v", s.ByOp[OpGetMissing], s.ByOp[OpStalePut])
	}
	if s.Achieved() < 0.5*cfg.Rate {
		t.Errorf("achieved %.0f req/s of %v", s.Achieved(), cfg.Rate)
	}
	if !strings.Contains(out.String(), "req/s (target 2000)") || !strings.Contains(out.String(), "seed 1") {
		t.Errorf("output:\n%s", out.String())
	}
	for b := range cfg.Buckets {
		if _, err := fake.ListObjects(context.Background(), BucketName(cfg.BucketPrefix, b), "", 0); err != nil {
			t.Errorf("bucket %d: %v", b, err)
		}
	}
	var out2 bytes.Buffer
	again, err := Run(context.Background(), cfg, inProcess(t, fake, 2, nil), &out2)
	if err != nil || !again.Succeeded() || strings.Contains(out.String()+out2.String(), "waiting") {
		t.Errorf("second run over existing buckets: %v, %+v\n%s%s", err, again.Counts, out.String(), out2.String())
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	cfg := Defaults()
	cfg.Rate, cfg.Report = 500, 0
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	s, err := Run(ctx, cfg, inProcess(t, objctltest.NewFake(), 1, nil), &bytes.Buffer{})
	if err != nil || s.Requests == 0 || s.Elapsed > 5*time.Second {
		t.Fatalf("Run = %v, %d requests in %v", err, s.Requests, s.Elapsed)
	}
}

type blocking struct{}

func (blocking) RoundTrip(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

type refusing struct{}

func (refusing) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("connection refused")
}

func TestRunCountsTimeoutsAndMisses(t *testing.T) {
	setupAttempts, setupPause = 2, time.Millisecond
	t.Cleanup(func() { setupAttempts, setupPause = 30, time.Second })
	cfg := Defaults()
	cfg.Rate, cfg.Workers, cfg.Duration, cfg.Timeout, cfg.Report = 200, 1, 200*time.Millisecond, 20*time.Millisecond, 0
	var out bytes.Buffer
	s, err := Run(context.Background(), cfg, inProcess(t, nil, 1, blocking{}), &out)
	if err != nil {
		t.Fatal(err)
	}
	if s.Succeeded() || s.Class(CTimeout) == 0 || s.Missed == 0 || s.Failed != s.Requests {
		t.Errorf("counts %+v", s.Counts)
	}
	if !strings.Contains(out.String(), "waiting for 127.0.0.1:8080") || !strings.Contains(out.String(), "was not created") {
		t.Errorf("output:\n%s", out.String())
	}

	cfg.Timeout, cfg.Duration = time.Second, 50*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)
	s, err = Run(ctx, cfg, inProcess(t, nil, 1, refusing{}), &bytes.Buffer{})
	if err != nil || s.Succeeded() {
		t.Errorf("refusing: %v %+v", err, s.Counts)
	}
	if s.Requests > 0 && s.Class(CNet) != s.Requests {
		t.Errorf("refusing: classes %v", s.ByOp)
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	if _, err := Run(context.Background(), Config{}, nil, &bytes.Buffer{}); !errors.Is(err, ErrConfig) {
		t.Errorf("empty config: %v", err)
	}
	if _, err := Run(context.Background(), Defaults(), nil, &bytes.Buffer{}); !errors.Is(err, ErrConfig) {
		t.Errorf("no services: %v", err)
	}
}
