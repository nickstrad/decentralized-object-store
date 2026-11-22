package sagasim

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
)

const (
	warmup        = 25 * time.Second
	codeBlockMin  = env.CodeAppMin + 80
	codeBlockMax  = env.CodeAppMin + 95
	overCliff     = 33
	bigBucket     = 300
	modelSettleOK = 3 * time.Second
)

type warmHarness struct {
	inner Harness
	until env.Time
}

func (w warmHarness) Build(seed int64, o BuildOptions) (*sim.Sim, Cluster, error) {
	s, cl, err := w.inner.Build(seed, o)
	if err != nil {
		return s, cl, err
	}
	if err := s.RunUntil(w.until); err != nil {
		return s, cl, err
	}
	return s, cl, nil
}

// TestRunnerOnAWarmedUpCluster proves that Run times migrations relative to
// s.Now() across all 78 cells when Harness.Build warms the cluster up to 25 s.
func TestRunnerOnAWarmedUpCluster(t *testing.T) {
	h := warmHarness{inner: ModelHarness(), until: env.Time(warmup)}
	for _, c := range Cells() {
		t.Run(c.Name(), func(t *testing.T) {
			rep := &Report{}
			s, err := simtest.CheckDeterministic(1, Run(h, c, RunOptions{Report: rep}))
			if err != nil {
				t.Fatalf("%v", err)
			}
			if rep.State != c.Want() {
				t.Fatalf("state %v, want %v", rep.State, c.Want())
			}
			if rep.Start < env.Time(warmup) {
				t.Fatalf("the migration began at %v, inside the warm-up", rep.Start)
			}
			if rep.Elapsed <= 0 || rep.Elapsed > MatrixBound {
				t.Fatalf("elapsed %v", rep.Elapsed)
			}
			if got := s.Now().Sub(rep.Start.Add(rep.Elapsed)); got != MatrixSettle {
				t.Fatalf("settled for %v, want %v", got, MatrixSettle)
			}
		})
	}
}

// TestRunOptionsOverrideBoundSettleAndPolicy proves that non-zero Bound, Settle,
// and Policy in RunOptions override defaults and scale injected failure counts.
func TestRunOptionsOverrideBoundSettleAndPolicy(t *testing.T) {
	h := ModelHarness()
	cell := Cell{Step: 0, Kind: FailMidCompensation, Torn: true}
	_, err := simtest.CheckDeterministic(1, Run(h, cell, RunOptions{Bound: time.Millisecond}))
	if !errors.Is(err, sim.ErrDeadline) {
		t.Fatalf("a 1 ms bound did not stop the run: %v", err)
	}
	rep := &Report{}
	s, err := simtest.CheckDeterministic(1, Run(h, cell, RunOptions{Settle: modelSettleOK, Report: rep}))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Now().Sub(rep.Start.Add(rep.Elapsed)); got != modelSettleOK {
		t.Fatalf("settled for %v, want %v", got, modelSettleOK)
	}
	p := MatrixPolicy()
	p.MaxAttempts = 5
	for _, c := range []Cell{{Step: 1, Kind: FailAfterAction}, {Step: 1, Kind: FailMidCompensation}} {
		rep := &Report{}
		var seen saga.Policy
		o := RunOptions{Policy: p, Report: rep, Configure: func(cfg *saga.Config) { seen = cfg.Policy }}
		if _, err := simtest.CheckDeterministic(1, Run(h, c, o)); err != nil {
			t.Fatalf("%s: %v", c.Name(), err)
		}
		if seen != p {
			t.Fatalf("%s: the engine ran with %+v, want %+v", c.Name(), seen, p)
		}
		if c.Kind == FailAfterAction && rep.Calls[1] != p.MaxAttempts {
			t.Fatalf("%s: copy ran %d times, want the policy's %d", c.Name(), rep.Calls[1], p.MaxAttempts)
		}
		if c.Kind == FailMidCompensation && rep.CompFails != 2*p.MaxAttempts {
			t.Fatalf("%s: %d compensation failures, want %d", c.Name(), rep.CompFails, 2*p.MaxAttempts)
		}
	}
}

// TestRunnerFaultsTouchOnlyTheMatrixSaga proves that matrix hooks and wrapped
// steps pass foreign saga IDs through untouched without mutating cell state.
func TestRunnerFaultsTouchOnlyTheMatrixSaga(t *testing.T) {
	for _, c := range Cells() {
		st := &cellState{cell: c, calls: make([]int, MigrationSteps), lastComp: MigrationSteps, policy: MatrixPolicy()}
		for _, k := range []saga.RecordKind{saga.RecIntent, saga.RecDone, saga.RecCompDone, saga.RecAbort} {
			if st.hook(saga.Point{Saga: "other", Kind: k, Step: c.Step, Attempt: 1}) {
				t.Fatalf("%s: the hook froze the engine for another saga", c.Name())
			}
		}
		st.doubleArmed, st.failFired = true, true
		if st.hook(saga.Point{Saga: "other", Kind: saga.RecIntent, Step: c.Step, Attempt: 1}) {
			t.Fatalf("%s: an armed crash fired for another saga", c.Name())
		}
		def := st.wrap(saga.Definition{Name: "d", Steps: echoSteps(), Pivot: saga.MigrationPivot})
		for i, step := range def.Steps {
			var derr, cerr error
			calls := 0
			step.Do(saga.Call{Key: saga.Key{Saga: "other", Step: i}, Attempt: 1}, func(_ []byte, err error) { calls++; derr = err })
			step.Compensate(saga.Call{Key: saga.Key{Saga: "other", Step: i}, Attempt: 1}, func(err error) { calls++; cerr = err })
			if calls != 2 || derr != nil || cerr != nil {
				t.Fatalf("%s: step %d of another saga was injected: %v %v", c.Name(), i, derr, cerr)
			}
		}
		if st.crashes != 0 || st.lost != 0 || st.compFails != 0 || st.trig || st.calls[0] != 0 {
			t.Fatalf("%s: another saga moved the cell state: %+v", c.Name(), st)
		}
	}
}

type echoStep struct{}

func (echoStep) Name() string                                 { return "echo" }
func (echoStep) Do(c saga.Call, done func([]byte, error))     { done(nil, nil) }
func (echoStep) Compensate(c saga.Call, done func(err error)) { done(nil) }
func echoSteps() []saga.Step {
	out := make([]saga.Step, MigrationSteps)
	for i := range out {
		out[i] = echoStep{}
	}
	return out
}

type nilSimHarness struct{}

func (nilSimHarness) Build(int64, BuildOptions) (*sim.Sim, Cluster, error) {
	return nil, nil, env.ErrInvalidArgument
}

// TestRunnerSurvivesAHarnessWithoutASim proves that Run allocates a fallback
// Sim and records the error when Harness.Build returns a nil *sim.Sim.
func TestRunnerSurvivesAHarnessWithoutASim(t *testing.T) {
	s := Run(nilSimHarness{}, Cells()[0], RunOptions{})(7)
	if s == nil || !errors.Is(s.Err(), env.ErrInvalidArgument) {
		t.Fatalf("sim %v", s)
	}
}

type droppingHarness struct {
	dropWrap, dropHook bool
}

func (d droppingHarness) Build(seed int64, o BuildOptions) (*sim.Sim, Cluster, error) {
	if d.dropWrap {
		o.Wrap = nil
	}
	if d.dropHook {
		inner := o.Configure
		o.Configure = func(cfg *saga.Config) {
			inner(cfg)
			cfg.Hook = nil
		}
	}
	return ModelHarness().Build(seed, o)
}

// TestRunnerFailsACellWhoseFaultNeverFired proves that Run fails with ErrCheck
// whenever a harness omits Wrap or Hook and prevents a cell's fault from firing.
func TestRunnerFailsACellWhoseFaultNeverFired(t *testing.T) {
	for _, d := range []droppingHarness{{dropWrap: true}, {dropHook: true}} {
		for _, c := range Cells() {
			mustFail := (d.dropWrap && (!c.Kind.crash() || c.Kind.midComp())) || (d.dropHook && (c.Kind.crash() || c.Torn))
			_, err := simtest.CheckDeterministic(1, Run(d, c, RunOptions{}))
			switch {
			case mustFail && !errors.Is(err, ErrCheck):
				t.Fatalf("%+v %s: a cell whose fault was never injected passed: %v", d, c.Name(), err)
			case !mustFail && err != nil:
				t.Fatalf("%+v %s: %v", d, c.Name(), err)
			}
		}
	}
}

// TestRunnerFailsADoubleCellWithoutASecondCrash proves that every double-crash
// cell fails with ErrCheck if its second crash is suppressed.
func TestRunnerFailsADoubleCellWithoutASecondCrash(t *testing.T) {
	for _, c := range Cells() {
		if !c.Double {
			continue
		}
		h := hookFilter{keep: 1}
		_, err := simtest.CheckDeterministic(1, Run(h, c, RunOptions{}))
		if !errors.Is(err, ErrCheck) || !strings.Contains(err.Error(), "second crash") {
			t.Fatalf("%s: a double cell with one crash passed: %v", c.Name(), err)
		}
	}
}

type hookFilter struct{ keep int }

func (h hookFilter) Build(seed int64, o BuildOptions) (*sim.Sim, Cluster, error) {
	inner := o.Configure
	fired := 0
	o.Configure = func(cfg *saga.Config) {
		inner(cfg)
		hook := cfg.Hook
		cfg.Hook = func(p saga.Point) bool {
			if fired >= h.keep {
				return false
			}
			if hook(p) {
				fired++
				return true
			}
			return false
		}
	}
	return ModelHarness().Build(seed, o)
}

// TestScenarioDriversAcceptNilReports proves that NemesisRun and DuelRun run
// cleanly when given a nil report pointer.
func TestScenarioDriversAcceptNilReports(t *testing.T) {
	np := NemesisParams{Start: env.Time(300 * time.Millisecond), Phases: 1, Chaos: time.Second, Heal: time.Second, MaxGap: 300 * time.Millisecond,
		Converge: time.Minute, Writes: 2, WriteGap: 300 * time.Millisecond, Tick: 100 * time.Millisecond}
	if _, err := simtest.CheckDeterministic(1, NemesisRun(np, nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := simtest.CheckDeterministic(1, DuelRun(DuelParams{Converge: time.Minute, MinPartition: time.Second}, nil)); err != nil {
		t.Fatal(err)
	}
}

// TestCodesLieInTheSagaBlock proves that sagasim.Codes registers unique wire
// codes in the 144..159 block and builds a valid env.CodeTable.
func TestCodesLieInTheSagaBlock(t *testing.T) {
	if codeBlockMin != 144 || codeBlockMax != 159 {
		t.Fatalf("block %d..%d", codeBlockMin, codeBlockMax)
	}
	want := map[error]env.Code{ErrBusy: 152, ErrMoved: 153, ErrUnsettled: 154, ErrMalformed: 155}
	seen := map[env.Code]bool{}
	for _, row := range Codes() {
		if row.Code < codeBlockMin || row.Code > codeBlockMax {
			t.Fatalf("%v has code %d outside %d..%d", row.Err, row.Code, codeBlockMin, codeBlockMax)
		}
		if seen[row.Code] {
			t.Fatalf("code %d twice", row.Code)
		}
		seen[row.Code] = true
		if c, ok := want[row.Err]; ok && c != row.Code {
			t.Fatalf("%v has code %d, want %d", row.Err, row.Code, c)
		}
	}
	if len(Codes()) != len(saga.Codes())+len(want) {
		t.Fatalf("%d codes", len(Codes()))
	}
	if _, err := env.NewCodeTable(Codes()...); err != nil {
		t.Fatal(err)
	}
}

// TestObjectListCodecHasNoCliff proves that encodeObjs/decodeObjs round-trip
// up to MaxObjects within sim.DefaultMaxMessage and reject over-bound or
// malformed inputs with env.ErrTooLarge or ErrMalformed.
func TestObjectListCodecHasNoCliff(t *testing.T) {
	for _, n := range []int{0, 1, overCliff, bigBucket, MaxObjects} {
		objs := objects(n)
		b, err := encodeObjs(objs)
		if err != nil {
			t.Fatalf("%d objects: %v", n, err)
		}
		back, err := decodeObjs(b)
		if err != nil || len(back) != n || string(Root(back)) != string(Root(objs)) {
			t.Fatalf("%d objects: back %d, err %v", n, len(back), err)
		}
	}
	if _, err := encodeObjs(objects(MaxObjects + 1)); !errors.Is(err, env.ErrTooLarge) {
		t.Fatalf("an over-bound list was encoded: %v", err)
	}
	worst := make([]KV, MaxObjects)
	for i := range worst {
		worst[i] = KV{Key: fmt.Sprintf("%0*d", MaxKey, i), Val: make([]byte, MaxValue)}
	}
	b, err := encodeObjs(worst)
	if err != nil || len(b) > sim.DefaultMaxMessage {
		t.Fatalf("the largest list is %d bytes (message limit %d): %v", len(b), sim.DefaultMaxMessage, err)
	}
	for _, kv := range []KV{{Key: strings.Repeat("k", MaxKey+1)}, {Key: "k", Val: make([]byte, MaxValue+1)}} {
		if _, err := encodeObjs([]KV{kv}); !errors.Is(err, env.ErrTooLarge) {
			t.Fatalf("an oversized object was encoded: %v", err)
		}
	}
	good, _ := encodeObjs(objects(2))
	longKey := binary.AppendUvarint([]byte{1}, MaxKey+1)
	longVal := binary.AppendUvarint(append([]byte{1, 1}, 'k'), MaxValue+1)
	for i, bad := range [][]byte{nil, {0xFF}, good[:len(good)-1], append(good, 0), longKey, longVal} {
		if _, err := decodeObjs(bad); !errors.Is(err, ErrMalformed) {
			t.Fatalf("case %d: %v", i, err)
		}
	}
	over := binary.AppendUvarint(nil, MaxObjects+1)
	if _, err := decodeObjs(over); !errors.Is(err, ErrMalformed) {
		t.Fatalf("a count of %d was accepted: %v", MaxObjects+1, err)
	}
}

// TestStoreReloadsAndCopiesABucketAboveTheOldLimit proves that a Store persists,
// reloads across restart, and dumps a bucket holding 333 objects.
func TestStoreReloadsAndCopiesABucketAboveTheOldLimit(t *testing.T) {
	d := &mapDisk{blobs: map[string][]byte{}}
	clk := &nopClock{}
	seeds := []BucketSeed{{Name: Bucket, Loc: "a", Objs: objects(bigBucket)}}
	s := NewStore(seeds)(env.Env{ID: "a", Clock: clk, Disk: d, Net: &heldNet{}}).(*Store)
	s.Start()
	if !s.loaded {
		t.Fatal("the first load failed")
	}
	for i := 0; i < overCliff; i++ {
		if _, err := ask(s, opPut, Bucket, fmt.Sprintf("w%03d", i), "v"); err != nil {
			t.Fatal(err)
		}
	}
	again := NewStore(seeds)(env.Env{ID: "a", Clock: clk, Disk: d, Net: &heldNet{}}).(*Store)
	again.Start()
	if !again.loaded || again.loadErrs != 0 || len(again.st.bucket(Bucket).Objs) != bigBucket+overCliff {
		t.Fatalf("restart: loaded %v errors %d", again.loaded, again.loadErrs)
	}
	resp, err := ask(again, opDump, Bucket)
	if err != nil {
		t.Fatal(err)
	}
	objs, err := decodeObjs(resp)
	if err != nil || len(objs) != bigBucket+overCliff {
		t.Fatalf("dump: %d objects, %v", len(objs), err)
	}
}

// TestStoreWritePathEnforcesTheBound proves that every store write operation
// accepts up to MaxObjects, MaxKey, MaxValue, MaxName, MaxRegs, and MaxBuckets,
// refuses past each bound with env.ErrTooLarge, and reloads cleanly at the cap.
func TestStoreWritePathEnforcesTheBound(t *testing.T) {
	d := &mapDisk{blobs: map[string][]byte{}}
	clk := &nopClock{}
	seeds := []BucketSeed{{Name: Bucket, Loc: "a", Objs: objects(MaxObjects - 1)}}
	s := NewStore(seeds)(env.Env{ID: "a", Clock: clk, Disk: d, Net: &heldNet{}}).(*Store)
	s.Start()
	if _, err := ask(s, opPut, Bucket, "last", "v"); err != nil {
		t.Fatal(err)
	}
	if _, err := ask(s, opPut, Bucket, "one-too-many", "v"); !errors.Is(err, env.ErrTooLarge) {
		t.Fatalf("a put past the bound: %v", err)
	}
	if _, err := ask(s, opPut, Bucket, "last", "overwrite"); err != nil {
		t.Fatalf("an overwrite at the bound: %v", err)
	}
	if _, err := ask(s, opPut, Bucket, strings.Repeat("k", MaxKey+1), "v"); !errors.Is(err, env.ErrTooLarge) {
		t.Fatalf("an oversized key: %v", err)
	}
	if _, err := ask(s, opPut, Bucket, "k2", strings.Repeat("v", MaxValue+1)); !errors.Is(err, env.ErrTooLarge) {
		t.Fatalf("an oversized value: %v", err)
	}
	if _, err := ask(s, opRegister, Bucket, strings.Repeat("s", MaxName+1)); !errors.Is(err, env.ErrTooLarge) {
		t.Fatalf("an oversized saga id: %v", err)
	}
	if _, err := ask(s, opClaim, strings.Repeat("b", MaxName+1), "s1"); !errors.Is(err, env.ErrTooLarge) {
		t.Fatalf("an oversized bucket name: %v", err)
	}
	for i := 0; i < MaxRegs; i++ {
		if _, err := ask(s, opRegister, Bucket, fmt.Sprintf("s%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ask(s, opRegister, Bucket, "one-more"); !errors.Is(err, env.ErrTooLarge) {
		t.Fatalf("a registration past the bound: %v", err)
	}
	for i := 1; i < MaxBuckets; i++ {
		if _, err := ask(s, opClaim, fmt.Sprintf("nb%03d", i), "s1"); err != nil {
			t.Fatal(err)
		}
		if _, err := ask(s, opFlip, fmt.Sprintf("nb%03d", i), "a", stamp(1), "w"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ask(s, opClaim, "one-more", "s1"); !errors.Is(err, env.ErrTooLarge) {
		t.Fatalf("a claim past the bucket bound: %v", err)
	}
	if _, err := ask(s, opFlip, "one-more", "a", stamp(1), "w"); !errors.Is(err, env.ErrTooLarge) {
		t.Fatalf("a pointer past the bucket bound: %v", err)
	}
	again := NewStore(seeds)(env.Env{ID: "a", Clock: clk, Disk: d, Net: &heldNet{}}).(*Store)
	again.Start()
	if !again.loaded || len(again.st.bucket(Bucket).Objs) != MaxObjects || len(again.st.Buckets) != MaxBuckets {
		t.Fatalf("the state at every bound did not reload: loaded %v", again.loaded)
	}
}

// TestBuildRefusesAnUnstorableBucket proves that Build rejects Objects > MaxObjects
// with env.ErrInvalidArgument.
func TestBuildRefusesAnUnstorableBucket(t *testing.T) {
	_, _, err := Build(1, Options{Stores: threeStores(), Source: "a", Objects: MaxObjects + 1, Policy: MatrixPolicy()})
	if !errors.Is(err, env.ErrInvalidArgument) {
		t.Fatalf("err %v", err)
	}
}

// TestMigrationOfABucketAboveTheOldLimit proves that buckets of 33 and 300
// objects migrate cleanly even when the target store crashes and restarts.
func TestMigrationOfABucketAboveTheOldLimit(t *testing.T) {
	for _, n := range []int{overCliff, bigBucket} {
		run := func(seed int64) *sim.Sim {
			s, m, err := Build(seed, Options{
				Coordinators: []sim.NodeSpec{{ID: "c1", Region: "ra"}},
				Stores:       threeStores(),
				Source:       "a", Objects: n, Policy: MatrixPolicy(),
			})
			if err != nil {
				s.Fail(err)
				return s
			}
			in := saga.MigrationInput{Bucket: Bucket, From: "a", To: "b"}
			s.At(env.Time(time.Millisecond), "begin", func() {
				if err := m.Begin("c1", "big", in); err != nil {
					s.Fail(err)
				}
			})
			s.At(env.Time(500*time.Millisecond), "crash store b", func() { _ = s.Crash("b") })
			s.At(env.Time(time.Second), "restart store b", func() { _ = s.Restart("b") })
			if err := s.RunUntilConverged(env.Time(time.Minute), func() bool {
				st, ok := m.Status("c1", "big")
				return ok && st.State.Terminal()
			}); err != nil {
				return s
			}
			if st, _ := m.Status("c1", "big"); st.State != saga.StateCompleted {
				s.Fail(fmt.Errorf("%d objects: state %v cause %q", n, st.State, st.Cause))
				return s
			}
			if err := m.CheckMoved(in); err != nil {
				s.Fail(err)
			}
			return s
		}
		if _, err := simtest.CheckDeterministic(1, run); err != nil {
			t.Fatalf("%d objects: %v", n, err)
		}
	}
}
