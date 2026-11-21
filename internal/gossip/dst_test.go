package gossip_test

import (
	"errors"
	"fmt"
	"math/bits"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/gossip"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
)

const (
	dstLogCap        = 1 << 12
	dstPoll          = 100 * time.Millisecond
	dstInvariantEach = 64

	convergeLossMinPPM        = 50_000
	convergeLossMaxPPM        = 100_000
	convergeQuiet             = env.Time(30 * time.Second)
	convergeC                 = 8
	maxMilliMsgsPerNodePeriod = 5_000

	asymRun = 300 * time.Second

	crashNodes   = 20
	crashLossPPM = 50_000
	crashAt      = env.Time(10 * time.Second)
	detectBound  = 40 * time.Second

	partitionNodes = 12
	partitionAt    = env.Time(10 * time.Second)
	splitBound     = 60 * time.Second
	healBound      = 60 * time.Second

	refuteNodes = 10
	pauseAt     = env.Time(10 * time.Second)
	pauseFor    = 8 * time.Second
	refuteRun   = 60 * time.Second

	restartNodes = 10
	rejoinBound  = 30 * time.Second
)

func ceilLog2(n int) int {
	if n <= 2 {
		return 1
	}
	return bits.Len(uint(n - 1))
}

type evKey struct {
	subject env.NodeID
	kind    gossip.EventKind
}

type recorder struct {
	s      *sim.Sim
	counts map[evKey]int
	dead   map[env.NodeID]map[env.NodeID]uint32
}

func (r *recorder) reset(observer env.NodeID) {
	r.dead[observer] = map[env.NodeID]uint32{}
}

func (r *recorder) observe(observer env.NodeID, ev gossip.Event) {
	r.counts[evKey{ev.Member.ID, ev.Kind}]++
	switch ev.Kind {
	case gossip.EventDied, gossip.EventLeft:
		r.dead[observer][ev.Member.ID] = ev.Member.Incarnation
	case gossip.EventJoined:
		if inc, ok := r.dead[observer][ev.Member.ID]; ok && ev.Member.Incarnation <= inc {
			r.s.Fail(fmt.Errorf("%s overturned its verdict on %s at incarnation %d without a higher incarnation (was %d)", observer, ev.Member.ID, ev.Member.Incarnation, inc))
		}
	}
}

func (r *recorder) count(subject env.NodeID, kind gossip.EventKind) int {
	return r.counts[evKey{subject, kind}]
}

func (r *recorder) total(kind gossip.EventKind, except ...env.NodeID) int {
	keys := make([]evKey, 0, len(r.counts))
	for k := range r.counts {
		keys = append(keys, k)
	}
	n := 0
	for _, k := range keys {
		if k.kind == kind && !slices.Contains(except, k.subject) {
			n += r.counts[k]
		}
	}
	return n
}

type cluster struct {
	s    *sim.Sim
	cfg  gossip.Config
	rec  *recorder
	left map[env.NodeID]bool
	ids  []env.NodeID
}

func newCluster(seed int64, cfg gossip.Config) *cluster {
	codes, err := gossip.CodeTable()
	s := sim.New(sim.Config{Seed: seed, Codes: codes, LogCap: dstLogCap})
	if err != nil {
		s.Fail(err)
	}
	c := &cluster{s: s, cfg: cfg, left: map[env.NodeID]bool{}}
	c.rec = &recorder{s: s, counts: map[evKey]int{}, dead: map[env.NodeID]map[env.NodeID]uint32{}}
	lastInc := map[env.NodeID][2]uint32{}
	s.AddInvariant("gossip/self-alive-and-monotonic", dstInvariantEach, func() error {
		for _, id := range s.Nodes() {
			n := gnode(s, id)
			if n == nil {
				continue
			}
			self := n.Self()
			if !n.Closed() && self.State != gossip.StateAlive {
				return fmt.Errorf("%s declares itself %s", id, self.State)
			}
			life := s.Status(id).Incarnation
			if prev, ok := lastInc[id]; ok && prev[0] == life && self.Incarnation < prev[1] {
				return fmt.Errorf("%s incarnation went from %d to %d", id, prev[1], self.Incarnation)
			}
			lastInc[id] = [2]uint32{life, self.Incarnation}
		}
		return nil
	})
	return c
}

func (c *cluster) factory(e env.Env) env.Process {
	n, err := gossip.New(e, c.cfg)
	if err != nil {
		c.s.Fail(err)
		return nil
	}
	id := e.ID
	c.rec.reset(id)
	n.Subscribe(func(ev gossip.Event) { c.rec.observe(id, ev) })
	return n
}

func region(i int) env.Region { return env.Region(fmt.Sprintf("r%d", i%3)) }

func (c *cluster) add(ids ...env.NodeID) {
	for _, id := range ids {
		i := slices.Index(c.ids, id)
		if i < 0 {
			c.s.Fail(fmt.Errorf("unknown test node %s", id))
			return
		}
		if err := c.s.AddNode(sim.NodeSpec{ID: id, Region: region(i)}, c.factory); err != nil {
			c.s.Fail(err)
		}
	}
}

func (c *cluster) start(n int) {
	c.ids = sim.NodeIDs(n)
	c.cfg.Seeds = c.ids[:3]
	c.add(c.ids...)
}

func gnode(s *sim.Sim, id env.NodeID) *gossip.Node {
	if s.Status(id).Paused {
		return nil
	}
	n, _ := s.Process(id).(*gossip.Node)
	return n
}

func (c *cluster) live() []env.NodeID {
	var out []env.NodeID
	for _, id := range c.s.Nodes() {
		if st := c.s.Status(id); st.Up && !c.left[id] {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

func agreed(s *sim.Sim, expect []env.NodeID) bool {
	var ref []gossip.Member
	for _, id := range expect {
		n := gnode(s, id)
		if n == nil {
			return false
		}
		ms := n.Members()
		if len(ms) != len(expect) {
			return false
		}
		for i, m := range ms {
			if m.ID != expect[i] || m.State != gossip.StateAlive {
				return false
			}
		}
		if ref == nil {
			ref = ms
		} else if !slices.Equal(ms, ref) {
			return false
		}
	}
	return true
}

func poll(s *sim.Sim, deadline env.Time, pred func() bool) (env.Time, bool) {
	for {
		if s.Err() != nil {
			return s.Now(), false
		}
		if pred() {
			return s.Now(), true
		}
		if s.Now() >= deadline {
			return s.Now(), false
		}
		next := s.Now().Add(dstPoll)
		if next > deadline {
			next = deadline
		}
		if err := s.RunUntil(next); err != nil {
			return s.Now(), false
		}
	}
}

func setLoss(s *sim.Sim, ppm uint32, from env.NodeID, to []env.NodeID) {
	for _, o := range to {
		if o == from {
			continue
		}
		if err := s.SetLinkLoss(from, o, ppm); err != nil {
			s.Fail(err)
		}
		if err := s.SetLinkLoss(o, from, ppm); err != nil {
			s.Fail(err)
		}
	}
}

func seesState(s *sim.Sim, observers []env.NodeID, subject env.NodeID, want gossip.State) bool {
	for _, id := range observers {
		n := gnode(s, id)
		if n == nil {
			return false
		}
		m, err := n.Lookup(subject)
		if err != nil || m.State != want {
			return false
		}
	}
	return true
}

func seesLive(s *sim.Sim, observers []env.NodeID, subject env.NodeID) bool {
	for _, id := range observers {
		n := gnode(s, id)
		if n == nil {
			return false
		}
		m, err := n.Lookup(subject)
		if err != nil || !m.State.Live() {
			return false
		}
	}
	return true
}

func without(ids []env.NodeID, drop ...env.NodeID) []env.NodeID {
	var out []env.NodeID
	for _, id := range ids {
		if !slices.Contains(drop, id) {
			out = append(out, id)
		}
	}
	return out
}

type tally struct {
	mu   sync.Mutex
	vals []int64
}

func (t *tally) add(v int64) {
	t.mu.Lock()
	t.vals = append(t.vals, v)
	t.mu.Unlock()
}

func (t *tally) summary() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.vals) == 0 {
		return "no runs"
	}
	v := slices.Clone(t.vals)
	slices.Sort(v)
	var sum int64
	for _, x := range v {
		sum += x
	}
	return fmt.Sprintf("runs=%d min=%d median=%d p99=%d max=%d mean_milli=%d", len(v), v[0], v[len(v)/2], v[len(v)*99/100], v[len(v)-1], sum*1000/int64(len(v)))
}

type convergeOut struct {
	periods   int64
	milliMsgs int64
	falseDead int
}

func convergeRun(n int, out *convergeOut) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		c := newCluster(seed, gossip.Config{})
		s := c.s
		c.ids = sim.NodeIDs(n)
		c.cfg.Seeds = c.ids[:3]
		late := max(1, n/10)
		initial := c.ids[:n-late]
		c.add(initial...)
		r := s.WorkloadRand()
		loss := convergeLossMinPPM + r.Uint32N(convergeLossMaxPPM-convergeLossMinPPM+1)
		for i, id := range initial {
			setLoss(s, loss, id, initial[i+1:])
		}
		victims := slices.Clone(c.ids[3 : n-late])
		r.Shuffle(len(victims), func(i, j int) { victims[i], victims[j] = victims[j], victims[i] })
		k := max(1, n/20)
		window := int64(convergeQuiet) - int64(6*time.Second)
		crashed := victims[:k]
		leavers := victims[k : 2*k]
		for _, id := range crashed {
			s.At(env.Time(3*time.Second)+env.Time(r.Int64N(window)), "crash", func() {
				if err := s.Crash(id); err != nil {
					s.Fail(err)
				}
			})
		}
		for _, id := range leavers {
			s.At(env.Time(3*time.Second)+env.Time(r.Int64N(window)), "leave", func() {
				if g := gnode(s, id); g != nil {
					if err := g.Leave(); err != nil {
						s.Fail(err)
					}
					c.left[id] = true
				}
			})
		}
		for _, id := range c.ids[n-late:] {
			s.At(env.Time(time.Second)+env.Time(r.Int64N(window)), "join", func() {
				c.add(id)
				setLoss(s, loss, id, without(s.Nodes(), id))
			})
		}
		s.At(convergeQuiet, "heal", s.HealNet)
		if err := s.RunUntil(convergeQuiet); err != nil {
			return s
		}
		expect := c.live()
		bound := time.Duration(convergeC*ceilLog2(n)) * gossip.DefaultProbeInterval
		at, ok := poll(s, convergeQuiet.Add(bound), func() bool { return agreed(s, expect) })
		if !ok {
			s.Fail(fmt.Errorf("views of %d live nodes not identical within %v (%d periods = %d*log2(%d)) after faults stopped", len(expect), bound, convergeC*ceilLog2(n), convergeC, n))
			return s
		}
		out.periods = int64((at.Sub(convergeQuiet) + gossip.DefaultProbeInterval - 1) / gossip.DefaultProbeInterval)
		var sent, nodes int64
		for _, id := range initial {
			if g := gnode(s, id); g != nil && !c.left[id] {
				sent += int64(g.Stats().Sent)
				nodes++
			}
		}
		periods := int64(at) / int64(gossip.DefaultProbeInterval)
		out.milliMsgs = sent * 1000 / (nodes * periods)
		out.falseDead = c.rec.total(gossip.EventDied, append(slices.Clone(crashed), leavers...)...)
		if out.milliMsgs > maxMilliMsgsPerNodePeriod {
			s.Fail(fmt.Errorf("%d.%03d datagrams per node per period, bound %d.%03d", out.milliMsgs/1000, out.milliMsgs%1000, maxMilliMsgsPerNodePeriod/1000, maxMilliMsgsPerNodePeriod%1000))
		}
		return s
	}
}

func TestSWIMConvergenceDST(t *testing.T) {
	for _, n := range []int{20, 50, 100} {
		t.Run(fmt.Sprintf("N=%d", n), func(t *testing.T) {
			var periods, msgs, falseDead tally
			t.Cleanup(func() {
				t.Logf("MEASURE converge N=%d log2N=%d bound=%d periods: periods-to-converge %s", n, ceilLog2(n), convergeC*ceilLog2(n), periods.summary())
				t.Logf("MEASURE converge N=%d milli-datagrams per node per period %s", n, msgs.summary())
				t.Logf("MEASURE converge N=%d false Died events (healthy nodes, any observer) %s", n, falseDead.summary())
			})
			simtest.Sweep(t, func(t *testing.T, seed int64) {
				t.Parallel()
				var out convergeOut
				simtest.RequireDeterministic(t, seed, convergeRun(n, &out))
				periods.add(out.periods)
				msgs.add(out.milliMsgs)
				falseDead.add(int64(out.falseDead))
			})
		})
	}
}

type asymOut struct {
	suspected int
	died      int
}

func asymRunFn(cfg gossip.Config, out *asymOut) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		c := newCluster(seed, cfg)
		c.start(partitionNodes)
		s := c.s
		r := s.WorkloadRand()
		pair := r.Perm(partitionNodes)
		from, to := c.ids[pair[0]], c.ids[pair[1]]
		s.At(env.Time(10*time.Second), "one-way", func() {
			if err := s.PartitionOneWay([]env.NodeID{from}, []env.NodeID{to}); err != nil {
				s.Fail(err)
			}
		})
		if err := s.RunFor(asymRun); err != nil {
			return s
		}
		out.suspected = c.rec.total(gossip.EventSuspected)
		out.died = c.rec.total(gossip.EventDied)
		return s
	}
}

func TestSWIMAsymmetricLinkDST(t *testing.T) {
	var sus tally
	t.Cleanup(func() { t.Logf("MEASURE asymmetric suspicions %s", sus.summary()) })
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		t.Parallel()
		var out asymOut
		simtest.RequireDeterministic(t, seed, asymRunFn(gossip.Config{}, &out))
		sus.add(int64(out.suspected))
		if out.died != 0 {
			t.Fatalf("%d evictions under a single one-way link", out.died)
		}
		if out.suspected != 0 {
			t.Fatalf("%d suspicions under a single one-way link with indirect probes", out.suspected)
		}
	})
}

func TestCanaryNoIndirectProbe(t *testing.T) {
	var sus, died tally
	t.Cleanup(func() {
		t.Logf("MEASURE canary suspicions %s", sus.summary())
		t.Logf("MEASURE canary evictions %s", died.summary())
	})
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		t.Parallel()
		var out asymOut
		simtest.RequireDeterministic(t, seed, asymRunFn(gossip.WithoutIndirectProbes(gossip.Config{}), &out))
		sus.add(int64(out.suspected))
		died.add(int64(out.died))
		if out.suspected == 0 && out.died == 0 {
			t.Fatal("with indirect probes off the asymmetric-link check still passed: the test cannot see the bug")
		}
	})
}

type crashOut struct {
	detect    time.Duration
	falseDead int
}

func crashRun(out *crashOut) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		c := newCluster(seed, gossip.Config{})
		c.start(crashNodes)
		s := c.s
		for i, id := range c.ids {
			setLoss(s, crashLossPPM, id, c.ids[i+1:])
		}
		r := s.WorkloadRand()
		victim := c.ids[3+r.IntN(crashNodes-3)]
		s.At(crashAt, "crash", func() {
			if err := s.Crash(victim); err != nil {
				s.Fail(err)
			}
		})
		if err := s.RunUntil(crashAt); err != nil {
			return s
		}
		others := without(c.ids, victim)
		if !seesLive(s, others, victim) {
			s.Fail(fmt.Errorf("setup: %s was not a member at every node when it crashed", victim))
			return s
		}
		at, ok := poll(s, crashAt.Add(detectBound), func() bool { return seesState(s, others, victim, gossip.StateDead) })
		if !ok {
			s.Fail(fmt.Errorf("crashed %s not dead at every live node within %v", victim, detectBound))
			return s
		}
		out.detect = at.Sub(crashAt)
		out.falseDead = c.rec.total(gossip.EventDied, victim)
		if out.falseDead != 0 {
			s.Fail(fmt.Errorf("%d Died events about healthy nodes", out.falseDead))
		}
		return s
	}
}

func TestSWIMCrashDetectionDST(t *testing.T) {
	var det tally
	t.Cleanup(func() {
		t.Logf("MEASURE crash detection ms (N=%d, loss %d ppm, bound %v) %s", crashNodes, crashLossPPM, detectBound, det.summary())
	})
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		t.Parallel()
		var out crashOut
		simtest.RequireDeterministic(t, seed, crashRun(&out))
		det.add(out.detect.Milliseconds())
	})
}

func partitionRun(out *time.Duration) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		c := newCluster(seed, gossip.Config{})
		c.start(partitionNodes)
		s := c.s
		if err := s.RunUntil(partitionAt); err != nil {
			return s
		}
		if err := s.PartitionRegion("r0"); err != nil {
			s.Fail(err)
			return s
		}
		var a, b []env.NodeID
		for i, id := range c.ids {
			if region(i) == "r0" {
				a = append(a, id)
			} else {
				b = append(b, id)
			}
		}
		split := func() bool {
			for _, x := range a {
				if !seesState(s, b, x, gossip.StateDead) {
					return false
				}
			}
			for _, y := range b {
				if !seesState(s, a, y, gossip.StateDead) {
					return false
				}
			}
			return true
		}
		if _, ok := poll(s, partitionAt.Add(splitBound), split); !ok {
			s.Fail(fmt.Errorf("the two sides did not declare each other dead within %v", splitBound))
			return s
		}
		healed := s.Now()
		s.HealNet()
		at, ok := poll(s, healed.Add(healBound), func() bool { return agreed(s, c.ids) })
		if !ok {
			s.Fail(fmt.Errorf("views did not re-merge within %v of the heal", healBound))
			return s
		}
		*out = at.Sub(healed)
		return s
	}
}

func TestSWIMPartitionHealDST(t *testing.T) {
	var heal tally
	t.Cleanup(func() {
		t.Logf("MEASURE partition heal ms (N=%d, bound %v) %s", partitionNodes, healBound, heal.summary())
	})
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		t.Parallel()
		var out time.Duration
		simtest.RequireDeterministic(t, seed, partitionRun(&out))
		heal.add(out.Milliseconds())
	})
}

type refuteOut struct {
	suspected int
	died      int
	inc       uint32
}

func refuteRunFn(out *refuteOut) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		c := newCluster(seed, gossip.Config{})
		c.start(refuteNodes)
		s := c.s
		victim := c.ids[3+s.WorkloadRand().IntN(refuteNodes-3)]
		s.At(pauseAt, "pause", func() {
			if err := s.Pause(victim); err != nil {
				s.Fail(err)
			}
		})
		s.At(pauseAt.Add(pauseFor), "resume", func() {
			if err := s.Resume(victim); err != nil {
				s.Fail(err)
			}
		})
		if err := s.RunFor(refuteRun); err != nil {
			return s
		}
		out.suspected = c.rec.count(victim, gossip.EventSuspected)
		out.died = c.rec.count(victim, gossip.EventDied)
		v := gnode(s, victim)
		if v == nil {
			s.Fail(fmt.Errorf("victim %s is not running", victim))
			return s
		}
		out.inc = v.Self().Incarnation
		if !agreed(s, c.ids) {
			s.Fail(fmt.Errorf("views disagree %v after the refutation", refuteRun))
		}
		return s
	}
}

func TestSWIMRefutationDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		t.Parallel()
		var out refuteOut
		simtest.RequireDeterministic(t, seed, refuteRunFn(&out))
		if out.suspected == 0 {
			t.Fatal("the paused node was never suspected: the scenario did not exercise refutation")
		}
		if out.died != 0 {
			t.Fatalf("the falsely suspected node was declared dead %d times", out.died)
		}
		if out.inc == 0 {
			t.Fatal("the suspected node never raised its incarnation")
		}
	})
}

type restartOut struct {
	before gossip.Member
	after  gossip.Member
}

func restartRun(waitForDeath bool, out *restartOut) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		c := newCluster(seed, gossip.Config{})
		c.start(restartNodes)
		s := c.s
		victim := c.ids[3+s.WorkloadRand().IntN(restartNodes-3)]
		others := without(c.ids, victim)
		if err := s.RunUntil(crashAt); err != nil {
			return s
		}
		if err := s.Crash(victim); err != nil {
			s.Fail(err)
			return s
		}
		if waitForDeath {
			if _, ok := poll(s, crashAt.Add(detectBound), func() bool { return seesState(s, others, victim, gossip.StateDead) }); !ok {
				s.Fail(fmt.Errorf("%s not declared dead", victim))
				return s
			}
		} else if err := s.RunFor(500 * time.Millisecond); err != nil {
			return s
		}
		m, err := gnode(s, others[0]).Lookup(victim)
		if err != nil {
			s.Fail(err)
			return s
		}
		out.before = m
		restarted := s.Now()
		if err := s.WipeRejoin(victim); err != nil {
			s.Fail(err)
			return s
		}
		if _, ok := poll(s, restarted.Add(rejoinBound), func() bool { return agreed(s, c.ids) }); !ok {
			s.Fail(fmt.Errorf("restarted %s not alive in every view within %v", victim, rejoinBound))
			return s
		}
		m, _ = gnode(s, others[0]).Lookup(victim)
		out.after = m
		return s
	}
}

func TestSWIMRestartSameIDDST(t *testing.T) {
	for _, wait := range []bool{true, false} {
		name := "restarted before anyone noticed the crash"
		if wait {
			name = "restarted after every node declared it dead"
		}
		t.Run(name, func(t *testing.T) {
			simtest.Sweep(t, func(t *testing.T, seed int64) {
				t.Parallel()
				var out restartOut
				simtest.RequireDeterministic(t, seed, restartRun(wait, &out))
				if out.after.Incarnation < out.before.Incarnation || (out.before.State != gossip.StateAlive && out.after.Incarnation == out.before.Incarnation) {
					t.Fatalf("rejoined as %+v over the old entry %+v", out.after, out.before)
				}
			})
		})
	}
}

func joinFailRun(out *error) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		c := newCluster(seed, gossip.Config{})
		c.ids = sim.NodeIDs(3)
		c.add(c.ids...)
		s := c.s
		if err := s.RunFor(time.Second); err != nil {
			return s
		}
		for _, id := range c.ids[1:] {
			if err := s.Crash(id); err != nil {
				s.Fail(err)
				return s
			}
		}
		s.At(s.Now().Add(time.Millisecond), "join", func() {
			if err := gnode(s, c.ids[0]).Join(append(slices.Clone(c.ids[1:]), "ghost"), func(err error) { *out = err }); err != nil {
				s.Fail(err)
			}
		})
		if err := s.RunFor(10 * time.Second); err != nil {
			return s
		}
		return s
	}
}

func TestJoinNoReachableSeedDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		t.Parallel()
		var got error
		simtest.RequireDeterministic(t, seed, joinFailRun(&got))
		if !errors.Is(got, gossip.ErrJoinFailed) || !errors.Is(got, env.ErrUnreachable) {
			t.Fatalf("join error %v, want ErrJoinFailed wrapping env.ErrUnreachable", got)
		}
	})
}
