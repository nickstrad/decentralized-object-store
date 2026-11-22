package node

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/merkle"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

type migrator struct {
	n       *Node
	fenced  map[string]bool
	copying map[string]int
	waiting map[string][]func()
}

func newMigrator(n *Node) *migrator {
	return &migrator{n: n, fenced: map[string]bool{}, copying: map[string]int{}, waiting: map[string][]func(){}}
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

func (g *migrator) set(rd occ.Reader, key string) []string {
	val, _, err := rd.Get(key)
	if err != nil {
		return nil
	}
	s, err := decodeSet(val)
	if err != nil {
		return nil
	}
	return s
}

func (g *migrator) writeSet(key string, ids []string) error {
	if _, err := g.n.occ.SetScalar(key, encodeSet(ids)); err != nil {
		return err
	}
	g.n.RecordMeta(key)
	return nil
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
	rk := bucketKey(m.Bucket, tagReg)
	reg := g.set(rd, rk)
	if !slices.Contains(reg, id) {
		return g.writeSet(rk, append(reg, id))
	}
	return nil
}

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
	rk := bucketKey(bucket, tagReg)
	reg := g.set(rd, rk)
	if !slices.Contains(reg, id) {
		return nil
	}
	return g.writeSet(rk, slices.DeleteFunc(reg, func(s string) bool { return s == id }))
}

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
	if err != nil {
		g.n.later(func() { done(nil, err) })
		return
	}
	objs := g.n.liveObjects(rd, m.Bucket)
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
		done(g.root(rd, m.Bucket, m.To, id), nil)
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
		sk := stageKey(m.Bucket, o.key, id)
		if val, _, err := rd.Get(sk); err == nil {
			if st, err := decodeStaged(val); err == nil && st.SrcETag == o.etag {
				g.n.later(func() { step(i + 1) })
				return
			}
		}
		g.n.store.Get(o.rec.Manifest, func(res storage.GetResult, err error) {
			if err != nil {
				finish(fmt.Errorf("node: copy %s/%s: read: %w", m.Bucket, o.key, err))
				return
			}
			g.n.scope(m.To).Put(m.Bucket, o.key, res.Data, func(man *storage.Manifest, err error) {
				if err != nil {
					finish(fmt.Errorf("node: copy %s/%s: write: %w", m.Bucket, o.key, err))
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
	}
	step(0)
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

func (g *migrator) regionOf(id env.NodeID) env.Region {
	if id == g.n.e.ID {
		return g.n.e.Region
	}
	m, err := g.n.gossip.Lookup(id)
	if err != nil {
		return ""
	}
	return m.Region
}

func (g *migrator) placedIn(man *storage.Manifest, loc string) bool {
	for _, s := range man.Stripes {
		for _, ref := range s.Shards {
			if string(g.regionOf(ref.Node)) != loc {
				return false
			}
		}
	}
	return true
}

func rootEntry(key string, man *storage.Manifest) merkle.Entry {
	h := sha256.New()
	h.Write(man.ContentSHA[:])
	h.Write(binary.BigEndian.AppendUint64(nil, uint64(man.Size)))
	var sum merkle.Hash
	copy(sum[:], h.Sum(nil))
	return merkle.Entry{Key: key, Hash: sum}
}

func (g *migrator) root(rd occ.Reader, bucket, loc, id string) []byte {
	staged := map[string]*storage.Manifest{}
	for _, s := range g.staged(rd, bucket) {
		if s.rec.Saga == id {
			staged[s.object] = s.obj.Manifest
		}
	}
	entries := map[string]merkle.Entry{}
	for _, o := range g.n.liveObjects(rd, bucket) {
		if s, ok := staged[o.key]; ok && g.placedIn(s, loc) {
			entries[o.key] = rootEntry(o.key, s)
		} else if g.placedIn(o.rec.Manifest, loc) {
			entries[o.key] = rootEntry(o.key, o.rec.Manifest)
		}
	}
	for _, k := range sortedKeys(staged) {
		if _, ok := entries[k]; !ok && g.placedIn(staged[k], loc) {
			entries[k] = rootEntry(k, staged[k])
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

func (g *migrator) Root(c saga.Call, bucket, loc string, done func([]byte, error)) {
	g.n.later(func() {
		rd, err := g.n.reader()
		if err != nil {
			done(nil, err)
			return
		}
		done(g.root(rd, bucket, loc, string(c.Key.Saga)), nil)
	})
}

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
	g.purgeRegion(m.Bucket, m.To, done)
}

func (g *migrator) keep(bucket string) (map[storage.VersionID]bool, error) {
	rd, err := g.n.reader()
	if err != nil {
		return nil, err
	}
	keep := map[storage.VersionID]bool{}
	for _, o := range g.n.liveObjects(rd, bucket) {
		keep[o.rec.Manifest.Version] = true
	}
	for _, s := range g.staged(rd, bucket) {
		keep[s.obj.Manifest.Version] = true
	}
	return keep, nil
}

func (g *migrator) purgeRegion(bucket, region string, done func(error)) {
	keep, err := g.keep(bucket)
	if err != nil {
		done(err)
		return
	}
	var w wbuf
	w.byte(opPurge)
	w.str(bucket)
	w.str(region)
	vs := sortedKeys(keep)
	w.uvarint(uint64(len(vs)))
	for _, v := range vs {
		w.uvarint(uint64(v))
	}
	targets := g.n.view.InRegion(env.Region(region))
	var errs []error
	pending := 0
	for _, t := range targets {
		if t == g.n.e.ID {
			if _, err := g.n.purge(bucket, keep); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		pending++
		g.n.net.Request(t, w.b, g.n.cfg.RPCTimeout, func(_ []byte, err error) {
			if err != nil {
				errs = append(errs, fmt.Errorf("node: purge %q on %s: %w", bucket, t, err))
			}
			pending--
			if pending == 0 {
				done(errors.Join(errs...))
			}
		})
	}
	if pending == 0 {
		g.n.later(func() { done(errors.Join(errs...)) })
	}
}

func (g *migrator) Flip(c saga.Call, m saga.MigrationInput, done func(error)) {
	g.n.later(func() {
		_, bs, err := g.state(m)
		if err != nil {
			done(err)
			return
		}
		if bs.route == m.To {
			done(nil)
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

func (g *migrator) Pointer(c saga.Call, bucket string, done func(string, error)) {
	rd, err := g.n.reader()
	if err != nil {
		g.n.later(func() { done("", err) })
		return
	}
	local, _ := rd.Route(bucket)
	var w wbuf
	w.byte(opPointer)
	w.str(bucket)
	peers := g.n.view.Peers()
	slices.Sort(peers)
	var errs []error
	agreed := true
	pending := len(peers)
	reply := func() {
		switch {
		case len(errs) > 0:
			done("", errors.Join(errs...))
		case !agreed:
			done("", fmt.Errorf("node: pointer of %q: %w", bucket, ErrUnsettled))
		default:
			done(string(local), nil)
		}
	}
	for _, p := range peers {
		g.n.net.Request(p, w.b, g.n.cfg.RPCTimeout, func(resp []byte, err error) {
			if err != nil {
				errs = append(errs, fmt.Errorf("node: pointer from %s: %w", p, err))
			} else {
				r := rbuf{b: resp}
				loc := r.str()
				if r.end("pointer reply") != nil || loc != string(local) {
					agreed = false
				}
			}
			pending--
			if pending == 0 {
				reply()
			}
		})
	}
	if pending == 0 {
		g.n.later(reply)
	}
}

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
		for _, other := range g.set(rd, bucketKey(m.Bucket, tagReg)) {
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
		for _, s := range g.staged(rd, m.Bucket) {
			if s.rec.Saga != id {
				continue
			}
			mk := occ.ManifestKey(m.Bucket, s.object)
			cur := rd.Record(mk)
			if cur.Live() && cur.ETag() == s.rec.SrcETag {
				if _, err := g.n.occ.Put(mk, s.rec.Object, occ.IfMatch(s.rec.SrcETag)); err != nil {
					done(err)
					return
				}
				g.n.RecordMeta(mk)
			}
			if err := g.del(s.key); err != nil {
				done(err)
				return
			}
		}
		g.purgeRegion(m.Bucket, m.From, func(err error) {
			if err != nil {
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
}

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
