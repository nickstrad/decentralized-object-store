package sagasim

import (
	"bytes"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

const (
	Bucket         = "bk"
	clientRetry    = 200 * time.Millisecond
	clientMaxTries = 15
	invariantEvery = 20
	defaultObjects = 8
)

type Options struct {
	Coordinators []sim.NodeSpec
	Stores       []sim.NodeSpec
	Client       *sim.NodeSpec
	Source       env.NodeID
	Objects      int
	Policy       saga.Policy
	Wrap         func(id env.NodeID, d saga.Definition) saga.Definition
	Configure    func(id env.NodeID, cfg *saga.Config)
}

type Model struct {
	s         *sim.Sim
	o         Options
	stores    []env.NodeID
	initial   []KV
	attempted []KV
	acked     []KV
	calls     map[env.NodeID][]int
}

type Coordinator struct {
	e   env.Env
	eng *saga.Engine
	err error
}

func (c *Coordinator) Start()                        {}
func (c *Coordinator) OnDatagram(env.NodeID, []byte) {}
func (c *Coordinator) OnRequest(_ env.NodeID, _ []byte, reply env.Reply) {
	reply(nil, fmt.Errorf("sagasim: coordinator %s serves no requests: %w", c.e.ID, env.ErrInvalidArgument))
}

func (c *Coordinator) EncodeState() []byte {
	if c.eng == nil {
		return []byte{0}
	}
	return c.eng.EncodeState()
}

func (c *Coordinator) Engine() *saga.Engine { return c.eng }

func (c *Coordinator) Err() error { return c.err }

type coordProcess struct {
	*Coordinator
	start func()
}

func (p *coordProcess) Start() { p.start() }

type countingStep struct {
	saga.Step
	n []int
	i int
}

func (c *countingStep) Do(call saga.Call, done func([]byte, error)) {
	c.n[c.i]++
	c.Step.Do(call, done)
}

func objects(n int) []KV {
	out := make([]KV, n)
	for i := range out {
		out[i] = KV{Key: fmt.Sprintf("o%03d", i), Val: []byte(strconv.Itoa(i * 7919))}
	}
	return out
}

func Build(seed int64, o Options) (*sim.Sim, *Model, error) {
	table, err := env.NewCodeTable(Codes()...)
	if err != nil {
		return sim.New(sim.Config{Seed: seed}), nil, err
	}
	s := sim.New(sim.Config{Seed: seed, Codes: table})
	if o.Objects <= 0 {
		o.Objects = defaultObjects
	}
	m := &Model{s: s, o: o, initial: objects(o.Objects), calls: map[env.NodeID][]int{}}
	for _, sp := range o.Stores {
		m.stores = append(m.stores, sp.ID)
	}
	seeds := []BucketSeed{{Name: Bucket, Loc: o.Source, Objs: m.initial}}
	for _, sp := range o.Stores {
		if err := s.AddNode(sp, NewStore(seeds)); err != nil {
			return s, m, err
		}
	}
	for _, sp := range o.Coordinators {
		id := sp.ID
		m.calls[id] = make([]int, saga.MigrationSteps)
		counts := m.calls[id]
		if err := s.AddNode(sp, func(e env.Env) env.Process {
			c := &Coordinator{e: e}
			return &coordProcess{Coordinator: c, start: func() {
				def := saga.Migration(NewOps(e, m.stores))
				for i, st := range def.Steps {
					def.Steps[i] = &countingStep{Step: st, n: counts, i: i}
				}
				if o.Wrap != nil {
					def = o.Wrap(id, def)
				}
				cfg := saga.Config{Policy: o.Policy}
				if o.Configure != nil {
					o.Configure(id, &cfg)
				}
				c.eng, c.err = saga.New(e, cfg, def)
				if c.err == nil {
					c.err = c.eng.Recover()
				}
			}}
		}); err != nil {
			return s, m, err
		}
	}
	if o.Client != nil {
		if err := s.AddNode(*o.Client, func(e env.Env) env.Process { return &Client{e: e, m: m} }); err != nil {
			return s, m, err
		}
	}
	return s, m, nil
}

func (m *Model) coordinator(id env.NodeID) *Coordinator {
	p := m.s.Process(id)
	if p == nil || m.s.Status(id).Paused {
		return nil
	}
	cp, ok := p.(*coordProcess)
	if !ok {
		return nil
	}
	return cp.Coordinator
}

func (m *Model) Engine(id env.NodeID) *saga.Engine {
	if c := m.coordinator(id); c != nil {
		return c.eng
	}
	return nil
}

func (m *Model) Begin(coord env.NodeID, id saga.ID, in saga.MigrationInput) error {
	eng := m.Engine(coord)
	if eng == nil {
		return fmt.Errorf("sagasim: begin %s: coordinator %s is not running: %w", id, coord, env.ErrUnreachable)
	}
	return eng.Begin(id, saga.MigrationName, in.Encode())
}

func (m *Model) Status(coord env.NodeID, id saga.ID) (saga.Status, bool) {
	eng := m.Engine(coord)
	if eng == nil {
		return saga.Status{}, false
	}
	st, err := eng.Status(id)
	return st, err == nil
}

func (m *Model) Attempts(coord env.NodeID) []int { return slices.Clone(m.calls[coord]) }

func (m *Model) Stores() []env.NodeID { return slices.Clone(m.stores) }

func (m *Model) Acked() int { return len(m.acked) }

func (m *Model) client() *Client {
	if m.o.Client == nil {
		return nil
	}
	p := m.s.Process(m.o.Client.ID)
	if p == nil {
		return nil
	}
	c, _ := p.(*Client)
	return c
}

func (m *Model) Write(i int) {
	c := m.client()
	if c == nil || m.s.Status(m.o.Client.ID).Paused {
		return
	}
	kv := KV{Key: fmt.Sprintf("w%03d", i), Val: []byte("v" + strconv.Itoa(i))}
	m.attempted = append(m.attempted, kv)
	c.put(kv, 0)
}

func (m *Model) WritesIdle() bool {
	c := m.client()
	return m.o.Client == nil || c == nil || c.inflight == 0
}

type Client struct {
	e        env.Env
	m        *Model
	inflight int
	acked    int
	failed   int
}

func (c *Client) Start()                        {}
func (c *Client) OnDatagram(env.NodeID, []byte) {}
func (c *Client) OnRequest(_ env.NodeID, _ []byte, reply env.Reply) {
	reply(nil, env.ErrInvalidArgument)
}

func (c *Client) EncodeState() []byte {
	return []byte(fmt.Sprintf("%d/%d/%d", c.inflight, c.acked, c.failed))
}

func (c *Client) put(kv KV, tries int) {
	if tries == 0 {
		c.inflight++
	}
	stores := c.m.stores
	ask := stores[c.e.Rand.IntN(len(stores))]
	again := func() {
		if tries+1 >= clientMaxTries {
			c.inflight--
			c.failed++
			return
		}
		c.e.Clock.AfterFunc(clientRetry, func() { c.put(kv, tries+1) })
	}
	c.e.Net.Request(ask, encodeFields(opPointer, []byte(Bucket)), RPCTimeout, func(resp []byte, err error) {
		if err != nil {
			again()
			return
		}
		_, f, err := decodeFields(resp)
		if err != nil || len(f) != 3 {
			again()
			return
		}
		c.e.Net.Request(env.NodeID(f[0]), encodeFields(opPut, []byte(Bucket), []byte(kv.Key), kv.Val), RPCTimeout, func(_ []byte, err error) {
			if err != nil {
				again()
				return
			}
			c.inflight--
			c.acked++
			c.m.acked = append(c.m.acked, kv)
		})
	})
}

func (m *Model) view(loc string) (storeState, bool) {
	b, ok := m.s.Disk(env.NodeID(loc)).PeekDurable(stateBlob)
	if !ok {
		return storeState{}, false
	}
	st, err := decodeState(b)
	return st, err == nil
}

func (m *Model) Pointers(bucket string) []string {
	var out []string
	for _, id := range m.stores {
		st, _ := m.view(string(id))
		p, _ := st.pointer(bucket)
		out = append(out, p.Loc)
	}
	return out
}

func (m *Model) Pointer(bucket string) string {
	ps := m.Pointers(bucket)
	for _, p := range ps {
		if p != ps[0] {
			return ""
		}
	}
	if len(ps) == 0 {
		return ""
	}
	return ps[0]
}

func checkf(format string, args ...any) error {
	return fmt.Errorf("sagasim: "+format+": %w", append(args, ErrCheck)...)
}

func (m *Model) checkPointer(bucket, want string) error {
	for i, p := range m.Pointers(bucket) {
		if p != want {
			return checkf("pointer at %s is %q, want %q", m.stores[i], p, want)
		}
	}
	return nil
}

func (m *Model) checkHolder(loc, bucket string, writes bool) error {
	st, ok := m.view(loc)
	if !ok {
		return checkf("%s has no readable state", loc)
	}
	bk := st.bucket(bucket)
	if bk == nil || bk.Status != statusLive {
		return checkf("%s does not hold %s live", loc, bucket)
	}
	if len(bk.Regs) != 0 {
		return checkf("%s still has registrations %v", loc, bk.Regs)
	}
	if !writes {
		if !bytes.Equal(Root(bk.Objs), Root(m.initial)) {
			return checkf("%s root differs from the original", loc)
		}
		return nil
	}
	for _, kv := range m.initial {
		if !hasKV(bk.Objs, kv) {
			return checkf("%s lost original object %s", loc, kv.Key)
		}
	}
	return nil
}

func hasKV(objs []KV, kv KV) bool {
	for _, o := range objs {
		if o.Key == kv.Key && bytes.Equal(o.Val, kv.Val) {
			return true
		}
	}
	return false
}

func (m *Model) checkEmptyExcept(bucket string, keep string) error {
	for _, id := range m.stores {
		if string(id) == keep {
			continue
		}
		st, ok := m.view(string(id))
		if !ok {
			return checkf("%s has no readable state", id)
		}
		bk := st.bucket(bucket)
		if bk == nil {
			continue
		}
		if bk.Status != statusRetired || len(bk.Objs) != 0 || len(bk.Regs) != 0 {
			return checkf("%s holds %d orphan objects of %s (status %d, owner %q, regs %v)", id, len(bk.Objs), bucket, bk.Status, bk.Owner, bk.Regs)
		}
	}
	return nil
}

func (m *Model) CheckAt(bucket, loc string) error {
	if err := m.checkPointer(bucket, loc); err != nil {
		return err
	}
	if err := m.checkHolder(loc, bucket, m.o.Client != nil); err != nil {
		return err
	}
	return m.checkEmptyExcept(bucket, loc)
}

func (m *Model) CheckMoved(in saga.MigrationInput) error { return m.CheckAt(in.Bucket, in.To) }

func (m *Model) CheckCompensated(in saga.MigrationInput) error { return m.CheckAt(in.Bucket, in.From) }

func (m *Model) CheckWrites(bucket, loc string) error {
	st, _ := m.view(loc)
	bk := st.bucket(bucket)
	if bk == nil {
		return checkf("%s does not hold %s", loc, bucket)
	}
	for _, kv := range m.acked {
		if !hasKV(bk.Objs, kv) {
			return checkf("acknowledged write %s lost at %s", kv.Key, loc)
		}
	}
	for _, o := range bk.Objs {
		if !hasKV(m.initial, o) && !hasKV(m.attempted, o) {
			return checkf("%s holds %s, which nobody wrote", loc, o.Key)
		}
	}
	return nil
}

func (m *Model) CheckDuel(ins []saga.MigrationInput, states []saga.State, outcomes []string) error {
	if len(ins) == 0 {
		return checkf("no migrations")
	}
	p := m.Pointer(ins[0].Bucket)
	if p == "" {
		return checkf("pointer replicas disagree: %v", m.Pointers(ins[0].Bucket))
	}
	if err := m.checkHolder(p, ins[0].Bucket, false); err != nil {
		return err
	}
	if err := m.checkEmptyExcept(ins[0].Bucket, p); err != nil {
		return err
	}
	for i, in := range ins {
		switch {
		case in.To == p && (states[i] != saga.StateCompleted || outcomes[i] != saga.OutcomeMoved):
			return checkf("migration %d to the winner %s ended %v %q", i, p, states[i], outcomes[i])
		case in.To != p && states[i] == saga.StateCompleted && outcomes[i] != saga.OutcomeSuperseded:
			return checkf("migration %d to %s ended %q but the pointer is %s", i, in.To, outcomes[i], p)
		case !states[i].Terminal():
			return checkf("migration %d did not finish: %v", i, states[i])
		}
	}
	return nil
}

func (m *Model) AddInvariants(s *sim.Sim) {
	s.AddInvariant("sagasim/one-live-location", invariantEvery, func() error {
		live := 0
		for _, id := range m.stores {
			st, ok := m.view(string(id))
			if !ok {
				continue
			}
			if bk := st.bucket(Bucket); bk != nil && bk.Status == statusLive {
				live++
			}
		}
		if live > 1 {
			return fmt.Errorf("%d stores hold %s live", live, Bucket)
		}
		return nil
	})
}
