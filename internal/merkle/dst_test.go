package merkle_test

import (
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/merkle"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
)

const (
	dstChunks       = 48
	dstMinChunk     = 64
	dstMaxChunk     = 1024
	dstPeriod       = 500 * time.Millisecond
	dstTimeout      = 250 * time.Millisecond
	dstScrubBatch   = 16
	dstWriteStart   = env.Time(time.Second)
	dstWriteWindow  = 8 * time.Second
	dstRotPhases    = 3
	dstRotChaos     = 4 * time.Second
	dstRotHeal      = 2 * time.Second
	dstRotMaxGap    = 400 * time.Millisecond
	dstInvariantGap = 10

	BitRotRepairBound    = 20 * time.Second
	BitRotConvergeBound  = 25 * time.Second
	RepairCopiesPerRot   = 4
	MissedWriteBound     = 20 * time.Second
	PartitionLength      = 5 * time.Second
	LossPPM              = 200_000
	LossConvergeBound    = 60 * time.Second
	LyingConvergeBound   = 30 * time.Second
	LoneLiarRunFor       = 20 * time.Second
	lyingInvariantGap    = 50
	measureEnv           = "MERKLE_MEASURE"
	dstLiarKnownFraction = 2
)

type world struct {
	s        *sim.Sim
	ids      []env.NodeID
	honest   []env.NodeID
	depth    int
	want     map[string][]byte
	keys     []string
	root     merkle.Hash
	dirty    bool
	writesAt env.Time

	rotFirst   map[string]env.Time
	maxRepair  time.Duration
	repairs    int
	rotEvents  int
	rotBytes   uint64
	corruptErr error
	convergeAt env.Time
	quietAt    env.Time
}

func newWorld(s *sim.Sim, depth int) *world {
	return &world{s: s, depth: depth, want: map[string][]byte{}, rotFirst: map[string]env.Time{}}
}

func (w *world) workerConfig() merkle.Config {
	return merkle.Config{Depth: w.depth, Period: dstPeriod, Timeout: dstTimeout, ScrubBatch: dstScrubBatch}
}

func (w *world) expectedRoot() merkle.Hash {
	if w.dirty {
		entries := make([]merkle.Entry, 0, len(w.want))
		for _, k := range w.keys {
			entries = append(entries, merkle.Entry{Key: k, Hash: merkle.Sum(w.want[k])})
		}
		b, err := merkle.Build(w.depth, entries)
		if err != nil {
			w.s.Fail(err)
			return merkle.Hash{}
		}
		w.root, w.dirty = b.Root(), false
	}
	return w.root
}

func (w *world) converged() bool {
	root := w.expectedRoot()
	for _, id := range w.honest {
		st := storeOf(w.s, id)
		if st == nil || st.w == nil || st.w.Tree().Root() != root {
			return false
		}
	}
	for _, id := range w.honest {
		d := w.s.Disk(id)
		if len(d.Damaged()) > 0 {
			return false
		}
		for _, k := range w.keys {
			b, ok := d.Peek(chunkPrefix + k)
			if !ok || merkle.Sum(b).String() != k {
				return false
			}
		}
	}
	return true
}

func (w *world) writes(count int, start env.Time, window time.Duration) {
	w.writesAt = start.Add(window)
	w.s.Workload(start, count, window/time.Duration(count), func(i int) {
		r := w.s.WorkloadRand()
		data := make([]byte, dstMinChunk+r.IntN(dstMaxChunk-dstMinChunk+1))
		for j := range data {
			data[j] = byte(r.Uint32())
		}
		binary.BigEndian.PutUint64(data, uint64(i))
		var live []env.NodeID
		for _, id := range w.honest {
			if storeOf(w.s, id) != nil {
				live = append(live, id)
			}
		}
		if len(live) == 0 {
			return
		}
		st := storeOf(w.s, live[r.IntN(len(live))])
		if st.ClientWrite(data) != nil {
			return
		}
		k := merkle.Sum(data).String()
		if _, ok := w.want[k]; !ok {
			w.keys = append(w.keys, k)
			slices.Sort(w.keys)
		}
		w.want[k] = data
		w.dirty = true
	})
}

func (w *world) trackRot() {
	w.s.AddInvariant("merkle/rot-repair-time", dstInvariantGap, w.scanRot)
}

func (w *world) scanRot() error {
	{
		now := w.s.Now()
		seen := map[string]bool{}
		for _, id := range w.honest {
			for _, b := range w.s.Disk(id).Damaged() {
				k := string(id) + "|" + b
				seen[k] = true
				if _, ok := w.rotFirst[k]; !ok {
					w.rotFirst[k] = now
				}
			}
		}
		for _, k := range sortedTimes(w.rotFirst) {
			if seen[k] {
				if d := now.Sub(w.rotFirst[k]); d > BitRotRepairBound {
					return fmt.Errorf("%s still damaged after %v", k, d)
				}
				continue
			}
			if d := now.Sub(w.rotFirst[k]); d > w.maxRepair {
				w.maxRepair = d
			}
			w.repairs++
			delete(w.rotFirst, k)
		}
		return nil
	}
}

func sortedTimes(m map[string]env.Time) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (w *world) guardContent(every int) {
	w.s.AddInvariant("merkle/honest-content-valid", every, func() error {
		for _, id := range w.honest {
			d := w.s.Disk(id)
			for _, name := range d.Names() {
				if !strings.HasPrefix(name, chunkPrefix) {
					continue
				}
				b, ok := d.Peek(name)
				if ok && merkle.Sum(b).String() != strings.TrimPrefix(name, chunkPrefix) {
					return fmt.Errorf("node %s holds invalid bytes under %s", id, name)
				}
			}
		}
		return nil
	})
}

func (w *world) stats() []merkle.Stats {
	var out []merkle.Stats
	for _, id := range w.honest {
		if st := storeOf(w.s, id); st != nil && st.w != nil {
			out = append(out, st.w.Stats())
		}
	}
	return out
}

func (w *world) fetchedBytes() uint64 {
	var n uint64
	for _, id := range w.honest {
		if st := storeOf(w.s, id); st != nil {
			n += st.fetched
		}
	}
	return n
}

func codes() *env.CodeTable {
	t, err := env.NewCodeTable(merkle.CodeEntries()...)
	if err != nil {
		panic(err)
	}
	return t
}

func newSim(seed int64) *sim.Sim {
	return sim.New(sim.Config{Seed: seed, Codes: codes()})
}

func (w *world) cluster(n int, replicate bool, wrap func(id env.NodeID, t env.Transport) env.Transport) error {
	w.ids = sim.NodeIDs(n)
	w.honest = w.ids
	return w.s.AddCluster(w.ids, 3, func(e env.Env) env.Process {
		cfg := storeConfig{peers: w.ids, worker: w.workerConfig(), replicate: replicate}
		if wrap != nil {
			id := e.ID
			cfg.wrapNet = func(t env.Transport) env.Transport { return wrap(id, t) }
		}
		return storeFactory(cfg)(e)
	})
}

func (w *world) finish(deadline env.Time) {
	if err := w.s.RunUntilConverged(deadline, w.converged); err != nil {
		return
	}
	w.convergeAt = w.s.Now()
}

var worlds = map[*sim.Sim]*world{}

func remember(w *world) *sim.Sim {
	worlds[w.s] = w
	return w.s
}

func take(s *sim.Sim) *world {
	w := worlds[s]
	clear(worlds)
	return w
}

func bitRotRun(nemesis bool) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		w := newWorld(newSim(seed), merkle.DepthFor(dstChunks))
		if err := w.cluster(3, true, nil); err != nil {
			w.s.Fail(err)
			return remember(w)
		}
		w.writes(dstChunks, dstWriteStart, dstWriteWindow)
		w.trackRot()
		quiet := w.writesAt
		var nem *sim.Nemesis
		if nemesis {
			var err error
			nem, err = w.s.StartNemesis(sim.NemesisConfig{
				Start:  dstWriteStart.Add(time.Second),
				Phases: dstRotPhases,
				Chaos:  dstRotChaos,
				Heal:   dstRotHeal,
				MaxGap: dstRotMaxGap,
				Kinds:  []sim.FaultKind{sim.FaultBitRot},
				Budget: sim.Budget{
					MinLive:            3,
					MaxDamagedDisks:    3,
					MaxDamagedPerGroup: 2,
					GroupOf: func(_ env.NodeID, blob string) string {
						if strings.HasPrefix(blob, chunkPrefix) {
							return blob
						}
						return ""
					},
				},
			})
			if err != nil {
				w.s.Fail(err)
				return remember(w)
			}
			quiet = max(quiet, nem.QuietAt())
		}
		w.quietAt = quiet
		if err := w.s.RunUntil(quiet); err != nil {
			return remember(w)
		}
		w.finish(quiet.Add(BitRotConvergeBound))
		if err := w.scanRot(); err != nil {
			w.s.Fail(err)
		}
		if nem != nil {
			for _, st := range nem.History() {
				if st.Fault.Kind == sim.FaultBitRot && st.Outcome == sim.OutcomeApplied {
					w.rotEvents++
					w.rotBytes += uint64(len(w.want[strings.TrimPrefix(st.Fault.Blob, chunkPrefix)]))
				}
			}
		}
		return remember(w)
	}
}

func TestBitRotRepairDST(t *testing.T) {
	t.Run("rot during traffic is detected and repaired", func(t *testing.T) {
		simtest.Sweep(t, func(t *testing.T, seed int64) {
			s := simtest.RequireDeterministic(t, seed, bitRotRun(true))
			w := take(s)
			if w.convergeAt == 0 {
				t.Fatalf("replicas did not converge within %v of quiet", BitRotConvergeBound)
			}
			if len(w.rotFirst) != 0 {
				t.Fatalf("%d damaged blobs never repaired", len(w.rotFirst))
			}
			if w.rotEvents == 0 || w.repairs == 0 {
				t.Fatalf("no rot happened (events %d, repairs %d)", w.rotEvents, w.repairs)
			}
			if w.maxRepair > BitRotRepairBound {
				t.Fatalf("slowest repair %v over the bound %v", w.maxRepair, BitRotRepairBound)
			}
			moved := w.fetchedBytes()
			if moved > RepairCopiesPerRot*w.rotBytes {
				t.Fatalf("fetched %d bytes for %d rotted bytes", moved, w.rotBytes)
			}
			var rot uint64
			for _, st := range w.stats() {
				rot += st.Changed
			}
			if rot == 0 {
				t.Fatal("the scrub never saw changed bytes")
			}
			t.Logf("rot events %d rotted bytes %d fetched %d slowest repair %v converged %v after quiet",
				w.rotEvents, w.rotBytes, moved, w.maxRepair, w.convergeAt.Sub(w.quietAt))
		})
	})
	t.Run("without damage no chunk bytes move", func(t *testing.T) {
		simtest.Sweep(t, func(t *testing.T, seed int64) {
			s := simtest.RequireDeterministic(t, seed, bitRotRun(false))
			w := take(s)
			if w.convergeAt == 0 {
				t.Fatal("did not converge")
			}
			if n := w.fetchedBytes(); n != 0 {
				t.Fatalf("fetched %d bytes with no damage", n)
			}
		})
	})
}

func missedWriteRun(seed int64) *sim.Sim {
	w := newWorld(newSim(seed), merkle.DepthFor(dstChunks))
	if err := w.cluster(3, true, nil); err != nil {
		w.s.Fail(err)
		return remember(w)
	}
	r := w.s.WorkloadRand()
	cut := dstWriteStart.Add(time.Second + time.Duration(r.Int64N(int64(2*time.Second))))
	victim := w.ids[r.IntN(len(w.ids))]
	healAt := cut.Add(PartitionLength)
	w.s.At(cut, "isolate", func() {
		if err := w.s.Isolate(victim); err != nil {
			w.s.Fail(err)
		}
	})
	w.s.At(healAt, "heal", func() { w.s.HealNet() })
	w.writes(dstChunks, dstWriteStart, dstWriteWindow)
	w.quietAt = max(healAt, w.writesAt)
	if err := w.s.RunUntil(w.quietAt); err != nil {
		return remember(w)
	}
	w.finish(w.quietAt.Add(MissedWriteBound))
	return remember(w)
}

func TestMissedWriteRepairDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		s := simtest.RequireDeterministic(t, seed, missedWriteRun)
		w := take(s)
		if w.convergeAt == 0 {
			t.Fatalf("not converged within %v of heal", MissedWriteBound)
		}
		var repaired, replErrs uint64
		for _, st := range w.stats() {
			repaired += st.Repaired
		}
		for _, id := range w.honest {
			replErrs += storeOf(s, id).replErrs
		}
		if replErrs == 0 || repaired == 0 {
			t.Fatalf("the partition dropped %d replica writes and anti-entropy repaired %d", replErrs, repaired)
		}
		t.Logf("dropped %d repaired %d converged %v after heal", replErrs, repaired, w.convergeAt.Sub(w.quietAt))
	})
}

func lossRun(seed int64) *sim.Sim {
	w := newWorld(newSim(seed), merkle.DepthFor(dstChunks))
	if err := w.cluster(3, false, nil); err != nil {
		w.s.Fail(err)
		return remember(w)
	}
	for _, a := range w.ids {
		for _, b := range w.ids {
			if a != b {
				if err := w.s.SetLinkLoss(a, b, LossPPM); err != nil {
					w.s.Fail(err)
					return remember(w)
				}
			}
		}
	}
	w.writes(dstChunks, dstWriteStart, dstWriteWindow)
	w.quietAt = w.writesAt
	if err := w.s.RunUntil(w.quietAt); err != nil {
		return remember(w)
	}
	w.finish(w.quietAt.Add(LossConvergeBound))
	return remember(w)
}

func TestRepairUnderLossDST(t *testing.T) {
	simtest.Sweep(t, func(t *testing.T, seed int64) {
		s := simtest.RequireDeterministic(t, seed, lossRun)
		w := take(s)
		if w.convergeAt == 0 {
			t.Fatalf("not converged within %v under %d ppm loss", LossConvergeBound, LossPPM)
		}
		var failed, completed uint64
		for _, st := range w.stats() {
			inflight := uint64(0)
			if st.InFlight {
				inflight = 1
			}
			if st.Rounds != st.Completed+st.Failed+inflight {
				t.Fatalf("rounds %d != completed %d + failed %d + in flight %d", st.Rounds, st.Completed, st.Failed, inflight)
			}
			failed += st.Failed
			completed += st.Completed
		}
		if failed == 0 || completed == 0 {
			t.Fatalf("failed %d completed %d: loss did not bite", failed, completed)
		}
		t.Logf("rounds failed %d completed %d converged %v after the last write", failed, completed, w.convergeAt.Sub(w.quietAt))
	})
}

func lyingRun(seed int64) *sim.Sim {
	w := newWorld(newSim(seed), merkle.DepthFor(dstChunks))
	ids := sim.NodeIDs(4)
	w.ids, w.honest = ids, ids[:3]
	liarID := ids[3]
	for i, id := range ids {
		region := env.Region(fmt.Sprintf("r%d", i%3))
		var f sim.Factory
		if id == liarID {
			f = liarFactory(liarConfig{depth: w.depth, known: func() []merkle.Entry {
				var out []merkle.Entry
				for i, k := range w.keys {
					if i%dstLiarKnownFraction == 0 {
						out = append(out, merkle.Entry{Key: k, Hash: merkle.Sum(w.want[k])})
					}
				}
				return out
			}})
		} else {
			f = storeFactory(storeConfig{peers: ids, worker: w.workerConfig()})
		}
		if err := w.s.AddNode(sim.NodeSpec{ID: id, Region: region}, f); err != nil {
			w.s.Fail(err)
			return remember(w)
		}
	}
	w.guardContent(lyingInvariantGap)
	w.writes(dstChunks, dstWriteStart, dstWriteWindow)
	w.quietAt = w.writesAt
	if err := w.s.RunUntil(w.quietAt); err != nil {
		return remember(w)
	}
	w.finish(w.quietAt.Add(LyingConvergeBound))
	if err := w.s.RunFor(5 * time.Second); err != nil {
		return remember(w)
	}
	return remember(w)
}

func loneLiarRun(seed int64) *sim.Sim {
	w := newWorld(newSim(seed), merkle.DepthFor(dstChunks))
	ids := sim.NodeIDs(2)
	w.ids, w.honest = ids, ids[:1]
	var fake []merkle.Entry
	for i := 0; i < 10; i++ {
		h := merkle.Sum([]byte{byte(i)})
		fake = append(fake, merkle.Entry{Key: h.String(), Hash: h})
	}
	if err := w.s.AddNode(sim.NodeSpec{ID: ids[0], Region: "r0"}, storeFactory(storeConfig{peers: ids, worker: w.workerConfig()})); err != nil {
		w.s.Fail(err)
		return remember(w)
	}
	if err := w.s.AddNode(sim.NodeSpec{ID: ids[1], Region: "r1"}, liarFactory(liarConfig{depth: w.depth, known: func() []merkle.Entry { return fake }})); err != nil {
		w.s.Fail(err)
		return remember(w)
	}
	w.guardContent(1)
	if err := w.s.RunFor(LoneLiarRunFor); err != nil {
		return remember(w)
	}
	return remember(w)
}

func TestLyingPeerDST(t *testing.T) {
	t.Run("honest replicas converge beside a liar and never store its bytes", func(t *testing.T) {
		simtest.Sweep(t, func(t *testing.T, seed int64) {
			s := simtest.RequireDeterministic(t, seed, lyingRun)
			w := take(s)
			if w.convergeAt == 0 {
				t.Fatalf("honest replicas did not converge within %v", LyingConvergeBound)
			}
			var failed, invalid uint64
			for _, st := range w.stats() {
				if st.MaxRoundTrips > uint64(w.depth+1) {
					t.Fatalf("a round took %d round trips at depth %d", st.MaxRoundTrips, w.depth)
				}
				failed += st.Failed
				invalid += st.Invalid
			}
			if failed == 0 || invalid == 0 {
				t.Fatalf("the liar was never engaged: failed %d invalid %d", failed, invalid)
			}
		})
	})
	t.Run("a lone liar cannot plant bytes", func(t *testing.T) {
		simtest.Sweep(t, func(t *testing.T, seed int64) {
			s := simtest.RequireDeterministic(t, seed, loneLiarRun)
			take(s)
			st := storeOf(s, s.Nodes()[0])
			stats := st.w.Stats()
			if stats.Rejected == 0 {
				t.Fatalf("no fetched bytes were rejected: %+v", stats)
			}
			if stats.Repaired != 0 || len(s.Disk(s.Nodes()[0]).Names()) != 0 {
				t.Fatalf("the liar planted %d chunks", stats.Repaired)
			}
			if stats.MaxRoundTrips > uint64(merkle.DepthFor(dstChunks)+1) {
				t.Fatalf("a round took %d round trips", stats.MaxRoundTrips)
			}
		})
	})
}
