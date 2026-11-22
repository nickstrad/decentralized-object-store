package e2e

import (
	"bytes"
	"cmp"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

type commitEntry struct {
	key     string
	node    env.NodeID
	v       occ.Version
	content string
	client  bool
}

type discardEntry struct {
	node          env.NodeID
	key           string
	winner, loser occ.Version
}

type simOracle struct {
	c         *simCluster
	failed    []string
	commits   []commitEntry
	seen      map[occ.VersionID]map[occ.VersionID]bool
	seenAny   map[occ.VersionID]bool
	byContent map[string]occ.VersionID
	lost      map[occ.VersionID]bool
	discards  []discardEntry
	mans      map[storage.VersionID]*storage.Manifest
	declared  []storage.VersionID
	deleted   map[storage.VersionID][]string
	refs      map[occ.VersionID]storage.VersionID
	spreadOK  map[string]bool
	spreadN   int
	off       bool
	skipLoss  bool
	residual  map[string]bool
}

func newSimOracle(c *simCluster) *simOracle {
	return &simOracle{c: c, seen: map[occ.VersionID]map[occ.VersionID]bool{}, seenAny: map[occ.VersionID]bool{},
		byContent: map[string]occ.VersionID{}, lost: map[occ.VersionID]bool{}, mans: map[storage.VersionID]*storage.Manifest{},
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

func contentOf(key string, v occ.Version) string {
	man := manifestOf(key, v)
	if man == nil {
		return ""
	}
	return hex.EncodeToString(man.ContentSHA[:]) + fmt.Sprintf(":%d", man.Size)
}

func (o *simOracle) ancestors(key string, held occ.Record) map[occ.VersionID]bool {
	out := map[occ.VersionID]bool{}
	h, ok := held.Version()
	if !ok {
		return out
	}
	add := func(id occ.VersionID) {
		out[id] = true
		for a := range o.seen[id] {
			out[a] = true
		}
	}
	add(h.ID())
	if c := contentOf(key, h); c != "" {
		if id, ok := o.byContent[key+"\x00"+c]; ok {
			add(id)
		}
	}
	return out
}

func (o *simOracle) commit(id env.NodeID, key string, held occ.Record, v occ.Version) {
	anc := o.ancestors(key, held)
	for a := range anc {
		o.seenAny[a] = true
	}
	o.seen[v.ID()] = anc
	c := contentOf(key, v)
	if c != "" {
		if _, dup := o.byContent[key+"\x00"+c]; !dup {
			o.byContent[key+"\x00"+c] = v.ID()
		}
	}
	o.commits = append(o.commits, commitEntry{key: key, node: id, v: v, content: c})
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
	o.lost[l.ID()] = true
}

func (o *simOracle) settled(k *Call) {}

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

func lwwAfter(a, b occ.Version) bool {
	sa, sb := a.Stamp(), b.Stamp()
	if c := cmp.Compare(sa.Wall, sb.Wall); c != 0 {
		return c > 0
	}
	if c := cmp.Compare(sa.Logical, sb.Logical); c != 0 {
		return c > 0
	}
	if c := cmp.Compare(sa.Node, sb.Node); c != 0 {
		return c > 0
	}
	ia, ib := a.ID(), b.ID()
	return bytes.Compare(ia[:], ib[:]) > 0
}

func covers(x, w occ.Version) bool {
	return x.ID() != w.ID() && x.Clock().Get(w.Dot().Actor) >= w.Dot().Counter
}

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
				if ok && y.ID() != x.ID() && (covers(y, x) || lwwAfter(y, x)) {
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

func (o *simOracle) explain(e commitEntry, final occ.Record) string {
	if f, ok := final.Version(); ok {
		if f.ID() == e.v.ID() {
			return "current"
		}
		if e.content != "" && contentOf(e.key, f) == e.content {
			return "current-copy"
		}
	}
	switch {
	case o.seenAny[e.v.ID()]:
		return "superseded"
	case o.lost[e.v.ID()]:
		return "discarded"
	}
	return ""
}

func (o *simOracle) checkAcks(rd occ.Reader) (map[string]int, error) {
	tally := map[string]int{}
	for _, e := range o.commits {
		why := o.explain(e, rd.Record(e.key))
		if why == "" {
			id := e.v.ID()
			return tally, failf("acknowledged version %s of %q (written on %s) is not current, not superseded by a later write that held it, and named as a loser in no discard record",
				hex.EncodeToString(id[:4]), e.key, e.node)
		}
		tally[why]++
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
