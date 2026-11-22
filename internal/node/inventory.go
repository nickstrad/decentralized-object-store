package node

import (
	"errors"
	"slices"
	"strconv"
	"strings"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/merkle"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

const InventoryDepth = 2

type inventory struct {
	bucket     string
	actual     *merkle.Tree
	cursor     string
	full       bool
	rebuilding map[string]bool
	orphans    map[string]env.Time
	deadSince  map[string]env.Time
}

type expectedShard struct {
	man    *storage.Manifest
	stripe int
	idx    int
}

func isCorrupt(err error) bool { return errors.Is(err, storage.ErrCorrupt) }

func shardPrefix(bucket string) string { return "shards/" + bucket + "/" }

type liveRef struct {
	key  string
	etag occ.ETag
	man  *storage.Manifest
}

func (n *Node) expectedAll(rd occ.Reader) map[string]map[string]expectedShard {
	out := map[string]map[string]expectedShard{}
	n.orphaned = map[storage.VersionID]map[[2]int]bool{}
	n.liveRefs = n.liveRefs[:0]
	used := map[occ.ETag]objectRecord{}
	add := func(bucket string, m *storage.Manifest) {
		for s := range m.Stripes {
			for i, ref := range m.Stripes[s].Shards {
				if ref.Node != n.e.ID {
					continue
				}
				if out[bucket] == nil {
					out[bucket] = map[string]expectedShard{}
				}
				out[bucket][m.ShardID(s, i).BlobName()] = expectedShard{man: m, stripe: s, idx: i}
			}
		}
	}
	for _, k := range rd.Keys() {
		if len(k) == 0 {
			continue
		}
		switch occ.Kind(k[0]) {
		case occ.KindManifest:
			rec, etag, err := n.object(rd, k)
			if err != nil {
				continue
			}
			used[etag] = rec
			add(rec.Manifest.Bucket, rec.Manifest)
			n.liveRefs = append(n.liveRefs, liveRef{key: k, etag: etag, man: rec.Manifest})
		case occ.KindTag:
			_, parts, err := occ.ParseKey(k)
			if err != nil {
				continue
			}
			if strings.HasPrefix(parts[2], tagOrphan) {
				n.noteOrphan(rd, k, parts[2])
				continue
			}
			if !strings.HasPrefix(parts[2], tagStage) {
				continue
			}
			val, _, err := rd.Get(k)
			if err != nil {
				continue
			}
			st, err := decodeStaged(val)
			if err != nil {
				continue
			}
			rec, err := decodeObject(st.Object)
			if err != nil {
				continue
			}
			add(rec.Manifest.Bucket, rec.Manifest)
		}
	}
	n.cache = used
	return out
}

func (n *Node) localBuckets() []string {
	names, err := n.e.Disk.List("shards/")
	if err != nil {
		return nil
	}
	var out []string
	for _, name := range names {
		rest := strings.TrimPrefix(name, "shards/")
		if i := strings.IndexByte(rest, '/'); i > 0 {
			out = append(out, rest[:i])
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (n *Node) dead(rd occ.Reader, name string) bool {
	id, err := storage.ParseBlobName(name)
	if err != nil {
		return false
	}
	return n.losers[id.Version] || rd.Record(gcKey(id.Bucket, id.Version)).Live()
}

func (n *Node) runInventories(rd occ.Reader) {
	exp := n.expectedAll(rd)
	buckets := append(sortedKeys(exp), n.localBuckets()...)
	for _, b := range sortedKeys(n.inv) {
		buckets = append(buckets, b)
	}
	slices.Sort(buckets)
	buckets = slices.Compact(buckets)
	for _, b := range buckets {
		inv, ok := n.inv[b]
		if !ok {
			t, err := merkle.New(InventoryDepth)
			if err != nil {
				continue
			}
			inv = &inventory{bucket: b, actual: t, full: true, rebuilding: map[string]bool{}, orphans: map[string]env.Time{}, deadSince: map[string]env.Time{}}
			n.inv[b] = inv
			n.stats.InventoryStart++
		}
		files := n.checkInventory(rd, inv, exp[b])
		if len(exp[b]) == 0 && inv.actual.Len() == 0 && len(inv.rebuilding) == 0 && files == 0 {
			delete(n.inv, b)
			n.stats.InventoryStop++
		}
	}
	n.declareLost(rd)
}

func (n *Node) scrubShard(inv *inventory, name string) {
	id, err := storage.ParseBlobName(name)
	if err != nil {
		inv.actual.Delete(name)
		return
	}
	h, _, err := n.store.Store().Get(id)
	if err != nil {
		if isCorrupt(err) {
			n.stats.ScrubCorrupt++
		}
		inv.actual.Delete(name)
		return
	}
	n.stats.Scrubbed++
	if err := inv.actual.Set(name, merkle.Hash(h.Checksum)); err != nil {
		n.logf("inventory %s: %v", name, err)
	}
}

func (n *Node) checkInventory(rd occ.Reader, inv *inventory, exp map[string]expectedShard) int {
	names, err := n.e.Disk.List(shardPrefix(inv.bucket))
	if err != nil {
		return 1
	}
	union := append(slices.Clone(names), inv.actual.Keys()...)
	slices.Sort(union)
	union = slices.Compact(union)
	if inv.full {
		inv.full = false
		for _, name := range union {
			n.scrubShard(inv, name)
		}
	} else if len(union) > 0 {
		i, found := slices.BinarySearch(union, inv.cursor)
		if found {
			i++
		}
		for c := 0; c < min(n.cfg.InventoryScrub, len(union)); c++ {
			name := union[(i+c)%len(union)]
			n.scrubShard(inv, name)
			inv.cursor = name
		}
	}
	entries := make([]merkle.Entry, 0, len(exp))
	for _, name := range sortedKeys(exp) {
		es := exp[name]
		entries = append(entries, merkle.Entry{Key: name, Hash: merkle.Hash(es.man.Stripes[es.stripe].Shards[es.idx].Checksum)})
	}
	want, err := merkle.Build(InventoryDepth, entries)
	if err != nil {
		n.logf("inventory %s: %v", inv.bucket, err)
		return len(names)
	}
	res, err := merkle.Compare(want, inv.actual, merkle.Limits{})
	if err != nil {
		n.logf("inventory %s: %v", inv.bucket, err)
		return len(names)
	}
	for _, d := range res.Diffs {
		if d.HasLocal && (!d.HasRemote || d.Local != d.Remote) {
			v := exp[d.Key].man.Version
			if !inv.rebuilding[d.Key] && n.rebuilding < MaxRebuilds && !n.purged[v] && !n.dead(rd, d.Key) {
				n.rebuild(inv, d.Key, exp[d.Key])
			}
		}
	}
	now := n.e.Clock.Now()
	orphans, deadSince := map[string]env.Time{}, map[string]env.Time{}
	var orphanNow []string
	for _, name := range union {
		if _, ok := exp[name]; ok || inv.rebuilding[name] {
			continue
		}
		if n.dead(rd, name) {
			since, ok := inv.deadSince[name]
			if !ok {
				since = now
			}
			if now.Sub(since) >= n.cfg.GCGrace {
				n.collect(inv, name)
				continue
			}
			deadSince[name] = since
			continue
		}
		since, ok := inv.orphans[name]
		if !ok {
			since = now
		}
		if now.Sub(since) >= n.cfg.OrphanGrace {
			orphanNow = append(orphanNow, name)
			continue
		}
		orphans[name] = since
	}
	if len(orphanNow) > 0 && n.recordOrphans(inv.bucket, orphanNow) {
		for _, name := range orphanNow {
			n.collect(inv, name)
		}
	} else {
		for _, name := range orphanNow {
			orphans[name] = inv.orphans[name]
		}
	}
	inv.orphans, inv.deadSince = orphans, deadSince
	return len(names)
}

func (n *Node) recordOrphans(bucket string, names []string) bool {
	byVersion := map[storage.VersionID][]storage.ShardID{}
	for _, name := range names {
		id, err := storage.ParseBlobName(name)
		if err != nil {
			continue
		}
		byVersion[id.Version] = append(byVersion[id.Version], id)
	}
	for _, v := range sortedKeys(byVersion) {
		var w wbuf
		w.byte(recordVersion)
		w.uvarint(uint64(len(byVersion[v])))
		for _, id := range byVersion[v] {
			w.uvarint(uint64(id.Stripe))
			w.uvarint(uint64(id.Index))
		}
		k := bucketKey(bucket, tagOrphan+v.String()+"/"+string(n.e.ID))
		if _, err := n.occ.SetScalar(k, w.b); err != nil {
			n.logf("orphan record %s: %v", k, err)
			return false
		}
		n.RecordMeta(k)
		n.stats.OrphanRecords++
	}
	return true
}

func (n *Node) noteOrphan(rd occ.Reader, key, name string) {
	rest := strings.TrimPrefix(name, tagOrphan)
	i := strings.IndexByte(rest, '/')
	if i <= 0 {
		return
	}
	v, err := strconv.ParseUint(rest[:i], 16, 64)
	if err != nil {
		return
	}
	val, _, err := rd.Get(key)
	if err != nil {
		return
	}
	r := rbuf{b: val}
	if r.byte() != recordVersion {
		return
	}
	cnt := r.uvarint()
	if cnt > uint64(len(val)) {
		return
	}
	set := n.orphaned[storage.VersionID(v)]
	if set == nil {
		set = map[[2]int]bool{}
		n.orphaned[storage.VersionID(v)] = set
	}
	for range cnt {
		st, idx := r.uvarint(), r.uvarint()
		if r.err != nil {
			return
		}
		set[[2]int{int(st), int(idx)}] = true
	}
}

func lostByOrphans(man *storage.Manifest, gone map[[2]int]bool) bool {
	if len(gone) == 0 {
		return false
	}
	for s := range man.Stripes {
		c := 0
		for i := range man.Stripes[s].Shards {
			if gone[[2]int{s, i}] {
				c++
			}
		}
		if c > man.Layout.M {
			return true
		}
	}
	return false
}

func (n *Node) designated() bool {
	for _, p := range n.view.Peers() {
		if p < n.e.ID {
			return false
		}
	}
	return true
}

func (n *Node) declareLost(rd occ.Reader) {
	if len(n.orphaned) == 0 || !n.designated() {
		return
	}
	for _, r := range n.liveRefs {
		if !lostByOrphans(r.man, n.orphaned[r.man.Version]) {
			continue
		}
		lk := occ.TagKey(r.man.Bucket, r.man.Key, tagLost+r.man.Version.String())
		if _, err := n.occ.Delete(r.key, occ.IfMatch(r.etag)); err != nil {
			n.logf("declare %s/%s lost: %v", r.man.Bucket, r.man.Key, err)
			continue
		}
		n.RecordMeta(r.key)
		if _, err := n.occ.SetScalar(lk, []byte("orphan-collected")); err != nil {
			n.logf("loss record %s/%s: %v", r.man.Bucket, r.man.Key, err)
		} else {
			n.RecordMeta(lk)
		}
		n.markDead(r.man.Bucket, r.man.Version)
		n.usage(r.man.Bucket, -r.man.Size, -1)
		n.stats.LostDeclared++
		n.logf("object %s/%s version %s lost: more than %d shards of a stripe were collected as orphans", r.man.Bucket, r.man.Key, r.man.Version, r.man.Layout.M)
	}
}

func (n *Node) collect(inv *inventory, name string) {
	id, err := storage.ParseBlobName(name)
	if err != nil {
		return
	}
	if err := n.store.Store().Delete(id); err != nil && !isNotFound(err) {
		n.logf("collect %s: %v", name, err)
		return
	}
	inv.actual.Delete(name)
	n.stats.Collected++
	loser := n.losers[id.Version]
	if loser {
		n.stats.CollectedLoser++
	}
	if len(n.collected) >= CollectedLogLen {
		n.collected = n.collected[1:]
	}
	n.collected = append(n.collected, collectRecord{Bucket: id.Bucket, Version: id.Version, At: n.e.Clock.Now(), Loser: loser})
}

func (n *Node) rebuild(inv *inventory, name string, es expectedShard) {
	man := es.man
	codec, err := man.Layout.Codec()
	if err != nil {
		return
	}
	inv.rebuilding[name] = true
	n.rebuilding++
	total := codec.N()
	blobs := make([][]byte, total)
	want := make([][32]byte, total)
	for j, ref := range man.Stripes[es.stripe].Shards {
		want[j] = ref.Checksum
	}
	pending := 0
	finish := func() {
		delete(inv.rebuilding, name)
		n.rebuilding--
		if n.stopped {
			return
		}
		stripe := uint32(es.stripe)
		data, _, err := storage.DecodeStripe(codec, man.Version, stripe, man.Layout.StripeLen(man.Size, es.stripe), blobs, want)
		if err != nil {
			n.stats.RebuildFailures++
			return
		}
		enc, err := storage.EncodeStripe(codec, man.Version, stripe, data)
		if err != nil || storage.BlobChecksum(enc[es.idx]) != want[es.idx] || n.purged[man.Version] {
			n.stats.RebuildFailures++
			return
		}
		if err := n.store.Store().Put(man.ShardID(es.stripe, es.idx), enc[es.idx]); err != nil {
			n.stats.RebuildFailures++
			return
		}
		if err := inv.actual.Set(name, merkle.Hash(want[es.idx])); err != nil {
			n.logf("rebuild %s: %v", name, err)
		}
		n.stats.Rebuilds++
		n.stats.RebuiltBytes += uint64(len(enc[es.idx]))
	}
	net := chanNet{net: n.e.Net, ch: chStorage}
	for j, ref := range man.Stripes[es.stripe].Shards {
		if j == es.idx {
			continue
		}
		id := man.ShardID(es.stripe, j)
		if ref.Node == n.e.ID {
			if b, err := n.store.Store().GetBlob(id); err == nil {
				blobs[j] = b
			}
			continue
		}
		pending++
		net.Request(ref.Node, storage.EncodeShardRequest(storage.OpGetShard, id, nil), n.cfg.RequestTimeout, func(resp []byte, err error) {
			if err == nil {
				blobs[j] = resp
			}
			pending--
			if pending == 0 {
				finish()
			}
		})
	}
	if pending == 0 {
		n.later(finish)
	}
}

func (n *Node) serveStorage(from env.NodeID, body []byte, reply env.Reply) {
	op, id, blob, derr := storage.DecodeShardRequest(body)
	var sum [32]byte
	if derr == nil && op == storage.OpPutShard {
		sum = storage.BlobChecksum(blob)
	}
	n.store.OnRequest(from, body, func(resp []byte, err error) {
		if err == nil && derr == nil {
			if inv, ok := n.inv[id.Bucket]; ok {
				switch op {
				case storage.OpPutShard:
					if err := inv.actual.Set(id.BlobName(), merkle.Hash(sum)); err != nil {
						n.logf("inventory %s: %v", id.BlobName(), err)
					}
				case storage.OpDeleteShard:
					inv.actual.Delete(id.BlobName())
				}
			}
		}
		reply(resp, err)
	})
}

func (n *Node) purge(bucket string, versions []storage.VersionID) (int, error) {
	rd, err := n.occ.Reader()
	if err != nil {
		return 0, err
	}
	want := map[storage.VersionID]bool{}
	live := map[storage.VersionID]bool{}
	for _, o := range n.liveObjects(rd, bucket) {
		live[o.rec.Manifest.Version] = true
	}
	for _, v := range versions {
		if live[v] {
			n.stats.PurgeRefused++
			continue
		}
		want[v] = true
	}
	ids, err := n.store.Store().List(bucket)
	if err != nil {
		return 0, err
	}
	c := 0
	for _, id := range ids {
		if !want[id.Version] {
			continue
		}
		n.purged[id.Version] = true
		if err := n.store.Store().Delete(id); err != nil && !isNotFound(err) {
			return c, err
		}
		if inv, ok := n.inv[bucket]; ok {
			inv.actual.Delete(id.BlobName())
		}
		c++
		n.stats.Purged++
	}
	return c, nil
}

func (n *Node) deadVersions(rd occ.Reader, bucket string) []storage.VersionID {
	var out []storage.VersionID
	for _, t := range sysTags(rd, bucket, tagGC) {
		if v, err := strconv.ParseUint(t.rest, 16, 64); err == nil && t.object == "" {
			out = append(out, storage.VersionID(v))
		}
	}
	return out
}
