package e2e

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

type opKind uint8

const (
	opCrashNode opKind = iota
	opPauseNode
	opWipeNode
	opRestartNode
	opCutRegion
	opCutOneWay
	opIsolateNode
	opLossyLink
	opClogLink
	opRotShard
	opReadErrors
	opMigrate
	opKinds
)

var opNames = [...]string{"crash", "pause", "wipe", "restart", "cut-region", "cut-one-way", "isolate", "link-loss", "clog", "rot", "read-errors", "migrate"}

func (k opKind) String() string {
	if k < opKinds {
		return opNames[k]
	}
	return fmt.Sprintf("op(%d)", k)
}

func (k opKind) network() bool {
	return k == opCutRegion || k == opCutOneWay || k == opIsolateNode || k == opLossyLink || k == opClogLink
}

type chaosOp struct {
	kind opKind
	a, b int
	at   time.Duration
}

type cycle struct {
	chaos time.Duration
	ops   []chaosOp
}

type nemesisPlan struct {
	cycles []cycle
}

const (
	nemTick       = 250 * time.Millisecond
	nemMaxDown    = 3
	nemHealBound  = 60 * time.Second
	nemRepairWait = 60 * time.Second
	bucketW       = "w"
	bucketM       = "m"
)

type nemesisStats struct {
	applied, skipped, cycles        int
	puts, putsOK, gets, getsOK      int
	warmM, warmMOK, putsM, putsMOK  int
	availabilityGets                int
	discards, declared, migrated    int
	migratedObjects, migratedShards int
	ambiguousRecovered              int
	compensated                     int
	finalRegion                     string
	converge                        []time.Duration
	repair                          []time.Duration
	tally                           map[string]int
}

type nemesis struct {
	c                  *simCluster
	r                  *rand.Rand
	st                 nemesisStats
	pending            []*Call
	keys               []string
	last               map[string]*Call
	deleted            map[string]bool
	dirty              map[string]bool
	touched            map[env.NodeID]bool
	down               map[env.NodeID]bool
	paused             map[env.NodeID]bool
	netFault           bool
	stable             []*Call
	mig                *Call
	migCoord           Node
	migTo              string
	seenSagas          map[saga.ID]bool
	region             string
	seq                int
	warmM              int
	noGrace            bool
	loneWipe           bool
	forgetAmbiguousMig bool
	beforeCycle        func(n *nemesis, cycleIdx int) error
}

func newNemesis(c *simCluster) *nemesis {
	n := &nemesis{c: c, r: sim.NewStream(c.s.Seed(), "e2e/nemesis"), last: map[string]*Call{}, deleted: map[string]bool{},
		seenSagas: map[saga.ID]bool{}, region: c.regions[0]}
	for i := range 24 {
		n.keys = append(n.keys, fmt.Sprintf("k%02d", i))
	}
	return n
}

func (n *nemesis) entry(avoid bool) Node {
	var ok []Node
	for _, x := range n.c.nodes {
		id := env.NodeID(x.ID)
		if n.c.node(id) == nil || (avoid && n.touched[id]) {
			continue
		}
		ok = append(ok, x)
	}
	if len(ok) == 0 {
		return n.c.nodes[n.r.IntN(len(n.c.nodes))]
	}
	return ok[n.r.IntN(len(ok))]
}

func (n *nemesis) traffic() {
	c := n.c
	for range 1 + n.r.IntN(2) {
		key := n.keys[n.r.IntN(len(n.keys))]
		switch x := n.r.IntN(20); {
		case x < 8:
			n.seq++
			k := c.Put(n.entry(false), bucketW, key, randomBytes(n.r, 1+n.r.IntN(simStripe)), storage.Condition{})
			n.dirty[key] = true
			n.pending = append(n.pending, k)
		case x < 9:
			n.seq++
			k := c.Put(n.entry(false), bucketM, fmt.Sprintf("m%03d", n.seq), randomBytes(n.r, 1+n.r.IntN(simStripe/4)), storage.Condition{})
			n.pending = append(n.pending, k)
		case x < 11:
			n.dirty[key] = true
			n.pending = append(n.pending, c.PutTag(n.entry(false), bucketW, key, "t", fmt.Sprintf("v%d", n.seq), storage.Condition{}))
		case x < 12:
			n.dirty[key] = true
			n.pending = append(n.pending, c.Delete(n.entry(false), bucketW, key, storage.Condition{}))
		default:
			n.pending = append(n.pending, c.Get(n.entry(false), bucketW, key))
		}
	}
	if !n.netFault && len(n.stable) > 0 {
		k := n.stable[n.r.IntN(len(n.stable))]
		if !n.dirty[k.Key] {
			g := c.Get(n.entry(true), bucketW, k.Key)
			g.Size = -1
			n.pending = append(n.pending, g)
		}
	}
}

func allowedNemesisTransportError(err error) bool {
	if errors.Is(err, storage.ErrCorrupt) {
		return false
	}
	return errors.Is(err, env.ErrTimeout) ||
		errors.Is(err, env.ErrUnreachable) ||
		errors.Is(err, occ.ErrNotReady) ||
		errors.Is(err, node.ErrNotReady) ||
		errors.Is(err, storage.ErrNoSuchBucket)
}

func allowedNemesisError(k *Call) bool {
	if k.Err == nil {
		return true
	}
	if errors.Is(k.Err, storage.ErrCorrupt) {
		return false
	}
	switch k.Op {
	case "put":
		if allowedNemesisTransportError(k.Err) || errors.Is(k.Err, storage.ErrUnavailable) {
			return true
		}
		return k.Bucket == bucketM && errors.Is(k.Err, storage.ErrBusy)
	case "get":
		return allowedNemesisTransportError(k.Err) ||
			errors.Is(k.Err, storage.ErrUnavailable) ||
			errors.Is(k.Err, occ.ErrNotFound)
	case "tag":
		return allowedNemesisTransportError(k.Err) ||
			errors.Is(k.Err, occ.ErrNotFound)
	case "delete":
		if allowedNemesisTransportError(k.Err) || errors.Is(k.Err, occ.ErrNotFound) {
			return true
		}
		return k.Bucket == bucketM && errors.Is(k.Err, storage.ErrBusy)
	default:
		return false
	}
}

func (n *nemesis) settle() error {
	c := n.c
	if err := c.Wait(n.pending...); err != nil {
		return err
	}
	for _, k := range n.pending {
		switch k.Op {
		case "put":
			n.st.puts++
			if k.Bucket == bucketM {
				n.st.putsM++
			}
			if k.Err == nil {
				n.st.putsOK++
				if k.Bucket == bucketM {
					n.st.putsMOK++
				}
				if k.Bucket == bucketW {
					n.last[k.Key] = k
					delete(n.deleted, k.Key)
				}
			} else if !allowedNemesisError(k) {
				return failf("put %s/%s via %s: disallowed error: %v", k.Bucket, k.Key, k.At.ID, k.Err)
			}
		case "tag":
			if k.Err != nil && !allowedNemesisError(k) {
				return failf("tag %s/%s via %s: disallowed error: %v", k.Bucket, k.Key, k.At.ID, k.Err)
			}
		case "delete":
			if k.Err == nil {
				n.deleted[k.Key] = true
				delete(n.last, k.Key)
			} else if !allowedNemesisError(k) {
				return failf("delete %s/%s via %s: disallowed error: %v", k.Bucket, k.Key, k.At.ID, k.Err)
			}
		case "get":
			n.st.gets++
			if k.Err == nil {
				n.st.getsOK++
			} else if !allowedNemesisError(k) {
				return failf("get %s/%s via %s: disallowed error: %v", k.Bucket, k.Key, k.At.ID, k.Err)
			}
			if k.Size == -1 {
				n.st.availabilityGets++
				if k.Err != nil && !n.dirty[k.Key] && !errors.Is(k.Err, occ.ErrNotFound) {
					return failf("availability within the fault budget: get %s via untouched %s failed with no network fault in the cycle: %v", k.Key, k.At.ID, k.Err)
				}
			}
		default:
			return failf("unexpected pending op %q on %s/%s via %s (err=%v)", k.Op, k.Bucket, k.Key, k.At.ID, k.Err)
		}
	}
	n.pending = nil
	return c.ledger.Wrong()
}

func (n *nemesis) liveManifests() []*storage.Manifest {
	ns := n.c.upNodes()
	if len(ns) == 0 {
		return nil
	}
	var out []*storage.Manifest
	seen := map[storage.VersionID]bool{}
	for _, nd := range ns {
		rd, err := nd.OCC().Reader()
		if err != nil {
			continue
		}
		for _, k := range rd.Keys() {
			if len(k) == 0 || occ.Kind(k[0]) != occ.KindManifest {
				continue
			}
			v, ok := rd.Record(k).Version()
			if !ok {
				continue
			}
			if man := manifestOf(k, v); man != nil && !seen[man.Version] {
				seen[man.Version] = true
				out = append(out, man)
			}
		}
	}
	return out
}

func (n *nemesis) allows(node env.NodeID, blob string) bool {
	c := n.c
	l := c.cfg.Layout
	for _, man := range n.liveManifests() {
		for s, st := range man.Stripes {
			avail := 0
			for i, ref := range st.Shards {
				name := man.ShardID(s, i).BlobName()
				if ref.Node == node && (blob == "" || blob == name) {
					continue
				}
				if c.node(ref.Node) == nil || n.touched[ref.Node] && ref.Node != node {
					continue
				}
				d := c.s.Disk(ref.Node)
				b, ok := d.Peek(name)
				if !ok || storage.BlobChecksum(b) != ref.Checksum {
					continue
				}
				if _, _, err := storage.DecodeShard(b); err != nil {
					continue
				}
				avail++
			}
			if avail < l.K {
				return false
			}
		}
	}
	return true
}

func (n *nemesis) metadataElsewhere(id env.NodeID) bool {
	self := n.c.node(id)
	if self == nil {
		return false
	}
	rd, err := self.OCC().Reader()
	if err != nil {
		return false
	}
	var others []occ.Reader
	for _, x := range n.c.ids {
		if x == id {
			continue
		}
		if nd := n.c.node(x); nd != nil {
			if r, err := nd.OCC().Reader(); err == nil {
				others = append(others, r)
			}
		}
	}
	for _, k := range rd.Keys() {
		v, ok := rd.Record(k).Version()
		if !ok {
			continue
		}
		held := false
		for _, o := range others {
			if w, ok := o.Record(k).Version(); ok && w.ID() == v.ID() {
				held = true
				break
			}
		}
		if !held {
			return false
		}
	}
	return true
}

func (n *nemesis) apply(op chaosOp) error {
	c := n.c
	ids := c.ids
	id := ids[op.a%len(ids)]
	peer := ids[op.b%len(ids)]
	skip := func() error { n.st.skipped++; return nil }
	var f sim.Fault
	switch op.kind {
	case opCrashNode, opPauseNode, opWipeNode:
		if c.node(id) == nil || len(n.down)+len(n.paused) >= nemMaxDown || !n.allows(id, "") {
			return skip()
		}
		if op.kind == opWipeNode && ((n.mig != nil && n.migCoord.ID == string(id)) || n.hasActiveSaga(id)) {
			return skip()
		}
		if op.kind == opWipeNode && !n.loneWipe && !n.metadataElsewhere(id) {
			return skip()
		}
		f = sim.Fault{Kind: map[opKind]sim.FaultKind{opCrashNode: sim.FaultCrash, opPauseNode: sim.FaultPause, opWipeNode: sim.FaultWipeRejoin}[op.kind], Node: id}
		n.touched[id] = true
		switch op.kind {
		case opCrashNode:
			n.down[id] = true
		case opPauseNode:
			n.paused[id] = true
		}
	case opRestartNode:
		switch {
		case n.down[id]:
			delete(n.down, id)
			f = sim.Fault{Kind: sim.FaultRestart, Node: id}
		case n.paused[id]:
			delete(n.paused, id)
			f = sim.Fault{Kind: sim.FaultResume, Node: id}
		default:
			return skip()
		}
	case opCutRegion:
		f = sim.Fault{Kind: sim.FaultPartitionRegion, Region: env.Region(c.regions[op.a%len(c.regions)])}
	case opCutOneWay:
		f = sim.Fault{Kind: sim.FaultPartitionOneWay, Side: []env.NodeID{id}, Other: c.ids}
		if id == peer {
			return skip()
		}
		f.Other = slices.DeleteFunc(slices.Clone(c.ids), func(x env.NodeID) bool { return x == id })
	case opIsolateNode:
		f = sim.Fault{Kind: sim.FaultIsolate, Node: id}
	case opLossyLink:
		if id == peer {
			return skip()
		}
		f = sim.Fault{Kind: sim.FaultLinkLoss, Node: id, Peer: peer, PPM: uint32(100_000 + 100_000*(op.b%8))}
	case opClogLink:
		if id == peer {
			return skip()
		}
		f = sim.Fault{Kind: sim.FaultClog, Node: id, Peer: peer}
	case opRotShard, opReadErrors:
		d := c.s.Disk(id)
		var shards []string
		for _, name := range d.Names() {
			if _, err := storage.ParseBlobName(name); err == nil {
				shards = append(shards, name)
			}
		}
		if len(shards) == 0 || c.node(id) == nil {
			return skip()
		}
		blob := shards[op.b%len(shards)]
		if !n.allows(id, blob) {
			return skip()
		}
		n.touched[id] = true
		f = sim.Fault{Kind: sim.FaultBitRot, Node: id, Blob: blob}
		if op.kind == opReadErrors {
			f = sim.Fault{Kind: sim.FaultReadError, Node: id, Blob: blob, Count: 1 + op.b%4}
		}
	case opMigrate:
		if n.mig != nil {
			return skip()
		}
		coord := n.entry(true)
		if nd := c.anyNode(env.NodeID(coord.ID)); nd != nil {
			for _, sid := range nd.Engine().IDs() {
				n.seenSagas[sid] = true
			}
		}
		to := c.regions[(slices.Index(c.regions, n.region)+1+op.b%2)%len(c.regions)]
		n.migCoord = coord
		n.migTo = to
		n.mig = c.StartMigration(coord, bucketM, to)
		n.st.applied++
		return nil
	default:
		return skip()
	}
	if op.kind.network() {
		n.netFault = true
	}
	if err := c.s.Apply(f); err != nil {
		return skip()
	}
	n.st.applied++
	return nil
}

func (n *nemesis) fullyRepaired() (bool, error) {
	c := n.c
	for _, man := range n.liveManifests() {
		for s, st := range man.Stripes {
			for i, ref := range st.Shards {
				if c.node(ref.Node) == nil {
					continue
				}
				b, ok := c.s.Disk(ref.Node).Peek(man.ShardID(s, i).BlobName())
				if !ok || storage.BlobChecksum(b) != ref.Checksum {
					return false, nil
				}
			}
		}
	}
	return true, nil
}

func bucketShardsIn(c *simCluster, bucket, region string) (int, int, error) {
	ns := c.upNodes()
	if len(ns) == 0 {
		return 0, 0, failf("no node is up")
	}
	rd, err := ns[0].OCC().Reader()
	if err != nil {
		return 0, 0, err
	}
	objs, shards := 0, 0
	for _, k := range rd.Keys() {
		if len(k) == 0 || occ.Kind(k[0]) != occ.KindManifest || !rd.Record(k).Live() {
			continue
		}
		v, ok := rd.Record(k).Version()
		if !ok {
			continue
		}
		man := manifestOf(k, v)
		if man == nil || man.Bucket != bucket {
			continue
		}
		objs++
		for s, st := range man.Stripes {
			for i, ref := range st.Shards {
				if string(c.s.Region(ref.Node)) != region {
					continue
				}
				d := c.s.Disk(ref.Node)
				if d == nil {
					continue
				}
				if b, ok := d.Peek(man.ShardID(s, i).BlobName()); ok && storage.BlobChecksum(b) == ref.Checksum {
					shards++
				}
			}
		}
	}
	return objs, shards, nil
}

func (n *nemesis) findNewSaga(coord Node) (saga.ID, bool) {
	nd := n.c.node(env.NodeID(coord.ID))
	if nd == nil {
		return "", false
	}
	for _, id := range nd.Engine().IDs() {
		if !n.seenSagas[id] {
			return id, true
		}
	}
	return "", false
}

func (n *nemesis) hasActiveSaga(id env.NodeID) bool {
	nd := n.c.anyNode(id)
	if nd == nil {
		return false
	}
	eng := nd.Engine()
	for _, sid := range eng.IDs() {
		if st, err := eng.Status(sid); err == nil && !st.State.Terminal() {
			return true
		}
	}
	return false
}

func liveBucketRegs(c *simCluster, bucket string) []string {
	ns := c.upNodes()
	if len(ns) == 0 {
		return nil
	}
	rd, err := ns[0].OCC().Reader()
	if err != nil {
		return nil
	}
	var out []string
	for _, k := range rd.Keys() {
		if len(k) == 0 || occ.Kind(k[0]) != occ.KindTag || !rd.Record(k).Live() {
			continue
		}
		_, parts, err := occ.ParseKey(k)
		if err == nil && len(parts) == 3 && parts[0] == bucket && parts[1] == "" && strings.HasPrefix(parts[2], "~reg/") {
			out = append(out, strings.TrimPrefix(parts[2], "~reg/"))
		}
	}
	return out
}

func (n *nemesis) refreshRegion() error {
	ns := n.c.upNodes()
	if len(ns) == 0 {
		return failf("no node is up")
	}
	rd, err := ns[0].OCC().Reader()
	if err != nil {
		return err
	}
	r, err := rd.Route(bucketM)
	if err != nil {
		return failf("route of %s missing after convergence: %v", bucketM, err)
	}
	n.region = string(r)
	return nil
}

func (n *nemesis) finishMigration() error {
	c := n.c
	if n.mig == nil {
		return n.refreshRegion()
	}
	if err := c.Wait(n.mig); err != nil {
		return err
	}
	start := n.mig
	coord := n.migCoord
	wantTo := n.migTo
	prevRegion := n.region
	defer func() {
		n.mig = nil
		n.migTo = ""
	}()
	migID := ""
	if start.Err != nil {
		switch {
		case errors.Is(start.Err, env.ErrTimeout) || errors.Is(start.Err, env.ErrUnreachable):
			if n.forgetAmbiguousMig {
				return nil
			}
			sid, ok := n.findNewSaga(coord)
			if !ok {
				if regs := liveBucketRegs(c, bucketM); len(regs) > 0 {
					return failf("ambiguous migration start on %s left live registration %s without a saga in engine", coord.ID, regs[0])
				}
				return n.refreshRegion()
			}
			n.seenSagas[sid] = true
			n.st.ambiguousRecovered++
			migID = string(sid)
		case errors.Is(start.Err, storage.ErrBusy) || errors.Is(start.Err, storage.ErrUnavailable):
			return n.refreshRegion()
		default:
			return failf("start migration via %s: %v", start.At.ID, start.Err)
		}
	} else {
		migID = start.Mig.ID
		n.seenSagas[saga.ID(migID)] = true
		wantTo = start.Mig.To
	}
	mi, _, _, err := awaitMigration(c, bounds{migrate: nemHealBound}, coord, migID, nil)
	if err != nil {
		return err
	}
	if _, err := c.Await("convergence after migration", nemHealBound, c.Converged); err != nil {
		return err
	}
	if err := n.refreshRegion(); err != nil {
		return err
	}
	switch mi.State {
	case "completed":
		if n.region != wantTo {
			return failf("completed migration %s of %s left route on %s, want %s", migID, bucketM, n.region, wantTo)
		}
		n.st.migrated++
	case "compensated":
		if n.region != prevRegion {
			return failf("compensated migration %s of %s left route on %s, want %s", migID, bucketM, n.region, prevRegion)
		}
		n.st.compensated++
	default:
		return failf("migration %s ended in unexpected state %q", migID, mi.State)
	}
	_, err = c.Await("no shard of the migrated bucket outside its region", nemHealBound, func() (bool, error) {
		left, err := shardsOutside(c, bucketM, n.region)
		return len(left) == 0, err
	})
	if err != nil {
		return err
	}
	if mi.State == "completed" {
		objs, shards, err := bucketShardsIn(c, bucketM, n.region)
		if err != nil {
			return err
		}
		n.st.migratedObjects += objs
		n.st.migratedShards += shards
		if n.warmM > 0 && shards == 0 {
			return failf("completed migration of bucket %s moved 0 shards", bucketM)
		}
	}
	return nil
}

func (n *nemesis) run(plan nemesisPlan) error {
	c := n.c
	if len(plan.cycles) == 0 {
		return failf("nemesis plan has 0 cycles")
	}
	if err := createBucket(c, simBounds, storage.BucketSpec{Name: bucketW}); err != nil {
		return err
	}
	if err := createBucket(c, simBounds, storage.BucketSpec{Name: bucketM, Region: n.region}); err != nil {
		return err
	}
	if n.warmM > 0 {
		wr := sim.NewStream(c.s.Seed(), "e2e/nemesis-warm")
		var warm []*Call
		for i := range n.warmM {
			n.st.warmM++
			warm = append(warm, c.Put(c.nodes[i%len(c.nodes)], bucketM, fmt.Sprintf("warm%02d", i), randomBytes(wr, 4096), storage.Condition{}))
		}
		if err := c.Wait(warm...); err != nil {
			return err
		}
		for _, k := range warm {
			if k.Err != nil {
				return failf("fault-free warm-up put to routed bucket %s via %s failed: %v", bucketM, k.At.ID, k.Err)
			}
			n.st.warmMOK++
		}
		if _, err := c.Await("warm-up objects in "+bucketM+" replicated", nemHealBound, c.Converged); err != nil {
			return err
		}
	}
	for ci, cy := range plan.cycles {
		n.st.cycles++
		n.touched, n.down, n.paused, n.dirty, n.netFault = map[env.NodeID]bool{}, map[env.NodeID]bool{}, map[env.NodeID]bool{}, map[string]bool{}, false
		if n.beforeCycle != nil {
			if err := n.beforeCycle(n, ci); err != nil {
				return err
			}
		}
		n.stable = nil
		for _, k := range n.last {
			n.stable = append(n.stable, k)
		}
		slices.SortFunc(n.stable, func(a, b *Call) int { return compareStr(a.Key, b.Key) })
		ops := slices.Clone(cy.ops)
		slices.SortStableFunc(ops, func(a, b chaosOp) int { return int(a.at - b.at) })
		for t := time.Duration(0); t < cy.chaos; t += nemTick {
			for len(ops) > 0 && ops[0].at <= t {
				if err := n.apply(ops[0]); err != nil {
					return err
				}
				ops = ops[1:]
			}
			n.traffic()
			if err := c.Sleep(nemTick); err != nil {
				return err
			}
		}
		if err := c.apply(sim.Fault{Kind: sim.FaultHealAll}); err != nil {
			return err
		}
		if err := n.settle(); err != nil {
			return err
		}
		d, err := c.Await("convergence after the heal", nemHealBound, c.Converged)
		n.st.converge = append(n.st.converge, d)
		if err != nil {
			return err
		}
		if err := n.finishMigration(); err != nil {
			return err
		}
		d, err = c.Await("every shard of every live manifest back on its holder", nemRepairWait, n.fullyRepaired)
		n.st.repair = append(n.st.repair, d)
		if err != nil {
			return err
		}
		if _, err := c.Await("convergence after repair", nemHealBound, c.Converged); err != nil {
			return err
		}
		rd, err := c.upNodes()[0].OCC().Reader()
		if err != nil {
			return err
		}
		if n.st.tally, err = c.o.checkAcks(rd); err != nil {
			return err
		}
		n.st.declared = len(c.o.declared)
		if len(c.o.declared) > 0 {
			return failf("version %s declared lost inside the fault budget", c.o.declared[0])
		}
		if _, err := c.checkLiveReadableOrLost(); err != nil {
			return err
		}
	}
	if !n.noGrace {
		if err := c.Sleep(c.cfg.OrphanGrace + c.cfg.GCGrace + 10*time.Second); err != nil {
			return err
		}
		if err := c.o.checkOrphans(); err != nil {
			return err
		}
	}
	losers := map[occ.VersionID]bool{}
	for _, d := range c.o.discards {
		losers[d.loser.ID()] = true
	}
	n.st.discards = len(losers)
	n.st.declared = len(c.o.declared)
	n.st.finalRegion = n.region
	if n.st.puts+n.st.gets == 0 {
		return failf("nemesis ran 0 client puts and gets")
	}
	return c.ledger.Wrong()
}

func compareStr(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func randomPlan(r *rand.Rand, cycles int, chaos time.Duration) nemesisPlan {
	var p nemesisPlan
	for range cycles {
		cy := cycle{chaos: chaos}
		for range 2 + r.IntN(4) {
			cy.ops = append(cy.ops, chaosOp{kind: opKind(r.IntN(int(opKinds))), a: r.IntN(1 << 16), b: r.IntN(1 << 16), at: time.Duration(r.Int64N(int64(chaos)))})
		}
		for _, op := range slices.Clone(cy.ops) {
			if (op.kind == opCrashNode || op.kind == opPauseNode) && r.IntN(2) == 0 {
				cy.ops = append(cy.ops, chaosOp{kind: opRestartNode, a: op.a, at: op.at + time.Duration(r.Int64N(int64(chaos-op.at)+1))})
			}
		}
		p.cycles = append(p.cycles, cy)
	}
	return p
}

func planFromBytes(data []byte, maxCycles, maxOps int, chaos time.Duration) nemesisPlan {
	var p nemesisPlan
	if len(data) == 0 {
		return nemesisPlan{cycles: []cycle{{chaos: chaos}}}
	}
	cycles := 1 + int(data[0])%maxCycles
	data = data[1:]
	for range cycles {
		cy := cycle{chaos: chaos}
		for len(data) >= 4 && len(cy.ops) < maxOps {
			b0, b1, b2, b3 := data[0], data[1], data[2], data[3]
			data = data[4:]
			if b0 == 0xff {
				break
			}
			cy.ops = append(cy.ops, chaosOp{kind: opKind(b0 % byte(opKinds)), a: int(b1), b: int(b2), at: time.Duration(b3) * chaos / 256})
		}
		p.cycles = append(p.cycles, cy)
	}
	return p
}
