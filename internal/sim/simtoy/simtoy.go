package simtoy

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

const (
	Blob             = "max"
	InitialLimit     = 1 << 40
	DefaultPeriod    = 500 * time.Millisecond
	DefaultFanout    = 2
	DefaultTimeout   = 200 * time.Millisecond
	ConvergeBound    = 30 * time.Second
	checksumSize     = 8
	encodedSize      = 8 + checksumSize
	datagramSize     = 8
	chaosPhases      = 3
	chaosLength      = 5 * time.Second
	healLength       = 3 * time.Second
	chaosMaxGap      = 150 * time.Millisecond
	writesPerNode    = 3
	chaosStart       = env.Time(time.Second)
	invariantPerNode = 1
)

type Config struct {
	Peers       []env.NodeID
	Period      time.Duration
	Fanout      int
	PullTimeout time.Duration
}

type Node struct {
	e        env.Env
	cfg      Config
	value    uint64
	initial  uint64
	diskErrs int
	netErrs  int
	pulls    int
	writes   int
}

func Factory(cfg Config) sim.Factory {
	if cfg.Period <= 0 {
		cfg.Period = DefaultPeriod
	}
	if cfg.Fanout <= 0 {
		cfg.Fanout = DefaultFanout
	}
	if cfg.PullTimeout <= 0 {
		cfg.PullTimeout = DefaultTimeout
	}
	cfg.Peers = append([]env.NodeID(nil), cfg.Peers...)
	return func(e env.Env) env.Process { return &Node{e: e, cfg: cfg} }
}

func (n *Node) Value() uint64 { return n.value }

func (n *Node) Initial() uint64 { return n.initial }

func checksum(v uint64) uint64 {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	h := uint64(14695981039346656037)
	for _, c := range b {
		h ^= uint64(c)
		h *= 1099511628211
	}
	return h
}

func (n *Node) load() (uint64, bool) {
	b, err := n.e.Disk.Read(Blob)
	if err != nil || len(b) != encodedSize {
		return 0, false
	}
	v := binary.BigEndian.Uint64(b)
	if binary.BigEndian.Uint64(b[8:]) != checksum(v) {
		return 0, false
	}
	return v, true
}

func (n *Node) persist() {
	var b [encodedSize]byte
	binary.BigEndian.PutUint64(b[:], n.value)
	binary.BigEndian.PutUint64(b[8:], checksum(n.value))
	if err := n.e.Disk.Write(Blob, b[:]); err != nil {
		n.diskErrs++
		return
	}
	if err := n.e.Disk.Sync(Blob); err != nil {
		n.diskErrs++
	}
}

func (n *Node) Start() {
	v, ok := n.load()
	if !ok {
		v = n.e.Rand.Uint64N(InitialLimit)
	}
	n.value, n.initial = v, v
	n.persist()
	n.arm()
}

func (n *Node) arm() {
	d := n.cfg.Period/2 + time.Duration(n.e.Rand.Int64N(int64(n.cfg.Period)))
	n.e.Clock.AfterFunc(d, n.tick)
}

func (n *Node) others() []env.NodeID {
	out := make([]env.NodeID, 0, len(n.cfg.Peers))
	for _, p := range n.cfg.Peers {
		if p != n.e.ID {
			out = append(out, p)
		}
	}
	return out
}

func (n *Node) push() {
	others := n.others()
	if len(others) == 0 {
		return
	}
	var b [datagramSize]byte
	binary.BigEndian.PutUint64(b[:], n.value)
	for i := 0; i < n.cfg.Fanout; i++ {
		if err := n.e.Net.Send(others[n.e.Rand.IntN(len(others))], b[:]); err != nil {
			n.netErrs++
		}
	}
}

func (n *Node) tick() {
	n.push()
	others := n.others()
	if len(others) > 0 {
		n.pulls++
		n.e.Net.Request(others[n.e.Rand.IntN(len(others))], nil, n.cfg.PullTimeout, func(reply []byte, err error) {
			if err == nil && len(reply) == datagramSize {
				n.merge(binary.BigEndian.Uint64(reply))
			}
		})
	}
	n.arm()
}

func (n *Node) merge(v uint64) {
	if v <= n.value {
		return
	}
	n.value = v
	n.persist()
	n.push()
}

func (n *Node) Write(v uint64) {
	n.writes++
	n.merge(v)
}

func (n *Node) OnDatagram(from env.NodeID, msg []byte) {
	if len(msg) != datagramSize {
		return
	}
	n.merge(binary.BigEndian.Uint64(msg))
}

func (n *Node) OnRequest(from env.NodeID, req []byte, reply env.Reply) {
	var b [datagramSize]byte
	binary.BigEndian.PutUint64(b[:], n.value)
	reply(b[:], nil)
}

func (n *Node) EncodeState() []byte {
	out := binary.BigEndian.AppendUint64(nil, n.value)
	out = binary.BigEndian.AppendUint64(out, n.initial)
	out = binary.AppendUvarint(out, uint64(n.diskErrs))
	out = binary.AppendUvarint(out, uint64(n.netErrs))
	out = binary.AppendUvarint(out, uint64(n.writes))
	return binary.AppendUvarint(out, uint64(n.pulls))
}

type unwrapper interface{ Unwrap() env.Process }

func toyOf(p env.Process) *Node {
	for p != nil {
		if t, ok := p.(*Node); ok {
			return t
		}
		u, ok := p.(unwrapper)
		if !ok {
			return nil
		}
		p = u.Unwrap()
	}
	return nil
}

func live(s *sim.Sim) []*Node {
	var out []*Node
	for _, id := range s.Nodes() {
		if st := s.Status(id); st.Paused {
			continue
		}
		if t := toyOf(s.Process(id)); t != nil {
			out = append(out, t)
		}
	}
	return out
}

func Converged(s *sim.Sim) bool {
	nodes := live(s)
	if len(nodes) == 0 || len(nodes) != len(s.Nodes()) {
		return false
	}
	for _, t := range nodes[1:] {
		if t.value != nodes[0].value {
			return false
		}
	}
	return true
}

type seen struct {
	inc  uint32
	last uint64
}

func AddInvariants(s *sim.Sim, every int) {
	last := map[env.NodeID]seen{}
	s.AddInvariant("toy/bounded-monotonic", every, func() error {
		for _, id := range s.Nodes() {
			t := toyOf(s.Process(id))
			if t == nil {
				continue
			}
			if t.value >= InitialLimit || t.value < t.initial {
				return fmt.Errorf("node %s holds %d outside [%d, %d)", id, t.value, t.initial, uint64(InitialLimit))
			}
			inc := s.Status(id).Incarnation
			if prev, ok := last[id]; ok && prev.inc == inc && t.value < prev.last {
				return fmt.Errorf("node %s value went from %d to %d", id, prev.last, t.value)
			}
			last[id] = seen{inc: inc, last: t.value}
		}
		return nil
	})
}

type ChaosOptions struct {
	Nodes     int
	Wrap      func(e env.Env, p env.Process) env.Process
	Configure func(s *sim.Sim)
}

func ChaosBudget(n int) sim.Budget {
	return sim.Budget{MinLive: n/2 + 1, MaxDamagedDisks: n/4 + 1}
}

func ChaosRun(o ChaosOptions) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		s := sim.New(sim.Config{Seed: seed})
		if o.Configure != nil {
			o.Configure(s)
		}
		ids := sim.NodeIDs(o.Nodes)
		f := Factory(Config{Peers: ids})
		if o.Wrap != nil {
			inner := f
			f = func(e env.Env) env.Process { return o.Wrap(e, inner(e)) }
		}
		if err := s.AddCluster(ids, 3, f); err != nil {
			s.Fail(err)
			return s
		}
		AddInvariants(s, o.Nodes*invariantPerNode)
		writes := o.Nodes * writesPerNode
		window := time.Duration(chaosPhases) * (chaosLength + healLength)
		s.Workload(chaosStart, writes, window/time.Duration(writes), func(int) {
			r := s.WorkloadRand()
			id := ids[r.IntN(len(ids))]
			v := r.Uint64N(InitialLimit)
			if t := toyOf(s.Process(id)); t != nil && !s.Status(id).Paused {
				t.Write(v)
			}
		})
		nem, err := s.StartNemesis(sim.NemesisConfig{
			Start:  chaosStart,
			Phases: chaosPhases,
			Chaos:  chaosLength,
			Heal:   healLength,
			MaxGap: chaosMaxGap,
			Budget: ChaosBudget(o.Nodes),
		})
		if err != nil {
			s.Fail(err)
			return s
		}
		if err := s.RunUntil(nem.QuietAt()); err != nil {
			return s
		}
		_ = s.RunUntilConverged(nem.QuietAt().Add(ConvergeBound), func() bool { return Converged(s) })
		return s
	}
}

func Scenarios() []sim.Scenario {
	var out []sim.Scenario
	for _, n := range []int{5, 20, 50, 100} {
		out = append(out, sim.Scenario{Name: fmt.Sprintf("toy-chaos-%d", n), Run: ChaosRun(ChaosOptions{Nodes: n})})
	}
	return out
}
