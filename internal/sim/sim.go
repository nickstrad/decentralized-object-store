// Package sim implements a single-threaded discrete-event simulator for
// deterministic simulation testing of protocol cores written against internal/env.
//
// A run is driven entirely by one int64 seed forked into independent named PRNG
// streams (network, disk, nemesis, workload, and one per node). Global virtual
// time advances in integer nanoseconds up to Horizon, events execute in total
// (time, seq) order, and callbacks never run re-entrantly. Every failure—stall,
// invariant violation, or recovered panic—is sticky in Sim.Err and stops the
// loop.
package sim

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"math/rand/v2"
	"runtime/debug"
	"sort"
	"time"

	"github.com/nickstrad/object-store/internal/env"
)

// Simulator bounds and defaults:
//   - DefaultMaxSteps: default cap on events executed over a Sim's lifetime.
//   - DefaultLogCap: default ring-buffer capacity for retained event log records.
//   - MaxDriftPPM: maximum allowed magnitude of NodeSpec.DriftPPM (10%).
//   - MaxSkew: maximum allowed magnitude of NodeSpec.Skew (2^61 ns, ~73 years).
//   - Horizon: upper bound on global virtual time (2^62 ns, ~146 years); events
//     due after Horizon are never queued and never run.
const (
	DefaultMaxSteps = 10_000_000
	DefaultLogCap   = 1 << 20
	MaxDriftPPM     = 100_000
	MaxSkew         = time.Duration(1 << 61)
	Horizon         = env.Time(1 << 62)
)

// Sentinel errors returned by simulator control, loop, and validation methods:
//   - ErrStall: RunUntilConverged or Step stopped without progressing; wraps one
//     of ErrQueueEmpty, ErrBudgetExhausted, or ErrDeadline via *StallError.
//   - ErrQueueEmpty: the event queue emptied before convergence was reached.
//   - ErrBudgetExhausted: the run reached Config.MaxSteps.
//   - ErrDeadline: virtual time reached the convergence deadline without converging.
//   - ErrInvariant: a registered invariant returned a non-nil error.
//   - ErrInvariantNotRun: Verify found an invariant ran fewer times than scheduled.
//   - ErrUnknownNode: an operation named a NodeID not registered in the Sim.
//   - ErrUnknownRegion: PartitionRegion named a region with no registered nodes.
//   - ErrDuplicateNode: AddNode was called with an already-registered NodeID.
//   - ErrNodeDown: an operation requiring a live node targeted a crashed node.
//   - ErrNodeUp: Restart targeted a node that is already up.
//   - ErrNotPaused: Resume targeted a node that is not paused.
//   - ErrBudget: Budget.Check found the cluster outside its fault budget.
var (
	ErrStall           = errors.New("sim: stall")
	ErrQueueEmpty      = errors.New("event queue empty before convergence")
	ErrBudgetExhausted = errors.New("step budget exhausted")
	ErrDeadline        = errors.New("deadline passed before convergence")
	ErrInvariant       = errors.New("sim: invariant violated")
	ErrInvariantNotRun = errors.New("sim: invariant not run as often as scheduled")
	ErrUnknownNode     = errors.New("sim: unknown node")
	ErrUnknownRegion   = errors.New("sim: unknown region")
	ErrDuplicateNode   = errors.New("sim: duplicate node")
	ErrNodeDown        = errors.New("sim: node is down")
	ErrNodeUp          = errors.New("sim: node is up")
	ErrNotPaused       = errors.New("sim: node is not paused")
	ErrBudget          = errors.New("sim: fault budget exceeded")
)

// Config configures a new Sim. Zero Net selects DefaultNetConfig; zero Codes
// selects env.DefaultCodes; zero or negative MaxSteps and LogCap fall back to
// DefaultMaxSteps and DefaultLogCap.
type Config struct {
	Seed     int64
	Net      NetConfig
	Codes    *env.CodeTable
	MaxSteps uint64
	LogCap   int
}

// NodeSpec describes a node's identity, region, and local clock parameters.
// Skew must lie within ±MaxSkew and DriftPPM within ±MaxDriftPPM.
type NodeSpec struct {
	ID       env.NodeID
	Region   env.Region
	Skew     time.Duration
	DriftPPM int64
}

// Factory constructs a fresh env.Process for each incarnation of a node.
type Factory func(e env.Env) env.Process

// NodeStatus reports a node's lifecycle flags and current incarnation number.
type NodeStatus struct {
	Known       bool
	Up          bool
	Paused      bool
	Started     bool
	Incarnation uint32
}

// Scenario pairs a CLI name with a function that runs a simulation from a seed.
type Scenario struct {
	Name string
	Run  func(seed int64) *Sim
}

// StallError records a loop stall. It unwraps to both ErrStall and Reason
// (ErrQueueEmpty, ErrBudgetExhausted, or ErrDeadline).
type StallError struct {
	Seed   int64
	At     env.Time
	Steps  uint64
	Reason error
}

// Error formats the seed, virtual time, step count, and stall reason.
func (e *StallError) Error() string {
	return fmt.Sprintf("sim: seed %d: stall at t=%d after %d steps: %v", e.Seed, e.At, e.Steps, e.Reason)
}

// Unwrap exposes ErrStall and Reason for errors.Is matching.
func (e *StallError) Unwrap() []error { return []error{ErrStall, e.Reason} }

// InvariantError records a failed safety invariant check. It unwraps to both
// ErrInvariant and the underlying Err returned by the invariant function.
type InvariantError struct {
	Seed  int64
	Name  string
	At    env.Time
	Steps uint64
	Err   error
}

// Error formats the seed, invariant name, virtual time, step count, and cause.
func (e *InvariantError) Error() string {
	return fmt.Sprintf("sim: seed %d: invariant %q violated at t=%d after %d steps: %v", e.Seed, e.Name, e.At, e.Steps, e.Err)
}

// Unwrap exposes ErrInvariant and the underlying error for errors.Is matching.
func (e *InvariantError) Unwrap() []error { return []error{ErrInvariant, e.Err} }

// PanicError records a panic recovered from node code, a factory, an action, an
// invariant, a convergence predicate, EncodeState, or a fault callback. Stack is
// captured for diagnostics only and never enters digests or scheduling decisions.
type PanicError struct {
	Seed  int64
	Node  env.NodeID
	At    env.Time
	Event string
	Value any
	Stack []byte
}

// Error formats the seed, event label, node ID, virtual time, and panic value.
func (e *PanicError) Error() string {
	return fmt.Sprintf("sim: seed %d: panic in %s at node %q t=%d: %v", e.Seed, e.Event, e.Node, e.At, e.Value)
}

type invariant struct {
	name    string
	every   uint64
	fn      func() error
	runs    uint64
	addedAt uint64
}

type node struct {
	idx     int
	id      env.NodeID
	region  env.Region
	skew    int64
	drift   int64
	factory Factory
	rng     *rand.Rand
	inc     uint32
	up      bool
	paused  bool
	started bool
	proc    env.Process
	handle  *handle
	held    []*event
	relHeld int
	calls   map[uint64]*call
	disk    *Disk
}

// Sim owns the event queue, simulated nodes, directed link state, named PRNG
// streams, invariants, and canonical event log for one deterministic run.
// A Sim is not safe for concurrent use.
type Sim struct {
	cfg            Config
	codes          *env.CodeTable
	now            env.Time
	seq            uint64
	steps          uint64
	q              eventQueue
	nodes          []*node
	byID           map[env.NodeID]int
	links          [][]link
	netRand        *rand.Rand
	diskRand       *rand.Rand
	nemRand        *rand.Rand
	workRand       *rand.Rand
	invariants     []*invariant
	err            error
	dropInvariants bool
	nextCall       uint64
	logHash        hash.Hash
	logBuf         []record
	logStart       int
	logTotal       uint64
}

// New constructs a Sim from cfg. If cfg.Net is invalid, the validation error is
// recorded as the sticky error in Err() so callers observe it on the first Step
// or Verify.
func New(cfg Config) *Sim {
	if cfg.Net == (NetConfig{}) {
		cfg.Net = DefaultNetConfig()
	}
	if cfg.Net.MaxDatagram <= 0 {
		cfg.Net.MaxDatagram = DefaultMaxDatagram
	}
	if cfg.Net.MaxMessage <= 0 {
		cfg.Net.MaxMessage = DefaultMaxMessage
	}
	if cfg.MaxSteps == 0 {
		cfg.MaxSteps = DefaultMaxSteps
	}
	if cfg.LogCap <= 0 {
		cfg.LogCap = DefaultLogCap
	}
	s := &Sim{
		cfg:      cfg,
		codes:    cfg.Codes,
		byID:     map[env.NodeID]int{},
		netRand:  NewStream(cfg.Seed, StreamNet),
		diskRand: NewStream(cfg.Seed, StreamDisk),
		nemRand:  NewStream(cfg.Seed, StreamNemesis),
		workRand: NewStream(cfg.Seed, StreamWorkload),
		logHash:  sha256.New(),
	}
	if s.codes == nil {
		s.codes = env.DefaultCodes()
	}
	if err := cfg.Net.validate(); err != nil {
		s.err = err
	}
	return s
}

// Seed returns the root seed configured for this run.
func (s *Sim) Seed() int64 { return s.cfg.Seed }

// Now returns the current global virtual time.
func (s *Sim) Now() env.Time { return s.now }

// Steps returns the number of events executed so far.
func (s *Sim) Steps() uint64 { return s.steps }

// Err returns the first sticky failure recorded by the run, or nil.
func (s *Sim) Err() error { return s.err }

// Fail records err as the run's sticky error if none is already set, stopping
// subsequent loop execution.
func (s *Sim) Fail(err error) {
	if s.err == nil && err != nil {
		s.err = err
	}
}

// WorkloadRand returns the PRNG stream dedicated to test workload actions.
func (s *Sim) WorkloadRand() *rand.Rand { return s.workRand }

// NodeIDs returns n deterministic node identifiers ("n000", "n001", ...).
func NodeIDs(n int) []env.NodeID {
	ids := make([]env.NodeID, n)
	for i := range ids {
		ids[i] = env.NodeID(fmt.Sprintf("n%03d", i))
	}
	return ids
}

// AddCluster registers every node in ids using factory f, assigning regions
// "r0".."r<regions-1>" round-robin (clamping regions < 1 to 1).
func (s *Sim) AddCluster(ids []env.NodeID, regions int, f Factory) error {
	if regions < 1 {
		regions = 1
	}
	for i, id := range ids {
		if err := s.AddNode(NodeSpec{ID: id, Region: env.Region(fmt.Sprintf("r%d", i%regions))}, f); err != nil {
			return err
		}
	}
	return nil
}

// AddNode registers a node and schedules its initial Start event at the current
// time. It returns env.ErrInvalidArgument for an empty ID, nil factory, or
// out-of-range skew/drift, and ErrDuplicateNode if spec.ID already exists.
func (s *Sim) AddNode(spec NodeSpec, f Factory) error {
	if spec.ID == "" {
		return fmt.Errorf("sim: add node: empty id: %w", env.ErrInvalidArgument)
	}
	if f == nil {
		return fmt.Errorf("sim: add node %s: nil factory: %w", spec.ID, env.ErrInvalidArgument)
	}
	if spec.Skew > MaxSkew || spec.Skew < -MaxSkew {
		return fmt.Errorf("sim: add node %s: skew %d outside ±%d: %w", spec.ID, spec.Skew, MaxSkew, env.ErrInvalidArgument)
	}
	if spec.DriftPPM > MaxDriftPPM || spec.DriftPPM < -MaxDriftPPM {
		return fmt.Errorf("sim: add node %s: drift %d ppm outside ±%d: %w", spec.ID, spec.DriftPPM, MaxDriftPPM, env.ErrInvalidArgument)
	}
	if _, ok := s.byID[spec.ID]; ok {
		return fmt.Errorf("sim: add node %s: %w", spec.ID, ErrDuplicateNode)
	}
	n := &node{
		idx:     len(s.nodes),
		id:      spec.ID,
		region:  spec.Region,
		skew:    int64(spec.Skew),
		drift:   spec.DriftPPM,
		factory: f,
		rng:     NewStream(s.cfg.Seed, NodeStream(spec.ID)),
		calls:   map[uint64]*call{},
	}
	n.disk = &Disk{s: s, blobs: map[string]*blob{}, damaged: map[string]bool{}, failReads: map[string]int{}}
	s.nodes = append(s.nodes, n)
	s.byID[spec.ID] = n.idx
	s.growLinks()
	s.boot(n)
	return nil
}

// boot advances the node to a new incarnation with a fresh handle and schedules
// its Start event so factory and Start panics are caught like any other event.
func (s *Sim) boot(n *node) {
	n.inc++
	n.up = true
	n.paused = false
	n.started = false
	n.handle = &handle{s: s, n: n, inc: n.inc}
	s.schedule(&event{at: s.now, kind: kindStart, node: n.idx, peer: -1, inc: n.inc})
}

func (s *Sim) node(id env.NodeID) (*node, error) {
	i, ok := s.byID[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownNode, id)
	}
	return s.nodes[i], nil
}

// Nodes returns all registered node IDs in registration order.
func (s *Sim) Nodes() []env.NodeID {
	ids := make([]env.NodeID, len(s.nodes))
	for i, n := range s.nodes {
		ids[i] = n.id
	}
	return ids
}

// Regions returns the sorted list of distinct regions present in the cluster.
func (s *Sim) Regions() []env.Region {
	seen := map[env.Region]bool{}
	var out []env.Region
	for _, n := range s.nodes {
		if !seen[n.region] {
			seen[n.region] = true
			out = append(out, n.region)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Region returns the region of node id, or "" if id is unknown.
func (s *Sim) Region(id env.NodeID) env.Region {
	n, err := s.node(id)
	if err != nil {
		return ""
	}
	return n.region
}

// Process returns the live, started env.Process for node id, or nil if id is
// unknown, down, or not yet started. A paused node's Process is still returned;
// callers should check Status(id).Paused before driving it.
func (s *Sim) Process(id env.NodeID) env.Process {
	n, err := s.node(id)
	if err != nil || !n.up || !n.started {
		return nil
	}
	return n.proc
}

// Status returns lifecycle flags for node id; Known is false for unknown IDs.
func (s *Sim) Status(id env.NodeID) NodeStatus {
	n, err := s.node(id)
	if err != nil {
		return NodeStatus{}
	}
	return NodeStatus{Known: true, Up: n.up, Paused: n.paused, Started: n.started, Incarnation: n.inc}
}

// Disk returns the simulated Disk for node id, or nil if id is unknown.
func (s *Sim) Disk(id env.NodeID) *Disk {
	n, err := s.node(id)
	if err != nil {
		return nil
	}
	return n.disk
}

// LiveCount returns the number of nodes that are currently up and not paused.
func (s *Sim) LiveCount() int {
	c := 0
	for _, n := range s.nodes {
		if n.up && !n.paused {
			c++
		}
	}
	return c
}

// LocalTime returns node id's local clock reading at the current global time,
// or the global time if id is unknown.
func (s *Sim) LocalTime(id env.NodeID) env.Time {
	n, err := s.node(id)
	if err != nil {
		return s.now
	}
	return n.local(s.now)
}

// schedule clamps past timestamps to s.now and drops events beyond Horizon
// (leaving hi == -1 so cancellation checks treat them as unqueued).
func (s *Sim) schedule(e *event) *event {
	if e.at < s.now {
		e.at = s.now
	}
	if e.at > Horizon {
		e.hi = -1
		return e
	}
	s.seq++
	e.seq = s.seq
	s.q.push(e)
	return e
}

// At schedules a named test action to run at virtual time t (or now if t is in
// the past). Actions due after Horizon are ignored.
func (s *Sim) At(t env.Time, name string, fn func()) {
	s.schedule(&event{at: t, kind: kindAction, node: -1, peer: -1, label: name, fn: fn})
}

// Workload schedules count invocations of fn(0)..fn(count-1) starting at start,
// separated by gaps drawn uniformly from [1, maxGap] on the workload stream.
func (s *Sim) Workload(start env.Time, count int, maxGap time.Duration, fn func(i int)) {
	if count <= 0 {
		return
	}
	if maxGap < 1 {
		maxGap = 1
	}
	var step func(i int) func()
	step = func(i int) func() {
		return func() {
			fn(i)
			if i+1 < count {
				gap := 1 + s.workRand.Int64N(int64(maxGap))
				s.At(later(s.now, time.Duration(gap)), "workload", step(i+1))
			}
		}
	}
	s.At(start, "workload", step(0))
}

// AddInvariant registers a safety check run after every N executed events
// (clamping every < 1 to 1). A non-nil error stops the loop with *InvariantError.
func (s *Sim) AddInvariant(name string, every int, fn func() error) {
	if every < 1 {
		every = 1
	}
	s.invariants = append(s.invariants, &invariant{name: name, every: uint64(every), fn: fn, addedAt: s.steps})
}

// InvariantRuns returns the total number of times invariants named name have run.
func (s *Sim) InvariantRuns(name string) uint64 {
	var total uint64
	for _, inv := range s.invariants {
		if inv.name == name {
			total += inv.runs
		}
	}
	return total
}

func (s *Sim) runInvariant(inv *invariant) {
	inv.runs++
	var err error
	s.guard(-1, "invariant "+inv.name, func() { err = inv.fn() })
	if err != nil && s.err == nil {
		s.err = &InvariantError{Seed: s.cfg.Seed, Name: inv.name, At: s.now, Steps: s.steps, Err: err}
	}
}

// CheckInvariants runs every registered invariant immediately and returns the
// sticky error if any fails or panics.
func (s *Sim) CheckInvariants() error {
	for _, inv := range s.invariants {
		if s.err != nil {
			break
		}
		s.runInvariant(inv)
	}
	return s.err
}

// Verify returns the sticky error if one occurred, or ErrInvariantNotRun if any
// registered invariant executed fewer times than its schedule required.
func (s *Sim) Verify() error {
	if s.err != nil {
		return s.err
	}
	for _, inv := range s.invariants {
		want := s.steps/inv.every - inv.addedAt/inv.every
		if inv.runs < want {
			return fmt.Errorf("sim: seed %d: invariant %q ran %d times, expected at least %d: %w", s.cfg.Seed, inv.name, inv.runs, want, ErrInvariantNotRun)
		}
	}
	return nil
}

// guard runs fn and turns any panic into a sticky *PanicError carrying the seed
// and originating node.
func (s *Sim) guard(nodeIdx int, what string, fn func()) {
	defer func() {
		if v := recover(); v != nil {
			pe := &PanicError{Seed: s.cfg.Seed, At: s.now, Event: what, Value: v, Stack: debug.Stack()}
			if nodeIdx >= 0 {
				pe.Node = s.nodes[nodeIdx].id
			}
			if s.err == nil {
				s.err = pe
			}
		}
	}()
	fn()
}

func (s *Sim) stall(reason error) error {
	if s.err == nil {
		s.err = &StallError{Seed: s.cfg.Seed, At: s.now, Steps: s.steps, Reason: reason}
	}
	return s.err
}

// Step pops and executes the next queued event, records it in the event log,
// and runs due invariants. It returns (false, nil) when the queue is empty, or
// (false, err) when a sticky error exists or Config.MaxSteps is exhausted.
func (s *Sim) Step() (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	if len(s.q) == 0 {
		return false, nil
	}
	if s.steps >= s.cfg.MaxSteps {
		return false, s.stall(ErrBudgetExhausted)
	}
	e := s.q.pop()
	if e.at > s.now {
		s.now = e.at
	}
	s.steps++
	// Hash network payloads before exec so handler mutations of the input slice
	// do not alter the logged wire digest.
	var wire uint64
	if e.kind.network() {
		wire = fnv64a(e.payload)
	}
	disp := s.exec(e)
	s.record(e, disp, wire)
	if !s.dropInvariants {
		for _, inv := range s.invariants {
			if s.err != nil {
				break
			}
			if s.steps%inv.every == 0 {
				s.runInvariant(inv)
			}
		}
	}
	return true, s.err
}

// RunUntil executes all events with timestamp <= t (clamped to Horizon) and
// advances Now() to t if no error stops the loop earlier.
func (s *Sim) RunUntil(t env.Time) error {
	if t > Horizon {
		t = Horizon
	}
	for s.err == nil {
		next := s.q.peek()
		if next == nil || next.at > t {
			if t > s.now {
				s.now = t
			}
			return nil
		}
		if _, err := s.Step(); err != nil {
			return err
		}
	}
	return s.err
}

// RunFor advances the simulation by d of global virtual time (saturating at Horizon).
func (s *Sim) RunFor(d time.Duration) error { return s.RunUntil(later(s.now, d)) }

// RunUntilQuiet executes events until the queue is empty or an error occurs.
func (s *Sim) RunUntilQuiet() error {
	for s.err == nil {
		ok, err := s.Step()
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}
	return s.err
}

// RunUntilConverged steps the loop until converged() returns true. It stalls
// with ErrQueueEmpty if no events remain or ErrDeadline if the next event falls
// after deadline (clamped to Horizon).
func (s *Sim) RunUntilConverged(deadline env.Time, converged func() bool) error {
	if deadline > Horizon {
		deadline = Horizon
	}
	for s.err == nil {
		done := false
		s.guard(-1, "convergence check", func() { done = converged() })
		if s.err != nil {
			return s.err
		}
		if done {
			return nil
		}
		next := s.q.peek()
		if next == nil {
			return s.stall(ErrQueueEmpty)
		}
		if next.at > deadline {
			if deadline > s.now {
				s.now = deadline
			}
			return s.stall(ErrDeadline)
		}
		if _, err := s.Step(); err != nil {
			return err
		}
	}
	return s.err
}

func (s *Sim) exec(e *event) disposition {
	switch e.kind {
	case kindAction:
		s.guard(-1, "action "+e.label, e.fn)
		return dispRan
	case kindFault:
		disp := dispFailed
		if e.nemesis != nil {
			s.guard(-1, "nemesis "+e.label, func() { disp = e.nemesis.fire(e) })
			return disp
		}
		s.guard(-1, "fault "+e.fault.Kind.String(), func() {
			if s.Apply(*e.fault) == nil {
				disp = dispRan
			}
		})
		return disp
	}
	n := s.nodes[e.node]
	// Preserve FIFO order at a release instant: events released from a paused
	// or starting node drain before link-released events, and both drain before
	// fresh arrivals scheduled at the same timestamp.
	if e.released {
		if e.relLink && n.relHeld > 0 {
			s.schedule(e)
			return dispRequeued
		}
		e.released = false
		if e.relLink {
			s.links[e.peer][e.node].relHeld--
		} else {
			n.relHeld--
		}
	} else if e.kind.stream() && e.peer >= 0 && s.links[e.peer][e.node].relHeld > 0 {
		s.schedule(e)
		return dispRequeued
	} else if n.relHeld > 0 {
		s.schedule(e)
		return dispRequeued
	}
	if e.dead {
		return dispStale
	}
	if e.inc != 0 && (e.inc != n.inc || !n.up) {
		return dispStale
	}
	// Once an event has crossed its link (e.arrived), it is never re-clogged if
	// it was later held by a paused or starting node.
	if !e.arrived && e.peer >= 0 && e.kind.network() {
		l := &s.links[e.peer][e.node]
		if l.clogged {
			l.held = append(l.held, e)
			return dispClogged
		}
	}
	e.arrived = true
	if !n.up {
		if e.kind == kindRequest {
			s.refuse(e)
			return dispDown
		}
		return dispDown
	}
	if n.paused || (!n.started && e.kind != kindStart) {
		n.held = append(n.held, e)
		return dispHeld
	}
	switch e.kind {
	case kindStart:
		s.start(n)
	case kindDatagram:
		from := s.nodes[e.peer].id
		s.guard(n.idx, "datagram", func() { n.proc.OnDatagram(from, e.payload) })
	case kindRequest:
		s.serve(n, e)
	case kindResponse, kindRefused, kindTimeout:
		return s.complete(n, e)
	case kindTimer:
		t := e.timer
		t.ev = nil
		s.guard(n.idx, "timer", t.fn)
	case kindLocal:
		s.guard(n.idx, "callback", e.fn)
	}
	return dispRan
}

func (k evKind) network() bool {
	return k == kindDatagram || k == kindRequest || k == kindResponse || k == kindRefused
}

func (k evKind) stream() bool {
	return k == kindRequest || k == kindResponse || k == kindRefused
}

func (s *Sim) start(n *node) {
	var p env.Process
	s.guard(n.idx, "factory", func() {
		p = n.factory(env.Env{ID: n.id, Region: n.region, Clock: n.handle, Rand: n.rng, Net: n.handle, Disk: n.handle})
	})
	if s.err != nil {
		return
	}
	if p == nil {
		s.err = &PanicError{Seed: s.cfg.Seed, Node: n.id, At: s.now, Event: "factory", Value: "factory returned a nil process"}
		return
	}
	n.proc = p
	n.started = true
	s.guard(n.idx, "start", p.Start)
	s.releaseNode(n)
}

// releaseNode re-schedules events held while n was paused or starting in FIFO
// order and marks them so later arrivals wait behind them.
func (s *Sim) releaseNode(n *node) {
	held := n.held
	n.held = nil
	for _, e := range held {
		if e.dead {
			continue
		}
		e.released = true
		e.relLink = false
		n.relHeld++
		s.schedule(e)
	}
}
