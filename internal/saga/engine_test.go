package saga_test

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

func TestStateMachineTable(t *testing.T) {
	const steps, pivot, maxA = 4, 2, 3
	run := saga.StateRunning
	comp := saga.StateCompensating
	type want struct {
		state   saga.State
		step    int
		attempt int
		next    saga.Action
		end     saga.State
	}
	tests := []struct {
		name string
		recs []saga.Record
		want want
		err  error
	}{
		{"a fresh saga runs its first step", []saga.Record{begin()}, want{run, 0, 0, saga.ActDo, 0}, nil},
		{"an intent without an outcome is run again as the next attempt", []saga.Record{begin(), intent(0, 1)}, want{run, 0, 1, saga.ActDo, 0}, nil},
		{"a step whose intents used every attempt before the pivot is aborted", []saga.Record{begin(), intent(0, 1), intent(0, 2), intent(0, 3)}, want{run, 0, 3, saga.ActAbort, 0}, nil},
		{"a done step moves on to the next step", []saga.Record{begin(), intent(0, 1), done(0, 1)}, want{run, 1, 0, saga.ActDo, 0}, nil},
		{"the pivot is never aborted for lack of attempts", []saga.Record{begin(), intent(0, 1), done(0, 1), intent(1, 1), done(1, 1), intent(2, 1), intent(2, 2), intent(2, 3), intent(2, 4)}, want{run, 2, 4, saga.ActDo, 0}, nil},
		{"a step after the pivot is never aborted for lack of attempts", []saga.Record{begin(), intent(0, 1), done(0, 1), intent(1, 1), done(1, 1), intent(2, 1), done(2, 1), intent(3, 1), intent(3, 2), intent(3, 3)}, want{run, 3, 3, saga.ActDo, 0}, nil},
		{"all steps done ends the saga completed", []saga.Record{begin(), intent(0, 1), done(0, 1), intent(1, 1), done(1, 1), intent(2, 1), done(2, 1), intent(3, 1), done(3, 1)}, want{run, 4, 0, saga.ActEnd, saga.StateCompleted}, nil},
		{"an abort starts compensation at the named step", []saga.Record{begin(), intent(0, 1), done(0, 1), intent(1, 1), abort(1)}, want{comp, 1, 0, saga.ActCompensate, 0}, nil},
		{"a compensation intent without an outcome is run again", []saga.Record{begin(), intent(0, 1), abort(0), cintent(0, 1)}, want{comp, 0, 1, saga.ActCompensate, 0}, nil},
		{"compensations walk down the steps", []saga.Record{begin(), intent(0, 1), done(0, 1), intent(1, 1), abort(1), cintent(1, 1), cdone(1, 1)}, want{comp, 0, 0, saga.ActCompensate, 0}, nil},
		{"compensation past step zero ends the saga compensated", []saga.Record{begin(), intent(0, 1), abort(0), cintent(0, 1), cintent(0, 2), cdone(0, 2)}, want{comp, -1, 0, saga.ActEnd, saga.StateCompensated}, nil},
		{"a definitive pivot failure aborts from the step below the pivot", []saga.Record{begin(), intent(0, 1), done(0, 1), intent(1, 1), done(1, 1), intent(2, 1), abort(1)}, want{comp, 1, 0, saga.ActCompensate, 0}, nil},
		{"a saga whose first step failed ends compensated once that step is undone", []saga.Record{begin(), intent(0, 1), abort(0), cintent(0, 1), cdone(0, 1)}, want{comp, -1, 0, saga.ActEnd, saga.StateCompensated}, nil},
		{"a completed saga has nothing left to do", []saga.Record{begin(), intent(0, 1), done(0, 1), intent(1, 1), done(1, 1), intent(2, 1), done(2, 1), intent(3, 1), done(3, 1), end(saga.StateCompleted)}, want{saga.StateCompleted, 4, 0, saga.ActNone, 0}, nil},
		{"a compensated saga has nothing left to do", []saga.Record{begin(), intent(0, 1), abort(0), cintent(0, 1), cdone(0, 1), end(saga.StateCompensated)}, want{saga.StateCompensated, -1, 0, saga.ActNone, 0}, nil},
		{"an abort of a step that has no open intent is corrupt", []saga.Record{begin(), abort(0)}, want{}, saga.ErrLogCorrupt},
		{"an abort of the next step after a done is corrupt", []saga.Record{begin(), intent(0, 1), done(0, 1), abort(1)}, want{}, saga.ErrLogCorrupt},
		{"an abort of the pivot's predecessor with no open pivot intent is corrupt", []saga.Record{begin(), intent(0, 1), done(0, 1), intent(1, 1), done(1, 1), abort(1)}, want{}, saga.ErrLogCorrupt},
		{"no records is corrupt", nil, want{}, saga.ErrLogCorrupt},
		{"a log that does not start with begin is corrupt", []saga.Record{intent(0, 1)}, want{}, saga.ErrLogCorrupt},
		{"a second begin is corrupt", []saga.Record{begin(), begin()}, want{}, saga.ErrLogCorrupt},
		{"an intent for a step other than the current one is corrupt", []saga.Record{begin(), intent(1, 1)}, want{}, saga.ErrLogCorrupt},
		{"an attempt that skips a number is corrupt", []saga.Record{begin(), intent(0, 2)}, want{}, saga.ErrLogCorrupt},
		{"a done without an intent is corrupt", []saga.Record{begin(), done(0, 1)}, want{}, saga.ErrLogCorrupt},
		{"a done for another attempt is corrupt", []saga.Record{begin(), intent(0, 1), intent(0, 2), done(0, 1)}, want{}, saga.ErrLogCorrupt},
		{"a done for another step is corrupt", []saga.Record{begin(), intent(0, 1), done(1, 1)}, want{}, saga.ErrLogCorrupt},
		{"an intent beyond the last step is corrupt", []saga.Record{begin(), intent(0, 1), done(0, 1), intent(1, 1), done(1, 1), intent(2, 1), done(2, 1), intent(3, 1), done(3, 1), intent(4, 1)}, want{}, saga.ErrLogCorrupt},
		{"an abort from a step that is not the current one is corrupt", []saga.Record{begin(), intent(0, 1), done(0, 1), abort(0)}, want{}, saga.ErrLogCorrupt},
		{"an abort after the pivot is done is corrupt", []saga.Record{begin(), intent(0, 1), done(0, 1), intent(1, 1), done(1, 1), intent(2, 1), done(2, 1), abort(2)}, want{}, saga.ErrLogCorrupt},
		{"an intent after an abort is corrupt", []saga.Record{begin(), intent(0, 1), abort(0), intent(0, 2)}, want{}, saga.ErrLogCorrupt},
		{"a compensation record while running is corrupt", []saga.Record{begin(), cintent(0, 1)}, want{}, saga.ErrLogCorrupt},
		{"a compensation done without an intent is corrupt", []saga.Record{begin(), intent(0, 1), abort(0), cdone(0, 1)}, want{}, saga.ErrLogCorrupt},
		{"a compensation out of order is corrupt", []saga.Record{begin(), intent(0, 1), done(0, 1), intent(1, 1), abort(1), cintent(0, 1)}, want{}, saga.ErrLogCorrupt},
		{"a compensation attempt that skips a number is corrupt", []saga.Record{begin(), intent(0, 1), abort(0), cintent(0, 2)}, want{}, saga.ErrLogCorrupt},
		{"a completed end before the last step is corrupt", []saga.Record{begin(), end(saga.StateCompleted)}, want{}, saga.ErrLogCorrupt},
		{"a compensated end while running is corrupt", []saga.Record{begin(), end(saga.StateCompensated)}, want{}, saga.ErrLogCorrupt},
		{"a compensated end before compensation finished is corrupt", []saga.Record{begin(), intent(0, 1), abort(0), end(saga.StateCompensated)}, want{}, saga.ErrLogCorrupt},
		{"an end naming a state that is not terminal is corrupt", []saga.Record{begin(), intent(0, 1), abort(0), cintent(0, 1), cdone(0, 1), end(saga.StateRunning)}, want{}, saga.ErrLogCorrupt},
		{"any record after the end is corrupt", []saga.Record{begin(), intent(0, 1), abort(0), cintent(0, 1), cdone(0, 1), end(saga.StateCompensated), intent(0, 1)}, want{}, saga.ErrLogCorrupt},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			snap, err := saga.Resume(tc.recs, steps, pivot, maxA)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := want{snap.State, snap.Step, snap.Attempt, snap.Next, snap.End}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
	t.Run("outputs, begin time and input come back from the log", func(t *testing.T) {
		recs := []saga.Record{{Kind: saga.RecBegin, Def: "d", Time: 42, Data: []byte("in")}, intent(0, 1), done(0, 1), intent(1, 1), done(1, 1)}
		snap, err := saga.Resume(recs, steps, pivot, maxA)
		if err != nil {
			t.Fatal(err)
		}
		if snap.Def != "d" || snap.Begun != 42 || string(snap.Input) != "in" || len(snap.Outputs) != 2 || !bytes.Equal(snap.Outputs[1], output(1)) {
			t.Fatalf("snapshot %+v", snap)
		}
	})
	t.Run("the abort cause is kept", func(t *testing.T) {
		snap, err := saga.Resume([]saga.Record{begin(), intent(0, 1), abort(0)}, steps, pivot, maxA)
		if err != nil || snap.Cause != "cause" {
			t.Fatalf("cause %q err %v", snap.Cause, err)
		}
	})
	t.Run("a pivot at step zero aborts with nothing to compensate", func(t *testing.T) {
		snap, err := saga.Resume([]saga.Record{begin(), intent(0, 1), abort(-1)}, 2, 0, maxA)
		if err != nil || snap.Next != saga.ActEnd || snap.End != saga.StateCompensated || snap.Step != -1 {
			t.Fatalf("snapshot %+v err %v", snap, err)
		}
		if _, err := saga.Resume([]saga.Record{begin(), abort(-1)}, steps, pivot, maxA); !errors.Is(err, saga.ErrLogCorrupt) {
			t.Fatalf("abort from -1 below a later pivot: %v", err)
		}
	})
	t.Run("invalid shapes are refused", func(t *testing.T) {
		for _, c := range [][3]int{{0, 0, 1}, {2, 3, 1}, {2, -1, 1}, {2, 1, 0}} {
			if _, err := saga.Resume([]saga.Record{begin()}, c[0], c[1], c[2]); !errors.Is(err, env.ErrInvalidArgument) {
				t.Fatalf("shape %v: err %v", c, err)
			}
		}
	})
}

func TestRetryBackoffSchedule(t *testing.T) {
	p := saga.Policy{MaxAttempts: 10, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second, StepTimeout: time.Second}
	t.Run("delays double from the base and stop at the cap", func(t *testing.T) {
		want := []time.Duration{100, 200, 400, 800, 1000, 1000, 1000}
		for i, w := range want {
			if got := p.Backoff(i+1, nil); got != w*time.Millisecond {
				t.Fatalf("attempt %d: %v, want %v", i+1, got, w*time.Millisecond)
			}
		}
	})
	t.Run("huge and non-positive attempts stay within the cap", func(t *testing.T) {
		for _, a := range []int{64, 65, 1000, 1 << 40} {
			if got := p.Backoff(a, nil); got != time.Second {
				t.Fatalf("attempt %d: %v", a, got)
			}
		}
		if got := p.Backoff(0, nil); got != p.BaseDelay {
			t.Fatalf("attempt 0: %v", got)
		}
		big := saga.Policy{MaxAttempts: 1, BaseDelay: time.Duration(1) << 61, MaxDelay: time.Duration(1<<63 - 1), StepTimeout: 1}
		if got := big.Backoff(5, nil); got != big.MaxDelay {
			t.Fatalf("overflowing base: %v", got)
		}
	})
	t.Run("jitter stays within its parts-per-million share and is seeded", func(t *testing.T) {
		j := p
		j.JitterPPM = 500_000
		r1, r2 := sim.NewStream(9, "x"), sim.NewStream(9, "x")
		varied := false
		for a := 1; a <= 50; a++ {
			base := p.Backoff(a, nil)
			d1, d2 := j.Backoff(a, r1), j.Backoff(a, r2)
			if d1 != d2 {
				t.Fatalf("attempt %d: same seed gave %v and %v", a, d1, d2)
			}
			if d1 < base || d1 > base+base/2 {
				t.Fatalf("attempt %d: %v outside [%v, %v]", a, d1, base, base+base/2)
			}
			varied = varied || d1 != base
		}
		if !varied {
			t.Fatal("jitter never moved a delay")
		}
		huge := saga.Policy{MaxAttempts: 1, BaseDelay: time.Duration(1<<63 - 1), MaxDelay: time.Duration(1<<63 - 1), StepTimeout: 1, JitterPPM: 1_000_000}
		if got := huge.Backoff(1, r1); got < huge.MaxDelay/2 {
			t.Fatalf("saturated jitter: %v", got)
		}
	})
	t.Run("invalid policies are refused", func(t *testing.T) {
		bad := []saga.Policy{
			{MaxAttempts: 0, BaseDelay: 1, MaxDelay: 1, StepTimeout: 1},
			{MaxAttempts: 1, BaseDelay: 0, MaxDelay: 1, StepTimeout: 1},
			{MaxAttempts: 1, BaseDelay: 2, MaxDelay: 1, StepTimeout: 1},
			{MaxAttempts: 1, BaseDelay: 1, MaxDelay: 1, StepTimeout: 0},
			{MaxAttempts: 1, BaseDelay: 1, MaxDelay: 1, StepTimeout: 1, JitterPPM: -1},
			{MaxAttempts: 1, BaseDelay: 1, MaxDelay: 1, StepTimeout: 1, JitterPPM: 1_000_001},
		}
		for i, b := range bad {
			if err := b.Validate(); !errors.Is(err, env.ErrInvalidArgument) {
				t.Fatalf("policy %d: %v", i, err)
			}
		}
		if err := saga.DefaultPolicy().Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("the engine waits exactly the backoff between failed attempts", func(t *testing.T) {
		f := newFx(1, 1)
		f.do[0] = []result{retry(false), retry(false), retry(false)}
		pol := saga.Policy{MaxAttempts: 4, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second, StepTimeout: time.Second}
		u := newSingle(t, 1, f, saga.Config{Policy: pol})
		u.begin(t, beginAt, "b", nil)
		u.run(t, 10*time.Second)
		if st := u.status(t, "b"); st.State != saga.StateCompleted {
			t.Fatalf("state %v", st.State)
		}
		var at []env.Time
		for _, c := range f.calls {
			at = append(at, c.at)
		}
		if len(at) != 4 {
			t.Fatalf("%d attempts", len(at))
		}
		for i := 1; i < len(at); i++ {
			gap := at[i].Sub(at[i-1])
			if want := stepLatency + pol.Backoff(i, nil); gap != want {
				t.Fatalf("gap before attempt %d: %v, want %v", i+1, gap, want)
			}
		}
	})
	t.Run("a step that never answers times out and is retried", func(t *testing.T) {
		f := newFx(1, 1)
		f.latency = 5 * time.Second
		pol := saga.Policy{MaxAttempts: 2, BaseDelay: 10 * time.Millisecond, MaxDelay: 10 * time.Millisecond, StepTimeout: time.Second}
		u := newSingle(t, 1, f, saga.Config{Policy: pol})
		u.begin(t, beginAt, "slow", nil)
		u.s.At(env.Time(2500*time.Millisecond), "speed up", func() { f.latency = stepLatency })
		u.run(t, 30*time.Second)
		st := u.status(t, "slow")
		if st.State != saga.StateCompensated {
			t.Fatalf("state %v", st.State)
		}
		if f.doCalls[0] != 2 || f.calls[1].at.Sub(f.calls[0].at) != time.Second+10*time.Millisecond {
			t.Fatalf("calls %+v", f.calls)
		}
		var se *saga.StepError
		if !errors.As(st.Err, &se) || !errors.Is(se, env.ErrTimeout) {
			t.Fatalf("status err %v", st.Err)
		}
	})
}

func TestCompensationOrder(t *testing.T) {
	tests := []struct {
		name     string
		n, pivot int
		setup    func(f *fx)
		wantComp []int
	}{
		{"a failure at the last compensable step undoes every step in reverse", 4, 4, func(f *fx) { f.do[3] = []result{permanent()} }, []int{3, 2, 1, 0}},
		{"a failure in the middle undoes only the steps that ran", 4, 4, func(f *fx) { f.do[1] = []result{permanent()} }, []int{1, 0}},
		{"a failure at the first step undoes only that step", 3, 3, func(f *fx) { f.do[0] = []result{permanent()} }, []int{0}},
		{"exhausted retries undo the step that may have acted", 3, 3, func(f *fx) {
			f.do[2] = []result{retry(true), retry(true), retry(true), retry(true)}
		}, []int{2, 1, 0}},
		{"a compensation that fails is retried before the next one starts", 3, 3, func(f *fx) {
			f.do[2] = []result{permanent()}
			f.comp[1] = []result{compFail(), compFail(), compFail(), compFail(), compFail(), compFail()}
		}, []int{2, 1, 1, 1, 1, 1, 1, 1, 0}},
		{"a definitive pivot failure undoes the steps below the pivot only", 4, 2, func(f *fx) { f.do[2] = []result{permanent()} }, []int{1, 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(tc.n, tc.pivot)
			tc.setup(f)
			u := newSingle(t, 1, f, saga.Config{Policy: testPolicy()})
			u.begin(t, beginAt, "c", nil)
			u.run(t, time.Minute)
			st := u.status(t, "c")
			if st.State != saga.StateCompensated {
				t.Fatalf("state %v", st.State)
			}
			if got := f.compSeq(); !slices.Equal(got, tc.wantComp) {
				t.Fatalf("compensations %v, want %v", got, tc.wantComp)
			}
			for i, on := range f.effect {
				if on {
					t.Fatalf("step %d effect remains", i)
				}
			}
			first := slices.IndexFunc(f.calls, func(c fcall) bool { return c.comp })
			for _, c := range f.calls[first:] {
				if !c.comp {
					t.Fatalf("a forward step ran after compensation began: %+v", c)
				}
			}
		})
	}
}

func TestIdempotencyKey(t *testing.T) {
	t.Run("keys differ across sagas and steps and encode unambiguously", func(t *testing.T) {
		keys := []saga.Key{{Saga: "a", Step: 1}, {Saga: "a", Step: 2}, {Saga: "b", Step: 1}, {Saga: "a1", Step: 1}, {Saga: "a", Step: 11}, {Saga: "", Step: 0}}
		seenS := map[string]bool{}
		seenB := map[string]bool{}
		for _, k := range keys {
			s, b := k.String(), string(k.Bytes())
			if seenS[s] || seenB[b] {
				t.Fatalf("key %+v collides (%q)", k, s)
			}
			seenS[s], seenB[b] = true, true
		}
		if (saga.Key{Saga: "m", Step: 3}).String() != "m#3" {
			t.Fatalf("String %q", (saga.Key{Saga: "m", Step: 3}).String())
		}
	})
	t.Run("the key is the same for every attempt and across a crash", func(t *testing.T) {
		f := newFx(2, 2)
		f.do[1] = []result{retry(true), retry(true)}
		u := newSingle(t, 1, f, saga.Config{Policy: testPolicy()})
		u.begin(t, beginAt, "k", []byte("in"))
		u.s.At(beginAt.Add(stepLatency+stepLatency/2), "crash", func() { _ = u.s.Crash(hostID) })
		u.s.At(env.Time(time.Second), "restart", func() { _ = u.s.Restart(hostID) })
		u.run(t, 10*time.Second)
		if st := u.status(t, "k"); st.State != saga.StateCompleted {
			t.Fatalf("state %v", st.State)
		}
		var step1 []fcall
		for _, c := range f.calls {
			if c.key.Step == 1 {
				step1 = append(step1, c)
			}
			if c.key.Saga != "k" {
				t.Fatalf("key names saga %q", c.key.Saga)
			}
		}
		if len(step1) < 3 {
			t.Fatalf("step 1 ran %d times, want a crash re-run and a retry", len(step1))
		}
		incs := map[uint32]bool{}
		for i, c := range step1 {
			if c.key != step1[0].key || c.attempt != i+1 {
				t.Fatalf("call %d: key %v attempt %d", i, c.key, c.attempt)
			}
			incs[c.inc] = true
		}
		if len(incs) < 2 {
			t.Fatal("the crash did not split the attempts across incarnations")
		}
	})
	t.Run("a step sees the begin time, the input and every earlier output", func(t *testing.T) {
		var seen []saga.Call
		probe := &probeStep{onDo: func(c saga.Call) { seen = append(seen, c) }}
		clk := &manualClock{now: 123}
		d := newMemDisk()
		e := memEnv(d, clk)
		f := newFx(1, 1)
		def := f.def(e)
		def.Name = "p"
		def.Steps = append(def.Steps, probe)
		def.Pivot = 2
		eng, err := saga.New(e, saga.Config{Policy: testPolicy()}, def)
		if err != nil {
			t.Fatal(err)
		}
		if err := eng.Begin("p1", "p", []byte("input")); err != nil {
			t.Fatal(err)
		}
		for clk.fireNext() {
		}
		if len(seen) != 1 || seen[0].Begun != 123 || string(seen[0].Input) != "input" || len(seen[0].Outputs) != 1 || !bytes.Equal(seen[0].Outputs[0], output(0)) || seen[0].Key != (saga.Key{Saga: "p1", Step: 1}) {
			t.Fatalf("calls %+v", seen)
		}
	})
}

type probeStep struct {
	onDo func(saga.Call)
}

func (p *probeStep) Name() string { return "probe" }
func (p *probeStep) Do(c saga.Call, done func([]byte, error)) {
	p.onDo(c)
	done([]byte("probe"), nil)
}
func (p *probeStep) Compensate(c saga.Call, done func(error)) { done(nil) }

func TestPivotSemantics(t *testing.T) {
	tests := []struct {
		name      string
		n, pivot  int
		setup     func(f *fx)
		crashAt   time.Duration
		want      saga.State
		wantComp  []int
		minPivots int
	}{
		{"a pivot that keeps failing retryably is retried past the attempt bound and completes", 3, 1, func(f *fx) {
			f.do[1] = []result{retry(false), retry(false), retry(false), retry(false), retry(false), retry(false), retry(false)}
		}, 0, saga.StateCompleted, nil, 8},
		{"a definitive pivot failure compensates only the steps before it", 3, 1, func(f *fx) { f.do[1] = []result{permanent()} }, 0, saga.StateCompensated, []int{0}, 1},
		{"a step after the pivot that fails permanently is retried and completes", 3, 1, func(f *fx) {
			f.do[2] = []result{permanent(), retry(true), permanent(), retry(false), retry(false)}
		}, 0, saga.StateCompleted, nil, 1},
		{"a crash after the pivot is done recovers forward", 3, 1, func(f *fx) {}, 2*stepLatency + stepLatency/2, saga.StateCompleted, nil, 1},
		{"a pivot at step zero has nothing to compensate", 2, 0, func(f *fx) { f.do[0] = []result{permanent()} }, 0, saga.StateCompensated, nil, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFx(tc.n, tc.pivot)
			tc.setup(f)
			u := newSingle(t, 1, f, saga.Config{Policy: testPolicy()})
			u.begin(t, beginAt, "p", nil)
			if tc.crashAt > 0 {
				u.s.At(beginAt.Add(tc.crashAt), "crash", func() { _ = u.s.Crash(hostID) })
				u.s.At(env.Time(time.Second), "restart", func() { _ = u.s.Restart(hostID) })
			}
			u.run(t, time.Minute)
			st := u.status(t, "p")
			if st.State != tc.want {
				t.Fatalf("state %v, want %v", st.State, tc.want)
			}
			if got := f.compSeq(); !slices.Equal(got, tc.wantComp) {
				t.Fatalf("compensations %v, want %v", got, tc.wantComp)
			}
			if f.doCalls[tc.pivot] < tc.minPivots {
				t.Fatalf("pivot ran %d times, want at least %d", f.doCalls[tc.pivot], tc.minPivots)
			}
			recs := durableRecords(t, u.s, "p")
			pivotDone := false
			for _, r := range recs {
				if r.Kind == saga.RecDone && r.Step == tc.pivot {
					pivotDone = true
				}
				if pivotDone && (r.Kind == saga.RecAbort || r.Kind == saga.RecCompIntent) {
					t.Fatalf("compensation after the pivot: %s", kinds(recs))
				}
			}
		})
	}
	t.Run("definitions with the pivot out of range are refused", func(t *testing.T) {
		clk := &manualClock{}
		e := memEnv(newMemDisk(), clk)
		f := newFx(2, 2)
		for _, p := range []int{-1, 3} {
			d := f.def(e)
			d.Pivot = p
			if _, err := saga.New(e, saga.Config{Policy: testPolicy()}, d); !errors.Is(err, env.ErrInvalidArgument) {
				t.Fatalf("pivot %d: %v", p, err)
			}
		}
	})
}

func TestStepErrorFields(t *testing.T) {
	f := newFx(2, 2)
	f.do[1] = []result{retry(false), permanent()}
	var ended []saga.Status
	u := newSingle(t, 1, f, saga.Config{Policy: testPolicy(), OnEnd: func(s saga.Status) { ended = append(ended, s) }})
	u.begin(t, beginAt, "e", nil)
	u.run(t, time.Minute)
	st := u.status(t, "e")
	var se *saga.StepError
	if !errors.As(st.Err, &se) {
		t.Fatalf("status err %v is not a *StepError", st.Err)
	}
	if se.Saga != "e" || se.Step != stepName(1) || se.Attempt != 2 || se.Compensation {
		t.Fatalf("fields %+v", se)
	}
	if !errors.Is(st.Err, saga.ErrPermanent) || errors.Is(st.Err, errFlaky) {
		t.Fatalf("unwrap chain wrong: %v", st.Err)
	}
	if want := "saga e: step s1 attempt 2: refused: saga: permanent step failure"; se.Error() != want {
		t.Fatalf("message %q, want %q", se.Error(), want)
	}
	if st.Cause == "" || len(ended) != 1 || ended[0].State != saga.StateCompensated {
		t.Fatalf("cause %q ended %+v", st.Cause, ended)
	}
	ce := &saga.StepError{Saga: "x", Step: "y", Attempt: 3, Compensation: true, Err: errFlaky}
	if ce.Error() != "saga x: compensate y attempt 3: flaky step" || !errors.Is(ce, errFlaky) {
		t.Fatalf("compensation error %q", ce.Error())
	}
}

func TestPermanentStopsRetry(t *testing.T) {
	f := newFx(3, 3)
	f.do[1] = []result{permanent(), retry(false), retry(false)}
	u := newSingle(t, 1, f, saga.Config{Policy: testPolicy()})
	u.begin(t, beginAt, "x", nil)
	u.run(t, time.Minute)
	if st := u.status(t, "x"); st.State != saga.StateCompensated {
		t.Fatalf("state %v", st.State)
	}
	if f.doCalls[1] != 1 || f.doCalls[2] != 0 {
		t.Fatalf("do calls %v: a permanent failure was retried or a later step ran", f.doCalls)
	}
	f2 := newFx(3, 3)
	f2.do[1] = []result{retry(false), retry(false), retry(false), retry(false), retry(false)}
	u2 := newSingle(t, 1, f2, saga.Config{Policy: testPolicy()})
	u2.begin(t, beginAt, "y", nil)
	u2.run(t, time.Minute)
	if f2.doCalls[1] != testPolicy().MaxAttempts {
		t.Fatalf("retryable failures made %d attempts, want %d", f2.doCalls[1], testPolicy().MaxAttempts)
	}
}

func TestSagaNotFound(t *testing.T) {
	clk := &manualClock{}
	e := memEnv(newMemDisk(), clk)
	eng, err := saga.New(e, saga.Config{Policy: testPolicy()}, newFx(1, 1).def(e))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Status("nope"); !errors.Is(err, saga.ErrSagaNotFound) {
		t.Fatalf("err %v", err)
	}
}

func TestCodesRoundTrip(t *testing.T) {
	table, err := env.NewCodeTable(saga.Codes()...)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []error{saga.ErrPermanent, saga.ErrSagaNotFound, saga.ErrLogCorrupt} {
		wrapped := &saga.StepError{Saga: "a", Step: "b", Attempt: 1, Err: fmt.Errorf("x: %w", s)}
		code, msg := table.Encode(wrapped)
		if code < env.CodeAppMin {
			t.Fatalf("%v encoded as %d", s, code)
		}
		back := table.Decode(code, msg)
		if !errors.Is(back, s) || !errors.Is(back, env.ErrRemote) {
			t.Fatalf("%v came back as %v", s, back)
		}
	}
}

func TestEngineEdges(t *testing.T) {
	newEng := func(t *testing.T, d *memDisk, cfg saga.Config, defs ...saga.Definition) (*saga.Engine, *manualClock) {
		t.Helper()
		clk := &manualClock{}
		e := memEnv(d, clk)
		if len(defs) == 0 {
			defs = []saga.Definition{newFx(2, 2).def(e)}
		}
		if cfg.Policy == (saga.Policy{}) {
			cfg.Policy = testPolicy()
		}
		eng, err := saga.New(e, cfg, defs...)
		if err != nil {
			t.Fatal(err)
		}
		return eng, clk
	}
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{"New refuses a bad policy, a nil disk or clock, and bad or duplicate definitions", func(t *testing.T) {
			clk := &manualClock{}
			e := memEnv(newMemDisk(), clk)
			def := newFx(1, 1).def(e)
			cases := []struct {
				e    env.Env
				cfg  saga.Config
				defs []saga.Definition
			}{
				{e, saga.Config{}, []saga.Definition{def}},
				{env.Env{Clock: clk}, saga.Config{Policy: testPolicy()}, []saga.Definition{def}},
				{env.Env{Disk: newMemDisk()}, saga.Config{Policy: testPolicy()}, []saga.Definition{def}},
				{e, saga.Config{Policy: testPolicy()}, []saga.Definition{def, def}},
				{e, saga.Config{Policy: testPolicy()}, []saga.Definition{{Name: "", Steps: def.Steps, Pivot: 1}}},
				{e, saga.Config{Policy: testPolicy()}, []saga.Definition{{Name: "z", Pivot: 0}}},
				{e, saga.Config{Policy: testPolicy()}, []saga.Definition{{Name: "z", Steps: []saga.Step{nil}, Pivot: 1}}},
			}
			for i, c := range cases {
				if _, err := saga.New(c.e, c.cfg, c.defs...); !errors.Is(err, env.ErrInvalidArgument) {
					t.Fatalf("case %d: %v", i, err)
				}
			}
		}},
		{"Begin refuses bad ids, unknown definitions and ids already in use", func(t *testing.T) {
			d := newMemDisk()
			eng, _ := newEng(t, d, saga.Config{})
			for _, id := range []saga.ID{"", "a/b", "a b", saga.ID(bytes.Repeat([]byte("x"), 129))} {
				if err := eng.Begin(id, testDef, nil); !errors.Is(err, env.ErrInvalidArgument) {
					t.Fatalf("id %q: %v", id, err)
				}
			}
			if err := eng.Begin("ok", "nodef", nil); !errors.Is(err, env.ErrInvalidArgument) {
				t.Fatalf("unknown def: %v", err)
			}
			if err := eng.Begin("ok", testDef, nil); err != nil {
				t.Fatal(err)
			}
			if err := eng.Begin("ok", testDef, nil); !errors.Is(err, env.ErrInvalidArgument) {
				t.Fatalf("duplicate: %v", err)
			}
			d.put(saga.DefaultPrefix+"ondisk", encodeLog(t, begin()))
			if err := eng.Begin("ondisk", testDef, nil); !errors.Is(err, env.ErrInvalidArgument) {
				t.Fatalf("id with a log on disk: %v", err)
			}
		}},
		{"a log write failure suspends the saga and reports it", func(t *testing.T) {
			d := newMemDisk()
			eng, clk := newEng(t, d, saga.Config{})
			if err := eng.Begin("w", testDef, nil); err != nil {
				t.Fatal(err)
			}
			d.fail["sync"] = env.ErrIO
			for clk.fireNext() {
			}
			st, err := eng.Status("w")
			if err != nil || !st.Suspended || !errors.Is(st.Err, env.ErrIO) || st.State.Terminal() {
				t.Fatalf("status %+v err %v", st, err)
			}
			if clk.pending() != 0 {
				t.Fatal("a suspended saga still has timers")
			}
		}},
		{"a failing begin write is returned to the caller", func(t *testing.T) {
			for _, op := range []string{"append", "sync"} {
				d := newMemDisk()
				eng, _ := newEng(t, d, saga.Config{})
				d.fail[op] = env.ErrIO
				if err := eng.Begin("w", testDef, nil); !errors.Is(err, env.ErrIO) {
					t.Fatalf("%s: %v", op, err)
				}
				if _, err := eng.Status("w"); !errors.Is(err, saga.ErrSagaNotFound) {
					t.Fatalf("%s: a failed begin left a saga: %v", op, err)
				}
			}
		}},
		{"an oversized input is refused before anything is written", func(t *testing.T) {
			d := newMemDisk()
			eng, _ := newEng(t, d, saga.Config{})
			if err := eng.Begin("big", testDef, make([]byte, saga.MaxRecord)); !errors.Is(err, env.ErrTooLarge) {
				t.Fatalf("err %v", err)
			}
			if len(d.blobs) != 0 {
				t.Fatal("an oversized begin wrote a log")
			}
		}},
		{"Recover reports list errors and is a no-op the second time", func(t *testing.T) {
			d := newMemDisk()
			d.fail["list"] = env.ErrIO
			eng, _ := newEng(t, d, saga.Config{})
			if err := eng.Recover(); !errors.Is(err, env.ErrIO) {
				t.Fatalf("err %v", err)
			}
			delete(d.fail, "list")
			d.put(saga.DefaultPrefix+"a", encodeLog(t, begin()))
			if err := eng.Recover(); err != nil {
				t.Fatal(err)
			}
			n := len(d.ops)
			if err := eng.Recover(); err != nil || len(d.ops) != n {
				t.Fatalf("second Recover err %v, ops %v", err, d.ops[n:])
			}
		}},
		{"Recover refuses a log that names an unknown definition", func(t *testing.T) {
			d := newMemDisk()
			d.put(saga.DefaultPrefix+"u", encodeLog(t, saga.Record{Kind: saga.RecBegin, Def: "other"}))
			eng, _ := newEng(t, d, saga.Config{})
			if err := eng.Recover(); !errors.Is(err, env.ErrInvalidArgument) {
				t.Fatalf("err %v", err)
			}
			if _, err := eng.Status("u"); !errors.Is(err, env.ErrInvalidArgument) {
				t.Fatalf("status err %v", err)
			}
		}},
		{"Recover refuses a log whose records break the state machine", func(t *testing.T) {
			d := newMemDisk()
			d.put(saga.DefaultPrefix+"v", encodeLog(t, begin(), done(0, 1)))
			eng, _ := newEng(t, d, saga.Config{})
			if err := eng.Recover(); !errors.Is(err, saga.ErrLogCorrupt) {
				t.Fatalf("err %v", err)
			}
		}},
		{"Recover surfaces truncate and delete failures and retries them", func(t *testing.T) {
			for _, op := range []string{"write", "delete"} {
				d := newMemDisk()
				full := encodeLog(t, begin())
				if op == "write" {
					d.put(saga.DefaultPrefix+"t", append(slices.Clone(full), full[:3]...))
				} else {
					d.put(saga.DefaultPrefix+"t", full[:3])
				}
				d.fail[op] = env.ErrIO
				eng, clk := newEng(t, d, saga.Config{})
				if err := eng.Recover(); !errors.Is(err, env.ErrIO) {
					t.Fatalf("%s: err %v", op, err)
				}
				delete(d.fail, op)
				for clk.fireNext() {
				}
				if op == "write" {
					if st, err := eng.Status("t"); err != nil || st.State != saga.StateCompleted {
						t.Fatalf("%s: status %+v err %v", op, st, err)
					}
				} else if _, present := d.blobs[saga.DefaultPrefix+"t"]; present {
					t.Fatalf("%s: blob kept", op)
				}
			}
		}},
		{"an empty log blob is removed", func(t *testing.T) {
			d := newMemDisk()
			d.put(saga.DefaultPrefix+"e", nil)
			eng, _ := newEng(t, d, saga.Config{})
			if err := eng.Recover(); err != nil {
				t.Fatal(err)
			}
			if _, present := d.blobs[saga.DefaultPrefix+"e"]; present {
				t.Fatal("empty blob kept")
			}
		}},
		{"a custom prefix keeps logs apart", func(t *testing.T) {
			d := newMemDisk()
			eng, clk := newEng(t, d, saga.Config{Prefix: "mig/"})
			if err := eng.Begin("a", testDef, nil); err != nil {
				t.Fatal(err)
			}
			for clk.fireNext() {
			}
			if _, present := d.blobs["mig/a"]; !present || eng.LogName("a") != "mig/a" {
				t.Fatalf("blobs %v", d.ops)
			}
			if ids := eng.IDs(); !slices.Equal(ids, []saga.ID{"a"}) {
				t.Fatalf("ids %v", ids)
			}
		}},
		{"a frozen engine does nothing more", func(t *testing.T) {
			d := newMemDisk()
			n := 0
			eng, clk := newEng(t, d, saga.Config{Hook: func(p saga.Point) bool {
				n++
				return n == 2
			}})
			if err := eng.Begin("f", testDef, nil); err != nil {
				t.Fatal(err)
			}
			for clk.fireNext() {
			}
			if !eng.Frozen() {
				t.Fatal("not frozen")
			}
			ops := len(d.ops)
			if err := eng.Begin("g", testDef, nil); !errors.Is(err, env.ErrClosed) {
				t.Fatalf("Begin on a frozen engine: %v", err)
			}
			if err := eng.Recover(); err != nil || len(d.ops) != ops || clk.pending() != 0 {
				t.Fatalf("frozen engine acted: err %v ops %v", err, d.ops[ops:])
			}
		}},
		{"a step that answers twice or late is heard once", func(t *testing.T) {
			d := newMemDisk()
			clk := &manualClock{}
			e := memEnv(d, clk)
			ds := &doubleStep{}
			eng, err := saga.New(e, saga.Config{Policy: testPolicy()}, saga.Definition{Name: "dd", Steps: []saga.Step{ds}, Pivot: 1})
			if err != nil {
				t.Fatal(err)
			}
			if err := eng.Begin("d", "dd", nil); err != nil {
				t.Fatal(err)
			}
			for clk.fireNext() {
			}
			recs, _, err := saga.DecodeLog(d.blobs[saga.DefaultPrefix+"d"])
			if err != nil || kinds(recs) != "begin(0,0) intent(0,1) done(0,1) end(0,0)" {
				t.Fatalf("log %s err %v", kinds(recs), err)
			}
		}},
		{"stats count attempts, failures and compensations per step", func(t *testing.T) {
			f := newFx(2, 2)
			f.do[1] = []result{retry(false), permanent()}
			u := newSingle(t, 1, f, saga.Config{Policy: testPolicy()})
			u.begin(t, beginAt, "s", nil)
			u.run(t, time.Minute)
			st := hostOf(u.s).eng.Stats()
			want := []saga.StepStats{
				{Def: testDef, Step: "s0", Attempts: 1, Failures: 0, Compensations: 1},
				{Def: testDef, Step: "s1", Attempts: 2, Failures: 2, Compensations: 1},
			}
			if !slices.Equal(st, want) {
				t.Fatalf("stats %+v", st)
			}
		}},
		{"state names print", func(t *testing.T) {
			for _, s := range []saga.State{0, saga.StateRunning, saga.StateCompensating, saga.StateCompleted, saga.StateCompensated, 99} {
				if s.String() == "" {
					t.Fatalf("state %d has no name", s)
				}
			}
			for _, k := range []saga.RecordKind{0, saga.RecBegin, saga.RecIntent, saga.RecDone, saga.RecAbort, saga.RecCompIntent, saga.RecCompDone, saga.RecEnd, 99} {
				if k.String() == "" {
					t.Fatalf("kind %d has no name", k)
				}
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, tc.run)
	}
}

type doubleStep struct{}

func (d *doubleStep) Name() string { return "double" }
func (d *doubleStep) Do(c saga.Call, done func([]byte, error)) {
	done(nil, nil)
	done(nil, errFlaky)
}
func (d *doubleStep) Compensate(c saga.Call, done func(error)) { done(nil) }
