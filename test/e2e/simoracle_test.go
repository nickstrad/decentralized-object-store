package e2e

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/nodetest"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

type discardEntry struct {
	node          env.NodeID
	key           string
	winner, loser occ.Version
}

// simOracle runs event-driven safety checks on tier S: acknowledged-write
// lineage, deletion safety against any possible winner across all nodes,
// region-spread bounds on PUT, loss-declaration soundness, and post-grace
// orphan checks.
type simOracle struct {
	c        *simCluster
	failed   []string
	lineage  *nodetest.Lineage
	discards []discardEntry
	mans     map[storage.VersionID]*storage.Manifest
	declared []storage.VersionID
	deleted  map[storage.VersionID][]string
	refs     map[occ.VersionID]storage.VersionID
	spreadOK map[string]bool
	spreadN  int
	off      bool
	skipLoss bool
	residual map[string]bool
}

func newSimOracle(c *simCluster) *simOracle {
	return &simOracle{c: c, lineage: nodetest.NewLineage(), mans: map[storage.VersionID]*storage.Manifest{},
		deleted: map[storage.VersionID][]string{}, refs: map[occ.VersionID]storage.VersionID{}, spreadOK: map[string]bool{},
		residual: map[string]bool{}}
}

func (o *simOracle) fail(err error) {
	if o.off {
		return
	}
	o.failed = append(o.failed, err.Error())
	o.c.s.Fail(err)
}

func (o *simOracle) err() error {
	if len(o.failed) == 0 {
		return nil
	}
	return failf("%s", o.failed[0])
}

func manifestOf(key string, v occ.Version) *storage.Manifest {
	if v.Tombstone() || len(key) == 0 || occ.Kind(key[0]) != occ.KindManifest {
		return nil
	}
	man, err := node.ManifestOfRecord(v.Value())
	if err != nil {
		return nil
	}
	return man
}

// commit records every OCC commit into the lineage oracle, fails if a committed
// manifest or staged version already had a shard deleted, checks placement
// spread for new client PUTs, and validates any ~lost/ declaration.
func (o *simOracle) commit(id env.NodeID, key string, held occ.Record, v occ.Version) {
	o.lineage.Commit(id, key, held, v)
	if sv, ok := o.ref(key, v); ok && len(o.deleted[sv]) > 0 && !o.residual[key] {
		o.fail(fmt.Errorf("version %s committed as %q on %s after its shard %s was deleted", sv, key, id, o.deleted[sv][0]))
	}
	if man := manifestOf(key, v); man != nil {
		o.mans[man.Version] = man
		o.checkSpread(id, man)
	}
	o.checkLossDeclaration(id, key, v)
}

func (o *simOracle) discard(id env.NodeID, key string, w, l occ.Version) {
	o.discards = append(o.discards, discardEntry{node: id, key: key, winner: w, loser: l})
	o.lineage.Discard(l)
}

func (o *simOracle) settled(k *Call) {}

// notePut snapshots whether the writer's live view at PUT start had enough
// cross-region capacity (sum of min(members, m) >= k+m on an unrouted bucket)
// for checkSpread to enforce at most m shards per region.
func (o *simOracle) notePut(n *node.Node, bucket, key string) {
	o.spreadOK[string(n.ID())+"|"+bucket+"/"+key] = o.regionsAllow(n, bucket)
}

func (o *simOracle) regionsAllow(n *node.Node, bucket string) bool {
	if rd, err := n.OCC().Reader(); err == nil {
		if r, err := rd.Route(bucket); err == nil && r != "" {
			return false
		}
	}
	l := o.c.cfg.Layout
	sizes := map[env.Region]int{}
	for _, m := range n.Gossip().Members() {
		sizes[m.Region]++
	}
	sum := 0
	for _, s := range sizes {
		sum += min(s, l.M)
	}
	return sum >= l.K+l.M
}

func (o *simOracle) checkSpread(id env.NodeID, man *storage.Manifest) {
	tag := string(id) + "|" + man.Bucket + "/" + man.Key
	allow, ok := o.spreadOK[tag]
	if !ok {
		return
	}
	delete(o.spreadOK, tag)
	if !allow {
		return
	}
	o.spreadN++
	for s, st := range man.Stripes {
		per := map[env.Region]int{}
		for _, sh := range st.Shards {
			r := o.c.s.Region(sh.Node)
			per[r]++
			if per[r] > man.Layout.M {
				o.fail(fmt.Errorf("%s/%s version %s stripe %d places %d shards in region %s, more than m=%d, although the writer's view had room", man.Bucket, man.Key, man.Version, s, per[r], r, man.Layout.M))
				return
			}
		}
	}
}

// checkLossDeclaration verifies that whenever a node commits a ~lost/<ver>
// record, some stripe of that version genuinely holds fewer than k verifiable
// shards across its holders' disks.
func (o *simOracle) checkLossDeclaration(id env.NodeID, key string, v occ.Version) {
	if v.Tombstone() || len(key) == 0 || occ.Kind(key[0]) != occ.KindTag {
		return
	}
	_, parts, err := occ.ParseKey(key)
	if err != nil || !strings.HasPrefix(parts[2], "~lost/") {
		return
	}
	var ver storage.VersionID
	var man *storage.Manifest
	for vid, m := range o.mans {
		if vid.String() == strings.TrimPrefix(parts[2], "~lost/") && m.Bucket == parts[0] && m.Key == parts[1] {
			ver, man = vid, m
		}
	}
	if man == nil {
		o.fail(fmt.Errorf("%s declared %s of %s/%s lost, a version no node ever committed", id, parts[2], parts[0], parts[1]))
		return
	}
	o.declared = append(o.declared, ver)
	if o.skipLoss {
		return
	}
	if _, ok := o.unrecoverableStripe(man); !ok {
		o.fail(fmt.Errorf("%s declared %s/%s version %s lost while every stripe still had k=%d verifiable shards on its holders' disks", id, man.Bucket, man.Key, ver, man.Layout.K))
	}
}

func (o *simOracle) verifiable(man *storage.Manifest, stripe int) int {
	c := 0
	for i, ref := range man.Stripes[stripe].Shards {
		d := o.c.s.Disk(ref.Node)
		if d == nil {
			continue
		}
		blob, ok := d.Peek(man.ShardID(stripe, i).BlobName())
		if !ok {
			continue
		}
		if _, _, err := storage.DecodeShard(blob); err == nil && storage.BlobChecksum(blob) == ref.Checksum {
			c++
		}
	}
	return c
}

func (o *simOracle) unrecoverableStripe(man *storage.Manifest) (int, bool) {
	for s := range man.Stripes {
		if o.verifiable(man, s) < man.Layout.K {
			return s, true
		}
	}
	return 0, false
}

// watchDisk intercepts every simulated disk Delete to catch premature removal
// of any shard belonging to a version that can still win on some node.
type watchDisk struct {
	env.Disk
	id env.NodeID
	o  *simOracle
}

func (d watchDisk) Delete(name string) error {
	if sid, err := storage.ParseBlobName(name); err == nil {
		d.o.onDelete(d.id, name, sid.Version)
	}
	return d.Disk.Delete(name)
}

func (o *simOracle) ref(key string, v occ.Version) (storage.VersionID, bool) {
	if id, ok := o.refs[v.ID()]; ok {
		return id, id != 0
	}
	var out storage.VersionID
	if !v.Tombstone() && len(key) > 0 {
		switch occ.Kind(key[0]) {
		case occ.KindManifest:
			if man := manifestOf(key, v); man != nil {
				out = man.Version
			}
		case occ.KindTag:
			if _, parts, err := occ.ParseKey(key); err == nil && strings.HasPrefix(parts[2], "~stage/") {
				if man, err := node.ManifestOfStaged(v.Value()); err == nil {
					out = man.Version
				}
			}
		}
	}
	o.refs[v.ID()] = out
	return out, out != 0
}

func (o *simOracle) readers() []occ.Reader {
	var out []occ.Reader
	for _, id := range o.c.ids {
		n := o.c.anyNode(id)
		if n == nil {
			continue
		}
		if rd, err := n.OCC().Reader(); err == nil {
			out = append(out, rd)
		}
	}
	return out
}

// protector reports whether any node (up, down, or paused) still holds an
// unbeaten manifest or staged record referencing shard version v.
func (o *simOracle) protector(v storage.VersionID) (string, bool) {
	rds := o.readers()
	for _, rd := range rds {
		for _, k := range rd.Keys() {
			x, ok := rd.Record(k).Version()
			if !ok {
				continue
			}
			if got, ok := o.ref(k, x); !ok || got != v {
				continue
			}
			beaten := false
			for _, other := range rds {
				y, ok := other.Record(k).Version()
				if ok && y.ID() != x.ID() && (nodetest.Covers(y, x) || nodetest.LWWAfter(y, x)) {
					beaten = true
					break
				}
			}
			if !beaten {
				return k, true
			}
		}
	}
	return "", false
}

func (o *simOracle) onDelete(id env.NodeID, name string, v storage.VersionID) {
	o.deleted[v] = append(o.deleted[v], string(id)+"|"+name)
	if o.off {
		return
	}
	if k, ok := o.protector(v); ok && !o.residual[k] {
		o.fail(fmt.Errorf("node %s deleted shard %s of version %s, which %q can still make the winner", id, name, v, k))
	}
}

func (o *simOracle) checkAcks(rd occ.Reader) (map[string]int, error) {
	tally, err := o.lineage.Check(rd, nil)
	if err != nil {
		return tally, failf("%v", err)
	}
	return tally, nil
}

func (o *simOracle) lostOnRecord(man *storage.Manifest) bool {
	k := occ.TagKey(man.Bucket, man.Key, "~lost/"+man.Version.String())
	for _, n := range o.c.upNodes() {
		if rd, err := n.OCC().Reader(); err == nil && rd.Record(k).Live() {
			return true
		}
	}
	return false
}

// checkOrphans verifies that every shard blob remaining on any up node's disk
// is expected by a live manifest in converged metadata.
func (o *simOracle) checkOrphans() error {
	ref := o.c.upNodes()
	if len(ref) == 0 {
		return nil
	}
	rd, err := ref[0].OCC().Reader()
	if err != nil {
		return err
	}
	expect := map[string]bool{}
	for _, k := range rd.Keys() {
		val, _, err := rd.Get(k)
		if err != nil || len(k) == 0 || occ.Kind(k[0]) != occ.KindManifest {
			continue
		}
		man, err := node.ManifestOfRecord(val)
		if err != nil {
			return err
		}
		for s := range man.Stripes {
			for i, sh := range man.Stripes[s].Shards {
				expect[string(sh.Node)+"|"+man.ShardID(s, i).BlobName()] = true
			}
		}
	}
	var stray []string
	for _, id := range o.c.ids {
		if o.c.node(id) == nil {
			continue
		}
		for _, name := range o.c.s.Disk(id).Names() {
			if _, err := storage.ParseBlobName(name); err != nil {
				continue
			}
			if !expect[string(id)+"|"+name] {
				stray = append(stray, string(id)+":"+name)
			}
		}
	}
	slices.Sort(stray)
	if len(stray) > 0 {
		return failf("%d shards that no live manifest expects are still on disk after the grace periods; first %s", len(stray), stray[0])
	}
	return nil
}

type outcome struct {
	readable, lost int
}

// checkLiveReadableOrLost reads every live manifest through a random node after
// a heal and enforces the three-outcome rule: readable with valid bytes,
// declared lost on record (with an unrecoverable stripe), never silently
// unreadable.
func (c *simCluster) checkLiveReadableOrLost() (outcome, error) {
	var out outcome
	ns := c.upNodes()
	if len(ns) == 0 {
		return out, failf("no node is up")
	}
	rd, err := ns[0].OCC().Reader()
	if err != nil {
		return out, err
	}
	type pend struct {
		man  *storage.Manifest
		call *Call
	}
	var ps []pend
	for _, k := range rd.Keys() {
		if len(k) == 0 || occ.Kind(k[0]) != occ.KindManifest || !rd.Record(k).Live() {
			continue
		}
		val, _, err := rd.Get(k)
		if err != nil {
			continue
		}
		man, err := node.ManifestOfRecord(val)
		if err != nil {
			return out, failf("live manifest %q: %v", k, err)
		}
		at := pick(c, c.nodes)
		ps = append(ps, pend{man: man, call: c.Get(at, man.Bucket, man.Key)})
	}
	var calls []*Call
	for _, p := range ps {
		calls = append(calls, p.call)
	}
	if err := c.Wait(calls...); err != nil {
		return out, err
	}
	for _, p := range ps {
		switch {
		case p.call.Err == nil:
			out.readable++
		case c.o.lostOnRecord(p.man):
			out.lost++
		default:
			return out, failf("%s/%s version %s is live in metadata, unreadable through %s after a full heal, and declared lost on no record: %v", p.man.Bucket, p.man.Key, p.man.Version, p.call.At.ID, p.call.Err)
		}
	}
	for _, v := range c.o.declared {
		if man := c.o.mans[v]; man != nil {
			if _, ok := c.o.unrecoverableStripe(man); !ok && !c.o.skipLoss {
				return out, failf("version %s of %s/%s is declared lost but every stripe has k verifiable shards", v, man.Bucket, man.Key)
			}
		}
	}
	return out, nil
}

// finalChecks awaits convergence, verifies acknowledged-write lineage and live
// object readability, optionally waits out OrphanGrace+GCGrace to check for
// stray shards, and confirms the client ledger saw zero wrong bytes.
func (c *simCluster) finalChecks(grace bool) (map[string]int, outcome, error) {
	if _, err := c.Await("convergence after the heal", 60*time.Second, c.Converged); err != nil {
		return nil, outcome{}, err
	}
	rd, err := c.upNodes()[0].OCC().Reader()
	if err != nil {
		return nil, outcome{}, err
	}
	tally, err := c.o.checkAcks(rd)
	if err != nil {
		return tally, outcome{}, err
	}
	oc, err := c.checkLiveReadableOrLost()
	if err != nil {
		return tally, oc, err
	}
	if oc.readable+oc.lost == 0 {
		return tally, oc, failf("final check found 0 live objects")
	}
	if grace {
		if err := c.Sleep(c.cfg.OrphanGrace + c.cfg.GCGrace + 10*time.Second); err != nil {
			return tally, oc, err
		}
		if err := c.o.checkOrphans(); err != nil {
			return tally, oc, err
		}
	}
	return tally, oc, c.ledger.Wrong()
}
