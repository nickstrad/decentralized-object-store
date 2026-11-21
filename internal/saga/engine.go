package saga

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

const (
	DefaultPrefix = "saga/"
	maxIDLen      = 128
	maxCause      = 200
)

type ID string

type State uint8

const (
	StateRunning State = iota + 1
	StateCompensating
	StateCompleted
	StateCompensated
)

func (s State) String() string {
	switch s {
	case StateRunning:
		return "running"
	case StateCompensating:
		return "compensating"
	case StateCompleted:
		return "completed"
	case StateCompensated:
		return "compensated"
	}
	return "state(" + strconv.Itoa(int(s)) + ")"
}

func (s State) Terminal() bool { return s == StateCompleted || s == StateCompensated }

type Key struct {
	Saga ID
	Step int
}

func (k Key) String() string { return string(k.Saga) + "#" + strconv.Itoa(k.Step) }

func (k Key) Bytes() []byte {
	b := binary.AppendUvarint(nil, uint64(len(k.Saga)))
	b = append(b, k.Saga...)
	return binary.AppendVarint(b, int64(k.Step))
}

type Call struct {
	Key     Key
	Attempt int
	Begun   env.Time
	Input   []byte
	Outputs [][]byte
}

type Step interface {
	Name() string
	Do(c Call, done func(out []byte, err error))
	Compensate(c Call, done func(err error))
}

type Definition struct {
	Name  string
	Steps []Step
	Pivot int
}

func (d Definition) Validate() error {
	if d.Name == "" || len(d.Steps) == 0 || d.Pivot < 0 || d.Pivot > len(d.Steps) {
		return fmt.Errorf("saga: definition %q: %d steps, pivot %d: %w", d.Name, len(d.Steps), d.Pivot, env.ErrInvalidArgument)
	}
	for i, s := range d.Steps {
		if s == nil {
			return fmt.Errorf("saga: definition %q: step %d is nil: %w", d.Name, i, env.ErrInvalidArgument)
		}
	}
	return nil
}

type Point struct {
	Saga    ID
	Kind    RecordKind
	Step    int
	Attempt int
}

type Config struct {
	Policy   Policy
	Prefix   string
	Hook     func(Point) bool
	OnEnd    func(Status)
	skipComp int
}

type Status struct {
	ID        ID
	Def       string
	State     State
	Step      int
	Attempt   int
	Outputs   [][]byte
	Cause     string
	Err       error
	Suspended bool
}

type StepStats struct {
	Def           string
	Step          string
	Attempts      int
	Failures      int
	Compensations int
	CompFailures  int
}

type run struct {
	id        ID
	def       int
	name      string
	snap      Snapshot
	busy      bool
	token     uint64
	timer     env.Timer
	err       error
	suspended bool
}

type Engine struct {
	e            env.Env
	cfg          Config
	defs         []Definition
	stats        [][]StepStats
	runs         map[ID]*run
	broken       map[ID]error
	pending      []ID
	retryTimer   env.Timer
	retryAttempt int
	token        uint64
	frozen       bool
}

func New(e env.Env, cfg Config, defs ...Definition) (*Engine, error) {
	if e.Disk == nil || e.Clock == nil {
		return nil, fmt.Errorf("saga: new engine: disk and clock are required: %w", env.ErrInvalidArgument)
	}
	if err := cfg.Policy.Validate(); err != nil {
		return nil, err
	}
	if cfg.Prefix == "" {
		cfg.Prefix = DefaultPrefix
	}
	g := &Engine{e: e, cfg: cfg, runs: map[ID]*run{}, broken: map[ID]error{}}
	for _, d := range defs {
		if err := d.Validate(); err != nil {
			return nil, err
		}
		g.defs = append(g.defs, Definition{Name: d.Name, Steps: slices.Clone(d.Steps), Pivot: d.Pivot})
	}
	slices.SortFunc(g.defs, func(a, b Definition) int { return strings.Compare(a.Name, b.Name) })
	for i := 1; i < len(g.defs); i++ {
		if g.defs[i].Name == g.defs[i-1].Name {
			return nil, fmt.Errorf("saga: definition %q registered twice: %w", g.defs[i].Name, env.ErrInvalidArgument)
		}
	}
	for _, d := range g.defs {
		row := make([]StepStats, len(d.Steps))
		for i, s := range d.Steps {
			row[i] = StepStats{Def: d.Name, Step: s.Name()}
		}
		g.stats = append(g.stats, row)
	}
	return g, nil
}

func (g *Engine) LogName(id ID) string { return g.cfg.Prefix + string(id) }

func (g *Engine) Frozen() bool { return g.frozen }

func (g *Engine) findDef(name string) int {
	for i, d := range g.defs {
		if d.Name == name {
			return i
		}
	}
	return -1
}

func (g *Engine) shape(r *run) shape {
	d := g.defs[r.def]
	return shape{steps: len(d.Steps), pivot: d.Pivot, maxAttempts: g.cfg.Policy.MaxAttempts}
}

func validID(id ID) error {
	if len(id) == 0 || len(id) > maxIDLen {
		return fmt.Errorf("saga: id of %d bytes: %w", len(id), env.ErrInvalidArgument)
	}
	for _, c := range []byte(id) {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return fmt.Errorf("saga: id %q: character %q: %w", id, c, env.ErrInvalidArgument)
		}
	}
	return nil
}

func (g *Engine) known(id ID) bool {
	_, r := g.runs[id]
	_, b := g.broken[id]
	return r || b || slices.Contains(g.pending, id)
}

func (g *Engine) Begin(id ID, def string, input []byte) error {
	if g.frozen {
		return fmt.Errorf("saga: begin %s: engine stopped: %w", id, env.ErrClosed)
	}
	if err := validID(id); err != nil {
		return err
	}
	di := g.findDef(def)
	if di < 0 {
		return fmt.Errorf("saga: begin %s: unknown definition %q: %w", id, def, env.ErrInvalidArgument)
	}
	if g.known(id) {
		return fmt.Errorf("saga: begin %s: id in use: %w", id, env.ErrInvalidArgument)
	}
	name := g.LogName(id)
	if _, err := g.e.Disk.Read(name); err == nil {
		return fmt.Errorf("saga: begin %s: a log already exists: %w", id, env.ErrInvalidArgument)
	} else if !errors.Is(err, env.ErrNotFound) {
		return fmt.Errorf("saga: begin %s: %w", id, err)
	}
	rec := Record{Kind: RecBegin, Def: def, Time: g.e.Clock.Now(), Data: bytes.Clone(input)}
	if _, err := AppendRecord(nil, rec); err != nil {
		return fmt.Errorf("saga: begin %s: %w", id, err)
	}
	r := &run{id: id, def: di, name: name}
	ok, err := g.write(r, rec)
	if err != nil {
		derr := g.e.Disk.Delete(name)
		if derr == nil {
			derr = g.e.Disk.Sync(name)
		}
		if errors.Is(derr, env.ErrNotFound) {
			derr = nil
		}
		return errors.Join(fmt.Errorf("saga: begin %s: %w", id, err), derr)
	}
	if !ok {
		return fmt.Errorf("saga: begin %s: engine stopped: %w", id, env.ErrClosed)
	}
	g.runs[id] = r
	g.drive(r)
	return nil
}

func (g *Engine) write(r *run, rec Record) (bool, error) {
	next := r.snap
	if err := next.apply(rec, g.shape(r)); err != nil {
		return false, err
	}
	b, err := AppendRecord(nil, rec)
	if err != nil {
		return false, err
	}
	if err := g.e.Disk.Append(r.name, b); err != nil {
		return false, err
	}
	if g.cfg.Hook != nil && g.cfg.Hook(Point{Saga: r.id, Kind: rec.Kind, Step: rec.Step, Attempt: rec.Attempt}) {
		g.freeze()
		return false, nil
	}
	if err := g.e.Disk.Sync(r.name); err != nil {
		return false, err
	}
	r.snap = next
	return true, nil
}

func (g *Engine) log(r *run, rec Record) bool {
	ok, err := g.write(r, rec)
	if err != nil {
		g.suspend(r, fmt.Errorf("saga %s: log %s: %w", r.id, rec.Kind, err))
	}
	return ok
}

func (g *Engine) suspend(r *run, err error) {
	r.suspended, r.err, r.busy = true, err, false
	if r.timer != nil {
		r.timer.Stop()
	}
}

func (g *Engine) freeze() {
	g.frozen = true
	for _, id := range g.sortedIDs() {
		if t := g.runs[id].timer; t != nil {
			t.Stop()
		}
	}
	if g.retryTimer != nil {
		g.retryTimer.Stop()
	}
}

func (g *Engine) sortedIDs() []ID {
	var ids []ID
	for id := range g.runs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (g *Engine) IDs() []ID { return g.sortedIDs() }

func cause(err error) []byte {
	msg := "attempts exhausted"
	if err != nil {
		msg = err.Error()
	}
	if len(msg) > maxCause {
		msg = msg[:maxCause]
	}
	return []byte(msg)
}

func (g *Engine) drive(r *run) {
	for !g.frozen && !r.suspended && !r.busy {
		sh := g.shape(r)
		r.snap.plan(sh)
		switch r.snap.Next {
		case ActEnd:
			if g.log(r, Record{Kind: RecEnd, State: r.snap.End}) && g.cfg.OnEnd != nil {
				g.cfg.OnEnd(g.status(r))
			}
			return
		case ActAbort:
			if !g.log(r, Record{Kind: RecAbort, Step: sh.abortFrom(r.snap.Step), Data: cause(r.err)}) {
				return
			}
		case ActDo:
			g.startDo(r)
			return
		case ActCompensate:
			g.startComp(r)
			return
		default:
			return
		}
	}
}

func (g *Engine) arm(r *run, onTimeout func()) uint64 {
	g.token++
	tok := g.token
	r.token, r.busy = tok, true
	r.timer = g.e.Clock.AfterFunc(g.cfg.Policy.StepTimeout, func() {
		if g.live(r, tok) {
			onTimeout()
		}
	})
	return tok
}

func (g *Engine) live(r *run, tok uint64) bool {
	return !g.frozen && !r.suspended && r.busy && r.token == tok
}

func (g *Engine) settle(r *run) {
	r.busy = false
	if r.timer != nil {
		r.timer.Stop()
	}
}

func (g *Engine) call(r *run, step, attempt int) Call {
	return Call{
		Key:     Key{Saga: r.id, Step: step},
		Attempt: attempt,
		Begun:   r.snap.Begun,
		Input:   bytes.Clone(r.snap.Input),
		Outputs: cloneOutputs(r.snap.Outputs),
	}
}

func timeoutErr(p Policy) error {
	return fmt.Errorf("saga: no answer within %v: %w", p.StepTimeout, env.ErrTimeout)
}

func (g *Engine) startDo(r *run) {
	step, attempt := r.snap.Step, r.snap.Attempt+1
	if !g.log(r, Record{Kind: RecIntent, Step: step, Attempt: attempt}) {
		return
	}
	g.stats[r.def][step].Attempts++
	var tok uint64
	tok = g.arm(r, func() { g.onDo(r, tok, step, attempt, nil, timeoutErr(g.cfg.Policy)) })
	c := g.call(r, step, attempt)
	g.defs[r.def].Steps[step].Do(c, func(out []byte, err error) { g.onDo(r, tok, step, attempt, out, err) })
}

func (g *Engine) onDo(r *run, tok uint64, step, attempt int, out []byte, err error) {
	if !g.live(r, tok) {
		return
	}
	g.settle(r)
	if err == nil {
		if g.log(r, Record{Kind: RecDone, Step: step, Attempt: attempt, Data: bytes.Clone(out)}) {
			r.err = nil
			g.drive(r)
		}
		return
	}
	sh := g.shape(r)
	se := &StepError{Saga: r.id, Step: g.defs[r.def].Steps[step].Name(), Attempt: attempt, Err: err}
	r.err = se
	g.stats[r.def][step].Failures++
	if errors.Is(err, ErrPermanent) && step <= sh.pivot {
		if g.log(r, Record{Kind: RecAbort, Step: sh.abortFrom(step), Data: cause(se)}) {
			g.drive(r)
		}
		return
	}
	if step < sh.pivot && attempt >= sh.maxAttempts {
		g.drive(r)
		return
	}
	g.wait(r, attempt)
}

func (g *Engine) wait(r *run, attempt int) {
	g.token++
	tok := g.token
	r.token, r.busy = tok, true
	r.timer = g.e.Clock.AfterFunc(g.cfg.Policy.Backoff(attempt, g.e.Rand), func() {
		if g.live(r, tok) {
			r.busy = false
			g.drive(r)
		}
	})
}

func (g *Engine) startComp(r *run) {
	step, attempt := r.snap.Step, r.snap.Attempt+1
	if !g.log(r, Record{Kind: RecCompIntent, Step: step, Attempt: attempt}) {
		return
	}
	g.stats[r.def][step].Compensations++
	var tok uint64
	tok = g.arm(r, func() { g.onComp(r, tok, step, attempt, timeoutErr(g.cfg.Policy)) })
	if g.cfg.skipComp == step+1 {
		g.onComp(r, tok, step, attempt, nil)
		return
	}
	c := g.call(r, step, attempt)
	g.defs[r.def].Steps[step].Compensate(c, func(err error) { g.onComp(r, tok, step, attempt, err) })
}

func (g *Engine) onComp(r *run, tok uint64, step, attempt int, err error) {
	if !g.live(r, tok) {
		return
	}
	g.settle(r)
	if err == nil {
		if g.log(r, Record{Kind: RecCompDone, Step: step, Attempt: attempt}) {
			g.drive(r)
		}
		return
	}
	r.err = &StepError{Saga: r.id, Step: g.defs[r.def].Steps[step].Name(), Attempt: attempt, Compensation: true, Err: err}
	g.stats[r.def][step].CompFailures++
	g.wait(r, attempt)
}

func (g *Engine) Recover() error {
	if g.frozen {
		return nil
	}
	names, err := g.e.Disk.List(g.cfg.Prefix)
	if err != nil {
		return fmt.Errorf("saga: recover: list %q: %w", g.cfg.Prefix, err)
	}
	var errs []error
	for _, name := range names {
		id := ID(strings.TrimPrefix(name, g.cfg.Prefix))
		if g.known(id) {
			continue
		}
		if err := g.load(id); err != nil {
			errs = append(errs, err)
		}
		if g.frozen {
			break
		}
	}
	return errors.Join(errs...)
}

func (g *Engine) fail(id ID, err error) error {
	err = fmt.Errorf("saga: recover %s: %w", id, err)
	g.broken[id] = err
	return err
}

func (g *Engine) load(id ID) error {
	if err := validID(id); err != nil {
		return g.fail(id, err)
	}
	name := g.LogName(id)
	b, err := g.e.Disk.Read(name)
	if errors.Is(err, env.ErrNotFound) {
		return nil
	}
	if err != nil {
		g.later(id)
		return fmt.Errorf("saga: recover %s: read: %w", id, err)
	}
	recs, valid, err := DecodeLog(b)
	if err != nil {
		return g.fail(id, err)
	}
	if len(recs) == 0 {
		err := g.e.Disk.Delete(name)
		if err == nil {
			err = g.e.Disk.Sync(name)
		}
		if err != nil {
			g.later(id)
			return fmt.Errorf("saga: recover %s: remove empty log: %w", id, err)
		}
		return nil
	}
	di := -1
	if recs[0].Kind == RecBegin {
		if di = g.findDef(recs[0].Def); di < 0 {
			return g.fail(id, fmt.Errorf("unknown definition %q: %w", recs[0].Def, env.ErrInvalidArgument))
		}
	}
	sh := shape{steps: 1, pivot: 1, maxAttempts: g.cfg.Policy.MaxAttempts}
	if di >= 0 {
		sh = shape{steps: len(g.defs[di].Steps), pivot: g.defs[di].Pivot, maxAttempts: g.cfg.Policy.MaxAttempts}
	}
	snap, err := Resume(recs, sh.steps, sh.pivot, sh.maxAttempts)
	if err != nil {
		return g.fail(id, err)
	}
	if valid < len(b) {
		err := g.e.Disk.Write(name, b[:valid])
		if err == nil {
			err = g.e.Disk.Sync(name)
		}
		if err != nil {
			g.later(id)
			return fmt.Errorf("saga: recover %s: truncate torn tail: %w", id, err)
		}
	}
	r := &run{id: id, def: di, name: name, snap: snap}
	g.runs[id] = r
	g.drive(r)
	return nil
}

func (g *Engine) later(id ID) {
	if !slices.Contains(g.pending, id) {
		g.pending = append(g.pending, id)
		slices.Sort(g.pending)
	}
	if g.retryTimer != nil && g.retryTimer.Stop() {
		g.retryTimer.Reset(g.cfg.Policy.Backoff(g.retryAttempt, g.e.Rand))
		return
	}
	g.retryAttempt++
	g.retryTimer = g.e.Clock.AfterFunc(g.cfg.Policy.Backoff(g.retryAttempt, g.e.Rand), g.retryPending)
}

func (g *Engine) retryPending() {
	if g.frozen {
		return
	}
	ids := g.pending
	g.pending = nil
	for _, id := range ids {
		if g.frozen {
			return
		}
		_ = g.load(id)
	}
	if len(g.pending) == 0 {
		g.retryAttempt = 0
	}
}

func cloneOutputs(outs [][]byte) [][]byte {
	if outs == nil {
		return nil
	}
	out := make([][]byte, len(outs))
	for i, o := range outs {
		out[i] = bytes.Clone(o)
	}
	return out
}

func (g *Engine) status(r *run) Status {
	out := make([][]byte, len(r.snap.Outputs))
	for i, o := range r.snap.Outputs {
		out[i] = bytes.Clone(o)
	}
	return Status{
		ID: r.id, Def: g.defs[r.def].Name, State: r.snap.State, Step: r.snap.Step, Attempt: r.snap.Attempt,
		Outputs: out, Cause: r.snap.Cause, Err: r.err, Suspended: r.suspended,
	}
}

func (g *Engine) Status(id ID) (Status, error) {
	if r, ok := g.runs[id]; ok {
		return g.status(r), nil
	}
	if err, ok := g.broken[id]; ok {
		return Status{ID: id}, err
	}
	if slices.Contains(g.pending, id) {
		return Status{ID: id}, fmt.Errorf("saga %s: log unreadable, recovery retrying: %w", id, ErrSagaNotFound)
	}
	return Status{ID: id}, fmt.Errorf("saga %s: %w", id, ErrSagaNotFound)
}

func (g *Engine) Stats() []StepStats {
	var out []StepStats
	for _, row := range g.stats {
		out = append(out, row...)
	}
	return out
}

func appendString(b []byte, s string) []byte {
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

func flag(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func (g *Engine) EncodeState() []byte {
	b := []byte{flag(g.frozen)}
	for _, id := range g.sortedIDs() {
		r := g.runs[id]
		b = appendString(b, string(id))
		b = append(b, byte(r.snap.State), flag(r.busy), flag(r.suspended), flag(r.snap.Open))
		b = binary.AppendVarint(b, int64(r.snap.Step))
		b = binary.AppendUvarint(b, uint64(r.snap.Attempt))
		b = binary.AppendUvarint(b, uint64(len(r.snap.Outputs)))
	}
	var broken []ID
	for id := range g.broken {
		broken = append(broken, id)
	}
	slices.Sort(broken)
	for _, id := range broken {
		b = appendString(b, "broken:"+string(id))
	}
	for _, id := range g.pending {
		b = appendString(b, "pending:"+string(id))
	}
	for _, s := range g.Stats() {
		b = binary.AppendUvarint(b, uint64(s.Attempts))
		b = binary.AppendUvarint(b, uint64(s.Failures))
		b = binary.AppendUvarint(b, uint64(s.Compensations))
		b = binary.AppendUvarint(b, uint64(s.CompFailures))
	}
	return b
}
