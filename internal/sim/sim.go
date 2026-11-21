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

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

const (
	DefaultMaxSteps = 10_000_000
	DefaultLogCap   = 1 << 20
	MaxDriftPPM     = 100_000
	MaxSkew         = time.Duration(1 << 61)
	Horizon         = env.Time(1 << 62)
)

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

type Config struct {
	Seed     int64
	Net      NetConfig
	Codes    *env.CodeTable
	MaxSteps uint64
	LogCap   int
}

type NodeSpec struct {
	ID       env.NodeID
	Region   env.Region
	Skew     time.Duration
	DriftPPM int64
}

type Factory func(e env.Env) env.Process

type NodeStatus struct {
	Known       bool
	Up          bool
	Paused      bool
	Started     bool
	Incarnation uint32
}

type Scenario struct {
	Name string
	Run  func(seed int64) *Sim
}

type StallError struct {
	Seed   int64
	At     env.Time
	Steps  uint64
	Reason error
}

func (e *StallError) Error() string {
	return fmt.Sprintf("sim: seed %d: stall at t=%d after %d steps: %v", e.Seed, e.At, e.Steps, e.Reason)
}

func (e *StallError) Unwrap() []error { return []error{ErrStall, e.Reason} }

type InvariantError struct {
	Seed  int64
	Name  string
	At    env.Time
	Steps uint64
	Err   error
}

func (e *InvariantError) Error() string {
	return fmt.Sprintf("sim: seed %d: invariant %q violated at t=%d after %d steps: %v", e.Seed, e.Name, e.At, e.Steps, e.Err)
}

func (e *InvariantError) Unwrap() []error { return []error{ErrInvariant, e.Err} }

type PanicError struct {
	Seed  int64
	Node  env.NodeID
	At    env.Time
	Event string
	Value any
	Stack []byte
}

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

func (s *Sim) Seed() int64 { return s.cfg.Seed }

func (s *Sim) Now() env.Time { return s.now }

func (s *Sim) Steps() uint64 { return s.steps }

func (s *Sim) Err() error { return s.err }

func (s *Sim) Fail(err error) {
	if s.err == nil && err != nil {
		s.err = err
	}
}

func (s *Sim) WorkloadRand() *rand.Rand { return s.workRand }

func NodeIDs(n int) []env.NodeID {
	ids := make([]env.NodeID, n)
	for i := range ids {
		ids[i] = env.NodeID(fmt.Sprintf("n%03d", i))
	}
	return ids
}

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

func (s *Sim) AddNode(spec NodeSpec, f Factory) error {
	if spec.ID == "" {
		return fmt.Errorf("sim: add node: empty id: %w", env.ErrInvalidArgument)
	}
	if f == nil {
		return fmt.Errorf("sim: add node %s: nil factory: %w", spec.ID, env.ErrInvalidArgument)
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

func (s *Sim) Nodes() []env.NodeID {
	ids := make([]env.NodeID, len(s.nodes))
	for i, n := range s.nodes {
		ids[i] = n.id
	}
	return ids
}

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

func (s *Sim) Region(id env.NodeID) env.Region {
	n, err := s.node(id)
	if err != nil {
		return ""
	}
	return n.region
}

func (s *Sim) Process(id env.NodeID) env.Process {
	n, err := s.node(id)
	if err != nil || !n.up || !n.started {
		return nil
	}
	return n.proc
}

func (s *Sim) Status(id env.NodeID) NodeStatus {
	n, err := s.node(id)
	if err != nil {
		return NodeStatus{}
	}
	return NodeStatus{Known: true, Up: n.up, Paused: n.paused, Started: n.started, Incarnation: n.inc}
}

func (s *Sim) Disk(id env.NodeID) *Disk {
	n, err := s.node(id)
	if err != nil {
		return nil
	}
	return n.disk
}

func (s *Sim) LiveCount() int {
	c := 0
	for _, n := range s.nodes {
		if n.up && !n.paused {
			c++
		}
	}
	return c
}

func (s *Sim) LocalTime(id env.NodeID) env.Time {
	n, err := s.node(id)
	if err != nil {
		return s.now
	}
	return n.local(s.now)
}

func (s *Sim) schedule(e *event) *event {
	if e.at < s.now {
		e.at = s.now
	}
	s.seq++
	e.seq = s.seq
	s.q.push(e)
	return e
}

func (s *Sim) At(t env.Time, name string, fn func()) {
	s.schedule(&event{at: t, kind: kindAction, node: -1, peer: -1, label: name, fn: fn})
}

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
				s.At(s.now+env.Time(gap), "workload", step(i+1))
			}
		}
	}
	s.At(start, "workload", step(0))
}

func (s *Sim) AddInvariant(name string, every int, fn func() error) {
	if every < 1 {
		every = 1
	}
	s.invariants = append(s.invariants, &invariant{name: name, every: uint64(every), fn: fn, addedAt: s.steps})
}

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

func (s *Sim) CheckInvariants() error {
	for _, inv := range s.invariants {
		if s.err != nil {
			break
		}
		s.runInvariant(inv)
	}
	return s.err
}

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

func (s *Sim) RunUntil(t env.Time) error {
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

func (s *Sim) RunFor(d time.Duration) error { return s.RunUntil(s.now.Add(d)) }

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

func (s *Sim) RunUntilConverged(deadline env.Time, converged func() bool) error {
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
		if e.nemesis != nil {
			return e.nemesis.fire(e)
		}
		if err := s.Apply(*e.fault); err != nil {
			return dispFailed
		}
		return dispRan
	}
	n := s.nodes[e.node]
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
