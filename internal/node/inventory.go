package node

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/merkle"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

// InventoryDepth is the Merkle trie depth used for local per-bucket shard
// inventories and migration root comparisons.
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

// expectedAll scans replicated manifests and staged migration records to build
// the map of shards this node should hold per bucket, refreshes n.absent from
// bucket-scoped absence records, and collects n.liveRefs for loss checks.
func (n *Node) expectedAll(rd occ.Reader) map[string]map[string]expectedShard {
	out := map[string]map[string]expectedShard{}
	n.absent = map[storage.VersionID]map[[2]int]map[env.NodeID]bool{}
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
			if strings.HasPrefix(parts[2], tagAbsent) {
				if parts[1] == "" && rd.Record(k).Live() {
					n.noteAbsent(parts[2])
				}
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

// dead reports whether name's storage version has death evidence: either this
// node observed it as an LWW loser or a live ~gc/<version> marker exists.
func (n *Node) dead(rd occ.Reader, name string) bool {
	id, err := storage.ParseBlobName(name)
	if err != nil {
		return false
	}
	return n.losers[id.Version] || rd.Record(gcKey(id.Bucket, id.Version)).Live()
}

// runInventories runs one scrub, expected-vs-actual Merkle diff, rebuild, and
// collection pass per active bucket, stops inventories with no expected shards
// and no local files remaining, and checks for unrecoverable live objects.
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
		} else if !isNotFound(err) {
			return
		}
		inv.actual.Delete(name)
		return
	}
	n.stats.Scrubbed++
	if err := inv.actual.Set(name, merkle.Hash(h.Checksum)); err != nil {
		n.logf("inventory %s: %v", name, err)
	}
}

// checkInventory scrubs up to InventoryScrub shards (or all shards on a full
// pass), compares the local verified tree against the expected manifest tree
// to trigger bounded rebuilds, and collects unreferenced disk files (including
// corrupt ones) via two paths:
//  1. Dead version: death evidence seen by this holder for at least GCGrace.
//  2. Orphan: no evidence, unreferenced for OrphanGrace; writes a per-shard
//     replicated ~orphan/<version>/<stripe>.<index>/<holder> record first and
//     deletes nothing if that write fails.
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
			if !inv.rebuilding[d.Key] && n.rebuilding < MaxRebuilds && !n.purged[v] && !n.dead(rd, d.Key) && !n.statedAbsent(rd, exp[d.Key].man.ShardID(exp[d.Key].stripe, exp[d.Key].idx)) {
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
		for _, id := range byVersion[v] {
			k := orphanKey(bucket, id, n.e.ID)
			if _, err := n.occ.SetScalar(k, orphanValue(id)); err != nil {
				n.logf("orphan record %s: %v", k, err)
				return false
			}
			n.RecordMeta(k)
			n.stats.OrphanRecords++
		}
	}
	return true
}

func orphanKey(bucket string, id storage.ShardID, holder env.NodeID) string {
	return bucketKey(bucket, tagOrphan+id.Version.String()+"/"+strconv.FormatUint(uint64(id.Stripe), 10)+"."+strconv.FormatUint(uint64(id.Index), 10)+"/"+string(holder))
}

func orphanValue(id storage.ShardID) []byte {
	var w wbuf
	w.byte(recordVersion)
	w.uvarint(1)
	w.uvarint(uint64(id.Stripe))
	w.uvarint(uint64(id.Index))
	return w.b
}

func absentKey(bucket string, id storage.ShardID, holder env.NodeID) string {
	return bucketKey(bucket, tagAbsent+id.Version.String()+"/"+strconv.FormatUint(uint64(id.Stripe), 10)+"."+strconv.FormatUint(uint64(id.Index), 10)+"/"+string(holder))
}

// noteAbsent parses a bucket-scoped ~absent/<version>/<stripe>.<index>/<holder>
// tag name (splitting into at most three parts so holder IDs containing "/"
// round-trip) and records it in n.absent.
func (n *Node) noteAbsent(name string) {
	parts := strings.SplitN(strings.TrimPrefix(name, tagAbsent), "/", 3)
	if len(parts) != 3 || parts[2] == "" {
		return
	}
	v, err := strconv.ParseUint(parts[0], 16, 64)
	if err != nil {
		return
	}
	st, idx, ok := strings.Cut(parts[1], ".")
	if !ok {
		return
	}
	s, err1 := strconv.ParseUint(st, 10, 32)
	i, err2 := strconv.ParseUint(idx, 10, 32)
	if err1 != nil || err2 != nil {
		return
	}
	byShard := n.absent[storage.VersionID(v)]
	if byShard == nil {
		byShard = map[[2]int]map[env.NodeID]bool{}
		n.absent[storage.VersionID(v)] = byShard
	}
	key := [2]int{int(s), int(i)}
	if byShard[key] == nil {
		byShard[key] = map[env.NodeID]bool{}
	}
	byShard[key][env.NodeID(parts[2])] = true
}

func (n *Node) statedAbsent(rd occ.Reader, id storage.ShardID) bool {
	return rd.Record(absentKey(id.Bucket, id, n.e.ID)).Live()
}

func (n *Node) liveVersion(rd occ.Reader, man *storage.Manifest) bool {
	rec, _, err := n.object(rd, occ.ManifestKey(man.Bucket, man.Key))
	return err == nil && rec.Manifest.Version == man.Version
}

// stateAbsent writes a binding, permanently retained absence record
// ~absent/<version>/<stripe>.<index>/<holder> only when all of the following
// hold in one loop step:
//   - the manifest is live in this holder's metadata (never a staged migration
//     copy), is neither purged nor dead, and has no existing absence record;
//   - a fresh local read finds no matching shard (only not found, corrupt, or
//     a checksum mismatch qualifies; a transient disk I/O error writes nothing);
//   - the just-finished rebuild received an answer (shard, not found, or
//     corrupt) from every other holder of the stripe and fewer than k verified.
//
// Once written, this holder refuses OpPutShard for this shard (ErrFenced) and
// skips rebuilding it. Residual limit: refusal relies on keeping this metadata;
// a disk wipe loses the local record until metadata sync restores it, and a
// sufficiently late shard write arriving in that window can be stored.
func (n *Node) stateAbsent(man *storage.Manifest, stripe, idx int, verifiable int) {
	id := man.ShardID(stripe, idx)
	rd, err := n.occ.Reader()
	if err != nil {
		return
	}
	if n.purged[man.Version] || n.dead(rd, id.BlobName()) || !n.liveVersion(rd, man) || n.statedAbsent(rd, id) {
		return
	}
	b, err := n.store.Store().GetBlob(id)
	if err == nil && storage.BlobChecksum(b) == man.Stripes[stripe].Shards[idx].Checksum {
		return
	}
	if err != nil && !isNotFound(err) && !isCorrupt(err) {
		return
	}
	k := absentKey(man.Bucket, id, n.e.ID)
	if _, err := n.occ.SetScalar(k, orphanValue(id)); err != nil {
		n.logf("absence record %s: %v", k, err)
		return
	}
	n.RecordMeta(k)
	n.stats.AbsentRecords++
	n.logf("shard %s: every holder of stripe %d answered and %d of %d shards verify, need %d; recorded that this holder holds none and will store none", id.BlobName(), stripe, verifiable, len(man.Stripes[stripe].Shards), man.Layout.K)
}

// unrecoverableStripe reports whether any stripe of man has more than m indices
// whose manifest-designated holder wrote an absence record. Records from
// non-holders do not count.
func (n *Node) unrecoverableStripe(man *storage.Manifest) (int, int, bool) {
	byShard := n.absent[man.Version]
	if len(byShard) == 0 {
		return 0, 0, false
	}
	for s := range man.Stripes {
		c := 0
		for i, ref := range man.Stripes[s].Shards {
			if byShard[[2]int{s, i}][ref.Node] {
				c++
			}
		}
		if c > man.Layout.M {
			return s, c, true
		}
	}
	return 0, 0, false
}

func (n *Node) designated() bool {
	for _, p := range n.view.Peers() {
		if p < n.e.ID {
			return false
		}
	}
	return true
}

// declareLost runs on the lowest-ID live node in its view and declares any
// live version lost once some stripe has more than m binding absence records
// from that stripe's holders. An unreachable holder, or one whose disk keeps
// failing reads with transient I/O errors, blocks new absence records and
// therefore blocks the decision; until every holder of the stripe is up, holds
// the manifest, can reach the others, and its disk answers (or forever if a
// holder is permanently gone), the key stays live and GET fails with
// storage.ErrUnavailable.
//
// The loss record ~lost/<version> is written before the manifest tombstone
// (IfMatch its ETag) so a crash between the two leaves a live key with a loss
// record that the next tick finishes, never a deleted key without one. After
// tombstoning, the version is marked dead (~gc/<version>) and usage is
// decremented. A concurrent newer write on another partition side competes with
// the tombstone under last-writer-wins (counted and logged).
func (n *Node) declareLost(rd occ.Reader) {
	if len(n.absent) == 0 || !n.designated() {
		return
	}
	for _, r := range n.liveRefs {
		stripe, gone, ok := n.unrecoverableStripe(r.man)
		if !ok {
			continue
		}
		lk := occ.TagKey(r.man.Bucket, r.man.Key, tagLost+r.man.Version.String())
		if lheld := rd.Record(lk); !lheld.Live() {
			lv, err := n.occ.SetScalar(lk, []byte("unrecoverable"))
			if err != nil {
				n.logf("loss record %s/%s: %v", r.man.Bucket, r.man.Key, err)
				continue
			}
			n.committed(lk, lheld, lv)
			n.RecordMeta(lk)
		}
		held := rd.Record(r.key)
		tv, err := n.occ.Delete(r.key, occ.IfMatch(r.etag))
		if err != nil {
			n.logf("declare %s/%s lost: %v", r.man.Bucket, r.man.Key, err)
			continue
		}
		n.committed(r.key, held, tv)
		n.RecordMeta(r.key)
		n.markDead(r.man.Bucket, r.man.Version)
		n.usage(r.man.Bucket, -r.man.Size, -1)
		n.stats.LostDeclared++
		n.logf("object %s/%s version %s lost: the holders of %d of the %d shards of stripe %d recorded that they hold none, fewer than k=%d remain", r.man.Bucket, r.man.Key, r.man.Version, gone, len(r.man.Stripes[stripe].Shards), stripe, r.man.Layout.K)
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

// rebuild fetches the other holders' shards for one missing or rotted local
// shard, decodes and re-encodes the stripe to restore index es.idx, and
// invokes stateAbsent only when every other holder answered (shard, not found,
// or corrupt) and fewer than k verified.
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
	answered := make([]bool, total)
	answered[es.idx] = true
	heard := func(j int, err error) {
		if err == nil || isNotFound(err) || isCorrupt(err) {
			answered[j] = true
		}
	}
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
			verifiable := 0
			for j, b := range blobs {
				if b == nil {
					continue
				}
				if _, _, derr := storage.DecodeShard(b); derr == nil && storage.BlobChecksum(b) == want[j] {
					verifiable++
				}
			}
			if b, rerr := n.store.Store().GetBlob(man.ShardID(es.stripe, es.idx)); rerr == nil && storage.BlobChecksum(b) == want[es.idx] {
				if serr := inv.actual.Set(name, merkle.Hash(want[es.idx])); serr != nil {
					n.logf("rebuild %s: %v", name, serr)
				}
				return
			}
			if verifiable < man.Layout.K && !slices.Contains(answered, false) {
				n.stateAbsent(man, es.stripe, es.idx, verifiable)
			}
			return
		}
		enc, err := storage.EncodeStripe(codec, man.Version, stripe, data)
		if err != nil || storage.BlobChecksum(enc[es.idx]) != want[es.idx] || n.purged[man.Version] {
			n.stats.RebuildFailures++
			return
		}
		// Re-check metadata before storing so an in-flight rebuild cannot write a
		// shard whose absence record, death marker, or purge arrived meanwhile.
		rd, err := n.occ.Reader()
		if err != nil || n.dead(rd, name) || n.statedAbsent(rd, man.ShardID(es.stripe, es.idx)) {
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
			b, err := n.store.Store().GetBlob(id)
			if err == nil {
				blobs[j] = b
			}
			heard(j, err)
			continue
		}
		pending++
		net.Request(ref.Node, storage.EncodeShardRequest(storage.OpGetShard, id, nil), n.cfg.RequestTimeout, func(resp []byte, err error) {
			if err == nil {
				blobs[j] = resp
			}
			heard(j, err)
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

// serveStorage handles inbound shard RPCs: it refuses OpDeleteShard over the
// wire (shards are deleted only by local collection or explicit dead-version
// purge), refuses OpPutShard before metadata loads or when this holder wrote a
// binding absence record (ErrFenced), and keeps the local inventory tree fresh
// on accepted writes.
func (n *Node) serveStorage(from env.NodeID, body []byte, reply env.Reply) {
	op, id, blob, derr := storage.DecodeShardRequest(body)
	if derr == nil && op == storage.OpDeleteShard {
		n.stats.Malformed++
		reply(nil, fmt.Errorf("node %s: shard delete from %s: shards are deleted only by collection or purge: %w", n.e.ID, from, ErrMalformed))
		return
	}
	var sum [32]byte
	if derr == nil && op == storage.OpPutShard {
		sum = storage.BlobChecksum(blob)
		rd, err := n.occ.Reader()
		if err != nil {
			reply(nil, fmt.Errorf("node %s: put shard %s from %s: %w", n.e.ID, id.BlobName(), from, err))
			return
		}
		if n.statedAbsent(rd, id) {
			n.stats.AbsentRefused++
			reply(nil, fmt.Errorf("node %s: put shard %s from %s: this holder recorded that it holds none: %w", n.e.ID, id.BlobName(), from, ErrFenced))
			return
		}
	}
	n.store.OnRequest(from, body, func(resp []byte, err error) {
		if err == nil && derr == nil {
			if inv, ok := n.inv[id.Bucket]; ok {
				switch op {
				case storage.OpPutShard:
					if err := inv.actual.Set(id.BlobName(), merkle.Hash(sum)); err != nil {
						n.logf("inventory %s: %v", id.BlobName(), err)
					}
				}
			}
		}
		reply(resp, err)
	})
}

// purge deletes local shards belonging to the named dead versions, refusing
// any version that this node's own metadata still calls live (PurgeRefused)
// and marking purged versions so in-flight or later rebuilds never recreate
// them.
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

type verifyShardReq struct {
	version  storage.VersionID
	stripe   uint32
	index    uint16
	checksum [32]byte
}

func (n *Node) expectedBucket(rd occ.Reader, bucket string) map[string]expectedShard {
	out := map[string]expectedShard{}
	add := func(m *storage.Manifest) {
		if m.Bucket != bucket {
			return
		}
		for s := range m.Stripes {
			for i, ref := range m.Stripes[s].Shards {
				if ref.Node != n.e.ID {
					continue
				}
				out[m.ShardID(s, i).BlobName()] = expectedShard{man: m, stripe: s, idx: i}
			}
		}
	}
	for _, k := range prefixKeys(rd.Keys(), occ.BucketPrefix(occ.KindManifest, bucket)) {
		rec, _, err := n.object(rd, k)
		if err != nil {
			continue
		}
		add(rec.Manifest)
	}
	for _, k := range prefixKeys(rd.Keys(), occ.BucketPrefix(occ.KindTag, bucket)) {
		_, parts, err := occ.ParseKey(k)
		if err != nil || !strings.HasPrefix(parts[2], tagStage) {
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
		add(rec.Manifest)
	}
	return out
}

func (n *Node) verifyShards(bucket string, requireExpected bool, metaDoc []byte, reqs []verifyShardReq) ([]byte, error) {
	if _, err := n.reader(); err != nil {
		return nil, err
	}
	if len(metaDoc) > 0 {
		doc, err := occ.Decode(metaDoc)
		if err != nil {
			return nil, fmt.Errorf("node: verify shards %q: %w", bucket, err)
		}
		keys := doc.Keys()
		for _, k := range keys {
			_, parts, err := occ.ParseKey(k)
			if err != nil || len(parts) == 0 || parts[0] != bucket {
				return nil, fmt.Errorf("node: verify shards %q: key %q outside bucket: %w", bucket, k, ErrMalformed)
			}
		}
		if _, err := n.occ.Merge(doc); err != nil {
			return nil, fmt.Errorf("node: verify shards %q: merge: %w", bucket, err)
		}
		for _, k := range keys {
			n.RecordMeta(k)
		}
	}
	rd, err := n.reader()
	if err != nil {
		return nil, err
	}
	var exp map[string]expectedShard
	if requireExpected {
		exp = n.expectedBucket(rd, bucket)
	}
	out := make([]byte, len(reqs))
	for i, req := range reqs {
		id := storage.ShardID{Bucket: bucket, Version: req.version, Stripe: req.stripe, Index: req.index}
		name := id.BlobName()
		if requireExpected {
			es, ok := exp[name]
			if !ok || es.man.Stripes[es.stripe].Shards[es.idx].Checksum != req.checksum {
				continue
			}
		}
		if n.purged[id.Version] || n.dead(rd, name) || n.statedAbsent(rd, id) {
			continue
		}
		b, err := n.store.Store().GetBlob(id)
		if err != nil {
			if isNotFound(err) || isCorrupt(err) {
				continue
			}
			return nil, fmt.Errorf("node: verify shard %s: %w", name, err)
		}
		if _, _, derr := storage.DecodeShard(b); derr != nil || storage.BlobChecksum(b) != req.checksum {
			continue
		}
		out[i] = 1
		if requireExpected {
			if inv, ok := n.inv[bucket]; ok {
				delete(inv.orphans, name)
				if serr := inv.actual.Set(name, merkle.Hash(req.checksum)); serr != nil {
					n.logf("verify %s: %v", name, serr)
				}
			}
		}
	}
	return out, nil
}
