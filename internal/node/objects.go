package node

import (
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

type sortable interface{ ~string | ~uint64 | ~int }

func sortedKeys[K sortable, V any](m map[K]V) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func prefixKeys(keys []string, prefix string) []string {
	i := sort.SearchStrings(keys, prefix)
	j := i
	for j < len(keys) && strings.HasPrefix(keys[j], prefix) {
		j++
	}
	return keys[i:j]
}

type liveObject struct {
	key  string
	etag occ.ETag
	rec  objectRecord
}

func (n *Node) object(rd occ.Reader, key string) (objectRecord, occ.ETag, error) {
	val, etag, err := rd.Get(key)
	if err != nil {
		return objectRecord{}, etag, err
	}
	if rec, ok := n.cache[etag]; ok {
		return rec, etag, nil
	}
	rec, err := decodeObject(val)
	if err != nil {
		return objectRecord{}, etag, err
	}
	n.cache[etag] = rec
	return rec, etag, nil
}

func (n *Node) liveObjects(rd occ.Reader, bucket string) []liveObject {
	var out []liveObject
	for _, k := range prefixKeys(rd.Keys(), occ.BucketPrefix(occ.KindManifest, bucket)) {
		rec, etag, err := n.object(rd, k)
		if err != nil {
			continue
		}
		_, parts, err := occ.ParseKey(k)
		if err != nil {
			continue
		}
		out = append(out, liveObject{key: parts[1], etag: etag, rec: rec})
	}
	return out
}

type bucketState struct {
	name   string
	rec    bucketRecord
	route  string
	live   string
	hasLiv bool
}

// writable reports whether the bucket accepts writes. A region-routed bucket
// (b.rec.Region != "") is refused until its routing pointer arrives, and any
// routed bucket is unwritable while its route differs from ~live (between
// migration flip and promote).
func (b bucketState) writable() bool {
	if b.route == "" {
		return b.rec.Region == "" && (!b.hasLiv || b.live != "")
	}
	return b.hasLiv && b.live == b.route
}

func (n *Node) bucket(rd occ.Reader, name string) (bucketState, error) {
	if err := storage.ValidBucket(name); err != nil {
		return bucketState{}, err
	}
	val, _, err := rd.Get(bucketKey(name, tagBucket))
	if err != nil {
		if isNotFound(err) {
			return bucketState{}, fmt.Errorf("node: bucket %q: %w", name, storage.ErrNoSuchBucket)
		}
		return bucketState{}, err
	}
	rec, err := decodeBucket(val)
	if err != nil {
		return bucketState{}, err
	}
	bs := bucketState{name: name, rec: rec}
	if r, err := rd.Route(name); err == nil {
		bs.route = string(r)
	}
	if v, _, err := rd.Get(bucketKey(name, tagLive)); err == nil {
		bs.live, bs.hasLiv = string(v), true
	}
	return bs, nil
}

func validObjectKey(key string) error {
	if len(key) == 0 || len(key) > storage.MaxKeyLen {
		return fmt.Errorf("node: object key length %d outside [1, %d]: %w", len(key), storage.MaxKeyLen, storage.ErrInvalid)
	}
	return nil
}

// precondition translates a storage.Condition into an occ.Precondition against
// rd. Any present If-Match header (including an empty strong-tag list) never
// degrades to occ.Unconditional.
func precondition(rd occ.Reader, key string, c storage.Condition) (occ.Precondition, error) {
	ifMatch := c.IfMatch != "" || c.IfMatchList
	switch {
	case c.IfNoneMatch && ifMatch:
		return occ.Precondition{}, fmt.Errorf("node: both If-Match and If-None-Match: %w", storage.ErrInvalid)
	case c.IfNoneMatch:
		return occ.IfNoneMatch(), nil
	case !ifMatch:
		return occ.Unconditional(), nil
	}
	rec := rd.Record(key)
	if c.IfMatch == "*" {
		if !rec.Live() {
			return occ.Precondition{}, fmt.Errorf("node: if-match * on %q: %w", key, occ.ErrPreconditionFailed)
		}
		return occ.IfMatch(rec.ETag()), nil
	}
	tags := c.IfMatchAny
	if c.IfMatch != "" {
		tags = append(slices.Clone(tags), strings.Trim(c.IfMatch, `"`))
	}
	for _, t := range tags {
		e, err := occ.ParseETag(t)
		if err == nil && rec.Live() && e == rec.ETag() {
			return occ.IfMatch(e), nil
		}
	}
	return occ.Precondition{}, fmt.Errorf("node: if-match %q on %q: %w", tags, key, occ.ErrPreconditionFailed)
}

func info(bucket, key string, etag occ.ETag, rec objectRecord) storage.ObjectInfo {
	m := rec.Manifest
	return storage.ObjectInfo{
		Bucket: bucket, Key: key, Size: m.Size, ETag: etag.String(), Version: m.Version.String(),
		ContentSHA: hex.EncodeToString(m.ContentSHA[:]), ContentType: rec.ContentType,
	}
}

func (n *Node) failLater(cb func(storage.ObjectInfo, error), err error) {
	n.later(func() { cb(storage.ObjectInfo{}, err) })
}

func (n *Node) quotaCheck(rd occ.Reader, bs bucketState, dBytes, dObjects int64) error {
	if (bs.rec.QuotaBytes == 0 || dBytes <= 0) && (bs.rec.QuotaObjects == 0 || dObjects <= 0) {
		return nil
	}
	u, _, err := rd.ReadQuota(occ.QuotaKey(bs.name))
	if err != nil && !isNotFound(err) {
		u = occ.Usage{}
	}
	limit := occ.Usage{Bytes: bs.rec.QuotaBytes, Objects: bs.rec.QuotaObjects}
	if _, err := occ.AddUsage(dBytes, dObjects, limit)(u); err != nil {
		return fmt.Errorf("node: put into %q: %w", bs.name, err)
	}
	return nil
}

// PutObject writes req.Data into the bucket's current placement scope, commits
// the single OCC manifest record under req.Cond, updates local quota usage, and
// invokes cb asynchronously with the resulting ObjectInfo or error. Quotas are
// pre-checked locally without cross-node reservation; concurrent writes on
// separate nodes may both succeed and later settle via last-writer-wins.
func (n *Node) PutObject(req storage.PutRequest, cb func(storage.ObjectInfo, error)) {
	rd, err := n.reader()
	if err != nil {
		n.failLater(cb, err)
		return
	}
	bs, err := n.bucket(rd, req.Bucket)
	if err == nil {
		err = validObjectKey(req.Key)
	}
	if err == nil && int64(len(req.Data)) > n.cfg.MaxObjectSize {
		err = fmt.Errorf("node: object of %d bytes over %d: %w", len(req.Data), n.cfg.MaxObjectSize, env.ErrTooLarge)
	}
	if err == nil && !bs.writable() {
		err = fmt.Errorf("node: bucket %q is moving: %w", req.Bucket, storage.ErrBusy)
	}
	key := occ.ManifestKey(req.Bucket, req.Key)
	var pre occ.Precondition
	if err == nil {
		pre, err = precondition(rd, key, req.Cond)
	}
	if err == nil && !pre.Holds(rd.Record(key)) {
		err = fmt.Errorf("node: put %s/%s %s: %w", req.Bucket, req.Key, pre.String(), occ.ErrPreconditionFailed)
	}
	if err == nil {
		dBytes, dObjects := n.delta(rd, key, int64(len(req.Data)))
		err = n.quotaCheck(rd, bs, dBytes, dObjects)
	}
	if err != nil {
		n.stats.PutFailures++
		n.failLater(cb, err)
		return
	}
	n.scope(bs.route).Put(req.Bucket, req.Key, req.Data, func(man *storage.Manifest, err error) {
		n.commitPut(req, bs.route, pre, man, err, cb)
	})
}

func (n *Node) delta(rd occ.Reader, key string, size int64) (int64, int64) {
	if old, _, err := n.object(rd, key); err == nil {
		return size - old.Manifest.Size, 0
	}
	return size, 1
}

func (n *Node) commitPut(req storage.PutRequest, region string, pre occ.Precondition, man *storage.Manifest, err error, cb func(storage.ObjectInfo, error)) {
	fail := func(err error) {
		n.stats.PutFailures++
		cb(storage.ObjectInfo{}, err)
	}
	if err != nil {
		fail(err)
		return
	}
	rd, err := n.reader()
	if err != nil {
		fail(err)
		return
	}
	bs, err := n.bucket(rd, req.Bucket)
	if err != nil {
		fail(err)
		return
	}
	// Re-check bucket writability and route synchronously before committing so a
	// PUT started before a migration flip cannot commit once route != ~live.
	if !bs.writable() || bs.route != region {
		n.markDead(req.Bucket, man.Version)
		fail(fmt.Errorf("node: bucket %q moved during the put: %w", req.Bucket, storage.ErrBusy))
		return
	}
	key := occ.ManifestKey(req.Bucket, req.Key)
	dBytes, dObjects := n.delta(rd, key, man.Size)
	dead := func(err error) {
		n.markDead(req.Bucket, man.Version)
		fail(err)
	}
	if err := n.quotaCheck(rd, bs, dBytes, dObjects); err != nil {
		dead(err)
		return
	}
	rec := objectRecord{ContentType: req.ContentType, Manifest: man}
	val, err := encodeObject(rec)
	if err != nil {
		dead(err)
		return
	}
	old, _, oldErr := n.object(rd, key)
	held := rd.Record(key)
	v, err := n.occ.Put(key, val, pre)
	if err != nil {
		dead(fmt.Errorf("node: commit %s/%s: %w", req.Bucket, req.Key, err))
		return
	}
	n.committed(key, held, v)
	if oldErr == nil {
		n.markDead(req.Bucket, old.Manifest.Version)
	}
	n.stats.Puts++
	n.RecordMeta(key)
	n.usage(req.Bucket, dBytes, dObjects)
	n.cache[v.ETag()] = rec
	cb(info(req.Bucket, req.Key, v.ETag(), rec), nil)
}

// GetObject reads the object's live manifest and reconstructs its full payload
// from any k verified shards, invoking cb asynchronously. If a shard fails
// verification, it flags the bucket's inventory for a full scrub.
func (n *Node) GetObject(bucket, key string, cb func(storage.ObjectInfo, []byte, error)) {
	fail := func(err error) {
		n.stats.GetFailures++
		n.later(func() { cb(storage.ObjectInfo{}, nil, err) })
	}
	oi, rec, err := n.head(bucket, key)
	if err != nil {
		fail(err)
		return
	}
	n.store.Get(rec.Manifest, func(res storage.GetResult, err error) {
		if err != nil {
			n.stats.GetFailures++
			if inv, ok := n.inv[bucket]; ok && isCorrupt(err) {
				inv.full = true
			}
			cb(oi, nil, err)
			return
		}
		n.stats.Gets++
		cb(oi, res.Data, nil)
	})
}

func (n *Node) head(bucket, key string) (storage.ObjectInfo, objectRecord, error) {
	rd, err := n.reader()
	if err != nil {
		return storage.ObjectInfo{}, objectRecord{}, err
	}
	if _, err := n.bucket(rd, bucket); err != nil {
		return storage.ObjectInfo{}, objectRecord{}, err
	}
	if err := validObjectKey(key); err != nil {
		return storage.ObjectInfo{}, objectRecord{}, err
	}
	rec, etag, err := n.object(rd, occ.ManifestKey(bucket, key))
	if err != nil {
		return storage.ObjectInfo{}, objectRecord{}, fmt.Errorf("node: object %s/%s: %w", bucket, key, err)
	}
	return info(bucket, key, etag, rec), rec, nil
}

// HeadObject returns metadata for the live object at bucket/key, or an error
// wrapping ErrNotReady, ErrStopped, storage.ErrNoSuchBucket, storage.ErrInvalid,
// or occ.ErrNotFound.
func (n *Node) HeadObject(bucket, key string) (storage.ObjectInfo, error) {
	oi, _, err := n.head(bucket, key)
	return oi, err
}

// DeleteObject writes an OCC tombstone for bucket/key subject to cond, marks
// the replaced storage version dead (~gc/<version>) so its shards are collected
// after GCGrace, and decrements local quota usage. Tombstones are retained
// indefinitely so stale replicas cannot resurrect deleted keys.
func (n *Node) DeleteObject(bucket, key string, cond storage.Condition) (storage.ObjectInfo, error) {
	rd, err := n.reader()
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	bs, err := n.bucket(rd, bucket)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	if err := validObjectKey(key); err != nil {
		return storage.ObjectInfo{}, err
	}
	if !bs.writable() {
		return storage.ObjectInfo{}, fmt.Errorf("node: bucket %q is moving: %w", bucket, storage.ErrBusy)
	}
	k := occ.ManifestKey(bucket, key)
	pre, err := precondition(rd, k, cond)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	old, _, oerr := n.object(rd, k)
	held := rd.Record(k)
	v, err := n.occ.Delete(k, pre)
	if err != nil {
		return storage.ObjectInfo{}, fmt.Errorf("node: delete %s/%s: %w", bucket, key, err)
	}
	n.committed(k, held, v)
	n.stats.Deletes++
	n.RecordMeta(k)
	oi := storage.ObjectInfo{Bucket: bucket, Key: key, ETag: v.ETag().String(), Deleted: true}
	if oerr == nil {
		n.usage(bucket, -old.Manifest.Size, -1)
		oi.Version = old.Manifest.Version.String()
		n.markDead(bucket, old.Manifest.Version)
	}
	return oi, nil
}

// ListObjects returns live objects in bucket whose keys start with prefix, up
// to limit (unlimited when limit <= 0).
func (n *Node) ListObjects(bucket, prefix string, limit int) ([]storage.ObjectInfo, error) {
	rd, err := n.reader()
	if err != nil {
		return nil, err
	}
	if _, err := n.bucket(rd, bucket); err != nil {
		return nil, err
	}
	var out []storage.ObjectInfo
	for _, o := range n.liveObjects(rd, bucket) {
		if !strings.HasPrefix(o.key, prefix) {
			continue
		}
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, info(bucket, o.key, o.etag, o.rec))
	}
	return out, nil
}

// CreateBucket creates a new bucket record and, when spec.Region is non-empty,
// initializes its routing pointer and ~live tag to that region. It returns
// storage.ErrBucketExists if the bucket already exists locally.
func (n *Node) CreateBucket(spec storage.BucketSpec) error {
	rd, err := n.reader()
	if err != nil {
		return err
	}
	if err := storage.ValidBucket(spec.Name); err != nil {
		return err
	}
	if len(spec.Region) > 255 {
		return fmt.Errorf("node: bucket region of %d bytes: %w", len(spec.Region), storage.ErrInvalid)
	}
	if rd.Record(bucketKey(spec.Name, tagBucket)).Live() {
		return fmt.Errorf("node: create %q: %w", spec.Name, storage.ErrBucketExists)
	}
	rec := bucketRecord{Region: spec.Region, QuotaBytes: spec.QuotaBytes, QuotaObjects: spec.QuotaObjects}
	k := bucketKey(spec.Name, tagBucket)
	if _, err := n.occ.Put(k, encodeBucket(rec), occ.IfNoneMatch()); err != nil {
		return fmt.Errorf("node: create %q: %w", spec.Name, err)
	}
	n.RecordMeta(k)
	if spec.Region == "" {
		return nil
	}
	if _, err := n.occ.SetRoute(spec.Name, env.Region(spec.Region)); err != nil {
		return fmt.Errorf("node: create %q: route: %w", spec.Name, err)
	}
	n.RecordMeta(occ.RouteKey(spec.Name))
	return n.setLive(spec.Name, spec.Region)
}

func (n *Node) setLive(bucket, region string) error {
	k := bucketKey(bucket, tagLive)
	if _, err := n.occ.SetScalar(k, []byte(region)); err != nil {
		return fmt.Errorf("node: bucket %q live at %q: %w", bucket, region, err)
	}
	n.RecordMeta(k)
	return nil
}

// PutTag writes one user tag on an existing object subject to cond. Names
// starting with "~" are reserved for system metadata and rejected with
// storage.ErrInvalid.
func (n *Node) PutTag(bucket, key, name, value string, cond storage.Condition) (storage.TagInfo, error) {
	rd, err := n.reader()
	if err != nil {
		return storage.TagInfo{}, err
	}
	if _, _, err := n.head(bucket, key); err != nil {
		return storage.TagInfo{}, err
	}
	if name == "" || systemName(name) || len(name) > 255 || len(value) > 4096 {
		return storage.TagInfo{}, fmt.Errorf("node: tag %q: %w", name, storage.ErrInvalid)
	}
	k := occ.TagKey(bucket, key, name)
	pre, err := precondition(rd, k, cond)
	if err != nil {
		return storage.TagInfo{}, err
	}
	held := rd.Record(k)
	v, err := n.occ.PutTag(bucket, key, name, value, pre)
	if err != nil {
		return storage.TagInfo{}, fmt.Errorf("node: tag %s/%s %q: %w", bucket, key, name, err)
	}
	n.committed(k, held, v)
	n.RecordMeta(k)
	return storage.TagInfo{Name: name, Value: value, ETag: v.ETag().String()}, nil
}

// Tags returns all non-system tags currently attached to bucket/key.
func (n *Node) Tags(bucket, key string) ([]storage.TagInfo, error) {
	rd, err := n.reader()
	if err != nil {
		return nil, err
	}
	if _, _, err := n.head(bucket, key); err != nil {
		return nil, err
	}
	var out []storage.TagInfo
	for _, k := range prefixKeys(rd.Keys(), occ.BucketPrefix(occ.KindTag, bucket)) {
		_, parts, err := occ.ParseKey(k)
		if err != nil || parts[1] != key || systemName(parts[2]) {
			continue
		}
		val, etag, err := rd.Tag(bucket, key, parts[2])
		if err != nil {
			continue
		}
		out = append(out, storage.TagInfo{Name: parts[2], Value: val, ETag: etag.String()})
	}
	return out, nil
}

func parsePerm(s string) (occ.Permission, error) {
	var p occ.Permission
	for _, c := range s {
		switch c {
		case 'r':
			p |= occ.PermRead
		case 'w':
			p |= occ.PermWrite
		case 'a':
			p |= occ.PermAdmin
		default:
			return 0, fmt.Errorf("node: permission %q: %w", s, storage.ErrInvalid)
		}
	}
	return p, nil
}

func permString(p occ.Permission) string {
	var b strings.Builder
	for _, x := range []struct {
		bit occ.Permission
		c   byte
	}{{occ.PermRead, 'r'}, {occ.PermWrite, 'w'}, {occ.PermAdmin, 'a'}} {
		if p&x.bit != 0 {
			b.WriteByte(x.c)
		}
	}
	return b.String()
}

// PutACL sets a principal's permission letters (subset of "r", "w", "a") on
// bucket subject to cond.
func (n *Node) PutACL(bucket, principal, perm string, cond storage.Condition) (storage.ACLInfo, error) {
	rd, err := n.reader()
	if err != nil {
		return storage.ACLInfo{}, err
	}
	if _, err := n.bucket(rd, bucket); err != nil {
		return storage.ACLInfo{}, err
	}
	if principal == "" || len(principal) > 255 {
		return storage.ACLInfo{}, fmt.Errorf("node: principal %q: %w", principal, storage.ErrInvalid)
	}
	p, err := parsePerm(perm)
	if err != nil {
		return storage.ACLInfo{}, err
	}
	k := occ.ACLKey(bucket, principal)
	pre, err := precondition(rd, k, cond)
	if err != nil {
		return storage.ACLInfo{}, err
	}
	v, err := n.occ.PutACL(bucket, principal, p, pre)
	if err != nil {
		return storage.ACLInfo{}, fmt.Errorf("node: acl %s %q: %w", bucket, principal, err)
	}
	n.RecordMeta(k)
	return storage.ACLInfo{Principal: principal, Perm: permString(p), ETag: v.ETag().String()}, nil
}

// ACL returns a principal's permission entry on bucket, or occ.ErrNotFound if
// none is set.
func (n *Node) ACL(bucket, principal string) (storage.ACLInfo, error) {
	rd, err := n.reader()
	if err != nil {
		return storage.ACLInfo{}, err
	}
	if _, err := n.bucket(rd, bucket); err != nil {
		return storage.ACLInfo{}, err
	}
	p, etag, err := rd.ACL(bucket, principal)
	if err != nil {
		return storage.ACLInfo{}, fmt.Errorf("node: acl %s %q: %w", bucket, principal, err)
	}
	return storage.ACLInfo{Principal: principal, Perm: permString(p), ETag: etag.String()}, nil
}

// Quota returns bucket's configured byte and object limits and its currently
// recorded usage.
func (n *Node) Quota(bucket string) (storage.QuotaInfo, error) {
	rd, err := n.reader()
	if err != nil {
		return storage.QuotaInfo{}, err
	}
	bs, err := n.bucket(rd, bucket)
	if err != nil {
		return storage.QuotaInfo{}, err
	}
	u, _, err := rd.ReadQuota(occ.QuotaKey(bucket))
	if err != nil && !isNotFound(err) {
		return storage.QuotaInfo{}, err
	}
	return storage.QuotaInfo{Bucket: bucket, LimitBytes: bs.rec.QuotaBytes, LimitObjects: bs.rec.QuotaObjects, UsedBytes: u.Bytes, UsedObjects: u.Objects}, nil
}

// SetQuota updates bucket's configured byte and object limits via If-Match on
// the bucket record and returns the resulting QuotaInfo.
func (n *Node) SetQuota(bucket string, bytes, objects uint64) (storage.QuotaInfo, error) {
	rd, err := n.reader()
	if err != nil {
		return storage.QuotaInfo{}, err
	}
	bs, err := n.bucket(rd, bucket)
	if err != nil {
		return storage.QuotaInfo{}, err
	}
	k := bucketKey(bucket, tagBucket)
	rec := bs.rec
	rec.QuotaBytes, rec.QuotaObjects = bytes, objects
	if _, err := n.occ.Put(k, encodeBucket(rec), occ.IfMatch(rd.Record(k).ETag())); err != nil {
		return storage.QuotaInfo{}, fmt.Errorf("node: quota of %q: %w", bucket, err)
	}
	n.RecordMeta(k)
	return n.Quota(bucket)
}

// StartMigration begins a compensating migration saga coordinated by this node
// to move a region-routed bucket to region to. Unrouted buckets or migrations
// to the current region fail with ErrNoMigration.
func (n *Node) StartMigration(bucket, to string) (storage.MigrationInfo, error) {
	rd, err := n.reader()
	if err != nil {
		return storage.MigrationInfo{}, err
	}
	bs, err := n.bucket(rd, bucket)
	if err != nil {
		return storage.MigrationInfo{}, err
	}
	if bs.route == "" || to == "" || to == bs.route || len(to) > 255 {
		return storage.MigrationInfo{}, fmt.Errorf("node: migrate %q from %q to %q: %w", bucket, bs.route, to, ErrNoMigration)
	}
	id := n.newSagaID()
	in := saga.MigrationInput{Bucket: bucket, From: bs.route, To: to}
	if err := n.engine.Begin(saga.ID(id), saga.MigrationName, in.Encode()); err != nil {
		return storage.MigrationInfo{}, fmt.Errorf("node: migrate %q: %w", bucket, err)
	}
	return storage.MigrationInfo{ID: id, Bucket: bucket, From: in.From, To: to, State: saga.StateRunning.String()}, nil
}

// BeginMigration starts a migration saga with an explicit saga.ID and input.
func (n *Node) BeginMigration(id saga.ID, in saga.MigrationInput) error {
	if _, err := n.reader(); err != nil {
		return err
	}
	return n.engine.Begin(id, saga.MigrationName, in.Encode())
}

func (n *Node) newSagaID() string {
	b := []byte(n.e.ID)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			b[i] = '_'
		}
	}
	if len(b) > 64 {
		b = b[:64]
	}
	return fmt.Sprintf("%s-%x-%x", b, uint64(n.e.Clock.Now()), n.e.Rand.Uint32())
}

// Migration returns the status of a migration saga coordinated by this node, or
// an error wrapping ErrUnknownSaga and saga.ErrSagaNotFound if unknown locally.
func (n *Node) Migration(id string) (storage.MigrationInfo, error) {
	if n.stopped {
		return storage.MigrationInfo{}, ErrStopped
	}
	st, err := n.engine.Status(saga.ID(id))
	if errors.Is(err, saga.ErrSagaNotFound) {
		return storage.MigrationInfo{}, fmt.Errorf("node: migration %q: %w: %w", id, ErrUnknownSaga, err)
	}
	if err != nil {
		return storage.MigrationInfo{}, fmt.Errorf("node: migration %q: %w", id, err)
	}
	mi := storage.MigrationInfo{ID: id, State: st.State.String(), Step: st.Step, Attempt: st.Attempt}
	if len(st.Outputs) == saga.MigrationSteps {
		mi.Outcome = string(st.Outputs[saga.MigrationSteps-1])
	}
	if st.Err != nil {
		mi.Error = st.Err.Error()
	}
	return mi, nil
}

// Members returns all members (alive, suspect, and retained dead entries) from
// the node's local gossip table.
func (n *Node) Members() []storage.MemberInfo {
	var out []storage.MemberInfo
	for _, m := range n.gossip.All() {
		out = append(out, storage.MemberInfo{ID: string(m.ID), Region: string(m.Region), State: m.State.String(), Incarnation: m.Incarnation})
	}
	return out
}

func readableKey(key string) string {
	kind, parts, err := occ.ParseKey(key)
	if err != nil {
		return hex.EncodeToString([]byte(key))
	}
	return kindName(kind) + ":" + strings.Join(parts, "/")
}

func kindName(k occ.Kind) string {
	switch k {
	case occ.KindManifest:
		return "manifest"
	case occ.KindQuota:
		return "quota"
	case occ.KindTag:
		return "tag"
	case occ.KindACL:
		return "acl"
	case occ.KindRoute:
		return "route"
	case occ.KindObject:
		return "object"
	}
	return fmt.Sprintf("kind%d", k)
}

// Health reports node readiness, live membership count, persisted LWW discard
// count, the recent discard tail, lost-object count, and diagnostic counters.
func (n *Node) Health() storage.Health {
	h := storage.Health{Node: string(n.e.ID), Region: string(n.e.Region), Ready: n.Ready(), LiveMembers: n.gossip.LiveCount()}
	if n.Ready() {
		d, r := n.occ.Digest(), n.meta.Tree().Root()
		h.MetaDigest, h.MetaRoot = hex.EncodeToString(d[:]), hex.EncodeToString(r[:])
	}
	h.Discards = n.occ.LWWDiscards()
	for _, d := range n.occ.DiscardTail() {
		h.RecentDiscards = append(h.RecentDiscards, storage.DiscardInfo{Key: readableKey(d.Key), Winner: d.Winner.ID().String(), Loser: d.Loser.ID().String()})
	}
	var lost uint64
	if rd, err := n.occ.Reader(); err == nil {
		for _, k := range rd.Keys() {
			if len(k) == 0 || occ.Kind(k[0]) != occ.KindTag || !rd.Record(k).Live() {
				continue
			}
			if _, parts, err := occ.ParseKey(k); err == nil && strings.HasPrefix(parts[2], tagLost) {
				lost++
			}
		}
	}
	s := n.stats
	ms := n.meta.Stats()
	for _, c := range []storage.Counter{
		{Name: "lww_discards", Value: h.Discards}, {Name: "discards_seen", Value: s.Discards},
		{Name: "discarded_objects", Value: s.DiscardedObject}, {Name: "quota_discards", Value: s.QuotaDiscards},
		{Name: "recounts", Value: s.Recounts}, {Name: "recount_writes", Value: s.RecountWrites},
		{Name: "puts", Value: s.Puts}, {Name: "put_failures", Value: s.PutFailures}, {Name: "gets", Value: s.Gets},
		{Name: "get_failures", Value: s.GetFailures}, {Name: "deletes", Value: s.Deletes},
		{Name: "rebuilds", Value: s.Rebuilds}, {Name: "rebuild_failures", Value: s.RebuildFailures}, {Name: "rebuilt_bytes", Value: s.RebuiltBytes},
		{Name: "collected_shards", Value: s.Collected}, {Name: "collected_loser_shards", Value: s.CollectedLoser},
		{Name: "meta_rounds", Value: ms.Rounds}, {Name: "meta_repaired", Value: ms.Repaired},
		{Name: "malformed", Value: s.Malformed}, {Name: "member_deaths", Value: s.MemberDeaths},
		{Name: "lost_objects", Value: lost}, {Name: "orphan_records", Value: s.OrphanRecords},
		{Name: "absent_records", Value: s.AbsentRecords}, {Name: "absent_refused", Value: s.AbsentRefused},
		{Name: "purged_shards", Value: s.Purged}, {Name: "purge_refused", Value: s.PurgeRefused},
	} {
		h.Counters = append(h.Counters, c)
	}
	return h
}
