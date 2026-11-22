package saga_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

const (
	hostID      = env.NodeID("n1")
	testDef     = "test"
	stepLatency = 10 * time.Millisecond
	beginAt     = env.Time(time.Millisecond)
)

var errFlaky = errors.New("flaky step")

type result struct {
	err   error
	acted bool
}

func ok() result                { return result{} }
func retry(acted bool) result   { return result{err: errFlaky, acted: acted} }
func permanent() result         { return result{err: fmt.Errorf("refused: %w", saga.ErrPermanent)} }
func compFail() result          { return result{err: errFlaky, acted: true} }
func stepName(i int) string     { return fmt.Sprintf("s%d", i) }
func output(i int) []byte       { return []byte{byte(i), 0xAB} }
func sagaKey(id string) saga.ID { return saga.ID(id) }

type fcall struct {
	comp    bool
	key     saga.Key
	attempt int
	at      env.Time
	outputs int
	inc     uint32
}

// fx is a scriptable multi-step test fixture that tracks side effects, call
// order, and whether any callback fired after RecEnd became durable.
type fx struct {
	n, pivot   int
	do         [][]result
	comp       [][]result
	latency    time.Duration
	calls      []fcall
	effect     []bool
	doCalls    []int
	compCalls  []int
	compOK     []int
	s          *sim.Sim
	afterEnd   []string
	endedAt    int
	ended      bool
	checkEnded func() bool
}

func newFx(n, pivot int) *fx {
	return &fx{
		n: n, pivot: pivot,
		do: make([][]result, n), comp: make([][]result, n),
		latency: stepLatency,
		effect:  make([]bool, n), doCalls: make([]int, n), compCalls: make([]int, n),
		endedAt: -1,
	}
}

func (f *fx) def(e env.Env) saga.Definition {
	steps := make([]saga.Step, f.n)
	for i := range steps {
		steps[i] = &fstep{f: f, i: i, e: e}
	}
	return saga.Definition{Name: testDef, Steps: steps, Pivot: f.pivot}
}

type fstep struct {
	f *fx
	i int
	e env.Env
}

func (s *fstep) Name() string { return stepName(s.i) }

func (s *fstep) record(comp bool, c saga.Call) {
	f := s.f
	inc := uint32(0)
	if f.s != nil {
		inc = f.s.Status(hostID).Incarnation
	}
	f.calls = append(f.calls, fcall{comp: comp, key: c.Key, attempt: c.Attempt, at: s.e.Clock.Now(), outputs: len(c.Outputs), inc: inc})
	if f.checkEnded != nil && f.checkEnded() {
		f.afterEnd = append(f.afterEnd, fmt.Sprintf("comp=%v step=%d attempt=%d", comp, c.Key.Step, c.Attempt))
	}
}

func (s *fstep) Do(c saga.Call, done func([]byte, error)) {
	f := s.f
	s.record(false, c)
	k := f.doCalls[s.i]
	f.doCalls[s.i]++
	res := ok()
	if k < len(f.do[s.i]) {
		res = f.do[s.i][k]
	}
	if res.err == nil || res.acted {
		f.effect[s.i] = true
	}
	s.e.Clock.AfterFunc(f.latency, func() {
		if res.err != nil {
			done(nil, res.err)
			return
		}
		done(output(s.i), nil)
	})
}

func (s *fstep) Compensate(c saga.Call, done func(error)) {
	f := s.f
	s.record(true, c)
	k := f.compCalls[s.i]
	f.compCalls[s.i]++
	res := ok()
	if k < len(f.comp[s.i]) {
		res = f.comp[s.i][k]
	}
	if res.err == nil || res.acted {
		f.effect[s.i] = false
	}
	s.e.Clock.AfterFunc(f.latency, func() {
		if res.err == nil {
			f.compOK = append(f.compOK, s.i)
		}
		done(res.err)
	})
}

type host struct {
	e   env.Env
	eng *saga.Engine
	err error
}

func (h *host) Start() {}

func (h *host) OnDatagram(env.NodeID, []byte) {}

func (h *host) OnRequest(_ env.NodeID, _ []byte, reply env.Reply) {
	reply(nil, env.ErrInvalidArgument)
}

func (h *host) EncodeState() []byte {
	if h.eng == nil {
		return nil
	}
	return h.eng.EncodeState()
}

func hostFactory(f *fx, cfg saga.Config, extra ...saga.Definition) sim.Factory {
	return func(e env.Env) env.Process {
		h := &host{e: e}
		return &starter{h: h, start: func() {
			defs := append([]saga.Definition{f.def(e)}, extra...)
			h.eng, h.err = saga.New(e, cfg, defs...)
			if h.err != nil {
				return
			}
			h.err = h.eng.Recover()
		}}
	}
}

type starter struct {
	h     *host
	start func()
}

func (s *starter) Start()                                 { s.start() }
func (s *starter) OnDatagram(from env.NodeID, msg []byte) { s.h.OnDatagram(from, msg) }
func (s *starter) OnRequest(from env.NodeID, req []byte, reply env.Reply) {
	s.h.OnRequest(from, req, reply)
}
func (s *starter) EncodeState() []byte { return s.h.EncodeState() }

func hostOf(s *sim.Sim) *host {
	p := s.Process(hostID)
	if p == nil {
		return nil
	}
	return p.(*starter).h
}

func testPolicy() saga.Policy {
	return saga.Policy{MaxAttempts: 4, BaseDelay: 20 * time.Millisecond, MaxDelay: 200 * time.Millisecond, StepTimeout: time.Second}
}

type single struct {
	s   *sim.Sim
	f   *fx
	cfg saga.Config
}

func newSingle(t testing.TB, seed int64, f *fx, cfg saga.Config, extra ...saga.Definition) *single {
	t.Helper()
	s := sim.New(sim.Config{Seed: seed})
	f.s = s
	if err := s.AddNode(sim.NodeSpec{ID: hostID, Region: "r"}, hostFactory(f, cfg, extra...)); err != nil {
		t.Fatal(err)
	}
	return &single{s: s, f: f, cfg: cfg}
}

func (u *single) begin(t testing.TB, at env.Time, id string, input []byte) {
	u.s.At(at, "begin "+id, func() {
		h := hostOf(u.s)
		if h == nil || h.eng == nil {
			u.s.Fail(fmt.Errorf("begin %s: host not running", id))
			return
		}
		if err := h.eng.Begin(saga.ID(id), testDef, input); err != nil && !errors.Is(err, env.ErrClosed) {
			u.s.Fail(fmt.Errorf("begin %s: %w", id, err))
		}
	})
}

func (u *single) status(t testing.TB, id string) saga.Status {
	t.Helper()
	h := hostOf(u.s)
	if h == nil || h.eng == nil {
		t.Fatalf("host is down")
	}
	st, err := h.eng.Status(saga.ID(id))
	if err != nil {
		t.Fatalf("status %s: %v", id, err)
	}
	return st
}

func (u *single) run(t testing.TB, d time.Duration) {
	t.Helper()
	if err := u.s.RunUntil(env.Time(d)); err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := u.s.Err(); err != nil {
		t.Fatalf("sim: %v", err)
	}
}

func (f *fx) doSeq() []int {
	var out []int
	for _, c := range f.calls {
		if !c.comp {
			out = append(out, c.key.Step)
		}
	}
	return out
}

func (f *fx) compSeq() []int {
	var out []int
	for _, c := range f.calls {
		if c.comp {
			out = append(out, c.key.Step)
		}
	}
	return out
}

type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

func durableRecords(t fataler, s *sim.Sim, id string) []saga.Record {
	t.Helper()
	b, okb := s.Disk(hostID).PeekDurable(saga.DefaultPrefix + id)
	if !okb {
		return nil
	}
	recs, _, err := saga.DecodeLog(b)
	if err != nil {
		t.Fatalf("decode durable log: %v", err)
	}
	return recs
}

func kinds(recs []saga.Record) string {
	var parts []string
	for _, r := range recs {
		parts = append(parts, fmt.Sprintf("%s(%d,%d)", r.Kind, r.Step, r.Attempt))
	}
	return strings.Join(parts, " ")
}

type memDisk struct {
	blobs   map[string][]byte
	durable map[string][]byte
	fail    map[string]error
	ops     []string
}

func newMemDisk() *memDisk {
	return &memDisk{blobs: map[string][]byte{}, durable: map[string][]byte{}, fail: map[string]error{}}
}

func (d *memDisk) put(name string, b []byte) {
	d.blobs[name] = slices.Clone(b)
	d.durable[name] = slices.Clone(b)
}

func (d *memDisk) crash() {
	d.blobs = map[string][]byte{}
	for k, v := range d.durable {
		d.blobs[k] = slices.Clone(v)
	}
}

func (d *memDisk) err(op, name string) error {
	if e, okf := d.fail[op]; okf {
		return e
	}
	if e, okf := d.fail[op+" "+name]; okf {
		return e
	}
	return nil
}

func (d *memDisk) Read(name string) ([]byte, error) {
	if err := d.err("read", name); err != nil {
		return nil, err
	}
	b, okb := d.blobs[name]
	if !okb {
		return nil, env.ErrNotFound
	}
	return slices.Clone(b), nil
}

func (d *memDisk) Write(name string, data []byte) error {
	d.ops = append(d.ops, "write "+name)
	if err := d.err("write", name); err != nil {
		return err
	}
	d.blobs[name] = slices.Clone(data)
	return nil
}

func (d *memDisk) Append(name string, data []byte) error {
	d.ops = append(d.ops, "append "+name)
	if err := d.err("append", name); err != nil {
		return err
	}
	d.blobs[name] = append(d.blobs[name], data...)
	return nil
}

func (d *memDisk) Sync(name string) error {
	d.ops = append(d.ops, "sync "+name)
	if err := d.err("sync", name); err != nil {
		return err
	}
	if b, okb := d.blobs[name]; okb {
		d.durable[name] = slices.Clone(b)
	} else {
		delete(d.durable, name)
	}
	return nil
}

func (d *memDisk) Delete(name string) error {
	d.ops = append(d.ops, "delete "+name)
	if err := d.err("delete", name); err != nil {
		return err
	}
	delete(d.blobs, name)
	return nil
}

func (d *memDisk) List(prefix string) ([]string, error) {
	if err := d.err("list", prefix); err != nil {
		return nil, err
	}
	var out []string
	for name := range d.blobs {
		out = append(out, name)
	}
	slices.Sort(out)
	var keep []string
	for _, n := range out {
		if strings.HasPrefix(n, prefix) {
			keep = append(keep, n)
		}
	}
	return keep, nil
}

type manualClock struct {
	now    env.Time
	timers []*manualTimer
}

type manualTimer struct {
	c       *manualClock
	at      env.Time
	fn      func()
	pending bool
}

func (c *manualClock) Now() env.Time { return c.now }

func (c *manualClock) AfterFunc(d time.Duration, fn func()) env.Timer {
	t := &manualTimer{c: c, at: c.now.Add(d), fn: fn, pending: true}
	c.timers = append(c.timers, t)
	return t
}

func (t *manualTimer) Stop() bool {
	was := t.pending
	t.pending = false
	return was
}

func (t *manualTimer) Reset(d time.Duration) bool {
	was := t.pending
	t.at, t.pending = t.c.now.Add(d), true
	return was
}

func (c *manualClock) pending() int {
	n := 0
	for _, t := range c.timers {
		if t.pending {
			n++
		}
	}
	return n
}

func (c *manualClock) fireNext() bool {
	var next *manualTimer
	for _, t := range c.timers {
		if t.pending && (next == nil || t.at < next.at) {
			next = t
		}
	}
	if next == nil {
		return false
	}
	next.pending = false
	if next.at > c.now {
		c.now = next.at
	}
	next.fn()
	return true
}

func memEnv(d env.Disk, c env.Clock) env.Env {
	return env.Env{ID: hostID, Region: "r", Clock: c, Rand: sim.NewStream(1, "test"), Disk: d}
}

func encodeLog(t testing.TB, recs ...saga.Record) []byte {
	t.Helper()
	var b []byte
	for _, r := range recs {
		var err error
		b, err = saga.AppendRecord(b, r)
		if err != nil {
			t.Fatalf("encode %v: %v", r.Kind, err)
		}
	}
	return b
}

func begin() saga.Record { return saga.Record{Kind: saga.RecBegin, Step: 0, Def: testDef, Time: 5} }
func intent(s, a int) saga.Record {
	return saga.Record{Kind: saga.RecIntent, Step: s, Attempt: a}
}
func done(s, a int) saga.Record {
	return saga.Record{Kind: saga.RecDone, Step: s, Attempt: a, Data: output(s)}
}
func abort(from int) saga.Record {
	return saga.Record{Kind: saga.RecAbort, Step: from, Data: []byte("cause")}
}
func cintent(s, a int) saga.Record {
	return saga.Record{Kind: saga.RecCompIntent, Step: s, Attempt: a}
}
func cdone(s, a int) saga.Record {
	return saga.Record{Kind: saga.RecCompDone, Step: s, Attempt: a}
}
func end(st saga.State) saga.Record { return saga.Record{Kind: saga.RecEnd, State: st} }
