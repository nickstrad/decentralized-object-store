package node

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/merkle"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

// presetSource is a single-value rand.Source letting migrator pre-record a
// copy intent ~cv/<saga>/<version> before storage.Node.Put writes any shards.
type presetSource struct{ next uint64 }

func (p *presetSource) Uint64() uint64 { return p.next }

// migrator implements saga.MigrationOps on real OCC metadata, storage nodes,
// and node RPCs. Registration keys (~reg/<saga>) are per-saga so concurrent
// duels commute under LWW; the in-memory fence stops a discarded saga's copy.
type migrator struct {
	n       *Node
	fenced  map[string]bool
	copying map[string]int
	waiting map[string][]func()
	src     presetSource
	copiers map[env.Region]*storage.Node
}

func newMigrator(n *Node) *migrator {
	return &migrator{n: n, fenced: map[string]bool{}, copying: map[string]int{}, waiting: map[string][]func(){},
		copiers: map[env.Region]*storage.Node{}}
}

func permanent(err error) error { return fmt.Errorf("%w: %w", err, saga.ErrPermanent) }

type stagedEntry struct {
	object string
	key    string
	rec    stagedRecord
	obj    objectRecord
}

func (g *migrator) staged(rd occ.Reader, bucket string) []stagedEntry {
	var out []stagedEntry
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
		obj, err := decodeObject(st.Object)
		if err != nil {
			continue
		}
		out = append(out, stagedEntry{object: parts[1], key: k, rec: st, obj: obj})
	}
	return out
}

func (g *migrator) scalar(rd occ.Reader, key string) (string, bool) {
	v, _, err := rd.Get(key)
	if err != nil {
		return "", false
	}
	return string(v), true
}

func (g *migrator) setScalar(key, val string) error {
	if _, err := g.n.occ.SetScalar(key, []byte(val)); err != nil {
		return err
	}
	g.n.RecordMeta(key)
	return nil
}

func (g *migrator) del(key string) error {
	_, err := g.n.occ.Delete(key, occ.Unconditional())
	if err != nil && !errors.Is(err, occ.ErrNotFound) {
		return err
	}
	g.n.RecordMeta(key)
	return nil
}

func (g *migrator) state(m saga.MigrationInput) (occ.Reader, bucketState, error) {
	rd, err := g.n.reader()
	if err != nil {
		return occ.Reader{}, bucketState{}, err
	}
	bs, err := g.n.bucket(rd, m.Bucket)
	if errors.Is(err, storage.ErrNoSuchBucket) || errors.Is(err, storage.ErrInvalid) {
		return rd, bs, permanent(err)
	}
	return rd, bs, err
}

func regKey(bucket, id string) string { return bucketKey(bucket, tagReg+id) }

func intentKey(bucket, id string, v storage.VersionID) string {
	return bucketKey(bucket, tagIntent+id+"/"+v.String())
}

func (g *migrator) registered(rd occ.Reader, bucket string) []string {
	var out []string
	for _, t := range sysTags(rd, bucket, tagReg) {
		if t.object == "" {
			out = append(out, t.rest)
		}
	}
	return out
}

func (g *migrator) intents(rd occ.Reader, bucket, id string) []storage.VersionID {
	var out []storage.VersionID
	for _, t := range sysTags(rd, bucket, tagIntent+id+"/") {
		if v, err := strconv.ParseUint(t.rest, 16, 64); err == nil && t.object == "" {
			out = append(out, storage.VersionID(v))
		}
	}
	return out
}

func (g *migrator) liveVersions(rd occ.Reader, bucket string) map[storage.VersionID]bool {
	out := map[storage.VersionID]bool{}
	for _, o := range g.n.liveObjects(rd, bucket) {
		out[o.rec.Manifest.Version] = true
	}
	return out
}

// Claim claims the target region (~claim/<to>) if unheld and writes this saga's
// registration key (~reg/<saga>), returning a permanent error if the bucket is
// not routed to m.From.
func (g *migrator) Claim(c saga.Call, m saga.MigrationInput, done func(error)) {
	g.n.later(func() { done(g.claim(c, m)) })
}

func (g *migrator) claim(c saga.Call, m saga.MigrationInput) error {
	rd, bs, err := g.state(m)
	if err != nil {
		return err
	}
	id := string(c.Key.Saga)
	if bs.route != m.From {
		return permanent(fmt.Errorf("node: claim %q: routed to %q, not %q: %w", m.Bucket, bs.route, m.From, ErrNoMigration))
	}
	ck := bucketKey(m.Bucket, tagClaim+m.To)
	if holder, ok := g.scalar(rd, ck); ok && holder != id {
		return fmt.Errorf("node: claim %q at %q held by %s: %w", m.Bucket, m.To, holder, storage.ErrBusy)
	} else if !ok {
		if err := g.setScalar(ck, id); err != nil {
			return err
		}
	}
	if !rd.Record(regKey(m.Bucket, id)).Live() {
		return g.setScalar(regKey(m.Bucket, id), id)
	}
	return nil
}

// Unclaim releases this saga's claim (if still held by it) and removes its
// registration key.
func (g *migrator) Unclaim(c saga.Call, m saga.MigrationInput, done func(error)) {
	g.n.later(func() { done(g.unclaim(c, m)) })
}

func (g *migrator) unclaim(c saga.Call, m saga.MigrationInput) error {
	rd, err := g.n.reader()
	if err != nil {
		return err
	}
	id := string(c.Key.Saga)
	ck := bucketKey(m.Bucket, tagClaim+m.To)
	if holder, ok := g.scalar(rd, ck); ok && holder == id {
		if err := g.del(ck); err != nil {
			return err
		}
	}
	return g.deregister(rd, m.Bucket, id)
}

func (g *migrator) deregister(rd occ.Reader, bucket, id string) error {
	if !rd.Record(regKey(bucket, id)).Live() {
		return nil
	}
	return g.del(regKey(bucket, id))
}

func (g *migrator) copier(region string) *storage.Node {
	r := env.Region(region)
	if s, ok := g.copiers[r]; ok {
		return s
	}
	e := g.n.e.WithNet(chanNet{net: g.n.e.Net, ch: chStorage})
	e.Rand = rand.New(&g.src)
	s, err := storage.NewNode(e, storage.Config{
		Layout: g.n.cfg.Layout, Members: regionView{v: g.n.view, region: r}, AckQuorum: g.n.cfg.AckQuorum, RequestTimeout: g.n.cfg.RequestTimeout,
	})
	if err != nil {
		return nil
	}
	g.copiers[r] = s
	return s
}

func (g *migrator) newVersion(rd occ.Reader, bucket string) storage.VersionID {
	for {
		v := storage.VersionID(g.n.e.Rand.Uint64())
		if v != 0 && !rd.Record(gcKey(bucket, v)).Present() {
			return v
		}
	}
}

// Copy sequentially copies each live object in m.Bucket into m.To, recording a
// copy intent ~cv/<saga>/<version> before writing shards and a staged record
// ~stage/<saga> after. Objects already staged at the current source ETag or
// already swapped to one of this saga's intents are skipped, making retries and
// post-flip catch-up idempotent.
func (g *migrator) Copy(c saga.Call, m saga.MigrationInput, done func([]byte, error)) {
	id := string(c.Key.Saga)
	if g.fenced[id] {
		g.n.later(func() {
			done(nil, permanent(fmt.Errorf("node: copy %q for %s after discard: %w", m.Bucket, id, ErrFenced)))
		})
		return
	}
	rd, bs, err := g.state(m)
	if err == nil && bs.route != m.From && bs.route != m.To {
		err = permanent(fmt.Errorf("node: copy %q: routed to %q: %w", m.Bucket, bs.route, ErrNoMigration))
	}
	cp := g.copier(m.To)
	if err == nil && cp == nil {
		err = fmt.Errorf("node: copy %q: no copier for %q: %w", m.Bucket, m.To, ErrConfig)
	}
	if err != nil {
		g.n.later(func() { done(nil, err) })
		return
	}
	objs := g.n.liveObjects(rd, m.Bucket)
	mine := map[storage.VersionID]bool{}
	for _, v := range g.intents(rd, m.Bucket, id) {
		mine[v] = true
	}
	g.copying[id]++
	finish := func(err error) {
		g.copying[id]--
		if g.copying[id] == 0 {
			delete(g.copying, id)
			w := g.waiting[id]
			delete(g.waiting, id)
			for _, f := range w {
				f()
			}
		}
		if err != nil {
			done(nil, err)
			return
		}
		if err := g.dropStale(m.Bucket, id); err != nil {
			done(nil, err)
			return
		}
		rd, err := g.n.reader()
		if err != nil {
			done(nil, err)
			return
		}
		done(g.root(rd, m, m.To, id), nil)
	}
	var step func(i int)
	step = func(i int) {
		if g.fenced[id] {
			finish(permanent(fmt.Errorf("node: copy %q for %s: %w", m.Bucket, id, ErrFenced)))
			return
		}
		if i == len(objs) {
			finish(nil)
			return
		}
		o := objs[i]
		rd, err := g.n.reader()
		if err != nil {
			finish(err)
			return
		}
		next := func() { g.n.later(func() { step(i + 1) }) }
		if mine[o.rec.Manifest.Version] {
			next()
			return
		}
		sk := stageKey(m.Bucket, o.key, id)
		if val, _, err := rd.Get(sk); err == nil {
			if st, err := decodeStaged(val); err == nil && st.SrcETag == o.etag {
				next()
				return
			}
		}
		g.n.store.Get(o.rec.Manifest, func(res storage.GetResult, err error) {
			if err != nil {
				finish(fmt.Errorf("node: copy %s/%s: read: %w", m.Bucket, o.key, err))
				return
			}
			if g.fenced[id] {
				finish(permanent(fmt.Errorf("node: copy %q for %s: %w", m.Bucket, id, ErrFenced)))
				return
			}
			rd, err := g.n.reader()
			if err != nil {
				finish(err)
				return
			}
			v := g.newVersion(rd, m.Bucket)
			if err := g.setScalar(intentKey(m.Bucket, id, v), m.To); err != nil {
				finish(err)
				return
			}
			g.src.next = uint64(v)
			cp.Put(m.Bucket, o.key, res.Data, func(man *storage.Manifest, err error) {
				if err != nil {
					finish(fmt.Errorf("node: copy %s/%s: write: %w", m.Bucket, o.key, err))
					return
				}
				if g.fenced[id] {
					finish(permanent(fmt.Errorf("node: copy %q for %s: %w", m.Bucket, id, ErrFenced)))
					return
				}
				rd, err := g.n.reader()
				if err != nil {
					finish(err)
					return
				}
				// Verify destination holders still carry a write quorum of shards
				// before writing the staged record (plain presence check: staged
				// metadata does not exist yet).
				g.verifyTargets(rd, m.Bucket, false, []verifyTarget{{object: o.key, man: man}}, func(verr error) {
					if verr != nil {
						g.n.markDead(m.Bucket, man.Version)
						finish(verr)
						return
					}
					if g.fenced[id] {
						finish(permanent(fmt.Errorf("node: copy %q for %s: %w", m.Bucket, id, ErrFenced)))
						return
					}
					obj, err := encodeObject(objectRecord{ContentType: o.rec.ContentType, Manifest: man})
					if err == nil {
						err = g.setScalar(sk, string(encodeStaged(stagedRecord{Saga: id, SrcETag: o.etag, Object: obj})))
					}
					if err != nil {
						finish(err)
						return
					}
					step(i + 1)
				})
			})
		})
	}
	g.n.later(func() { step(0) })
}

func (g *migrator) dropStale(bucket, id string) error {
	rd, err := g.n.reader()
	if err != nil {
		return err
	}
	for _, s := range g.staged(rd, bucket) {
		if s.rec.Saga != id {
			continue
		}
		if !rd.Record(occ.ManifestKey(bucket, s.object)).Live() {
			if err := g.del(s.key); err != nil {
				return err
			}
		}
	}
	return nil
}

func rootEntry(key string, man *storage.Manifest) merkle.Entry {
	h := sha256.New()
	h.Write(man.ContentSHA[:])
	h.Write(binary.BigEndian.AppendUint64(nil, uint64(man.Size)))
	var sum merkle.Hash
	copy(sum[:], h.Sum(nil))
	return merkle.Entry{Key: key, Hash: sum}
}

func (g *migrator) root(rd occ.Reader, m saga.MigrationInput, loc, id string) []byte {
	entries := map[string]merkle.Entry{}
	switch loc {
	case m.To:
		mine := map[storage.VersionID]bool{}
		for _, v := range g.intents(rd, m.Bucket, id) {
			mine[v] = true
		}
		for _, o := range g.n.liveObjects(rd, m.Bucket) {
			if mine[o.rec.Manifest.Version] {
				entries[o.key] = rootEntry(o.key, o.rec.Manifest)
			}
		}
		for _, s := range g.staged(rd, m.Bucket) {
			if s.rec.Saga == id {
				entries[s.object] = rootEntry(s.object, s.obj.Manifest)
			}
		}
	case m.From:
		for _, o := range g.n.liveObjects(rd, m.Bucket) {
			entries[o.key] = rootEntry(o.key, o.rec.Manifest)
		}
	}
	list := make([]merkle.Entry, 0, len(entries))
	for _, k := range sortedKeys(entries) {
		list = append(list, entries[k])
	}
	t, err := merkle.Build(InventoryDepth, list)
	if err != nil {
		return nil
	}
	r := t.Root()
	return r[:]
}

// Root computes the Merkle root over (key, SHA-256(contentHash || size)) for
// either m.From (live manifests) or m.To (this saga's staged records unioned
// with live manifests already holding one of its copy intents). For m.To it
// first verifies and pins destination shards through verifyDest.
func (g *migrator) Root(c saga.Call, bucket, loc string, done func([]byte, error)) {
	g.n.later(func() {
		m, err := saga.DecodeMigrationInput(c.Input)
		if err != nil || m.Bucket != bucket {
			done(nil, permanent(fmt.Errorf("node: root of %q: input %+v: %w", bucket, m, ErrMalformed)))
			return
		}
		id := string(c.Key.Saga)
		rd, err := g.n.reader()
		if err != nil {
			done(nil, err)
			return
		}
		if loc != m.To {
			done(g.root(rd, m, loc, id), nil)
			return
		}
		g.verifyDest(m, id, func(err error) {
			if err != nil {
				done(nil, err)
				return
			}
			rd, err := g.n.reader()
			if err != nil {
				done(nil, err)
				return
			}
			done(g.root(rd, m, loc, id), nil)
		})
	})
}

// migrationQuorum returns the minimum verified shards per stripe required
// before a copied version may proceed to flip or manifest swap.
func (n *Node) migrationQuorum(l storage.Layout) int {
	if n.cfg.AckQuorum > 0 {
		return n.cfg.AckQuorum
	}
	return min(l.K+1, l.K+l.M)
}

func encodeVerifyShardsReq(bucket string, requireExpected bool, metaDoc []byte, reqs []verifyShardReq) []byte {
	var w wbuf
	w.byte(opVerifyShards)
	w.str(bucket)
	if requireExpected {
		w.byte(1)
	} else {
		w.byte(0)
	}
	w.bytes(metaDoc)
	w.uvarint(uint64(len(reqs)))
	for _, req := range reqs {
		w.uvarint(uint64(req.version))
		w.uvarint(uint64(req.stripe))
		w.uvarint(uint64(req.index))
		w.b = append(w.b, req.checksum[:]...)
	}
	return w.b
}

func decodeVerifyShardsReq(r *rbuf) (string, bool, []byte, []verifyShardReq, error) {
	bucket := r.str()
	reqExp := r.byte() != 0
	docBytes := r.bytes()
	cnt := r.uvarint()
	if r.err == nil && cnt > uint64(len(r.b))/32 {
		r.fail("verify shards count")
	}
	reqs := make([]verifyShardReq, 0, min(cnt, uint64(len(r.b))/32))
	for i := uint64(0); i < cnt && r.err == nil; i++ {
		var req verifyShardReq
		req.version = storage.VersionID(r.uvarint())
		req.stripe = uint32(r.uvarint())
		req.index = uint16(r.uvarint())
		if r.err == nil && len(r.b) < len(req.checksum) {
			r.fail("verify shards checksum")
			break
		}
		if r.err == nil {
			copy(req.checksum[:], r.b[:len(req.checksum)])
			r.b = r.b[len(req.checksum):]
			reqs = append(reqs, req)
		}
	}
	return bucket, reqExp, docBytes, reqs, r.end("verify shards")
}

func encodeVerifyShardsResp(res []byte) []byte {
	var w wbuf
	w.bytes(res)
	return w.b
}

func decodeVerifyShardsResp(b []byte, want int) ([]byte, error) {
	r := rbuf{b: b}
	res := r.bytes()
	if err := r.end("verify shards response"); err != nil {
		return nil, err
	}
	if len(res) != want {
		return nil, fmt.Errorf("node: verify shards response of %d entries, want %d: %w", len(res), want, ErrMalformed)
	}
	return res, nil
}

type verifyTarget struct {
	object   string
	stageKey string
	metaKey  string
	man      *storage.Manifest
}

// verifyDest collects every destination version belonging to saga id (live
// swapped manifests and staged records) and confirms with their holders that
// each stripe meets the write quorum while pinning those records on each holder.
func (g *migrator) verifyDest(m saga.MigrationInput, id string, done func(error)) {
	rd, err := g.n.reader()
	if err != nil {
		g.n.later(func() { done(err) })
		return
	}
	mine := map[storage.VersionID]bool{}
	for _, v := range g.intents(rd, m.Bucket, id) {
		mine[v] = true
	}
	byKey := map[string]verifyTarget{}
	liveByKey := map[string]bool{}
	for _, o := range g.n.liveObjects(rd, m.Bucket) {
		if mine[o.rec.Manifest.Version] {
			liveByKey[o.key] = true
			byKey[o.key] = verifyTarget{
				object:  o.key,
				metaKey: occ.ManifestKey(m.Bucket, o.key),
				man:     o.rec.Manifest,
			}
		}
	}
	for _, s := range g.staged(rd, m.Bucket) {
		if s.rec.Saga == id && !liveByKey[s.object] {
			byKey[s.object] = verifyTarget{
				object:   s.object,
				stageKey: s.key,
				metaKey:  s.key,
				man:      s.obj.Manifest,
			}
		}
	}
	targets := make([]verifyTarget, 0, len(byKey))
	for _, k := range sortedKeys(byKey) {
		targets = append(targets, byKey[k])
	}
	g.verifyTargets(rd, m.Bucket, true, targets, done)
}

// verifyTargets batches shard-verification RPCs per holder (bounded by the
// 16 MiB frame limit for whole-bucket messages). When requireExpected is true,
// each holder first merges the pushed OCC records so checking and protecting
// the shards happen in one act. A stripe below quorum fails with ErrUnsettled;
// if verified plus unreachable holders fall below k, the staged version is
// marked dead and deleted so pre-flip sagas compensate and post-flip catch-up
// re-copies from the live source.
func (g *migrator) verifyTargets(rd occ.Reader, bucket string, requireExpected bool, targets []verifyTarget, done func(error)) {
	type shardSlot struct {
		holder env.NodeID
		slot   int
	}
	byHolder := map[env.NodeID][]verifyShardReq{}
	metaKeys := map[env.NodeID]map[string]bool{}
	slots := make([][][]shardSlot, len(targets))
	for ti, t := range targets {
		slots[ti] = make([][]shardSlot, len(t.man.Stripes))
		for s, st := range t.man.Stripes {
			slots[ti][s] = make([]shardSlot, len(st.Shards))
			for i, ref := range st.Shards {
				slot := len(byHolder[ref.Node])
				byHolder[ref.Node] = append(byHolder[ref.Node], verifyShardReq{
					version:  t.man.Version,
					stripe:   uint32(s),
					index:    uint16(i),
					checksum: ref.Checksum,
				})
				if requireExpected && t.metaKey != "" {
					if metaKeys[ref.Node] == nil {
						metaKeys[ref.Node] = map[string]bool{}
					}
					metaKeys[ref.Node][t.metaKey] = true
				}
				slots[ti][s][i] = shardSlot{holder: ref.Node, slot: slot}
			}
		}
	}
	if len(byHolder) == 0 {
		g.n.later(func() { done(nil) })
		return
	}
	holders := sortedKeys(byHolder)
	holderIdx := make(map[env.NodeID]int, len(holders))
	for i, h := range holders {
		holderIdx[h] = i
	}
	results := make([][]byte, len(holders))
	rpcErrs := make([]error, len(holders))
	finish := func() {
		if g.n.stopped {
			done(fmt.Errorf("node: verify %q: %w", bucket, ErrStopped))
			return
		}
		var errs []error
		for ti, t := range targets {
			quorum := g.n.migrationQuorum(t.man.Layout)
			k := t.man.Layout.K
			unrecoverable := false
			incomplete := false
			var detail string
			for s := range t.man.Stripes {
				verified := 0
				unreachable := 0
				for _, sl := range slots[ti][s] {
					hi := holderIdx[sl.holder]
					if rpcErrs[hi] != nil {
						unreachable++
					} else if sl.slot < len(results[hi]) && results[hi][sl.slot] == 1 {
						verified++
					}
				}
				if verified < quorum {
					incomplete = true
					if verified+unreachable < k {
						unrecoverable = true
						detail = fmt.Sprintf("node: verify %s/%s version %s stripe %d: %d of %d shards verified (%d unreachable), fewer than k=%d", bucket, t.object, t.man.Version, s, verified, len(t.man.Stripes[s].Shards), unreachable, k)
					} else if detail == "" {
						detail = fmt.Sprintf("node: verify %s/%s version %s stripe %d: %d of %d shards verified (%d unreachable), need %d", bucket, t.object, t.man.Version, s, verified, len(t.man.Stripes[s].Shards), unreachable, quorum)
					}
				}
			}
			if !incomplete {
				continue
			}
			if unrecoverable && t.stageKey != "" {
				g.n.markDead(bucket, t.man.Version)
				if derr := g.del(t.stageKey); derr != nil {
					errs = append(errs, derr)
				}
				errs = append(errs, permanent(fmt.Errorf("%s: %w", detail, ErrUnsettled)))
			} else {
				errs = append(errs, fmt.Errorf("%s: %w", detail, ErrUnsettled))
			}
		}
		done(errors.Join(errs...))
	}
	pending := 0
	for hi, h := range holders {
		reqs := byHolder[h]
		if h == g.n.e.ID {
			results[hi], rpcErrs[hi] = g.n.verifyShards(bucket, requireExpected, nil, reqs)
			continue
		}
		var docBytes []byte
		if requireExpected && len(metaKeys[h]) > 0 {
			doc := occ.NewDoc()
			for _, mk := range sortedKeys(metaKeys[h]) {
				if v, ok := rd.Record(mk).Version(); ok {
					doc.MergeVersion(mk, v)
				}
			}
			docBytes = doc.Encode()
		}
		payload := encodeVerifyShardsReq(bucket, requireExpected, docBytes, reqs)
		pending++
		idx := hi
		want := len(reqs)
		peer := h
		g.n.net.Request(peer, payload, g.n.cfg.RPCTimeout, func(resp []byte, err error) {
			if err == nil {
				results[idx], err = decodeVerifyShardsResp(resp, want)
			}
			if err != nil {
				rpcErrs[idx] = fmt.Errorf("node: verify %q on %s: %w", bucket, peer, err)
			}
			pending--
			if pending == 0 {
				finish()
			}
		})
	}
	if pending == 0 {
		g.n.later(finish)
	}
}

// Discard fences the saga in memory, waits for any in-flight Copy step to
// drain, deletes its staged records, marks its non-live copy intents dead,
// purges those versions across live peers, and removes the intent keys.
func (g *migrator) Discard(c saga.Call, m saga.MigrationInput, done func(error)) {
	id := string(c.Key.Saga)
	g.fenced[id] = true
	run := func() { g.discard(id, m, done) }
	if g.copying[id] > 0 {
		g.waiting[id] = append(g.waiting[id], run)
		return
	}
	g.n.later(run)
}

func (g *migrator) discard(id string, m saga.MigrationInput, done func(error)) {
	rd, err := g.n.reader()
	if err != nil {
		done(err)
		return
	}
	for _, s := range g.staged(rd, m.Bucket) {
		if s.rec.Saga == id {
			if err := g.del(s.key); err != nil {
				done(err)
				return
			}
		}
	}
	live := g.liveVersions(rd, m.Bucket)
	var dead []storage.VersionID
	for _, v := range g.intents(rd, m.Bucket, id) {
		if !live[v] {
			g.n.markDead(m.Bucket, v)
			dead = append(dead, v)
		}
	}
	g.purge(m.Bucket, dead, func(err error) {
		if err != nil {
			done(err)
			return
		}
		done(g.dropIntents(m.Bucket, id))
	})
}

func (g *migrator) dropIntents(bucket, id string) error {
	rd, err := g.n.reader()
	if err != nil {
		return err
	}
	for _, v := range g.intents(rd, bucket, id) {
		if err := g.del(intentKey(bucket, id, v)); err != nil {
			return err
		}
	}
	return nil
}

func (g *migrator) purge(bucket string, versions []storage.VersionID, done func(error)) {
	if !g.n.ViewSettled() {
		g.n.later(func() {
			done(fmt.Errorf("node: purge %q: membership view still settling: %w", bucket, ErrUnsettled))
		})
		return
	}
	vs := slices.Clone(versions)
	slices.Sort(vs)
	vs = slices.Compact(vs)
	if len(vs) == 0 {
		g.n.later(func() { done(nil) })
		return
	}
	var w wbuf
	w.byte(opPurge)
	w.str(bucket)
	w.uvarint(uint64(len(vs)))
	for _, v := range vs {
		w.uvarint(uint64(v))
	}
	if _, err := g.n.purge(bucket, vs); err != nil {
		g.n.later(func() { done(err) })
		return
	}
	peers := g.n.view.Peers()
	slices.Sort(peers)
	var errs []error
	pending := len(peers)
	for _, p := range peers {
		g.n.net.Request(p, w.b, g.n.cfg.RPCTimeout, func(_ []byte, err error) {
			if err != nil {
				errs = append(errs, fmt.Errorf("node: purge %q on %s: %w", bucket, p, err))
			}
			pending--
			if pending == 0 {
				done(errors.Join(errs...))
			}
		})
	}
	if pending == 0 {
		g.n.later(func() { done(nil) })
	}
}

func flipValue(id, to string) string { return id + "|" + to }

// Flip is the saga pivot: it records ~flip = <saga>|<to> and sets the bucket's
// routing pointer to m.To. If this saga already owns ~flip (for instance after
// a crash between the two writes), it repairs the route and succeeds.
func (g *migrator) Flip(c saga.Call, m saga.MigrationInput, done func(error)) {
	g.n.later(func() {
		rd, bs, err := g.state(m)
		if err != nil {
			done(err)
			return
		}
		id := string(c.Key.Saga)
		owner, _ := g.scalar(rd, bucketKey(m.Bucket, tagFlip))
		if bs.route == m.To && owner == flipValue(id, m.To) {
			done(nil)
			return
		}
		if owner == flipValue(id, m.To) {
			if _, err := g.n.occ.SetRoute(m.Bucket, env.Region(m.To)); err != nil {
				done(err)
				return
			}
			g.n.RecordMeta(occ.RouteKey(m.Bucket))
			done(nil)
			return
		}
		if bs.route != m.From {
			done(permanent(fmt.Errorf("node: flip %q to %q: routed to %q by %q: %w", m.Bucket, m.To, bs.route, owner, ErrNoMigration)))
			return
		}
		if err := g.setScalar(bucketKey(m.Bucket, tagFlip), flipValue(id, m.To)); err != nil {
			done(err)
			return
		}
		if _, err := g.n.occ.SetRoute(m.Bucket, env.Region(m.To)); err != nil {
			done(err)
			return
		}
		g.n.RecordMeta(occ.RouteKey(m.Bucket))
		done(nil)
	})
}

type pointerView struct {
	route  string
	owner  string
	digest [32]byte
}

func (n *Node) pointerView(rd occ.Reader, bucket string) pointerView {
	var p pointerView
	if r, err := rd.Route(bucket); err == nil {
		p.route = string(r)
	}
	if v, _, err := rd.Get(bucketKey(bucket, tagFlip)); err == nil {
		p.owner = string(v)
	}
	h := sha256.New()
	for _, k := range prefixKeys(rd.Keys(), occ.BucketPrefix(occ.KindManifest, bucket)) {
		if v, ok := rd.Record(k).Version(); ok {
			id := v.ID()
			h.Write(binary.BigEndian.AppendUint32(nil, uint32(len(k))))
			h.Write([]byte(k))
			h.Write(id[:])
		}
	}
	copy(p.digest[:], h.Sum(nil))
	return p
}

func (p pointerView) encode() []byte {
	var w wbuf
	w.str(p.route)
	w.str(p.owner)
	w.b = append(w.b, p.digest[:]...)
	return w.b
}

func decodePointerView(b []byte) (pointerView, error) {
	r := rbuf{b: b}
	p := pointerView{route: r.str(), owner: r.str()}
	if r.err == nil && len(r.b) != len(p.digest) {
		r.fail("pointer digest")
	}
	if r.err == nil {
		copy(p.digest[:], r.b)
		r.b = nil
	}
	return p, r.end("pointer view")
}

// agree requires a settled membership view and queries every live peer for its
// pointerView (route, flip owner, and bucket manifest digest including
// tombstones), returning ErrUnsettled until every peer matches the coordinator.
func (g *migrator) agree(bucket string, done func(pointerView, error)) {
	rd, err := g.n.reader()
	if err != nil {
		g.n.later(func() { done(pointerView{}, err) })
		return
	}
	if !g.n.ViewSettled() {
		g.n.later(func() {
			done(pointerView{}, fmt.Errorf("node: pointer of %q: membership view still settling: %w", bucket, ErrUnsettled))
		})
		return
	}
	var w wbuf
	w.byte(opPointer)
	w.str(bucket)
	peers := g.n.view.Peers()
	slices.Sort(peers)
	var errs []error
	pending := len(peers)
	views := map[env.NodeID]pointerView{}
	reply := func() {
		rd, err := g.n.reader()
		if err != nil {
			done(pointerView{}, err)
			return
		}
		local := g.n.pointerView(rd, bucket)
		if len(errs) > 0 {
			done(local, errors.Join(errs...))
			return
		}
		for _, p := range peers {
			if views[p] != local {
				done(local, fmt.Errorf("node: pointer of %q: %s disagrees: %w", bucket, p, ErrUnsettled))
				return
			}
		}
		done(local, nil)
	}
	for _, p := range peers {
		g.n.net.Request(p, w.b, g.n.cfg.RPCTimeout, func(resp []byte, err error) {
			if err == nil {
				var v pointerView
				if v, err = decodePointerView(resp); err == nil {
					views[p] = v
				}
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("node: pointer from %s: %w", p, err))
			}
			pending--
			if pending == 0 {
				reply()
			}
		})
	}
	_ = rd
	if pending == 0 {
		g.n.later(reply)
	}
}

// Pointer waits for every live peer to agree on the bucket's route, flip owner,
// and manifest digest (ensuring every pre-flip write acknowledged by a live
// peer has reached the coordinator). If another saga owns ~flip, it returns
// that owner prefixed with "~" so the saga engine treats this saga as
// superseded; if this saga owns ~flip but the route lags, it repairs the route
// and retries.
func (g *migrator) Pointer(c saga.Call, bucket string, done func(string, error)) {
	m, err := saga.DecodeMigrationInput(c.Input)
	if err != nil || m.Bucket != bucket {
		g.n.later(func() {
			done("", permanent(fmt.Errorf("node: pointer of %q: input %+v: %w", bucket, m, ErrMalformed)))
		})
		return
	}
	id := string(c.Key.Saga)
	g.agree(bucket, func(p pointerView, err error) {
		if err != nil {
			done("", err)
			return
		}
		switch {
		case p.owner == "":
			done("", fmt.Errorf("node: pointer of %q: no flip recorded: %w", bucket, ErrUnsettled))
		case p.owner != flipValue(id, m.To):
			done(sysPrefix+p.owner, nil)
		case p.route != m.To:
			if _, err := g.n.occ.SetRoute(bucket, env.Region(m.To)); err != nil {
				done("", err)
				return
			}
			g.n.RecordMeta(occ.RouteKey(bucket))
			done("", fmt.Errorf("node: pointer of %q: route %q behind the flip: %w", bucket, p.route, ErrUnsettled))
		default:
			done(m.To, nil)
		}
	})
}

// Seal checks whether this saga already retired the bucket, refuses with
// storage.ErrBusy while any other saga remains registered in ~reg/*, and
// clears ~live so writes stay blocked through catch-up and retire.
func (g *migrator) Seal(c saga.Call, m saga.MigrationInput, done func(bool, error)) {
	g.n.later(func() {
		rd, bs, err := g.state(m)
		if err != nil {
			done(false, err)
			return
		}
		id := string(c.Key.Saga)
		if r, ok := g.scalar(rd, bucketKey(m.Bucket, tagRetired)); ok && r == id {
			done(true, nil)
			return
		}
		for _, other := range g.registered(rd, m.Bucket) {
			if other != id {
				done(false, fmt.Errorf("node: seal %q: %s also registered: %w", m.Bucket, other, storage.ErrBusy))
				return
			}
		}
		if !bs.hasLiv || bs.live != "" {
			if err := g.n.setLive(m.Bucket, ""); err != nil {
				done(false, err)
				return
			}
		}
		done(false, nil)
	})
}

// Retire swaps each live manifest IfMatch its staged SrcETag and marks the
// replaced version dead, marks non-live copy intents dead, waits for every live
// peer's pointer view to match the swapped manifests, purges only the bucket's
// explicitly marked dead versions, drops intents, writes ~retired, and
// deregisters. Any unseen pre-flip write from an isolated peer is absent from
// the dead list and stays intact on disk.
func (g *migrator) Retire(c saga.Call, m saga.MigrationInput, done func(error)) {
	g.n.later(func() {
		rd, err := g.n.reader()
		if err != nil {
			done(err)
			return
		}
		id := string(c.Key.Saga)
		rk := bucketKey(m.Bucket, tagRetired)
		if r, ok := g.scalar(rd, rk); ok && r == id {
			done(nil)
			return
		}
		// Re-check destination shards immediately before swapping manifests in
		// case a holder lost shards after the catch-up Root verification.
		g.verifyDest(m, id, func(err error) {
			if err != nil {
				done(err)
				return
			}
			rd, err := g.n.reader()
			if err != nil {
				done(err)
				return
			}
			for _, s := range g.staged(rd, m.Bucket) {
				if s.rec.Saga != id {
					continue
				}
				mk := occ.ManifestKey(m.Bucket, s.object)
				cur := rd.Record(mk)
				if cur.Live() && cur.ETag() == s.rec.SrcETag {
					old, _, oerr := g.n.object(rd, mk)
					v, err := g.n.occ.Put(mk, s.rec.Object, occ.IfMatch(s.rec.SrcETag))
					if err != nil {
						done(err)
						return
					}
					g.n.committed(mk, cur, v)
					g.n.RecordMeta(mk)
					if oerr == nil {
						g.n.markDead(m.Bucket, old.Manifest.Version)
					}
				}
			}
			rd, err = g.n.reader()
			if err != nil {
				done(err)
				return
			}
			live := g.liveVersions(rd, m.Bucket)
			for _, v := range g.intents(rd, m.Bucket, id) {
				if !live[v] {
					g.n.markDead(m.Bucket, v)
				}
			}
			g.agree(m.Bucket, func(_ pointerView, err error) {
				if err != nil {
					done(fmt.Errorf("node: retire %q: waiting for every peer to hold the swapped manifests: %w", m.Bucket, err))
					return
				}
				rd, err := g.n.reader()
				if err != nil {
					done(err)
					return
				}
				// Delete staged records only after every live peer holds the
				// swapped manifests so destination holders never treat a newly
				// live shard as an orphan during propagation.
				for _, s := range g.staged(rd, m.Bucket) {
					if s.rec.Saga == id {
						if err := g.del(s.key); err != nil {
							done(err)
							return
						}
					}
				}
				g.purge(m.Bucket, g.n.deadVersions(rd, m.Bucket), func(err error) {
					if err != nil {
						done(err)
						return
					}
					if err := g.dropIntents(m.Bucket, id); err != nil {
						done(err)
						return
					}
					if err := g.setScalar(rk, id); err != nil {
						done(err)
						return
					}
					rd, err := g.n.reader()
					if err != nil {
						done(err)
						return
					}
					done(g.deregister(rd, m.Bucket, id))
				})
			})
		})
	})
}

// Promote sets ~live to m.To (re-enabling writes in the target region) and
// releases this saga's claim.
func (g *migrator) Promote(c saga.Call, m saga.MigrationInput, done func(error)) {
	g.n.later(func() {
		rd, err := g.n.reader()
		if err != nil {
			done(err)
			return
		}
		if err := g.n.setLive(m.Bucket, m.To); err != nil {
			done(err)
			return
		}
		ck := bucketKey(m.Bucket, tagClaim+m.To)
		if holder, ok := g.scalar(rd, ck); ok && holder == string(c.Key.Saga) {
			if err := g.del(ck); err != nil {
				done(err)
				return
			}
		}
		done(nil)
	})
}
