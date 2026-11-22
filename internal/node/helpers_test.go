package node_test

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

const (
	dstNodes   = 21
	dstRegions = 3
	dstStripe  = 4 << 10
	dstShards  = 4
)

func dstConfig() node.Config {
	return node.Config{
		Layout:         storage.Layout{K: 4, M: 2, StripeSize: dstStripe},
		RequestTimeout: 500 * time.Millisecond,
		GCGrace:        10 * time.Second,
		OrphanGrace:    10 * time.Minute,
		ScrubBatch:     16,
		MaintainPeriod: time.Second,
		RecountSettle:  3 * time.Second,
		RPCTimeout:     500 * time.Millisecond,
	}
}

type discardRec struct {
	key           string
	winner, loser occ.Version
}

// harness wires a simulated cluster of Nodes with three continuous oracles:
// ackOracle (per-key LWW lineage), gcOracle (independent watchDisk deletion
// safety), and onCommitLoss (converse loss-declaration disk check).
type harness struct {
	unmarked int
	s        *sim.Sim
	ids      []env.NodeID
	cfg      node.Config
	discards []discardRec
	acks     *ackOracle
	gc       *gcOracle
	mans     map[string]*storage.Manifest
	declared []string
}

func newHarness(seed int64, n int, cfg node.Config, net sim.NetConfig) (*harness, error) {
	table, err := node.CodeTable()
	if err != nil {
		return &harness{s: sim.New(sim.Config{Seed: seed})}, err
	}
	h := &harness{s: sim.New(sim.Config{Seed: seed, Codes: table, Net: net}), ids: sim.NodeIDs(n), acks: newAckOracle(), mans: map[string]*storage.Manifest{}}
	h.gc = newGCOracle(h)
	cfg.Seeds = []env.NodeID{h.ids[0], h.ids[1], h.ids[2]}
	sink := cfg.DiscardSink
	cfg.DiscardSink = func(key string, w, l occ.Version) {
		h.discards = append(h.discards, discardRec{key: key, winner: w, loser: l})
		h.acks.discard(l)
		if sink != nil {
			sink(key, w, l)
		}
	}
	commits := cfg.CommitSink
	h.cfg = cfg
	inner := func(e env.Env) env.Process {
		c := cfg
		id := e.ID
		c.CommitSink = func(key string, held occ.Record, v occ.Version) {
			h.acks.commit(id, key, held, v)
			h.gc.onCommit(key, v)
			h.onCommitLoss(id, key, v)
			if commits != nil {
				commits(key, held, v)
			}
		}
		return node.NewFactory(c)(e.WithDisk(watchDisk{Disk: e.Disk, id: e.ID, o: h.gc}))
	}
	if err := h.s.AddCluster(h.ids, dstRegions, inner); err != nil {
		return h, err
	}
	h.s.AddInvariant("node/no-shard-of-a-possible-winner-deleted", 500, h.gc.check)
	return h, nil
}

func (h *harness) node(id env.NodeID) *node.Node {
	n, _ := h.s.Process(id).(*node.Node)
	if n == nil || !h.s.Status(id).Up || h.s.Status(id).Paused {
		return nil
	}
	return n
}

func (h *harness) up() []*node.Node {
	var out []*node.Node
	for _, id := range h.ids {
		if n := h.node(id); n != nil {
			out = append(out, n)
		}
	}
	return out
}

func (h *harness) region(r string) []env.NodeID {
	var out []env.NodeID
	for _, id := range h.ids {
		if string(h.s.Region(id)) == r {
			out = append(out, id)
		}
	}
	return out
}

func (h *harness) pick(exclude ...env.NodeID) env.NodeID {
	w := h.s.WorkloadRand()
	for range 100 {
		id := h.ids[w.IntN(len(h.ids))]
		if h.node(id) != nil && !slices.Contains(exclude, id) {
			return id
		}
	}
	return h.ids[0]
}

func (h *harness) metaConverged() bool {
	var d [32]byte
	ns := h.up()
	if len(ns) == 0 {
		return false
	}
	for i, n := range ns {
		if !n.Ready() {
			return false
		}
		dg := n.OCC().Digest()
		if i == 0 {
			d = dg
		} else if dg != d {
			return false
		}
	}
	return true
}

func (h *harness) membershipAgrees() bool {
	var want []env.NodeID
	for _, id := range h.ids {
		if h.node(id) != nil {
			want = append(want, id)
		}
	}
	for _, n := range h.up() {
		var got []env.NodeID
		for _, m := range n.Gossip().Members() {
			got = append(got, m.ID)
		}
		if !slices.Equal(got, want) {
			return false
		}
	}
	return true
}

func (h *harness) converged() bool { return h.metaConverged() && h.membershipAgrees() }

func (h *harness) createBucket(at env.Time, spec storage.BucketSpec) {
	h.s.At(at, "create bucket", func() {
		if err := h.node(h.ids[0]).CreateBucket(spec); err != nil {
			h.s.Fail(fmt.Errorf("create bucket %s: %w", spec.Name, err))
		}
	})
}

func payload(w interface{ Uint32() uint32 }, size int) []byte {
	b := make([]byte, size)
	for i := range b {
		b[i] = byte(w.Uint32())
	}
	return b
}

func (h *harness) checkShardsReferenced(grace bool) error {
	live := map[storage.VersionID]map[string]bool{}
	ref := h.up()[0]
	rd, err := ref.OCC().Reader()
	if err != nil {
		return err
	}
	for _, k := range rd.Keys() {
		if len(k) == 0 || occ.Kind(k[0]) != occ.KindManifest {
			continue
		}
		val, _, err := rd.Get(k)
		if err != nil {
			continue
		}
		man, err := manifestOf(val)
		if err != nil {
			return err
		}
		for s := range man.Stripes {
			for i, sh := range man.Stripes[s].Shards {
				if live[man.Version] == nil {
					live[man.Version] = map[string]bool{}
				}
				live[man.Version][string(sh.Node)+"|"+man.ShardID(s, i).BlobName()] = true
			}
		}
	}
	var errs []error
	for _, id := range h.ids {
		if h.node(id) == nil {
			continue
		}
		for _, name := range h.s.Disk(id).Names() {
			sid, err := storage.ParseBlobName(name)
			if err != nil {
				continue
			}
			if !live[sid.Version][string(id)+"|"+name] && grace {
				if rd.Record(occ.TagKey(sid.Bucket, "", "~gc/"+sid.Version.String())).Live() {
					errs = append(errs, fmt.Errorf("shard %s of a dead version still on %s after the grace period", name, id))
				} else {
					h.unmarked++
				}
			}
		}
	}
	if err := h.gc.check(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func manifestOf(val []byte) (*storage.Manifest, error) { return node.ManifestOfRecord(val) }

func sweepSharded(t *testing.T, fn func(t *testing.T, seed int64)) {
	t.Helper()
	seeds := simtest.Seeds(t)
	const shards = 4
	for sh := range shards {
		var mine []int64
		for i, s := range seeds {
			if i%shards == sh {
				mine = append(mine, s)
			}
		}
		if len(mine) == 0 {
			continue
		}
		t.Run(fmt.Sprintf("shard=%d", sh), func(t *testing.T) {
			t.Parallel()
			for _, seed := range mine {
				t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) { fn(t, seed) })
			}
		})
	}
}

type measure struct {
	mu   sync.Mutex
	vals map[string][]int64
}

func (m *measure) add(name string, v int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.vals == nil {
		m.vals = map[string][]int64{}
	}
	m.vals[name] = append(m.vals[name], v)
}

func (m *measure) report(t *testing.T) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range m.vals {
			if !yield(k) {
				return
			}
		}
	}) {
		v := slices.Clone(m.vals[k])
		slices.Sort(v)
		t.Logf("MEASURE %s n=%d min=%d p50=%d p99=%d max=%d", k, len(v), v[0], v[len(v)/2], v[min(len(v)-1, len(v)*99/100)], v[len(v)-1])
	}
}

func occManifestKey(bucket, key string) string { return occ.ManifestKey(bucket, key) }

func (h *harness) lostOnRecord(man *storage.Manifest) bool {
	k := occ.TagKey(man.Bucket, man.Key, "~lost/"+man.Version.String())
	for _, n := range h.up() {
		if rd, err := n.OCC().Reader(); err == nil && rd.Record(k).Live() {
			return true
		}
	}
	return false
}

func (h *harness) checkLiveReadableOrLost(s *sim.Sim) {
	rd, err := h.up()[0].OCC().Reader()
	if err != nil {
		s.Fail(err)
		return
	}
	pending := 0
	for _, k := range rd.Keys() {
		if len(k) == 0 || occ.Kind(k[0]) != occ.KindManifest || !rd.Record(k).Live() {
			continue
		}
		val, _, err := rd.Get(k)
		if err != nil {
			continue
		}
		man, err := manifestOf(val)
		if err != nil {
			s.Fail(fmt.Errorf("live manifest %q: %w", k, err))
			return
		}
		from := h.pick()
		pending++
		h.node(from).GetObject(man.Bucket, man.Key, func(_ storage.ObjectInfo, _ []byte, err error) {
			pending--
			if err != nil && !h.lostOnRecord(man) {
				s.Fail(fmt.Errorf("%s/%s version %s is live in metadata, unreadable from %s and declared lost on no record: %w", man.Bucket, man.Key, man.Version, from, err))
			}
		})
	}
	if err := s.RunUntilConverged(s.Now().Add(30*time.Second), func() bool { return pending == 0 }); err != nil {
		s.Fail(fmt.Errorf("%d reads of live keys never completed: %w", pending, err))
	}
}

// onCommitLoss is the converse loss-declaration oracle: whenever any node
// commits a ~lost/<version> record, it inspects every manifest holder's disk
// (including paused or down nodes) and fails unless at least one stripe has
// fewer than k verifiable shards at that instant.
func (h *harness) onCommitLoss(id env.NodeID, key string, v occ.Version) {
	if len(key) == 0 || v.Tombstone() {
		return
	}
	switch occ.Kind(key[0]) {
	case occ.KindManifest:
		if man, err := manifestOf(v.Value()); err == nil {
			h.mans[man.Version.String()] = man
		}
	case occ.KindTag:
		_, parts, err := occ.ParseKey(key)
		if err != nil || !strings.HasPrefix(parts[2], "~lost/") {
			return
		}
		ver := strings.TrimPrefix(parts[2], "~lost/")
		h.declared = append(h.declared, ver)
		man := h.mans[ver]
		if man == nil {
			if rd, rerr := h.node(id).OCC().Reader(); rerr == nil {
				if val, _, gerr := rd.Get(occ.ManifestKey(parts[0], parts[1])); gerr == nil {
					if m, merr := manifestOf(val); merr == nil && m.Version.String() == ver {
						man = m
					}
				}
			}
		}
		if man == nil {
			h.s.Fail(fmt.Errorf("%s declared version %s of %s/%s lost, a version no node ever committed", id, ver, parts[0], parts[1]))
			return
		}
		if _, ok := h.unrecoverableStripe(man); !ok {
			h.s.Fail(fmt.Errorf("%s declared %s/%s version %s lost while every stripe still had at least k=%d verifiable shards on its holders' disks", id, man.Bucket, man.Key, ver, man.Layout.K))
		}
	}
}

func (h *harness) verifiableShards(man *storage.Manifest, stripe int) int {
	c := 0
	for i, ref := range man.Stripes[stripe].Shards {
		blob, ok := h.s.Disk(ref.Node).Peek(man.ShardID(stripe, i).BlobName())
		if !ok {
			continue
		}
		if _, _, err := storage.DecodeShard(blob); err == nil && storage.BlobChecksum(blob) == ref.Checksum {
			c++
		}
	}
	return c
}

func (h *harness) unrecoverableStripe(man *storage.Manifest) (int, bool) {
	for s := range man.Stripes {
		if h.verifiableShards(man, s) < man.Layout.K {
			return s, true
		}
	}
	return 0, false
}
