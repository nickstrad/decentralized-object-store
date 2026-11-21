package sim

import (
	"fmt"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type Budget struct {
	MinLive            int
	MaxDamagedDisks    int
	MaxDamagedPerGroup int
	GroupOf            func(node env.NodeID, blob string) string
	Lifted             bool
}

type damageRef struct {
	node int
	blob string
}

func (b Budget) damage(s *Sim, extra []damageRef, extraNode int) error {
	nodes := 0
	groups := map[string]int{}
	for _, n := range s.nodes {
		names := n.disk.Damaged()
		faulty := n.disk.faulty() || n.idx == extraNode
		for _, x := range extra {
			if x.node == n.idx {
				faulty = true
				if !n.disk.damaged[x.blob] {
					names = append(names, x.blob)
				}
			}
		}
		if faulty {
			nodes++
		}
		if b.GroupOf == nil {
			continue
		}
		for _, name := range names {
			g := b.GroupOf(n.id, name)
			groups[g]++
			if groups[g] > b.MaxDamagedPerGroup {
				return fmt.Errorf("%w: group %q has %d damaged blobs, limit %d", ErrBudget, g, groups[g], b.MaxDamagedPerGroup)
			}
		}
	}
	if nodes > b.MaxDamagedDisks {
		return fmt.Errorf("%w: %d damaged disks, limit %d", ErrBudget, nodes, b.MaxDamagedDisks)
	}
	return nil
}

func (b Budget) Check(s *Sim) error {
	if b.Lifted {
		return nil
	}
	if live := s.LiveCount(); live < b.MinLive {
		return fmt.Errorf("%w: %d live nodes, minimum %d", ErrBudget, live, b.MinLive)
	}
	return b.damage(s, nil, -1)
}

func (b Budget) Allows(s *Sim, f Fault) bool {
	if b.Lifted {
		return true
	}
	n, err := s.node(f.Node)
	switch f.Kind {
	case FaultCrash, FaultCrashTorn, FaultPause, FaultWipeRejoin:
		if err == nil && n.up && !n.paused && s.LiveCount()-1 < b.MinLive {
			return false
		}
	}
	if err != nil {
		return b.Check(s) == nil
	}
	var extra []damageRef
	extraNode := -1
	switch f.Kind {
	case FaultBitRot:
		extra = append(extra, damageRef{n.idx, f.Blob})
	case FaultWipeDisk, FaultWipeRejoin:
		extraNode = n.idx
		for _, name := range sortedKeys(n.disk.blobs) {
			extra = append(extra, damageRef{n.idx, name})
		}
	case FaultReadError:
		extraNode = n.idx
	}
	return b.damage(s, extra, extraNode) == nil
}

type NemesisConfig struct {
	Start  env.Time
	Phases int
	Chaos  time.Duration
	Heal   time.Duration
	MaxGap time.Duration
	Kinds  []FaultKind
	Budget Budget
}

type NemesisOutcome uint8

const (
	OutcomeApplied NemesisOutcome = iota + 1
	OutcomeRejected
	OutcomeSkipped
)

func (o NemesisOutcome) String() string {
	switch o {
	case OutcomeApplied:
		return "applied"
	case OutcomeRejected:
		return "rejected"
	case OutcomeSkipped:
		return "skipped"
	}
	return "?"
}

type NemesisStep struct {
	At      env.Time
	Fault   Fault
	Outcome NemesisOutcome
}

type Nemesis struct {
	s     *Sim
	cfg   NemesisConfig
	hist  []NemesisStep
	quiet env.Time
}

func (s *Sim) StartNemesis(cfg NemesisConfig) (*Nemesis, error) {
	if cfg.Phases < 1 || cfg.Chaos <= 0 || cfg.Heal < 0 || cfg.MaxGap <= 0 {
		return nil, fmt.Errorf("sim: nemesis: phases %d chaos %v heal %v gap %v: %w", cfg.Phases, cfg.Chaos, cfg.Heal, cfg.MaxGap, env.ErrInvalidArgument)
	}
	if len(cfg.Kinds) == 0 {
		cfg.Kinds = ChaosKinds()
	}
	for _, k := range cfg.Kinds {
		if !k.valid() {
			return nil, fmt.Errorf("sim: nemesis: fault kind %d: %w", k, env.ErrInvalidArgument)
		}
	}
	cfg.Kinds = append([]FaultKind(nil), cfg.Kinds...)
	n := &Nemesis{s: s, cfg: cfg}
	period := cfg.Chaos + cfg.Heal
	for i := 0; i < cfg.Phases; i++ {
		begin := cfg.Start.Add(time.Duration(i) * period)
		end := begin.Add(cfg.Chaos)
		s.schedule(&event{at: begin, kind: KindFault, node: -1, peer: -1, nemesis: n, label: "chaos", fault: &Fault{}, until: end})
		s.schedule(&event{at: end, kind: KindFault, node: -1, peer: -1, nemesis: n, label: "heal", fault: &Fault{Kind: FaultHealAll}})
		n.quiet = end
	}
	return n, nil
}

func (n *Nemesis) QuietAt() env.Time { return n.quiet }

func (n *Nemesis) History() []NemesisStep {
	return append([]NemesisStep(nil), n.hist...)
}

func (n *Nemesis) fire(e *event) Disposition {
	s := n.s
	if e.label == "heal" {
		s.HealAll()
		n.hist = append(n.hist, NemesisStep{At: s.now, Fault: *e.fault, Outcome: OutcomeApplied})
		return DispRan
	}
	end := e.until
	f, ok := n.draw()
	*e.fault = f
	var out NemesisOutcome
	switch {
	case !ok:
		out = OutcomeSkipped
	case !n.cfg.Budget.Allows(s, f):
		out = OutcomeRejected
	case s.Apply(f) != nil:
		out = OutcomeSkipped
	default:
		out = OutcomeApplied
	}
	n.hist = append(n.hist, NemesisStep{At: s.now, Fault: f, Outcome: out})
	next := s.now.Add(time.Duration(1 + s.nemRand.Int64N(int64(n.cfg.MaxGap))))
	if next < end {
		s.schedule(&event{at: next, kind: KindFault, node: -1, peer: -1, nemesis: n, label: "chaos", fault: &Fault{}, until: end})
	}
	switch out {
	case OutcomeApplied:
		return DispRan
	case OutcomeRejected:
		return DispRejected
	}
	return DispSkipped
}

func (n *Nemesis) pick(idx []int) (int, bool) {
	if len(idx) == 0 {
		return 0, false
	}
	return idx[n.s.nemRand.IntN(len(idx))], true
}

func (n *Nemesis) nodesWhere(pred func(*node) bool) []int {
	var out []int
	for _, nd := range n.s.nodes {
		if pred(nd) {
			out = append(out, nd.idx)
		}
	}
	return out
}

func (n *Nemesis) subset() []env.NodeID {
	s := n.s
	total := len(s.nodes)
	if total < 2 {
		return nil
	}
	k := 1 + s.nemRand.IntN(total/2)
	perm := make([]int, total)
	for i := range perm {
		perm[i] = i
	}
	for i := 0; i < k; i++ {
		j := i + s.nemRand.IntN(total-i)
		perm[i], perm[j] = perm[j], perm[i]
	}
	out := make([]env.NodeID, k)
	for i := 0; i < k; i++ {
		out[i] = s.nodes[perm[i]].id
	}
	return out
}

func (n *Nemesis) pair() (env.NodeID, env.NodeID, bool) {
	s := n.s
	if len(s.nodes) < 2 {
		return "", "", false
	}
	a := s.nemRand.IntN(len(s.nodes))
	b := s.nemRand.IntN(len(s.nodes) - 1)
	if b >= a {
		b++
	}
	return s.nodes[a].id, s.nodes[b].id, true
}

func (n *Nemesis) draw() (Fault, bool) {
	s := n.s
	f := Fault{Kind: n.cfg.Kinds[s.nemRand.IntN(len(n.cfg.Kinds))]}
	any := func(*node) bool { return true }
	var target []int
	switch f.Kind {
	case FaultCrash, FaultCrashTorn, FaultPause, FaultWipeRejoin:
		target = n.nodesWhere(func(nd *node) bool { return nd.up && !nd.paused })
	case FaultRestart:
		target = n.nodesWhere(func(nd *node) bool { return !nd.up })
	case FaultResume:
		target = n.nodesWhere(func(nd *node) bool { return nd.up && nd.paused })
	case FaultIsolate, FaultWipeDisk, FaultReadError, FaultBitRot:
		target = n.nodesWhere(any)
	case FaultPartition, FaultPartitionOneWay:
		f.Side = n.subset()
		return f, len(f.Side) > 0
	case FaultPartitionRegion:
		regions := s.Regions()
		if len(regions) < 2 {
			return f, false
		}
		f.Region = regions[s.nemRand.IntN(len(regions))]
		return f, true
	case FaultLinkLoss, FaultClog:
		a, b, ok := n.pair()
		f.Node, f.Peer = a, b
		if f.Kind == FaultLinkLoss {
			f.PPM = 1 + s.nemRand.Uint32N(PPM)
		}
		return f, ok
	case FaultUnclog:
		cl := s.clogged()
		if len(cl) == 0 {
			return f, false
		}
		l := cl[s.nemRand.IntN(len(cl))]
		f.Node, f.Peer = s.nodes[l[0]].id, s.nodes[l[1]].id
		return f, true
	case FaultHealNet, FaultHealAll:
		return f, true
	}
	i, ok := n.pick(target)
	if !ok {
		return f, false
	}
	f.Node = s.nodes[i].id
	switch f.Kind {
	case FaultBitRot:
		targets := s.nodes[i].disk.rotTargets()
		if len(targets) == 0 {
			return f, false
		}
		f.Blob = targets[s.nemRand.IntN(len(targets))]
	case FaultReadError:
		f.Count = 1 + s.nemRand.IntN(3)
	}
	return f, true
}
