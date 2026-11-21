package occ_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
)

const (
	dstNodes        = 6
	chaosStart      = env.Time(time.Second)
	chaosPhases     = 3
	chaosLength     = 4 * time.Second
	healLength      = 2 * time.Second
	chaosMaxGap     = 300 * time.Millisecond
	workloadOps     = 150
	workloadMaxGap  = 240 * time.Millisecond
	maxNodeSkew     = 400 * time.Millisecond
	maxNodeDriftPPM = 2_000
	invariantEvery  = 250
	dropPPM         = 20_000
	duplicatePPM    = 50_000
	canarySeeds     = 3

	ConvergeBound  = 15 * time.Second
	offlineFor     = 60 * time.Second
	offlineOps     = 200
	offlineMaxGap  = 400 * time.Millisecond
	raceConverge   = 10 * time.Second
	quotaLimit     = 1_000
	quotaObject    = 100
	quotaOps       = 60
	quotaPartition = 20 * time.Second
	quotaWriters   = 4
	MaxOvershoot   = (quotaWriters - 1) * quotaLimit
)

var regions = []env.Region{"region-a", "region-b", "region-c"}

var dstKeys = []string{"k0", "k1", "k2", "k3"}

type world struct {
	s           *sim.Sim
	ids         []env.NodeID
	or          *oracle
	history     map[string][]occ.ETag
	maxSiblings int
	acks        int
	refused     int
	convergedIn time.Duration
	final       map[int]int
	snapshot    map[env.Region]*occ.Doc
}

var lastWorld *world

func newWorld(s *sim.Sim, ids []env.NodeID) *world {
	w := &world{s: s, ids: ids, or: newOracle(), history: map[string][]occ.ETag{}, final: map[int]int{}}
	lastWorld = w
	return w
}

func dstNet() sim.NetConfig {
	n := sim.DefaultNetConfig()
	n.DropPPM = dropPPM
	n.DuplicatePPM = duplicatePPM
	return n
}

func dstSim(seed int64, net sim.NetConfig) *sim.Sim {
	codes, err := occ.Codes()
	s := sim.New(sim.Config{Seed: seed, Codes: codes, Net: net})
	if err != nil {
		s.Fail(err)
	}
	return s
}

func addNodes(s *sim.Sim, n int, skew bool) []env.NodeID {
	ids := sim.NodeIDs(n)
	f := occ.NewFactory(occ.Config{Membership: occ.StaticMembership(ids)})
	r := sim.NewStream(s.Seed(), "occ-test/skew")
	for i, id := range ids {
		spec := sim.NodeSpec{ID: id, Region: regions[i%len(regions)]}
		if skew {
			spec.Skew = time.Duration(r.Int64N(int64(2*maxNodeSkew))) - maxNodeSkew
			spec.DriftPPM = r.Int64N(2*maxNodeDriftPPM) - maxNodeDriftPPM
		}
		if err := s.AddNode(spec, f); err != nil {
			s.Fail(err)
		}
	}
	return ids
}

func (w *world) live(id env.NodeID) *occ.Node {
	if w.s.Status(id).Paused {
		return nil
	}
	n := occNode(w.s, id)
	if n == nil || !n.Ready() {
		return nil
	}
	return n
}

func (w *world) docs() ([]*occ.Doc, error) {
	var out []*occ.Doc
	for _, id := range w.ids {
		if n := occNode(w.s, id); n != nil && n.Ready() {
			out = append(out, n.Store().Doc())
			continue
		}
		b, ok := w.s.Disk(id).PeekDurable(occ.StateBlob)
		if !ok {
			continue
		}
		_, d, err := occ.DecodeStateBlob(b)
		if err != nil {
			return nil, fmt.Errorf("node %s durable state: %w", id, err)
		}
		out = append(out, d)
	}
	return out, nil
}

func (o *oracle) checkDocs(docs []*occ.Doc) error {
	for _, a := range o.acks {
		found := false
		for _, d := range docs {
			for _, w := range d.Record(a.key).Siblings() {
				if w.ID() == a.v.ID() || o.seen[w.ID()][a.v.ID()] {
					found = true
				}
			}
		}
		if !found {
			return fmt.Errorf("acknowledged version %s of %q (dot %s:%d) is on no replica, live or durable, and no replica holds a version whose writer saw it",
				a.v.ID().String()[:12], a.key, a.v.Dot().Actor.String(), a.v.Dot().Counter)
		}
	}
	return nil
}

func (w *world) noSilentLoss() error {
	docs, err := w.docs()
	if err != nil {
		return err
	}
	for _, d := range docs {
		for _, k := range d.Keys() {
			w.maxSiblings = max(w.maxSiblings, d.Record(k).Len())
		}
	}
	return w.or.checkDocs(docs)
}

func (w *world) converged() bool {
	var first [32]byte
	for i, id := range w.ids {
		st := w.s.Status(id)
		if !st.Up || st.Paused {
			return false
		}
		n := occNode(w.s, id)
		if n == nil || !n.Ready() {
			return false
		}
		if i == 0 {
			first = n.Digest()
		} else if n.Digest() != first {
			return false
		}
	}
	return true
}

func (w *world) write(n *occ.Node, key string, p occ.Precondition, del bool, value []byte) {
	rec := n.Store().Record(key)
	var v occ.Version
	var err error
	if del {
		v, err = n.Delete(key, p)
	} else {
		v, err = n.Put(key, value, p)
	}
	if err != nil {
		if !errors.Is(err, occ.ErrPreconditionFailed) && !errors.Is(err, occ.ErrNotFound) {
			w.s.Fail(fmt.Errorf("write %q on %s: unexpected %w", key, n.Store().Actor().String(), err))
		}
		if p.Holds(rec) && !(del && len(rec.Live()) == 0) {
			w.s.Fail(fmt.Errorf("write %q refused although %s held", key, p.String()))
		}
		w.refused++
		return
	}
	if !p.Holds(rec) {
		w.s.Fail(fmt.Errorf("write %q accepted although %s did not hold", key, p.String()))
	}
	var covered []occ.Version
	if !p.IsUnconditional() {
		covered = rec.Siblings()
	}
	w.history[key] = append(w.history[key], rec.ETag())
	w.or.record(key, v, covered)
	w.acks++
}

func (w *world) action(blind bool) func(int) {
	return func(i int) {
		r := w.s.WorkloadRand()
		n := w.live(w.ids[r.IntN(len(w.ids))])
		key := dstKeys[r.IntN(len(dstKeys))]
		op := r.IntN(100)
		val := fmt.Appendf(nil, "%s#%d", key, i)
		if n == nil {
			return
		}
		rec := n.Store().Record(key)
		cur := occ.IfMatch(rec.ETag())
		if rec.Len() == 0 {
			cur = occ.IfNoneMatch()
		}
		switch {
		case op < 50:
			w.write(n, key, cur, false, val)
		case op < 60:
			w.write(n, key, occ.IfNoneMatch(), false, val)
		case op < 70:
			w.write(n, key, cur, true, nil)
		case op < 78 && blind:
			w.write(n, key, occ.Unconditional(), false, val)
		case op < 88 && len(w.history[key]) > 0:
			h := w.history[key]
			w.write(n, key, occ.IfMatch(h[r.IntN(len(h))]), false, val)
		default:
			if _, err := n.SetScalar("scalar/"+key, val); err != nil {
				w.s.Fail(err)
			}
		}
	}
}

func (w *world) finish(quiet env.Time, bound time.Duration) {
	if w.s.Err() != nil {
		return
	}
	if err := w.s.RunUntilConverged(quiet.Add(bound), w.converged); err != nil {
		return
	}
	w.convergedIn = w.s.Now().Sub(quiet)
	final := occNode(w.s, w.ids[0]).Store().Doc()
	if err := w.or.checkDocs([]*occ.Doc{final}); err != nil {
		w.s.Fail(fmt.Errorf("after convergence: %w", err))
	}
	for _, id := range w.ids[1:] {
		d := occNode(w.s, id).Store().Doc()
		for _, k := range final.Keys() {
			if d.Record(k).ETag() != final.Record(k).ETag() {
				w.s.Fail(fmt.Errorf("etag of %q differs on %s after convergence", k, id))
			}
		}
	}
	for _, k := range final.Keys() {
		w.final[final.Record(k).Len()]++
	}
}

type multiRegionOptions struct{}

func multiRegionRun(multiRegionOptions) func(int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		s := dstSim(seed, dstNet())
		ids := addNodes(s, dstNodes, true)
		w := newWorld(s, ids)
		s.AddInvariant("occ/no-silent-loss", invariantEvery, w.noSilentLoss)
		s.Workload(chaosStart, workloadOps, workloadMaxGap, w.action(true))
		nem, err := s.StartNemesis(sim.NemesisConfig{
			Start:  chaosStart,
			Phases: chaosPhases,
			Chaos:  chaosLength,
			Heal:   healLength,
			MaxGap: chaosMaxGap,
			Kinds: []sim.FaultKind{sim.FaultCrash, sim.FaultRestart, sim.FaultPause, sim.FaultResume, sim.FaultPartition,
				sim.FaultPartitionOneWay, sim.FaultIsolate, sim.FaultPartitionRegion, sim.FaultLinkLoss, sim.FaultClog,
				sim.FaultUnclog, sim.FaultReadError, sim.FaultCrashTorn},
			Budget: sim.Budget{MinLive: dstNodes/2 + 1, MaxDamagedDisks: 1},
		})
		if err != nil {
			s.Fail(err)
			return s
		}
		if err := s.RunUntil(nem.QuietAt()); err != nil {
			return s
		}
		w.finish(nem.QuietAt(), ConvergeBound)
		return s
	}
}

func logWorld(t *testing.T, w *world) {
	t.Logf("MEASURE acks=%d refused=%d max_siblings=%d final_siblings=%v converged_in=%s events=%d",
		w.acks, w.refused, w.maxSiblings, w.final, w.convergedIn, w.s.Steps())
}

func TestMultiRegionConvergenceDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		simtest.RequireDeterministic(t, seed, multiRegionRun(multiRegionOptions{}))
		if lastWorld.acks == 0 {
			t.Fatal("the workload acknowledged nothing")
		}
		logWorld(t, lastWorld)
	})
}

func offlineRun(seed int64) *sim.Sim {
	s := dstSim(seed, dstNet())
	ids := addNodes(s, dstNodes, true)
	w := newWorld(s, ids)
	w.snapshot = map[env.Region]*occ.Doc{}
	s.AddInvariant("occ/no-silent-loss", invariantEvery, w.noSilentLoss)
	offline := regions[2]
	healAt := chaosStart.Add(offlineFor)
	s.At(chaosStart, "partition region", func() {
		if err := s.PartitionRegion(offline); err != nil {
			s.Fail(err)
		}
	})
	s.Workload(chaosStart.Add(10*time.Millisecond), offlineOps, offlineMaxGap, w.action(false))
	s.At(healAt, "snapshot both sides", func() {
		for _, id := range ids {
			side := regions[0]
			if s.Region(id) == offline {
				side = offline
			}
			if w.snapshot[side] == nil {
				w.snapshot[side] = occ.NewDoc()
			}
			w.snapshot[side].Merge(occNode(s, id).Store().Doc())
		}
	})
	s.At(healAt, "heal", s.HealNet)
	if err := s.RunUntil(healAt); err != nil {
		return s
	}
	w.finish(healAt, ConvergeBound)
	if s.Err() != nil {
		return s
	}
	final := occNode(s, ids[0]).Store().Doc()
	contested := 0
	for _, k := range dstKeys {
		want := w.snapshot[regions[0]].Record(k).Merge(w.snapshot[offline].Record(k))
		got := final.Record(k)
		if !got.Equal(want) {
			s.Fail(fmt.Errorf("key %q after heal is not the merge of both sides' heads", k))
		}
		for _, v := range w.snapshot[offline].Record(k).Siblings() {
			if !slices.Contains(got.IDs(), v.ID()) {
				s.Fail(fmt.Errorf("the offline region's version %s of %q was dropped", v.ID().String()[:12], k))
			}
		}
		if got.Len() > 1 {
			contested++
		}
	}
	if contested == 0 {
		s.Fail(errors.New("no key was edited on both sides; the scenario proves nothing"))
	}
	return s
}

func TestOfflineRegionDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		simtest.RequireDeterministic(t, seed, offlineRun)
		logWorld(t, lastWorld)
	})
}

type raceClient struct {
	e    env.Env
	c    *occ.Client
	etag occ.ETag
	res  occ.Result
	err  error
	done bool
}

func (c *raceClient) Start()                                        { c.c = occ.NewClient(c.e.Net, time.Second) }
func (c *raceClient) OnDatagram(env.NodeID, []byte)                 {}
func (c *raceClient) OnRequest(_ env.NodeID, _ []byte, r env.Reply) { r(nil, nil) }
func (c *raceClient) EncodeState() []byte                           { return append(c.etag[:0:0], c.etag[:]...) }

func (c *raceClient) get(s *sim.Sim, server env.NodeID, key string) {
	c.c.Get(server, key, func(r occ.Result, err error) {
		if err != nil {
			s.Fail(fmt.Errorf("client get: %w", err))
			return
		}
		c.etag = r.ETag
	})
}

func (c *raceClient) put(server env.NodeID, key string, value string, p occ.Precondition) {
	c.c.Put(server, key, []byte(value), p, func(r occ.Result, err error) {
		c.res, c.err, c.done = r, err, true
	})
}

func addClient(s *sim.Sim, id env.NodeID, region env.Region) {
	if err := s.AddNode(sim.NodeSpec{ID: id, Region: region}, func(e env.Env) env.Process { return &raceClient{e: e} }); err != nil {
		s.Fail(err)
	}
}

func client(s *sim.Sim, id env.NodeID) *raceClient {
	c, _ := s.Process(id).(*raceClient)
	return c
}

func raceRun(partitioned bool) func(int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		s := dstSim(seed, sim.DefaultNetConfig())
		servers := []env.NodeID{"s1", "s2"}
		if !partitioned {
			servers = servers[:1]
		}
		f := occ.NewFactory(occ.Config{Membership: occ.StaticMembership(servers)})
		for i, id := range servers {
			if err := s.AddNode(sim.NodeSpec{ID: id, Region: regions[i]}, f); err != nil {
				s.Fail(err)
			}
		}
		addClient(s, "c1", regions[0])
		addClient(s, "c2", regions[len(servers)-1])
		target := func(c env.NodeID) env.NodeID {
			if c == "c2" {
				return servers[len(servers)-1]
			}
			return servers[0]
		}
		s.At(env.Time(10*time.Millisecond), "create", func() {
			client(s, "c1").put(servers[0], "k", "base", occ.IfNoneMatch())
		})
		t0 := env.Time(3 * time.Second)
		if partitioned {
			s.At(t0, "partition", func() {
				if err := s.Partition([]env.NodeID{"s1", "c1"}, []env.NodeID{"s2", "c2"}); err != nil {
					s.Fail(err)
				}
			})
		}
		s.At(t0.Add(10*time.Millisecond), "read", func() {
			for _, c := range []env.NodeID{"c1", "c2"} {
				client(s, c).get(s, target(c), "k")
			}
		})
		s.At(t0.Add(time.Second), "race", func() {
			c1, c2 := client(s, "c1"), client(s, "c2")
			if c1.etag != c2.etag || c1.etag.IsZero() {
				s.Fail(errors.New("the two writers did not read the same etag"))
				return
			}
			c1.put(target("c1"), "k", "from-c1", occ.IfMatch(c1.etag))
			c2.put(target("c2"), "k", "from-c2", occ.IfMatch(c2.etag))
		})
		healAt := t0.Add(2 * time.Second)
		s.At(healAt, "heal", s.HealNet)
		if err := s.RunUntil(healAt); err != nil {
			return s
		}
		c1, c2 := client(s, "c1"), client(s, "c2")
		if !c1.done || !c2.done {
			s.Fail(errors.New("a racing write got no answer"))
			return s
		}
		wins := 0
		for _, c := range []*raceClient{c1, c2} {
			switch {
			case c.err == nil:
				wins++
			case !errors.Is(c.err, occ.ErrPreconditionFailed) || !errors.Is(c.err, env.ErrRemote):
				s.Fail(fmt.Errorf("the losing writer got %w, want ErrPreconditionFailed over the wire", c.err))
			}
		}
		want := 1
		if partitioned {
			want = 2
		}
		if wins != want {
			s.Fail(fmt.Errorf("%d writers won, want %d", wins, want))
			return s
		}
		converged := func() bool {
			d := occNode(s, servers[0]).Digest()
			for _, id := range servers[1:] {
				if n := occNode(s, id); n == nil || n.Digest() != d {
					return false
				}
			}
			return true
		}
		if err := s.RunUntilConverged(healAt.Add(raceConverge), converged); err != nil {
			return s
		}
		for _, id := range servers {
			r := occNode(s, id).Store().Record("k")
			if r.Len() != wins {
				s.Fail(fmt.Errorf("server %s holds %d versions, want %d", id, r.Len(), wins))
			}
			for _, c := range []*raceClient{c1, c2} {
				if c.err == nil && !slices.Contains(r.IDs(), c.res.Version) {
					s.Fail(fmt.Errorf("an acknowledged racing write is missing on %s", id))
				}
			}
		}
		return s
	}
}

func TestConditionalWriteRaceDST(t *testing.T) {
	t.Run("two writers on one node: exactly one wins", func(t *testing.T) {
		simtest.Sweep(t, func(t *testing.T, seed int64) {
			simtest.RequireDeterministic(t, seed, raceRun(false))
		})
	})
	t.Run("two writers across a partition: both win and both survive as siblings", func(t *testing.T) {
		simtest.Sweep(t, func(t *testing.T, seed int64) {
			simtest.RequireDeterministic(t, seed, raceRun(true))
		})
	})
}

var lastOvershoot int64

func quotaRun(seed int64) *sim.Sim {
	s := dstSim(seed, dstNet())
	ids := addNodes(s, quotaWriters, false)
	w := newWorld(s, ids)
	limit := occ.Usage{Bytes: quotaLimit}
	qk := occ.QuotaKey("b")
	s.At(chaosStart, "partition", func() {
		if err := s.Partition([]env.NodeID{ids[0], ids[2]}, []env.NodeID{ids[1], ids[3]}); err != nil {
			s.Fail(err)
		}
	})
	s.Workload(chaosStart.Add(10*time.Millisecond), quotaOps, quotaPartition/quotaOps, func(i int) {
		n := w.live(ids[w.s.WorkloadRand().IntN(len(ids))])
		if n == nil {
			return
		}
		obj := fmt.Sprintf("o%d", i)
		recount := func(string) (occ.Usage, error) { return n.Store().RecountObjects("b") }
		if _, err := n.UpdateQuota(qk, 3, recount, occ.AddUsage(quotaObject, 1, limit)); err != nil {
			if !errors.Is(err, occ.ErrQuotaExceeded) {
				s.Fail(err)
			}
			return
		}
		if _, err := n.Put(occ.ObjectKey("b", obj), occ.ObjectMeta{Size: quotaObject}.Encode(), occ.IfNoneMatch()); err != nil {
			s.Fail(err)
		}
	})
	healAt := chaosStart.Add(quotaPartition + time.Second)
	s.At(healAt, "heal", s.HealNet)
	if err := s.RunUntil(healAt); err != nil {
		return s
	}
	w.finish(healAt, ConvergeBound)
	if s.Err() != nil {
		return s
	}
	n := occNode(s, ids[0])
	recount := n.Store().RecountObjects
	u, err := n.UpdateQuota(qk, 3, func(string) (occ.Usage, error) { return recount("b") }, occ.AddUsage(0, 0, occ.Usage{}))
	if err != nil {
		s.Fail(err)
		return s
	}
	truth, err := recount("b")
	if err != nil || u != truth {
		s.Fail(fmt.Errorf("resolved usage %v, recount %v: %v", u, truth, err))
	}
	lastOvershoot = int64(u.Bytes) - quotaLimit
	if lastOvershoot > MaxOvershoot {
		s.Fail(fmt.Errorf("overshoot %d bytes is above the bound %d", lastOvershoot, MaxOvershoot))
	}
	w.finish(s.Now(), ConvergeBound)
	for _, id := range ids {
		if occNode(s, id).Store().Record(qk).Len() != 1 {
			s.Fail(errors.New("the resolution did not collapse the quota siblings everywhere"))
		}
	}
	return s
}

func TestQuotaOvershootUnderPartitionDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		simtest.RequireDeterministic(t, seed, quotaRun)
		t.Logf("MEASURE quota overshoot=%d bytes (limit %d)", lastOvershoot, quotaLimit)
	})
}
