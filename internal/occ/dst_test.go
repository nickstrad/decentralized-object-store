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

// DST scenario parameters and named bounds:
//   - ConvergeBound: maximum virtual time allowed to converge after final heal.
//   - MaxOvershoot: worst-case quota overshoot across partitioned writers before recount.
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
	diskLosses      = 3

	ConvergeBound     = 15 * time.Second
	offlineFor        = 60 * time.Second
	offlineOps        = 200
	offlineMaxGap     = 280 * time.Millisecond
	raceConverge      = 10 * time.Second
	quotaLimit        = 1_000
	quotaObject       = 100
	quotaOps          = 60
	quotaPartition    = 20 * time.Second
	quotaWriters      = 4
	MaxOvershoot      = (quotaWriters - 1) * quotaLimit
	maxRecountRounds  = 6
	recountConvergeIn = 10 * time.Second
)

var regions = []env.Region{"region-a", "region-b", "region-c"}

var dstKeys = []string{"k0", "k1", "k2", "k3"}

type world struct {
	s           *sim.Sim
	ids         []env.NodeID
	or          *oracle
	history     map[string][]occ.ETag
	hooked      map[env.NodeID]int
	extra       func(id env.NodeID, key string, winner, loser occ.Version)
	acks        int
	refused     int
	byNode      map[env.NodeID][]ack
	convergedIn time.Duration
	tally       map[string]int
	discards    uint64
	overshoot   int64
	rounds      int
	won, lost   int
	retired     uint64
	diskLosses  int
	maxCtx      int
	maxState    int
}

var lastWorld *world

func newWorld(s *sim.Sim) *world {
	w := &world{s: s, or: newOracle(), history: map[string][]occ.ETag{}, hooked: map[env.NodeID]int{}, byNode: map[env.NodeID][]ack{}}
	lastWorld = w
	return w
}

// tracked wraps an occ.Node in DST runs to report every decoded delta, push,
// and digest reply actually processed by the node to the independent oracle.
type tracked struct {
	*occ.Node
	w  *world
	id env.NodeID
}

func (t *tracked) Unwrap() *occ.Node { return t.Node }

func (t *tracked) observe(kind byte, b []byte) {
	if len(b) == 0 || b[0] != kind || !t.Node.Ready() {
		return
	}
	if d, err := occ.Decode(b[1:]); err == nil {
		t.w.or.reach(string(t.id), d)
	}
}

func (t *tracked) OnDatagram(from env.NodeID, msg []byte) {
	t.Node.OnDatagram(from, msg)
	t.observe(occ.MsgDelta, msg)
}

func (t *tracked) OnRequest(from env.NodeID, req []byte, reply env.Reply) {
	t.Node.OnRequest(from, req, reply)
	t.observe(occ.MsgPush, req)
}

type trackedNet struct {
	env.Transport
	t *tracked
}

func (n trackedNet) Request(to env.NodeID, req []byte, timeout time.Duration, cb func([]byte, error)) {
	n.Transport.Request(to, req, timeout, func(reply []byte, err error) {
		cb(reply, err)
		if err == nil {
			n.t.observe(occ.ReplyState, reply)
		}
	})
}

func (w *world) factory(ids []env.NodeID) sim.Factory {
	return func(e env.Env) env.Process {
		id := e.ID
		t := &tracked{w: w, id: id}
		e.Net = trackedNet{Transport: e.Net, t: t}
		t.Node = occ.NewNode(e, occ.Config{
			Membership: occ.StaticMembership(ids),
			OnDiscard: func(key string, winner, loser occ.Version) {
				w.or.discard(key, winner, loser)
				w.hooked[id]++
				if w.extra != nil {
					w.extra(id, key, winner, loser)
				}
			},
		})
		return t
	}
}

func (w *world) stateOf(id env.NodeID) (*occ.Doc, uint64) {
	if n := occNode(w.s, id); n != nil && n.Ready() && w.s.Status(id).Up {
		return n.Doc(), n.LWWDiscards()
	}
	b, ok := w.s.Disk(id).PeekDurable(occ.StateBlob)
	if !ok {
		return nil, 0
	}
	st, err := occ.DecodeStateBlob(b)
	if err != nil {
		return nil, 0
	}
	return st.Doc, st.Discards
}

const (
	lossWipeRejoin = iota
	lossStateRot
	lossCrashForGood
	lossKinds
)

func (w *world) loseDisk(id env.NodeID, kind int) {
	doc, discards := w.stateOf(id)
	w.or.diskLost(string(id), doc)
	w.retired += discards
	w.diskLosses++
	var err error
	switch kind {
	case lossWipeRejoin:
		err = w.s.WipeRejoin(id)
	case lossStateRot:
		if w.s.Status(id).Up {
			err = w.s.Crash(id)
		}
		if err == nil && w.s.BitRot(id, occ.StateBlob) != nil {
			err = w.s.WipeDisk(id)
		}
		if err == nil {
			err = w.s.Restart(id)
		}
	default:
		if w.s.Status(id).Up {
			err = w.s.Crash(id)
		}
		if err == nil {
			err = w.s.WipeDisk(id)
		}
	}
	if err != nil {
		w.s.Fail(fmt.Errorf("disk loss on %s: %w", id, err))
	}
}

func (w *world) scheduleDiskLosses(from, until env.Time, n int) {
	r := sim.NewStream(w.s.Seed(), "occ-test/disk-loss")
	for i := range n {
		at := from.Add(time.Duration(r.Int64N(int64(until.Sub(from)))))
		id := w.ids[r.IntN(len(w.ids))]
		kind := i % lossKinds
		w.s.At(at, "occ-test/disk-loss", func() { w.loseDisk(id, kind) })
	}
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

func (w *world) addNodes(n int, skew bool) []env.NodeID {
	ids := sim.NodeIDs(n)
	f := w.factory(ids)
	r := sim.NewStream(w.s.Seed(), "occ-test/skew")
	for i, id := range ids {
		spec := sim.NodeSpec{ID: id, Region: regions[i%len(regions)]}
		if skew {
			spec.Skew = time.Duration(r.Int64N(int64(2*maxNodeSkew))) - maxNodeSkew
			spec.DriftPPM = r.Int64N(2*maxNodeDriftPPM) - maxNodeDriftPPM
		}
		if err := w.s.AddNode(spec, f); err != nil {
			w.s.Fail(err)
		}
	}
	w.ids = ids
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
			out = append(out, n.Doc())
			continue
		}
		b, ok := w.s.Disk(id).PeekDurable(occ.StateBlob)
		if !ok {
			continue
		}
		st, err := occ.DecodeStateBlob(b)
		if err != nil {
			if slices.Contains(w.s.Disk(id).Damaged(), occ.StateBlob) {
				continue
			}
			return nil, fmt.Errorf("node %s durable state: %w", id, err)
		}
		out = append(out, st.Doc)
	}
	return out, nil
}

func (w *world) everyLossRecorded() error {
	docs, err := w.docs()
	if err != nil {
		return err
	}
	return w.or.check(docs, false)
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

func (w *world) write(n *occ.Node, id env.NodeID, key string, p occ.Precondition, del bool, value []byte) {
	held := n.Record(key)
	var v occ.Version
	var err error
	if del {
		v, err = n.Delete(key, p)
	} else {
		v, err = n.Put(key, value, p)
	}
	if err != nil {
		if !errors.Is(err, occ.ErrPreconditionFailed) && !errors.Is(err, occ.ErrNotFound) {
			w.s.Fail(fmt.Errorf("write %q on %s: unexpected %w", key, id, err))
		}
		if p.Holds(held) && !(del && !held.Live()) {
			w.s.Fail(fmt.Errorf("write %q refused although %s held", key, p.String()))
		}
		w.refused++
		return
	}
	if !p.Holds(held) {
		w.s.Fail(fmt.Errorf("write %q accepted although %s did not hold", key, p.String()))
	}
	w.history[key] = append(w.history[key], held.ETag())
	w.or.record(key, v, held)
	w.byNode[id] = append(w.byNode[id], ack{key: key, v: v})
	w.acks++
}

func current(rec occ.Record) occ.Precondition {
	if !rec.Present() {
		return occ.IfNoneMatch()
	}
	return occ.IfMatch(rec.ETag())
}

func (w *world) action(blind bool) func(int) {
	return func(i int) {
		r := w.s.WorkloadRand()
		id := w.ids[r.IntN(len(w.ids))]
		n := w.live(id)
		key := dstKeys[r.IntN(len(dstKeys))]
		op := r.IntN(100)
		val := fmt.Appendf(nil, "%s#%d", key, i)
		if n == nil {
			return
		}
		cur := current(n.Record(key))
		switch {
		case op < 50:
			w.write(n, id, key, cur, false, val)
		case op < 60:
			w.write(n, id, key, occ.IfNoneMatch(), false, val)
		case op < 70:
			w.write(n, id, key, cur, true, nil)
		case op < 78 && blind:
			w.write(n, id, key, occ.Unconditional(), false, val)
		case op < 88 && len(w.history[key]) > 0:
			h := w.history[key]
			w.write(n, id, key, occ.IfMatch(h[r.IntN(len(h))]), false, val)
		case blind:
			w.write(n, id, "scalar/"+key, occ.Unconditional(), false, val)
		default:
			w.write(n, id, key, cur, false, val)
		}
	}
}

func (w *world) finish(quiet env.Time, bound time.Duration) []*occ.Doc {
	if w.s.Err() != nil {
		return nil
	}
	if err := w.s.RunUntilConverged(quiet.Add(bound), w.converged); err != nil {
		return nil
	}
	w.convergedIn = w.s.Now().Sub(quiet)
	var docs []*occ.Doc
	var total uint64
	hooked := 0
	for _, id := range w.ids {
		n := occNode(w.s, id)
		docs = append(docs, n.Doc())
		total += n.LWWDiscards()
		hooked += w.hooked[id]
		if len(n.DiscardTail()) > occ.DiscardTailLen {
			w.s.Fail(errors.New("the discard tail is unbounded"))
		}
	}
	for i, d := range docs[1:] {
		if string(d.Encode()) != string(docs[0].Encode()) {
			w.s.Fail(fmt.Errorf("node %s holds different bytes after convergence", w.ids[i+1]))
		}
		for _, k := range docs[0].Keys() {
			if d.Record(k).ETag() != docs[0].Record(k).ETag() {
				w.s.Fail(fmt.Errorf("etag of %q differs on %s after convergence", k, w.ids[i+1]))
			}
		}
	}
	diskOnly := map[string]bool{}
	for _, a := range w.or.acks {
		if w.or.lostWithDisk(a.v.ID()) {
			diskOnly[a.key] = true
		}
	}
	for _, a := range w.or.acks {
		if !docs[0].Record(a.key).Present() && !diskOnly[a.key] {
			w.s.Fail(fmt.Errorf("acknowledged key %q has no version after convergence", a.key))
		}
	}
	if err := w.or.check(docs, true); err != nil {
		w.s.Fail(fmt.Errorf("after convergence: %w", err))
	}
	if total+w.retired != uint64(hooked) || hooked != w.or.logged {
		w.s.Fail(fmt.Errorf("the discard counters say %d (%d on lost disks), the hooks saw %d", total+w.retired, w.retired, hooked))
	}
	w.discards = total + w.retired
	for _, k := range docs[0].Keys() {
		if v, ok := docs[0].Record(k).Version(); ok {
			w.maxCtx = max(w.maxCtx, v.Context().Len())
		}
	}
	for _, id := range w.ids {
		if b, ok := w.s.Disk(id).PeekDurable(occ.StateBlob); ok {
			w.maxState = max(w.maxState, len(b))
		}
	}
	w.tally = w.or.tally(docs)
	return docs
}

func multiRegionRun(seed int64) *sim.Sim {
	s := dstSim(seed, dstNet())
	w := newWorld(s)
	w.addNodes(dstNodes, true)
	s.AddInvariant("occ/every-loss-recorded", invariantEvery, w.everyLossRecorded)
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
	w.scheduleDiskLosses(chaosStart, nem.QuietAt().Add(-time.Second), diskLosses)
	if err := s.RunUntil(nem.QuietAt()); err != nil {
		return s
	}
	w.finish(nem.QuietAt(), ConvergeBound)
	if w.diskLosses != diskLosses {
		s.Fail(fmt.Errorf("%d of %d disk losses ran", w.diskLosses, diskLosses))
	}
	return s
}

func logWorld(t *testing.T, w *world) {
	t.Logf("MEASURE acks=%d refused=%d discards=%d lost_versions=%d disk_losses=%d max_ctx=%d max_state=%d outcome=%v converged_in=%s events=%d won=%d discarded=%d overshoot=%d recount_rounds=%d",
		w.acks, w.refused, w.discards, len(w.or.lost), w.diskLosses, w.maxCtx, w.maxState, w.tally, w.convergedIn, w.s.Steps(), w.won, w.lost, w.overshoot, w.rounds)
}

// TestMultiRegionConvergenceDST proves 6-node, 3-region convergence under skew,
// drift, packet loss/duplication, nemesis faults, and 3 disk-loss kinds:
// byte-identical docs and ETags within ConvergeBound and every loss accounted for.
func TestMultiRegionConvergenceDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		simtest.RequireDeterministic(t, seed, multiRegionRun)
		if lastWorld.acks == 0 {
			t.Fatal("the workload acknowledged nothing")
		}
		logWorld(t, lastWorld)
	})
}

func (w *world) sideNode(side func(env.Region) bool) (env.NodeID, *occ.Node) {
	for _, id := range w.ids {
		if side(w.s.Region(id)) {
			if n := w.live(id); n != nil {
				return id, n
			}
		}
	}
	return "", nil
}

func offlineRun(seed int64) *sim.Sim {
	s := dstSim(seed, dstNet())
	w := newWorld(s)
	ids := w.addNodes(dstNodes, true)
	s.AddInvariant("occ/every-loss-recorded", invariantEvery, w.everyLossRecorded)
	offline := regions[2]
	isOffline := func(r env.Region) bool { return r == offline }
	isOnline := func(r env.Region) bool { return r != offline }
	healAt := chaosStart.Add(offlineFor)
	s.At(chaosStart, "partition region", func() {
		if err := s.PartitionRegion(offline); err != nil {
			s.Fail(err)
		}
	})
	s.Workload(chaosStart.Add(10*time.Millisecond), offlineOps, offlineMaxGap, w.action(false))
	contest := func(at time.Duration, key string, side func(env.Region) bool) {
		s.At(healAt.Add(-at), "contest "+key, func() {
			id, n := w.sideNode(side)
			if n == nil {
				s.Fail(errors.New("no live node on a side"))
				return
			}
			w.write(n, id, key, current(n.Record(key)), false, []byte("contest-"+string(id)))
		})
	}
	contest(2*time.Second, "k0", isOnline)
	contest(2*time.Second, "k1", isOffline)
	contest(time.Second, "k0", isOffline)
	contest(time.Second, "k1", isOnline)
	s.At(healAt, "heal", s.HealNet)
	if err := s.RunUntil(healAt); err != nil {
		return s
	}
	docs := w.finish(healAt, ConvergeBound)
	if s.Err() != nil {
		return s
	}
	final := docs[0]
	for _, id := range ids {
		if !isOffline(s.Region(id)) {
			continue
		}
		for _, a := range w.byNode[id] {
			why, ok := w.or.explain(a, docs, true)
			switch {
			case !ok:
				s.Fail(fmt.Errorf("the offline region's version %s of %q is gone with no record", short(a.v.ID()), a.key))
			case why == "current":
				w.won++
			case why == "discarded":
				w.lost++
			}
		}
	}
	if w.won == 0 || w.lost == 0 {
		s.Fail(fmt.Errorf("the offline region won %d keys and lost %d writes; the scenario must contest keys both ways", w.won, w.lost))
	}
	if v := value(final.Record("k0")); v[:8] != "contest-" || !isOffline(s.Region(env.NodeID(v[8:]))) {
		s.Fail(fmt.Errorf("the offline region's later write on k0 did not win: %q", v))
	}
	return s
}

// TestOfflineRegionDST proves that after one region edits conditionally offline
// for 60s and heals, every offline write either survives, is causally superseded,
// or is recorded as discarded, with both wins and losses occurring on every seed.
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
		w := newWorld(s)
		servers := []env.NodeID{"s1", "s2"}
		if !partitioned {
			servers = servers[:1]
		}
		w.ids = servers
		f := w.factory(servers)
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
		var acked []occ.VersionID
		for _, c := range []*raceClient{c1, c2} {
			switch {
			case c.err == nil:
				acked = append(acked, c.res.Version)
			case !errors.Is(c.err, occ.ErrPreconditionFailed) || !errors.Is(c.err, env.ErrRemote):
				s.Fail(fmt.Errorf("the losing writer got %w, want ErrPreconditionFailed over the wire", c.err))
			}
		}
		want := 1
		if partitioned {
			want = 2
		}
		if len(acked) != want {
			s.Fail(fmt.Errorf("%d writers won, want %d", len(acked), want))
			return s
		}
		if err := s.RunUntilConverged(healAt.Add(raceConverge), w.converged); err != nil {
			return s
		}
		final, _ := occNode(s, servers[0]).Record("k").Version()
		for _, id := range servers[1:] {
			if v, _ := occNode(s, id).Record("k").Version(); v.ID() != final.ID() {
				s.Fail(fmt.Errorf("server %s holds a different version", id))
			}
		}
		if !slices.Contains(acked, final.ID()) {
			s.Fail(errors.New("the surviving version is not an acknowledged racing write"))
		}
		for _, id := range acked {
			if id != final.ID() && !w.or.lost[id] {
				s.Fail(fmt.Errorf("the acknowledged racing write %s lost with no discard on any server's record", short(id)))
			}
		}
		if !partitioned && w.or.logged != 0 {
			s.Fail(errors.New("a race decided by a precondition recorded a discard"))
		}
		return s
	}
}

// TestConditionalWriteRaceDST proves two clients racing with the same ETag:
// on one node exactly one wins with zero discards; across a partition both are
// acknowledged, one wins everywhere after heal, and the other's discard is logged.
func TestConditionalWriteRaceDST(t *testing.T) {
	t.Run("two writers on one node: exactly one wins", func(t *testing.T) {
		simtest.Sweep(t, func(t *testing.T, seed int64) {
			simtest.RequireDeterministic(t, seed, raceRun(false))
		})
	})
	t.Run("two writers across a partition: both acknowledged, one survives, the other's discard is recorded", func(t *testing.T) {
		simtest.Sweep(t, func(t *testing.T, seed int64) {
			simtest.RequireDeterministic(t, seed, raceRun(true))
		})
	})
}

func quotaRun(seed int64) *sim.Sim {
	s := dstSim(seed, dstNet())
	w := newWorld(s)
	qk := occ.QuotaKey("b")
	pending := map[env.NodeID]bool{}
	contested := 0
	w.extra = func(id env.NodeID, key string, _, _ occ.Version) {
		if key == qk {
			pending[id] = true
			contested++
		}
	}
	ids := w.addNodes(quotaWriters, false)
	limit := occ.Usage{Bytes: quotaLimit}
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
		if _, err := n.UpdateQuota(qk, 3, occ.AddUsage(quotaObject, 1, limit)); err != nil {
			if !errors.Is(err, occ.ErrQuotaExceeded) && !errors.Is(err, occ.ErrPreconditionFailed) {
				s.Fail(err)
			}
			return
		}
		if _, err := n.PutObjectMeta("b", fmt.Sprintf("o%d", i), occ.ObjectMeta{Size: quotaObject}, occ.IfNoneMatch()); err != nil {
			s.Fail(err)
		}
	})
	healAt := chaosStart.Add(quotaPartition + time.Second)
	s.At(healAt, "heal", s.HealNet)
	if err := s.RunUntil(healAt); err != nil {
		return s
	}
	if w.finish(healAt, ConvergeBound) == nil {
		return s
	}
	if contested == 0 {
		s.Fail(errors.New("no discard of the quota key was recorded; the scenario did not contest the counter"))
		return s
	}
	for w.rounds = 0; len(pending) > 0; w.rounds++ {
		if w.rounds == maxRecountRounds {
			s.Fail(fmt.Errorf("recounts did not settle in %d rounds", maxRecountRounds))
			return s
		}
		wrote := false
		for _, id := range ids {
			if !pending[id] {
				continue
			}
			delete(pending, id)
			n := occNode(s, id)
			r, err := n.Reader()
			if err != nil {
				s.Fail(err)
				return s
			}
			_, did, err := n.RecountQuota(qk, 3, func(string) (occ.Usage, error) { return r.RecountObjects("b") })
			if err != nil {
				s.Fail(err)
				return s
			}
			wrote = wrote || did
		}
		if !wrote && len(pending) > 0 {
			s.Fail(errors.New("a recount round wrote nothing yet left work pending"))
			return s
		}
		if err := s.RunUntilConverged(s.Now().Add(recountConvergeIn), w.converged); err != nil {
			return s
		}
	}
	var truth occ.Usage
	for i, id := range ids {
		r, err := occNode(s, id).Reader()
		if err != nil {
			s.Fail(err)
			return s
		}
		u, _, err := r.ReadQuota(qk)
		count, cerr := r.RecountObjects("b")
		if err := errors.Join(err, cerr); err != nil {
			s.Fail(err)
			return s
		}
		if i == 0 {
			truth = count
		}
		if u != truth || count != truth {
			s.Fail(fmt.Errorf("node %s stores usage %v, the objects say %v", id, u, count))
		}
		if _, wrote, err := occNode(s, id).RecountQuota(qk, 1, func(string) (occ.Usage, error) { return r.RecountObjects("b") }); err != nil || wrote {
			s.Fail(fmt.Errorf("a recount after settling wrote (%v, %v)", wrote, err))
		}
	}
	w.discards = 0
	for _, id := range ids {
		w.discards += occNode(s, id).LWWDiscards()
	}
	w.overshoot = int64(truth.Bytes) - quotaLimit
	if w.overshoot > MaxOvershoot {
		s.Fail(fmt.Errorf("overshoot %d bytes is above the bound %d", w.overshoot, MaxOvershoot))
	}
	return s
}

// TestQuotaOvershootUnderPartitionDST proves that partitioned quota updates
// trigger a quota-key discard, subsequent RecountQuota rounds settle on true
// object usage with no further writes, and overshoot stays within MaxOvershoot.
func TestQuotaOvershootUnderPartitionDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		simtest.RequireDeterministic(t, seed, quotaRun)
		logWorld(t, lastWorld)
	})
}

func canaryTwoWriters(t *testing.T) error {
	or := newOracle()
	a, ca := newTestStore(t, "a", 1, 1)
	cb := &fakeClock{now: ca.now}
	cfg := storeConfig("b", 1, 2, cb)
	cfg.OnDiscard = or.discard
	b, err := occ.NewStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	base := mustPut(t, a, "k", "base", occ.IfNoneMatch())
	or.record("k", base, occ.Record{})
	mustMerge(t, b, a.Doc())
	ca.now += 20
	cb.now += 10
	held := a.Record("k")
	or.record("k", mustPut(t, a, "k", "left", occ.IfMatch(held.ETag())), held)
	or.record("k", mustPut(t, b, "k", "right", occ.IfMatch(held.ETag())), held)
	mustMerge(t, b, a.Doc())
	mustMerge(t, a, b.Doc())
	return or.check([]*occ.Doc{a.Doc(), b.Doc()}, true)
}

func canaryDST(t *testing.T) int {
	failed := 0
	for seed := int64(1); seed <= canarySeeds; seed++ {
		if multiRegionRun(seed).Err() != nil {
			failed++
		}
	}
	return failed
}

// TestCanaryDiscardNotRecorded plants the bug of suppressing all LWW discard
// recording and proves that both the two-writer check and multi-region DST fail.
func TestCanaryDiscardNotRecorded(t *testing.T) {
	if err := canaryTwoWriters(t); err != nil {
		t.Fatalf("the honest store must record the discard: %v", err)
	}
	restore := occ.SetCanaryUnrecordedDiscard(true)
	defer restore()
	if canaryTwoWriters(t) == nil {
		t.Fatal("the every-loss-recorded check did not catch a discard that was neither counted nor logged")
	}
	failed := canaryDST(t)
	if failed == 0 {
		t.Fatalf("the convergence DST passed on all %d seeds with unrecorded discards", canarySeeds)
	}
	t.Logf("canary: the convergence DST failed on %d of %d seeds with unrecorded discards", failed, canarySeeds)
}

// TestCanaryMergePrefersLocal plants the bug of keeping the local version on
// merge (arrival-order winner) and proves that merge laws and multi-region DST fail.
func TestCanaryMergePrefersLocal(t *testing.T) {
	a, b := occ.NewDoc(), occ.NewDoc()
	a.MergeVersion("k", version("a", 1, occ.VersionVector{}, false, "a", st(1, 0, "a")))
	b.MergeVersion("k", version("b", 1, occ.VersionVector{}, false, "b", st(2, 0, "b")))
	if err := checkLaws(a, b, occ.NewDoc()); err != nil {
		t.Fatal(err)
	}
	restore := occ.SetCanaryMergePrefersLocal(true)
	defer restore()
	if checkLaws(a, b, occ.NewDoc()) == nil {
		t.Fatal("the merge laws did not catch an arrival-order-dependent winner")
	}
	failed := canaryDST(t)
	if failed == 0 {
		t.Fatalf("the convergence DST passed on all %d seeds with a merge that prefers the local version", canarySeeds)
	}
	t.Logf("canary: the convergence DST failed on %d of %d seeds with a local-preferring merge", failed, canarySeeds)
}
