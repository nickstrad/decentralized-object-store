package saga_test

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/saga"
	"github.com/nickstrad/object-store/internal/sim"
)

const (
	propMaxAttempts  = 10
	propHorizon      = 5 * time.Minute
	propSlow         = 1500 * time.Millisecond
	propExhaustSlack = 2
	propMaxPostPerm  = 3
)

func propPolicy() saga.Policy {
	return saga.Policy{MaxAttempts: propMaxAttempts, BaseDelay: 10 * time.Millisecond, MaxDelay: 100 * time.Millisecond, StepTimeout: time.Second, JitterPPM: 200_000}
}

type crashPlan struct {
	appendAt int
	torn     bool
	restart  time.Duration
}

type scenario struct {
	n, pivot  int
	perm      []bool
	fails     []int
	acted     []bool
	compFails []int
	hooks     []crashPlan
	timed     []crashPlan
	postPerm  []int
}

func drawScenario(t *rapid.T) scenario {
	n := rapid.IntRange(1, 5).Draw(t, "steps")
	sc := scenario{
		n:         n,
		pivot:     rapid.IntRange(0, n).Draw(t, "pivot"),
		perm:      make([]bool, n),
		fails:     make([]int, n),
		acted:     make([]bool, n),
		compFails: make([]int, n),
		postPerm:  make([]int, n),
	}
	for i := 0; i < n; i++ {
		sc.perm[i] = i <= sc.pivot && rapid.IntRange(0, 4).Draw(t, fmt.Sprintf("perm%d", i)) == 0
		sc.fails[i] = rapid.IntRange(0, 2).Draw(t, fmt.Sprintf("fails%d", i))
		if rapid.IntRange(0, 5).Draw(t, fmt.Sprintf("exhaust%d", i)) == 0 {
			sc.fails[i] = propMaxAttempts + propExhaustSlack
		}
		if i > sc.pivot {
			sc.postPerm[i] = rapid.IntRange(0, propMaxPostPerm).Draw(t, fmt.Sprintf("postPerm%d", i))
		}
		sc.acted[i] = rapid.Bool().Draw(t, fmt.Sprintf("acted%d", i))
		sc.compFails[i] = rapid.IntRange(0, 2).Draw(t, fmt.Sprintf("compFails%d", i))
	}
	plan := func(label string, lo, hi int) []crashPlan {
		k := rapid.IntRange(0, 2).Draw(t, label)
		var out []crashPlan
		for j := 0; j < k; j++ {
			out = append(out, crashPlan{
				appendAt: rapid.IntRange(lo, hi).Draw(t, fmt.Sprintf("%s%d.at", label, j)),
				torn:     rapid.Bool().Draw(t, fmt.Sprintf("%s%d.torn", label, j)),
				restart:  time.Duration(rapid.IntRange(1, 2000).Draw(t, fmt.Sprintf("%s%d.restart", label, j))) * time.Millisecond,
			})
		}
		return out
	}
	sc.hooks = plan("hook", 1, 30)
	sc.timed = plan("timed", 1, 600)
	return sc
}

func (sc scenario) apply(f *fx) {
	for i := 0; i < sc.n; i++ {
		var rs []result
		for j := 0; j < sc.postPerm[i]; j++ {
			rs = append(rs, permanent())
		}
		for j := 0; j < sc.fails[i]; j++ {
			rs = append(rs, retry(sc.acted[i]))
		}
		f.do[i] = rs
		var cs []result
		for j := 0; j < sc.compFails[i]; j++ {
			cs = append(cs, compFail())
		}
		f.comp[i] = cs
	}
}

func (sc scenario) exhausts(i int) bool {
	return i < sc.pivot && !sc.perm[i] && sc.fails[i] >= propMaxAttempts
}

func (sc scenario) expect() (saga.State, []int, int) {
	for i := 0; i < sc.n; i++ {
		if !(sc.perm[i] && i <= sc.pivot) && !sc.exhausts(i) {
			continue
		}
		from := i
		if sc.perm[i] && i == sc.pivot {
			from = i - 1
		}
		var comps []int
		for j := from; j >= 0; j-- {
			comps = append(comps, j)
		}
		return saga.StateCompensated, comps, i
	}
	return saga.StateCompleted, nil, sc.n
}

type permFx struct {
	*fx
	perm []bool
}

func runScenario(seed int64, sc scenario, after func(s *sim.Sim, f *fx)) (*sim.Sim, *fx, error) {
	f := newFx(sc.n, sc.pivot)
	sc.apply(f)
	pf := &permFx{fx: f, perm: sc.perm}
	s := sim.New(sim.Config{Seed: seed})
	f.s = s
	appends := 0
	crashAt := func(c crashPlan) {
		now := s.Now()
		s.At(now, "crash", func() {
			if c.torn {
				_ = s.CrashTorn(hostID)
			} else {
				_ = s.Crash(hostID)
			}
		})
		s.At(now.Add(c.restart), "restart", func() { _ = s.Restart(hostID) })
	}
	cfg := saga.Config{Policy: propPolicy(), Hook: func(p saga.Point) bool {
		appends++
		for _, c := range sc.hooks {
			if c.appendAt == appends {
				crashAt(c)
				return true
			}
		}
		return false
	}}
	f.checkEnded = func() bool {
		b, okb := s.Disk(hostID).PeekDurable(saga.DefaultPrefix + "t")
		if !okb {
			return false
		}
		recs, _, err := saga.DecodeLog(b)
		return err == nil && len(recs) > 0 && recs[len(recs)-1].Kind == saga.RecEnd
	}
	factory := func(e env.Env) env.Process {
		h := &host{e: e}
		return &starter{h: h, start: func() {
			h.eng, h.err = saga.New(e, cfg, pf.def(e))
			if h.err == nil {
				h.err = h.eng.Recover()
			}
		}}
	}
	if err := s.AddNode(sim.NodeSpec{ID: hostID, Region: "r"}, factory); err != nil {
		return s, f, err
	}
	s.At(beginAt, "begin", func() {
		if h := hostOf(s); h != nil && h.eng != nil {
			if err := h.eng.Begin("t", testDef, []byte("in")); err != nil && !errors.Is(err, env.ErrClosed) {
				s.Fail(err)
			}
		}
	})
	for _, c := range sc.timed {
		s.At(env.Time(time.Duration(c.appendAt)*time.Millisecond), "timed crash", func() {
			if s.Status(hostID).Up {
				crashAt(c)
			}
		})
	}
	if after != nil {
		after(s, f)
	}
	if err := s.RunUntil(env.Time(propHorizon)); err != nil {
		return s, f, err
	}
	return s, f, s.Err()
}

func (p *permFx) def(e env.Env) saga.Definition {
	d := p.fx.def(e)
	for i := range d.Steps {
		if p.perm[i] {
			d.Steps[i] = &permStep{fstep: d.Steps[i].(*fstep)}
		}
	}
	return d
}

type permStep struct {
	*fstep
}

func (s *permStep) Do(c saga.Call, done func([]byte, error)) {
	s.record(false, c)
	s.f.doCalls[s.i]++
	s.e.Clock.AfterFunc(s.f.latency, func() { done(nil, fmt.Errorf("no room: %w", saga.ErrPermanent)) })
}

func dedupe(xs []int) []int {
	var out []int
	for _, x := range xs {
		if len(out) == 0 || out[len(out)-1] != x {
			out = append(out, x)
		}
	}
	return out
}

func checkTerminal(t *rapid.T, s *sim.Sim, f *fx, sc scenario) {
	h := hostOf(s)
	if h == nil || h.eng == nil {
		t.Fatalf("host down at the end")
	}
	st, err := h.eng.Status("t")
	if errors.Is(err, saga.ErrSagaNotFound) {
		if len(f.calls) != 0 {
			t.Fatalf("the begin record was lost but %d steps ran", len(f.calls))
		}
		return
	}
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	want, comps, failing := sc.expect()
	if st.State != want {
		t.Fatalf("state %v, want %v (calls %+v)", st.State, want, f.calls)
	}
	switch want {
	case saga.StateCompleted:
		for i, on := range f.effect {
			if !on || f.doCalls[i] == 0 {
				t.Fatalf("completed but step %d effect %v calls %d", i, on, f.doCalls[i])
			}
		}
		if len(f.compSeq()) != 0 {
			t.Fatalf("completed saga ran compensations %v", f.compSeq())
		}
	case saga.StateCompensated:
		for i, on := range f.effect {
			if on {
				t.Fatalf("compensated but step %d effect remains", i)
			}
		}
		if got := dedupe(f.compOK); !slices.Equal(got, comps) {
			t.Fatalf("compensations %v, want %v", got, comps)
		}
		first := slices.IndexFunc(f.calls, func(c fcall) bool { return c.comp })
		if first >= 0 {
			for _, c := range f.calls[first:] {
				if !c.comp {
					t.Fatalf("forward step %d ran after compensation began", c.key.Step)
				}
			}
		}
		for i := failing + 1; i < sc.n; i++ {
			if f.doCalls[i] != 0 {
				t.Fatalf("step %d ran although step %d failed", i, failing)
			}
		}
	}
	if len(f.afterEnd) != 0 {
		t.Fatalf("calls after the end record was durable: %v", f.afterEnd)
	}
	recs := durableRecords(t, s, "t")
	snap, err := saga.Resume(recs, sc.n, sc.pivot, propMaxAttempts)
	if err != nil || snap.Next != saga.ActNone || snap.State != want {
		t.Fatalf("durable log %s: snapshot %+v err %v", kinds(recs), snap, err)
	}
}

// TestPropTerminalState proves that under random step outcomes (including
// pre-pivot retry exhaustion and post-pivot ErrPermanent) and up to four
// hook-placed or timed torn/untorn crashes, the engine reaches the same terminal
// state, side effects, and compensation order as the reference model.
func TestPropTerminalState(t *testing.T) {
	var exhausted, postPerm int
	rapid.Check(t, func(t *rapid.T) {
		sc := drawScenario(t)
		seed := rapid.Int64().Draw(t, "seed")
		s, f, err := runScenario(seed, sc, nil)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		checkTerminal(t, s, f, sc)
		for i := 0; i < sc.n; i++ {
			if sc.exhausts(i) && f.doCalls[i] > 0 {
				exhausted++
			}
			if sc.postPerm[i] > 0 && f.doCalls[i] > sc.postPerm[i] {
				postPerm++
			}
		}
	})
	if exhausted == 0 || postPerm == 0 {
		t.Fatalf("the draws never reached retry exhaustion (%d) or a post-pivot ErrPermanent (%d)", exhausted, postPerm)
	}
}

// TestPropNoStepAfterTerminal proves that no forward step or compensation runs
// once RecEnd is durable, including under slow callbacks and late torn crashes.
func TestPropNoStepAfterTerminal(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		sc := drawScenario(t)
		seed := rapid.Int64().Draw(t, "seed")
		extra := rapid.IntRange(0, 3).Draw(t, "extraCrashes")
		slowFirst := rapid.Bool().Draw(t, "slowFirst")
		s, f, err := runScenario(seed, sc, func(s *sim.Sim, f *fx) {
			if slowFirst {
				f.latency = propSlow
				s.At(env.Time(propSlow), "speed up", func() { f.latency = stepLatency })
			}
			for j := 0; j < extra; j++ {
				at := env.Time(time.Minute + time.Duration(j)*10*time.Second)
				s.At(at, "late crash", func() { _ = s.CrashTorn(hostID) })
				s.At(at.Add(time.Second), "late restart", func() { _ = s.Restart(hostID) })
			}
		})
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if len(f.afterEnd) != 0 {
			t.Fatalf("calls after the end record was durable: %v", f.afterEnd)
		}
		recs := durableRecords(t, s, "t")
		if len(recs) == 0 {
			return
		}
		snap, err := saga.Resume(recs, sc.n, sc.pivot, propMaxAttempts)
		if err != nil {
			t.Fatalf("log %s: %v", kinds(recs), err)
		}
		if !snap.State.Terminal() {
			t.Fatalf("saga not terminal: %+v", snap)
		}
	})
}

func genLog(t *rapid.T, steps, pivot, maxA int) []saga.Record {
	recs := []saga.Record{{Kind: saga.RecBegin, Def: testDef, Time: env.Time(rapid.Int64Range(0, 1<<40).Draw(t, "begun")), Data: rapid.SliceOfN(rapid.Byte(), 0, 8).Draw(t, "input")}}
	n := rapid.IntRange(0, 30).Draw(t, "len")
	for i := 0; i < n; i++ {
		snap, err := saga.Resume(recs, steps, pivot, maxA)
		if err != nil {
			t.Fatalf("generator built a bad log: %v", err)
		}
		switch snap.Next {
		case saga.ActNone:
			return recs
		case saga.ActEnd:
			recs = append(recs, saga.Record{Kind: saga.RecEnd, State: snap.End})
		case saga.ActAbort:
			from := snap.Step
			if from == pivot {
				from--
			}
			recs = append(recs, saga.Record{Kind: saga.RecAbort, Step: from, Data: []byte("exhausted")})
		case saga.ActCompensate:
			if snap.Attempt > 0 && rapid.Bool().Draw(t, fmt.Sprintf("cdone%d", i)) {
				recs = append(recs, saga.Record{Kind: saga.RecCompDone, Step: snap.Step, Attempt: snap.Attempt})
			} else {
				recs = append(recs, saga.Record{Kind: saga.RecCompIntent, Step: snap.Step, Attempt: snap.Attempt + 1})
			}
		case saga.ActDo:
			choice := rapid.IntRange(0, 3).Draw(t, fmt.Sprintf("do%d", i))
			switch {
			case snap.Attempt > 0 && choice <= 1:
				recs = append(recs, saga.Record{Kind: saga.RecDone, Step: snap.Step, Attempt: snap.Attempt, Data: output(snap.Step)})
			case snap.Attempt > 0 && choice == 2 && snap.Step <= pivot:
				from := snap.Step
				if from == pivot {
					from--
				}
				recs = append(recs, saga.Record{Kind: saga.RecAbort, Step: from, Data: []byte("permanent")})
			default:
				recs = append(recs, saga.Record{Kind: saga.RecIntent, Step: snap.Step, Attempt: snap.Attempt + 1})
			}
		}
	}
	return recs
}

type recovered struct {
	disk   map[string][]byte
	status saga.Status
	err    error
	first  saga.Point
	acted  bool
}

func recoverOnce(blobs map[string][]byte, steps, pivot int) recovered {
	d := newMemDisk()
	for k, v := range blobs {
		d.put(k, v)
	}
	clk := &manualClock{}
	e := memEnv(d, clk)
	var out recovered
	cfg := saga.Config{Policy: propPolicy(), Hook: func(p saga.Point) bool {
		out.first, out.acted = p, true
		return true
	}}
	f := newFx(steps, pivot)
	eng, err := saga.New(e, cfg, f.def(e))
	if err != nil {
		out.err = err
		return out
	}
	out.err = eng.Recover()
	out.status, _ = eng.Status("t")
	d.crash()
	out.disk = d.durable
	return out
}

func sameStatus(a, b saga.Status) bool {
	if a.ID != b.ID || a.Def != b.Def || a.State != b.State || a.Step != b.Step || a.Attempt != b.Attempt || a.Cause != b.Cause || len(a.Outputs) != len(b.Outputs) {
		return false
	}
	for i := range a.Outputs {
		if !bytes.Equal(a.Outputs[i], b.Outputs[i]) {
			return false
		}
	}
	return true
}

func sameDisk(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, okw := b[k]
		if !okw || !bytes.Equal(v, w) {
			return false
		}
	}
	return true
}

// TestPropRecoveryIdempotent proves that recovering twice from any byte prefix
// of any valid log produces the same durable disk, status, and first planned
// action as recovering once.
func TestPropRecoveryIdempotent(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		steps := rapid.IntRange(1, 4).Draw(t, "steps")
		pivot := rapid.IntRange(0, steps).Draw(t, "pivot")
		recs := genLog(t, steps, pivot, propMaxAttempts)
		var blob []byte
		for _, r := range recs {
			var err error
			blob, err = saga.AppendRecord(blob, r)
			if err != nil {
				t.Fatal(err)
			}
		}
		cut := rapid.IntRange(0, len(blob)).Draw(t, "cut")
		name := saga.DefaultPrefix + "t"
		once := recoverOnce(map[string][]byte{name: blob[:cut]}, steps, pivot)
		if once.err != nil {
			t.Fatalf("recover: %v", once.err)
		}
		twice := recoverOnce(once.disk, steps, pivot)
		if twice.err != nil {
			t.Fatalf("second recover: %v", twice.err)
		}
		if !sameDisk(once.disk, twice.disk) {
			t.Fatalf("recovering twice changed the disk")
		}
		if !sameStatus(once.status, twice.status) || once.first != twice.first || once.acted != twice.acted {
			t.Fatalf("recovering twice differs: %+v %+v / %+v %+v", once.status, once.first, twice.status, twice.first)
		}
		prefix, valid, err := saga.DecodeLog(blob[:cut])
		if err != nil {
			t.Fatal(err)
		}
		got, present := once.disk[name]
		if len(prefix) == 0 {
			if present {
				t.Fatalf("a log with no whole record was kept")
			}
			return
		}
		if !bytes.Equal(got, blob[:valid]) {
			t.Fatalf("recovered blob is not the valid prefix")
		}
		snap, err := saga.Resume(prefix, steps, pivot, propMaxAttempts)
		if err != nil {
			t.Fatal(err)
		}
		wantKind := map[saga.Action]saga.RecordKind{saga.ActDo: saga.RecIntent, saga.ActAbort: saga.RecAbort, saga.ActCompensate: saga.RecCompIntent, saga.ActEnd: saga.RecEnd}
		if snap.Next == saga.ActNone {
			if once.acted {
				t.Fatalf("a terminal saga acted: %+v", once.first)
			}
			return
		}
		if !once.acted || once.first.Kind != wantKind[snap.Next] {
			t.Fatalf("first action %+v, want %v from %+v", once.first, wantKind[snap.Next], snap)
		}
	})
}

// FuzzSagaLogRecover proves that arbitrary byte slices never panic DecodeLog,
// Resume, or Engine.Recover: every input either returns ErrLogCorrupt/
// ErrInvalidArgument without modifying disk, deletes an empty/torn-only log, or
// preserves the valid prefix and finishes cleanly.
func FuzzSagaLogRecover(f *testing.F) {
	recs, blob := sampleLog(f)
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add(blob)
	f.Add(blob[:len(blob)-1])
	f.Add(blob[:7])
	bad := slices.Clone(blob)
	bad[20] ^= 4
	f.Add(bad)
	f.Add(encodeLog(f, recs[:4]...))
	f.Add(encodeLog(f, saga.Record{Kind: saga.RecBegin, Def: "other"}))
	f.Add(encodeLog(f, begin(), done(0, 1)))
	f.Add(append(encodeLog(f, begin()), 0xA5, 0, 0, 0, 1))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 64<<10 {
			return
		}
		recs, valid, err := saga.DecodeLog(b)
		if valid < 0 || valid > len(b) {
			t.Fatalf("valid %d of %d", valid, len(b))
		}
		if err != nil && !errors.Is(err, saga.ErrLogCorrupt) {
			t.Fatalf("untyped error %v", err)
		}
		var re []byte
		for _, r := range recs {
			var eerr error
			re, eerr = saga.AppendRecord(re, r)
			if eerr != nil {
				t.Fatalf("decoded record does not encode: %v", eerr)
			}
		}
		if !bytes.Equal(re, b[:valid]) {
			t.Fatalf("records re-encode to %d bytes, valid prefix is %d", len(re), valid)
		}
		if err == nil {
			again, v2, err2 := saga.DecodeLog(b[:valid])
			if err2 != nil || v2 != valid || len(again) != len(recs) {
				t.Fatalf("valid prefix does not decode alone: %v", err2)
			}
		}
		if _, rerr := saga.Resume(recs, 3, 2, 3); rerr != nil && !errors.Is(rerr, saga.ErrLogCorrupt) {
			t.Fatalf("Resume: untyped error %v", rerr)
		}
		d := newMemDisk()
		name := saga.DefaultPrefix + "f"
		d.put(name, b)
		clk := &manualClock{}
		e := memEnv(d, clk)
		eng, nerr := saga.New(e, saga.Config{Policy: testPolicy()}, newFx(3, 2).def(e))
		if nerr != nil {
			t.Fatal(nerr)
		}
		rerr := eng.Recover()
		if rerr != nil && !errors.Is(rerr, saga.ErrLogCorrupt) && !errors.Is(rerr, env.ErrInvalidArgument) {
			t.Fatalf("Recover: untyped error %v", rerr)
		}
		for i := 0; i < 1000 && clk.fireNext(); i++ {
		}
		after, present := d.blobs[name]
		switch {
		case err != nil || rerr != nil:
			if !present || !bytes.Equal(after, b) {
				t.Fatalf("a refused log was changed")
			}
		case len(recs) == 0:
			if present {
				t.Fatalf("a log with no whole record was kept")
			}
		default:
			if !present || !bytes.HasPrefix(after, b[:valid]) {
				t.Fatalf("recovery did not keep the valid prefix")
			}
			final, _, ferr := saga.DecodeLog(after)
			if ferr != nil {
				t.Fatalf("recovered log is corrupt: %v", ferr)
			}
			snap, serr := saga.Resume(final, 3, 2, testPolicy().MaxAttempts)
			if serr != nil || !snap.State.Terminal() {
				t.Fatalf("recovered saga did not finish: %+v %v", snap, serr)
			}
		}
	})
}
