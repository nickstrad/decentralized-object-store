package occ

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type Kind uint8

const (
	KindTag Kind = iota + 1
	KindACL
	KindQuota
	KindObject
	KindManifest
	KindRoute
	KindContentType
)

var kindParts = [...]int{KindTag: 3, KindACL: 2, KindQuota: 1, KindObject: 2, KindManifest: 2, KindRoute: 1, KindContentType: 2}

func (k Kind) String() string {
	switch k {
	case KindTag:
		return "tag"
	case KindACL:
		return "acl"
	case KindQuota:
		return "quota"
	case KindObject:
		return "object"
	case KindManifest:
		return "manifest"
	case KindRoute:
		return "route"
	case KindContentType:
		return "content-type"
	}
	return "kind(" + strconv.Itoa(int(k)) + ")"
}

func (k Kind) valid() bool { return k >= KindTag && k <= KindContentType }

func makeKey(k Kind, parts ...string) string {
	b := []byte{byte(k)}
	for _, p := range parts {
		b = binary.AppendUvarint(b, uint64(len(p)))
		b = append(b, p...)
	}
	return string(b)
}

func TagKey(bucket, object, name string) string { return makeKey(KindTag, bucket, object, name) }
func ACLKey(bucket, principal string) string    { return makeKey(KindACL, bucket, principal) }
func QuotaKey(bucket string) string             { return makeKey(KindQuota, bucket) }
func ObjectKey(bucket, object string) string    { return makeKey(KindObject, bucket, object) }
func ManifestKey(bucket, object string) string  { return makeKey(KindManifest, bucket, object) }
func RouteKey(bucket string) string             { return makeKey(KindRoute, bucket) }
func ContentTypeKey(bucket, object string) string {
	return makeKey(KindContentType, bucket, object)
}

func BucketPrefix(k Kind, bucket string) string { return makeKey(k, bucket) }

func ParseKey(key string) (Kind, []string, error) {
	if len(key) == 0 || !Kind(key[0]).valid() {
		return 0, nil, fmt.Errorf("occ: parse key %q: kind: %w", key, ErrMalformed)
	}
	k := Kind(key[0])
	d := &decoder{b: []byte(key), off: 1}
	parts := make([]string, 0, kindParts[k])
	for i := 0; i < kindParts[k] && d.err == nil; i++ {
		parts = append(parts, d.str())
	}
	d.end()
	if d.err != nil {
		return 0, nil, fmt.Errorf("occ: parse %s key %q: %w", k.String(), key, d.err)
	}
	if makeKey(k, parts...) != key {
		return 0, nil, fmt.Errorf("occ: parse %s key %q: non-canonical: %w", k.String(), key, ErrMalformed)
	}
	return k, parts, nil
}

type Permission uint8

const (
	PermRead Permission = 1 << iota
	PermWrite
	PermAdmin
	permAll = PermRead | PermWrite | PermAdmin
)

type ObjectMeta struct {
	Size    uint64
	Hash    [32]byte
	Created int64
}

func (m ObjectMeta) Encode() []byte {
	var e encoder
	e.uvarint(m.Size)
	e.fixed(&m.Hash)
	e.varint(m.Created)
	return e.b
}

func DecodeObjectMeta(b []byte) (ObjectMeta, error) {
	d := &decoder{b: b}
	var m ObjectMeta
	m.Size = d.uvarint()
	d.fixed(&m.Hash)
	m.Created = d.varint()
	d.end()
	if d.err == nil && string(m.Encode()) != string(b) {
		d.fail("non-canonical object metadata")
	}
	if d.err != nil {
		return ObjectMeta{}, fmt.Errorf("occ: object metadata: %w", d.err)
	}
	return m, nil
}

func putTag(w putter, bucket, object, name, value string, p Precondition) (Version, error) {
	return w.Put(TagKey(bucket, object, name), []byte(value), p)
}

func putACL(w putter, bucket, principal string, perm Permission, p Precondition) (Version, error) {
	if perm&^permAll != 0 {
		return Version{}, fmt.Errorf("occ: put acl %s/%s: permission %d: %w", bucket, principal, perm, env.ErrInvalidArgument)
	}
	return w.Put(ACLKey(bucket, principal), []byte{byte(perm)}, p)
}

func putObjectMeta(w putter, bucket, object string, m ObjectMeta, p Precondition) (Version, error) {
	return w.Put(ObjectKey(bucket, object), m.Encode(), p)
}

func putManifest(w putter, bucket, object string, manifest []byte, p Precondition) (Version, error) {
	return w.Put(ManifestKey(bucket, object), manifest, p)
}

func setRoute(w putter, bucket string, region env.Region) (Version, error) {
	return w.Put(RouteKey(bucket), []byte(region), Unconditional())
}

func setContentType(w putter, bucket, object, contentType string) (Version, error) {
	return w.Put(ContentTypeKey(bucket, object), []byte(contentType), Unconditional())
}

func (s *Store) PutTag(bucket, object, name, value string, p Precondition) (Version, error) {
	return putTag(s, bucket, object, name, value, p)
}

func (s *Store) PutACL(bucket, principal string, perm Permission, p Precondition) (Version, error) {
	return putACL(s, bucket, principal, perm, p)
}

func (s *Store) PutObjectMeta(bucket, object string, m ObjectMeta, p Precondition) (Version, error) {
	return putObjectMeta(s, bucket, object, m, p)
}

func (s *Store) PutManifest(bucket, object string, manifest []byte, p Precondition) (Version, error) {
	return putManifest(s, bucket, object, manifest, p)
}

func (s *Store) SetRoute(bucket string, region env.Region) (Version, error) {
	return setRoute(s, bucket, region)
}

func (s *Store) SetContentType(bucket, object, contentType string) (Version, error) {
	return setContentType(s, bucket, object, contentType)
}

func (r Reader) Tag(bucket, object, name string) (string, ETag, error) {
	b, e, err := r.Get(TagKey(bucket, object, name))
	return string(b), e, err
}

func (r Reader) ACL(bucket, principal string) (Permission, ETag, error) {
	b, e, err := r.Get(ACLKey(bucket, principal))
	if err != nil {
		return 0, e, err
	}
	if len(b) != 1 || Permission(b[0])&^permAll != 0 {
		return 0, e, fmt.Errorf("occ: acl %s/%s: %w", bucket, principal, ErrMalformed)
	}
	return Permission(b[0]), e, nil
}

func (r Reader) ObjectMeta(bucket, object string) (ObjectMeta, ETag, error) {
	b, e, err := r.Get(ObjectKey(bucket, object))
	if err != nil {
		return ObjectMeta{}, e, err
	}
	m, err := DecodeObjectMeta(b)
	return m, e, err
}

func (r Reader) Manifest(bucket, object string) ([]byte, ETag, error) {
	return r.Get(ManifestKey(bucket, object))
}

func (r Reader) Route(bucket string) (env.Region, error) {
	b, _, err := r.Scalar(RouteKey(bucket))
	return env.Region(b), err
}

func (r Reader) ContentType(bucket, object string) (string, error) {
	b, _, err := r.Scalar(ContentTypeKey(bucket, object))
	return string(b), err
}

func (r Reader) RecountObjects(bucket string) (Usage, error) {
	var u Usage
	for _, k := range r.doc.KeysWithPrefix(BucketPrefix(KindObject, bucket)) {
		rec := r.doc.Record(k)
		if !rec.Live() {
			continue
		}
		m, err := DecodeObjectMeta(rec.v.value)
		if err != nil {
			return Usage{}, fmt.Errorf("occ: recount %s: %w", bucket, err)
		}
		if u.Objects == math.MaxUint64 || m.Size > math.MaxUint64-u.Bytes {
			return Usage{}, fmt.Errorf("occ: recount %s: %d objects %d bytes plus %d bytes: %w", bucket, u.Objects, u.Bytes, m.Size, ErrUsageOverflow)
		}
		u.Objects++
		u.Bytes += m.Size
	}
	return u, nil
}
