package sagasim

import (
	"fmt"
	"strconv"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

// Matrix runner defaults and shape constants:
//   - MigrationSteps: number of steps in the bucket-migration workflow (5).
//   - MatrixObjects: default object count seeded by ModelHarness and NemesisRun.
//   - MatrixCells: total failure-matrix cells returned by Cells (78).
//   - MatrixBound: default virtual-time deadline from migration start to terminal state.
//   - MatrixSettle: default post-terminal virtual-time settle period before checks run.
const (
	MigrationSteps = saga.MigrationSteps
	MatrixObjects  = 8
	MatrixCells    = 78
	MatrixBound    = 20 * time.Second
	MatrixSettle   = 2 * time.Second
	matrixBegin    = 20 * time.Millisecond
	matrixSaga     = saga.ID("mig-1")
	restartDelay   = 500 * time.Millisecond
	lostAfterPivot = 2
)

// MatrixPolicy returns the default saga.Policy used by Run and the scenario drivers.
func MatrixPolicy() saga.Policy {
	return saga.Policy{MaxAttempts: 3, BaseDelay: 50 * time.Millisecond, MaxDelay: time.Second, StepTimeout: 5 * time.Second, JitterPPM: 100_000}
}

// FailureKind identifies the fault injection mode of a matrix Cell.
type FailureKind uint8

// Failure injection modes tested across the migration workflow:
//   - FailBeforeAction: step refuses with saga.ErrPermanent without acting.
//   - FailAfterAction: step acts, then its callback is replaced with env.ErrTimeout.
//   - FailMidCompensation: pivot refuses; named step's compensation acts then fails 2*MaxAttempts times.
//   - CrashBeforeAction: coordinator freezes and crashes after appending RecIntent, before Sync.
//   - CrashAfterAction: coordinator freezes and crashes after appending RecDone, before Sync.
//   - CrashMidCompensation: pivot refuses; coordinator freezes and crashes after appending RecCompDone, before Sync.
const (
	FailBeforeAction FailureKind = iota + 1
	FailAfterAction
	FailMidCompensation
	CrashBeforeAction
	CrashAfterAction
	CrashMidCompensation
)

// String returns the canonical hyphenated name of k, or "kind-N" if unknown.
func (k FailureKind) String() string {
	switch k {
	case FailBeforeAction:
		return "fail-before-action"
	case FailAfterAction:
		return "fail-after-action"
	case FailMidCompensation:
		return "fail-mid-compensation"
	case CrashBeforeAction:
		return "crash-before-action"
	case CrashAfterAction:
		return "crash-after-action"
	case CrashMidCompensation:
		return "crash-mid-compensation"
	}
	return "kind-" + strconv.Itoa(int(k))
}

func (k FailureKind) crash() bool {
	return k == CrashBeforeAction || k == CrashAfterAction || k == CrashMidCompensation
}

func (k FailureKind) midComp() bool {
	return k == FailMidCompensation || k == CrashMidCompensation
}

// Cell specifies one point in the failure matrix: target step index, fault kind,
// whether crashes tear the log tail (Torn), and whether a second torn crash
// fires on the first log append after restart (Double).
type Cell struct {
	Step   int
	Kind   FailureKind
	Torn   bool
	Double bool
}

var stepNames = [MigrationSteps]string{saga.StepAllocate, saga.StepCopy, saga.StepVerify, saga.StepFlip, saga.StepDeleteSource}

// Name returns a stable subtest-friendly label for c.
func (c Cell) Name() string {
	step := "step" + strconv.Itoa(c.Step)
	if c.Step >= 0 && c.Step < MigrationSteps {
		step = stepNames[c.Step]
	}
	n := step + "-" + c.Kind.String()
	if c.Torn {
		n += "-torn"
	} else {
		n += "-untorn"
	}
	if c.Double {
		n += "-double"
	}
	return n
}

// Want returns the terminal saga.State expected when c completes cleanly.
func (c Cell) Want() saga.State {
	switch {
	case c.Kind.midComp():
		return saga.StateCompensated
	case c.Kind == FailBeforeAction && c.Step <= saga.MigrationPivot:
		return saga.StateCompensated
	case c.Kind == FailAfterAction && c.Step < saga.MigrationPivot:
		return saga.StateCompensated
	}
	return saga.StateCompleted
}

// Cells returns all 78 matrix cells covering every step, failure kind (with
// mid-compensation cells for pre-pivot steps only), torn/untorn variants, and
// double-crash variants for crash kinds.
func Cells() []Cell {
	var out []Cell
	kinds := []FailureKind{FailBeforeAction, FailAfterAction, FailMidCompensation, CrashBeforeAction, CrashAfterAction, CrashMidCompensation}
	for step := 0; step < MigrationSteps; step++ {
		for _, k := range kinds {
			if k.midComp() && step >= saga.MigrationPivot {
				continue
			}
			for _, torn := range []bool{false, true} {
				doubles := []bool{false}
				if k.crash() {
					doubles = []bool{false, true}
				}
				for _, d := range doubles {
					out = append(out, Cell{Step: step, Kind: k, Torn: torn, Double: d})
				}
			}
		}
	}
	return out
}

// BuildOptions passes the effective policy, step wrapper, and engine config
// hook from Run into Harness.Build.
type BuildOptions struct {
	Policy    saga.Policy
	Wrap      func(saga.Definition) saga.Definition
	Configure func(*saga.Config)
}

// Cluster is the narrow handle Run uses to drive and verify a migration on any
// simulated cluster implementation.
type Cluster interface {
	Coordinator() env.NodeID
	Begin(id saga.ID) error
	Status(id saga.ID) (saga.Status, bool)
	LogName(id saga.ID) string
	CheckMoved() error
	CheckCompensated() error
}

// Harness constructs a simulated cluster for Run. Build may advance the Sim's
// clock to warm up cluster state before returning.
type Harness interface {
	Build(seed int64, o BuildOptions) (*sim.Sim, Cluster, error)
}

type modelHarness struct{}

// ModelHarness returns a Harness backed by this package's three-store simulated
// model cluster.
func ModelHarness() Harness { return modelHarness{} }

type modelCluster struct {
	m  *Model
	in saga.MigrationInput
}

func (modelHarness) Build(seed int64, o BuildOptions) (*sim.Sim, Cluster, error) {
	s, m, err := Build(seed, Options{
		Coordinators: []sim.NodeSpec{{ID: "c1", Region: "ra"}},
		Stores:       []sim.NodeSpec{{ID: "a", Region: "ra"}, {ID: "b", Region: "rb"}, {ID: "c", Region: "rc"}},
		Source:       "a",
		Objects:      MatrixObjects,
		Policy:       o.Policy,
		Wrap: func(_ env.NodeID, d saga.Definition) saga.Definition {
			if o.Wrap == nil {
				return d
			}
			return o.Wrap(d)
		},
		Configure: func(_ env.NodeID, cfg *saga.Config) {
			if o.Configure != nil {
				o.Configure(cfg)
			}
		},
	})
	if err != nil {
		return s, nil, err
	}
	m.AddInvariants(s)
	return s, &modelCluster{m: m, in: saga.MigrationInput{Bucket: Bucket, From: "a", To: "b"}}, nil
}

func (c *modelCluster) Coordinator() env.NodeID { return "c1" }
func (c *modelCluster) Begin(id saga.ID) error  { return c.m.Begin("c1", id, c.in) }
func (c *modelCluster) Status(id saga.ID) (saga.Status, bool) {
	return c.m.Status("c1", id)
}
func (c *modelCluster) LogName(id saga.ID) string { return saga.DefaultPrefix + string(id) }
func (c *modelCluster) CheckMoved() error         { return c.m.CheckMoved(c.in) }
func (c *modelCluster) CheckCompensated() error   { return c.m.CheckCompensated(c.in) }

// RunOptions customizes Run. Zero Bound, Settle, and Policy default to
// MatrixBound, MatrixSettle, and MatrixPolicy().
type RunOptions struct {
	Configure func(*saga.Config)
	Report    *Report
	Bound     time.Duration
	Settle    time.Duration
	Policy    saga.Policy
}

func (o RunOptions) bound() time.Duration {
	if o.Bound == 0 {
		return MatrixBound
	}
	return o.Bound
}

func (o RunOptions) settle() time.Duration {
	if o.Settle == 0 {
		return MatrixSettle
	}
	return o.Settle
}

func (o RunOptions) policy() saga.Policy {
	if o.Policy == (saga.Policy{}) {
		return MatrixPolicy()
	}
	return o.Policy
}

// Report records timing, fault counts, and terminal status from one matrix Run.
type Report struct {
	State     saga.State
	Outcome   string
	Start     env.Time
	Elapsed   time.Duration
	Crashes   int
	Torn      int
	CompFails int
	Calls     []int
}

type cellState struct {
	cell        Cell
	policy      saga.Policy
	s           *sim.Sim
	cl          Cluster
	fired       bool
	failFired   bool
	trig        bool
	tornDone    bool
	doubleArmed bool
	lost        int
	compFails   int
	crashes     int
	torn        int
	calls       []int
	lastComp    int
	err         error
}

func (st *cellState) note(err error) {
	if st.err == nil {
		st.err = err
	}
}

func (st *cellState) ended() bool {
	b, ok := st.s.Disk(st.cl.Coordinator()).PeekDurable(st.cl.LogName(matrixSaga))
	if !ok {
		return false
	}
	recs, _, err := saga.DecodeLog(b)
	return err == nil && len(recs) > 0 && recs[len(recs)-1].Kind == saga.RecEnd
}

func (st *cellState) crash(torn bool) {
	st.crashes++
	id := st.cl.Coordinator()
	now := st.s.Now()
	st.s.At(now, "crash coordinator", func() {
		if torn {
			_ = st.s.CrashTorn(id)
		} else {
			_ = st.s.Crash(id)
		}
	})
	st.s.At(now.Add(restartDelay), "restart coordinator", func() {
		if b, ok := st.s.Disk(id).PeekDurable(st.cl.LogName(matrixSaga)); ok {
			if _, valid, err := saga.DecodeLog(b); err == nil && valid < len(b) {
				st.torn++
			}
		}
		_ = st.s.Restart(id)
	})
}

// hook intercepts log appends for matrixSaga only, freezing and scheduling a
// coordinator crash at the cell's configured record boundary or recovery append.
func (st *cellState) hook(p saga.Point) bool {
	c := st.cell
	if p.Saga != matrixSaga {
		return false
	}
	if st.doubleArmed {
		st.doubleArmed = false
		st.crash(true)
		return true
	}
	if c.Kind.crash() && !st.fired && p.Step == c.Step {
		want := map[FailureKind]saga.RecordKind{CrashBeforeAction: saga.RecIntent, CrashAfterAction: saga.RecDone, CrashMidCompensation: saga.RecCompDone}[c.Kind]
		if p.Kind == want {
			st.fired = true
			st.doubleArmed = c.Double
			st.crash(c.Torn)
			return true
		}
	}
	if !c.Kind.crash() && c.Torn && st.failFired && !st.tornDone {
		st.tornDone = true
		st.crash(true)
		return true
	}
	return false
}

type injStep struct {
	saga.Step
	st *cellState
	i  int
}

func injected(what string, err error) error {
	return fmt.Errorf("sagasim: injected %s: %w", what, err)
}

func (s *injStep) Do(c saga.Call, done func([]byte, error)) {
	st, cell := s.st, s.st.cell
	if c.Key.Saga != matrixSaga {
		s.Step.Do(c, done)
		return
	}
	st.calls[s.i]++
	if st.ended() {
		st.note(fmt.Errorf("sagasim: step %d ran after the end record: %w", s.i, ErrCheck))
	}
	switch {
	// Keep FailBeforeAction sticky up to the pivot so a torn abort record still
	// retries into the same definitive refusal rather than succeeding.
	case cell.Kind == FailBeforeAction && s.i == cell.Step && (!st.failFired || s.i <= saga.MigrationPivot):
		st.failFired = true
		done(nil, injected("refusal", saga.ErrPermanent))
		return
	case cell.Kind.midComp() && s.i == saga.MigrationPivot && !st.trig:
		st.trig = true
		done(nil, injected("pivot refusal", saga.ErrPermanent))
		return
	case cell.Kind == FailAfterAction && s.i == cell.Step && st.lost < st.lostLimit():
		s.Step.Do(c, func([]byte, error) {
			st.lost++
			st.failFired = true
			done(nil, injected("lost response", env.ErrTimeout))
		})
		return
	}
	s.Step.Do(c, done)
}

func (st *cellState) lostLimit() int {
	if st.cell.Step < saga.MigrationPivot {
		return st.policy.MaxAttempts
	}
	return lostAfterPivot
}

func (s *injStep) Compensate(c saga.Call, done func(error)) {
	st, cell := s.st, s.st.cell
	if c.Key.Saga != matrixSaga {
		s.Step.Compensate(c, done)
		return
	}
	if st.ended() {
		st.note(fmt.Errorf("sagasim: compensation %d ran after the end record: %w", s.i, ErrCheck))
	}
	if cell.Kind == FailMidCompensation && s.i == cell.Step && st.compFails < 2*st.policy.MaxAttempts {
		s.Step.Compensate(c, func(error) {
			st.compFails++
			st.failFired = true
			done(injected("compensation failure", env.ErrTimeout))
		})
		return
	}
	s.Step.Compensate(c, func(err error) {
		if err == nil {
			if s.i > st.lastComp {
				st.note(fmt.Errorf("sagasim: compensation of step %d after step %d: %w", s.i, st.lastComp, ErrCheck))
			}
			st.lastComp = s.i
		}
		done(err)
	})
}

func (st *cellState) wrap(d saga.Definition) saga.Definition {
	steps := make([]saga.Step, len(d.Steps))
	for i, s := range d.Steps {
		steps[i] = &injStep{Step: s, st: st, i: i}
	}
	d.Steps = steps
	return d
}

// Run returns a deterministic simulation function for one matrix Cell on h.
// It starts the migration 20 ms after h.Build returns, waits for a terminal
// state within the effective bound plus settle window, asserts the cell's fault
// actually fired and terminal checks pass, and records any failure in Sim.Err
// wrapped with ErrCheck.
func Run(h Harness, c Cell, o RunOptions) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		st := &cellState{cell: c, policy: o.policy(), calls: make([]int, MigrationSteps), lastComp: MigrationSteps}
		s, cl, err := h.Build(seed, BuildOptions{
			Policy: st.policy,
			Wrap:   st.wrap,
			Configure: func(cfg *saga.Config) {
				cfg.Hook = st.hook
				if o.Configure != nil {
					o.Configure(cfg)
				}
			},
		})
		if s == nil {
			s = sim.New(sim.Config{Seed: seed})
		}
		if err == nil && cl == nil {
			err = fmt.Errorf("sagasim: cell %s: the harness built no cluster: %w", c.Name(), env.ErrInvalidArgument)
		}
		if err != nil {
			s.Fail(err)
			return s
		}
		if err := s.Err(); err != nil {
			return s
		}
		st.s, st.cl = s, cl
		start := s.Now().Add(matrixBegin)
		s.At(start, "begin", func() {
			if err := cl.Begin(matrixSaga); err != nil {
				s.Fail(err)
			}
		})
		done := func() bool {
			sta, ok := cl.Status(matrixSaga)
			return ok && sta.State.Terminal()
		}
		if err := s.RunUntilConverged(start.Add(o.bound()), done); err != nil {
			return s
		}
		end := s.Now()
		if err := s.RunFor(o.settle()); err != nil {
			return s
		}
		sta, ok := cl.Status(matrixSaga)
		if !ok {
			s.Fail(fmt.Errorf("sagasim: cell %s: coordinator gone after settling: %w", c.Name(), ErrCheck))
			return s
		}
		if o.Report != nil {
			*o.Report = Report{State: sta.State, Start: start, Elapsed: end.Sub(start), Crashes: st.crashes, Torn: st.torn, CompFails: st.compFails, Calls: st.calls}
			if len(sta.Outputs) == MigrationSteps {
				o.Report.Outcome = string(sta.Outputs[MigrationSteps-1])
			}
		}
		if err := st.check(cl, sta); err != nil {
			s.Fail(fmt.Errorf("sagasim: cell %s: %w", c.Name(), err))
		}
		return s
	}
}

func (st *cellState) check(cl Cluster, sta saga.Status) error {
	if st.err != nil {
		return st.err
	}
	if sta.State != st.cell.Want() {
		return fmt.Errorf("ended %v, want %v: %w", sta.State, st.cell.Want(), ErrCheck)
	}
	if err := st.injected(); err != nil {
		return err
	}
	if sta.State == saga.StateCompleted {
		if len(sta.Outputs) != MigrationSteps || string(sta.Outputs[MigrationSteps-1]) != saga.OutcomeMoved {
			return fmt.Errorf("completed without moving: %w", ErrCheck)
		}
		return cl.CheckMoved()
	}
	return cl.CheckCompensated()
}

// injected verifies that every fault required by the cell actually fired, so a
// harness that omits Wrap or Hook cannot pass silently.
func (st *cellState) injected() error {
	c := st.cell
	switch {
	case c.Kind.crash() && !st.fired:
		return fmt.Errorf("the crash point was never reached: %w", ErrCheck)
	case c.Kind.midComp() && !st.trig:
		return fmt.Errorf("the pivot refusal that starts compensation was never injected: %w", ErrCheck)
	case !c.Kind.crash() && !st.failFired:
		return fmt.Errorf("the %s failure was never injected: %w", c.Kind, ErrCheck)
	case c.Torn && !c.Kind.crash() && !st.tornDone:
		return fmt.Errorf("the torn crash after the failure never happened: %w", ErrCheck)
	case c.Double && st.crashes < 2:
		return fmt.Errorf("the second crash of a double cell never happened (%d crashes): %w", st.crashes, ErrCheck)
	}
	return nil
}
