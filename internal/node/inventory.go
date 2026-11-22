package node

import (
	"errors"
	"slices"
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
}

type expectedShard struct {
	man    *storage.Manifest
	stripe int
	idx    int
}

func isCorrupt(err error) bool { return errors.Is(err, storage.ErrCorrupt) }

func shardPrefix(bucket string) string { return "shards/" + bucket + "/" }

func (n *Node) expectedAll(rd occ.Reader) map[string]map[string]expectedShard {
	out := map[string]map[string]expectedShard{}
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
		case occ.KindTag:
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
			inv = &inventory{bucket: b, actual: t, full: true, rebuilding: map[string]bool{}, orphans: map[string]env.Time{}}
			n.inv[b] = inv
			n.stats.InventoryStart++
		}
		n.checkInventory(inv, exp[b])
		if len(exp[b]) == 0 && inv.actual.Len() == 0 && len(inv.rebuilding) == 0 {
			delete(n.inv, b)
			n.stats.InventoryStop++
		}
	}
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

func (n *Node) checkInventory(inv *inventory, exp map[string]expectedShard) {
	names, err := n.e.Disk.List(shardPrefix(inv.bucket))
	if err != nil {
		return
	}
	union := append(slices.Clone(names), inv.actual.Keys()...)
	slices.Sort(union)
	union = slices.Compact(union)
	if inv.full || len(union) <= n.cfg.ScrubBatch {
		inv.full = false
		for _, name := range union {
			n.scrubShard(inv, name)
		}
	} else if len(union) > 0 {
		i, found := slices.BinarySearch(union, inv.cursor)
		if found {
			i++
		}
		for c := 0; c < n.cfg.ScrubBatch; c++ {
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
		return
	}
	res, err := merkle.Compare(want, inv.actual, merkle.Limits{})
	if err != nil {
		n.logf("inventory %s: %v", inv.bucket, err)
		return
	}
	now := n.e.Clock.Now()
	orphans := map[string]env.Time{}
	for _, d := range res.Diffs {
		switch {
		case d.HasLocal && (!d.HasRemote || d.Local != d.Remote):
			if !inv.rebuilding[d.Key] && n.rebuilding < MaxRebuilds {
				n.rebuild(inv, d.Key, exp[d.Key])
			}
		case !d.HasLocal && d.HasRemote:
			since, ok := inv.orphans[d.Key]
			if !ok {
				since = now
			}
			if now.Sub(since) >= n.cfg.GCGrace {
				n.collect(inv, d.Key)
				continue
			}
			orphans[d.Key] = since
		}
	}
	inv.orphans = orphans
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
		if err != nil || storage.BlobChecksum(enc[es.idx]) != want[es.idx] {
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

func (n *Node) purge(bucket string, keep map[storage.VersionID]bool) (int, error) {
	ids, err := n.store.Store().List(bucket)
	if err != nil {
		return 0, err
	}
	c := 0
	for _, id := range ids {
		if keep[id.Version] {
			continue
		}
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
