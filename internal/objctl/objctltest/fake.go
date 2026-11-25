// Package objctltest holds objctl test support shared by the in-process tests
// in cmd/objctl and the real-process tests in test/e2e: an in-memory
// httpapi.Service that follows a single node's semantics, a seeded generator
// of objctl invocations with an oracle that predicts each exit code and
// output, and a human-readable transcript of every step. The in-process model
// run checks the CLI and transport path against the Fake's own semantics;
// independence from the server comes from the tier R run against real nodes.
package objctltest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/saga"
	"github.com/nickstrad/object-store/internal/storage"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
)

type record struct {
	live bool
	etag string
}

type fakeObject struct {
	record
	data    []byte
	ct      string
	version string
}

type fakeBucket struct {
	spec    storage.BucketSpec
	used    occ.Usage
	objects map[string]*fakeObject
	tags    map[string]map[string]*fakeTag
	acls    map[string]*fakeACL
}

type fakeTag struct {
	record
	value string
}

type fakeACL struct {
	record
	perm string
}

// state returns the record of o, t or a, or the zero (never written) record
// when the pointer is nil.
func (o *fakeObject) state() record {
	if o == nil {
		return record{}
	}
	return o.record
}

func (t *fakeTag) state() record {
	if t == nil {
		return record{}
	}
	return t.record
}

func (a *fakeACL) state() record {
	if a == nil {
		return record{}
	}
	return a.record
}

// Fake is an in-memory httpapi.Service modelling one node's bucket, object,
// tag, ACL and quota semantics: preconditions as node.precondition evaluates
// them, tombstoned deletes, tags that outlive their object, local quota usage,
// and the same sentinel errors. Migrations are not modelled.
type Fake struct {
	mu      sync.Mutex
	n       uint64
	buckets map[string]*fakeBucket
}

var _ httpapi.Service = (*Fake)(nil)

// NewFake returns an empty Fake.
func NewFake() *Fake { return &Fake{buckets: map[string]*fakeBucket{}} }

func (f *Fake) next() string {
	f.n++
	return fmt.Sprintf("%016x", f.n*0x9e3779b97f4a7c15)
}

func (f *Fake) bucket(name string) (*fakeBucket, error) {
	if err := storage.ValidBucket(name); err != nil {
		return nil, err
	}
	b, ok := f.buckets[name]
	if !ok {
		return nil, fmt.Errorf("fake: bucket %q: %w", name, storage.ErrNoSuchBucket)
	}
	return b, nil
}

// holds evaluates c against rec as a node does: c.Canonical() followed by
// occ.Precondition.Holds.
func holds(rec record, c storage.Condition) error {
	w, err := c.Canonical()
	if err != nil {
		return err
	}
	switch {
	case w.IfNoneMatch && rec.live,
		w.IfMatch == "*" && !rec.live,
		w.IfMatchList && (!rec.live || !slices.Contains(w.IfMatchAny, rec.etag)):
		return fmt.Errorf("fake: condition %+v against %+v: %w", w, rec, occ.ErrPreconditionFailed)
	}
	return nil
}

// CreateBucket implements httpapi.Service.
func (f *Fake) CreateBucket(_ context.Context, spec storage.BucketSpec) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := storage.ValidBucket(spec.Name); err != nil {
		return err
	}
	if _, ok := f.buckets[spec.Name]; ok {
		return fmt.Errorf("fake: create %q: %w", spec.Name, storage.ErrBucketExists)
	}
	f.buckets[spec.Name] = &fakeBucket{spec: spec, objects: map[string]*fakeObject{}, tags: map[string]map[string]*fakeTag{}, acls: map[string]*fakeACL{}}
	return nil
}

func (f *Fake) info(b *fakeBucket, key string, o *fakeObject) storage.ObjectInfo {
	sum := sha256.Sum256(o.data)
	return storage.ObjectInfo{Bucket: b.spec.Name, Key: key, Size: int64(len(o.data)), ETag: o.etag, Version: o.version,
		ContentSHA: hex.EncodeToString(sum[:]), ContentType: o.ct}
}

// ListObjects implements httpapi.Service.
func (f *Fake) ListObjects(_ context.Context, bucket, prefix string, limit int) ([]storage.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.bucket(bucket)
	if err != nil {
		return nil, err
	}
	var keys []string
	for k, o := range b.objects {
		if o.live && strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var out []storage.ObjectInfo
	for _, k := range keys {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, f.info(b, k, b.objects[k]))
	}
	return out, nil
}

// quotaCheck follows node.quotaCheck: a write that grows no limited
// dimension passes, otherwise occ.AddUsage decides.
func quotaCheck(limits, used occ.Usage, dBytes, dObjects int64) error {
	if (limits.Bytes == 0 || dBytes <= 0) && (limits.Objects == 0 || dObjects <= 0) {
		return nil
	}
	_, err := occ.AddUsage(dBytes, dObjects, limits)(used)
	return err
}

// addUsage applies a usage delta the way node.usage does.
func addUsage(used occ.Usage, dBytes, dObjects int64) occ.Usage {
	if u, err := occ.AddUsage(dBytes, dObjects, occ.Usage{})(used); err == nil {
		return u
	}
	return used
}

func limitsOf(spec storage.BucketSpec) occ.Usage {
	return occ.Usage{Bytes: spec.QuotaBytes, Objects: spec.QuotaObjects}
}

// PutObject implements httpapi.Service.
func (f *Fake) PutObject(_ context.Context, req storage.PutRequest) (storage.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.bucket(req.Bucket)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	if err := storage.ValidKey(req.Key); err != nil {
		return storage.ObjectInfo{}, err
	}
	if len(req.Data) > httpapi.DefaultMaxObjectSize {
		return storage.ObjectInfo{}, fmt.Errorf("fake: object of %d bytes: %w", len(req.Data), env.ErrTooLarge)
	}
	old := b.objects[req.Key]
	rec := old.state()
	if err := holds(rec, req.Cond); err != nil {
		return storage.ObjectInfo{}, err
	}
	dBytes, dObjects := int64(len(req.Data)), int64(1)
	if rec.live {
		dBytes, dObjects = int64(len(req.Data))-int64(len(old.data)), 0
	}
	if err := quotaCheck(limitsOf(b.spec), b.used, dBytes, dObjects); err != nil {
		return storage.ObjectInfo{}, err
	}
	o := &fakeObject{record: record{live: true, etag: f.next()}, data: slices.Clone(req.Data), ct: req.ContentType, version: f.next()}
	b.objects[req.Key] = o
	b.used = addUsage(b.used, dBytes, dObjects)
	return f.info(b, req.Key, o), nil
}

func (f *Fake) head(bucket, key string) (*fakeBucket, *fakeObject, error) {
	b, err := f.bucket(bucket)
	if err != nil {
		return nil, nil, err
	}
	if err := storage.ValidKey(key); err != nil {
		return nil, nil, err
	}
	o := b.objects[key]
	if o == nil || !o.live {
		return nil, nil, fmt.Errorf("fake: object %s/%s: %w", bucket, key, occ.ErrNotFound)
	}
	return b, o, nil
}

// GetObject implements httpapi.Service.
func (f *Fake) GetObject(_ context.Context, bucket, key string) (storage.ObjectInfo, []byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, o, err := f.head(bucket, key)
	if err != nil {
		return storage.ObjectInfo{}, nil, err
	}
	return f.info(b, key, o), slices.Clone(o.data), nil
}

// HeadObject implements httpapi.Service.
func (f *Fake) HeadObject(_ context.Context, bucket, key string) (storage.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, o, err := f.head(bucket, key)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	return f.info(b, key, o), nil
}

// DeleteObject implements httpapi.Service.
func (f *Fake) DeleteObject(_ context.Context, bucket, key string, cond storage.Condition) (storage.ObjectInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.bucket(bucket)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	if err := storage.ValidKey(key); err != nil {
		return storage.ObjectInfo{}, err
	}
	o := b.objects[key]
	rec := o.state()
	if err := holds(rec, cond); err != nil {
		return storage.ObjectInfo{}, err
	}
	if !rec.live {
		return storage.ObjectInfo{}, fmt.Errorf("fake: delete %s/%s: %w", bucket, key, occ.ErrNotFound)
	}
	b.used = addUsage(b.used, -int64(len(o.data)), -1)
	o.live, o.etag = false, f.next()
	return storage.ObjectInfo{Bucket: bucket, Key: key, ETag: o.etag, Version: o.version, Deleted: true}, nil
}

// PutTag implements httpapi.Service.
func (f *Fake) PutTag(_ context.Context, bucket, key, name, value string, cond storage.Condition) (storage.TagInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _, err := f.head(bucket, key)
	if err != nil {
		return storage.TagInfo{}, err
	}
	if name == "" || strings.HasPrefix(name, "~") || len(name) > 255 || len(value) > 4096 {
		return storage.TagInfo{}, fmt.Errorf("fake: tag %q: %w", name, storage.ErrInvalid)
	}
	if err := holds(b.tags[key][name].state(), cond); err != nil {
		return storage.TagInfo{}, err
	}
	if b.tags[key] == nil {
		b.tags[key] = map[string]*fakeTag{}
	}
	t := &fakeTag{record: record{live: true, etag: f.next()}, value: value}
	b.tags[key][name] = t
	return storage.TagInfo{Name: name, Value: value, ETag: t.etag}, nil
}

// Tags implements httpapi.Service.
func (f *Fake) Tags(_ context.Context, bucket, key string) ([]storage.TagInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _, err := f.head(bucket, key)
	if err != nil {
		return nil, err
	}
	var out []storage.TagInfo
	for name, t := range b.tags[key] {
		out = append(out, storage.TagInfo{Name: name, Value: t.value, ETag: t.etag})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// canonicalPerm returns the node's rendering of a permission string (the set
// of r, w, a in that order) and false if perm holds any other character.
func canonicalPerm(perm string) (string, bool) {
	var have [3]bool
	for _, c := range perm {
		i := strings.IndexRune("rwa", c)
		if i < 0 {
			return "", false
		}
		have[i] = true
	}
	var out []byte
	for i, c := range []byte("rwa") {
		if have[i] {
			out = append(out, c)
		}
	}
	return string(out), true
}

// PutACL implements httpapi.Service.
func (f *Fake) PutACL(_ context.Context, bucket, principal, perm string, cond storage.Condition) (storage.ACLInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.bucket(bucket)
	if err != nil {
		return storage.ACLInfo{}, err
	}
	if principal == "" || len(principal) > 255 {
		return storage.ACLInfo{}, fmt.Errorf("fake: principal %q: %w", principal, storage.ErrInvalid)
	}
	p, ok := canonicalPerm(perm)
	if !ok {
		return storage.ACLInfo{}, fmt.Errorf("fake: permission %q: %w", perm, storage.ErrInvalid)
	}
	if err := holds(b.acls[principal].state(), cond); err != nil {
		return storage.ACLInfo{}, err
	}
	a := &fakeACL{record: record{live: true, etag: f.next()}, perm: p}
	b.acls[principal] = a
	return storage.ACLInfo{Principal: principal, Perm: p, ETag: a.etag}, nil
}

// ACL implements httpapi.Service.
func (f *Fake) ACL(_ context.Context, bucket, principal string) (storage.ACLInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.bucket(bucket)
	if err != nil {
		return storage.ACLInfo{}, err
	}
	a := b.acls[principal]
	if a == nil {
		return storage.ACLInfo{}, fmt.Errorf("fake: acl %q: %w", principal, occ.ErrNotFound)
	}
	return storage.ACLInfo{Principal: principal, Perm: a.perm, ETag: a.etag}, nil
}

func quotaInfo(b *fakeBucket) storage.QuotaInfo {
	return storage.QuotaInfo{Bucket: b.spec.Name, LimitBytes: b.spec.QuotaBytes, LimitObjects: b.spec.QuotaObjects, UsedBytes: b.used.Bytes, UsedObjects: b.used.Objects}
}

// Quota implements httpapi.Service.
func (f *Fake) Quota(_ context.Context, bucket string) (storage.QuotaInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.bucket(bucket)
	if err != nil {
		return storage.QuotaInfo{}, err
	}
	return quotaInfo(b), nil
}

// SetQuota implements httpapi.Service.
func (f *Fake) SetQuota(_ context.Context, bucket string, bytes, objects uint64) (storage.QuotaInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := f.bucket(bucket)
	if err != nil {
		return storage.QuotaInfo{}, err
	}
	b.spec.QuotaBytes, b.spec.QuotaObjects = bytes, objects
	return quotaInfo(b), nil
}

// StartMigration implements httpapi.Service; the fake never migrates.
func (f *Fake) StartMigration(_ context.Context, bucket, to string) (storage.MigrationInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, err := f.bucket(bucket); err != nil {
		return storage.MigrationInfo{}, err
	}
	return storage.MigrationInfo{}, fmt.Errorf("fake: migrate %q to %q: %w", bucket, to, storage.ErrInvalid)
}

// Migration implements httpapi.Service; no migration exists.
func (f *Fake) Migration(_ context.Context, id string) (storage.MigrationInfo, error) {
	return storage.MigrationInfo{}, fmt.Errorf("fake: migration %q: %w", id, saga.ErrSagaNotFound)
}

// Members implements httpapi.Service with a single live member.
func (f *Fake) Members(context.Context) ([]storage.MemberInfo, error) {
	return []storage.MemberInfo{{ID: "fake:1", Region: "r0", State: "alive", Incarnation: 1}}, nil
}

// Health implements httpapi.Service.
func (f *Fake) Health(context.Context) (storage.Health, error) {
	return storage.Health{Node: "fake:1", Region: "r0", Ready: true, LiveMembers: 1}, nil
}
