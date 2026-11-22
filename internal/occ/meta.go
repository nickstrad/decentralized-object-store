package occ

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

// Kind identifies the typed metadata namespace encoded in the first byte of a key.
type Kind uint8

// Metadata key kinds stored in a Doc.
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

// String returns a lowercase name for k.
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

// makeKey encodes a kind byte followed by length-prefixed parts so arbitrary
// bucket, object, and tag names can never collide across boundaries.
func makeKey(k Kind, parts ...string) string {
	b := []byte{byte(k)}
	for _, p := range parts {
		b = binary.AppendUvarint(b, uint64(len(p)))
		b = append(b, p...)
	}
	return string(b)
}

// Key builders for each typed metadata namespace:
//   - TagKey: object tag identified by (bucket, object, name).
//   - ACLKey: bucket ACL entry for (bucket, principal).
//   - QuotaKey: bucket usage counter for bucket.
//   - ObjectKey: ObjectMeta record for (bucket, object).
//   - ManifestKey: raw manifest bytes for (bucket, object).
//   - RouteKey: bucket routing region scalar for bucket.
func TagKey(bucket, object, name string) string { return makeKey(KindTag, bucket, object, name) }
func ACLKey(bucket, principal string) string    { return makeKey(KindACL, bucket, principal) }
func QuotaKey(bucket string) string             { return makeKey(KindQuota, bucket) }
func ObjectKey(bucket, object string) string    { return makeKey(KindObject, bucket, object) }
func ManifestKey(bucket, object string) string  { return makeKey(KindManifest, bucket, object) }
func RouteKey(bucket string) string             { return makeKey(KindRoute, bucket) }

// ContentTypeKey builds the scalar key holding the content type of (bucket, object).
func ContentTypeKey(bucket, object string) string {
	return makeKey(KindContentType, bucket, object)
}

// BucketPrefix returns the key prefix matching all keys of kind k inside bucket.
func BucketPrefix(k Kind, bucket string) string { return makeKey(k, bucket) }

// ParseKey splits a typed metadata key back into its Kind and parts, returning
// ErrMalformed on unknown kinds, wrong arity, or non-canonical length prefixes.
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

// Permission is a bitmask of bucket ACL rights.
type Permission uint8

// Bucket ACL permission bits.
const (
	PermRead Permission = 1 << iota
	PermWrite
	PermAdmin
	permAll = PermRead | PermWrite | PermAdmin
)

// ObjectMeta holds the size, content SHA-256 hash, and creation timestamp of
// one object version.
type ObjectMeta struct {
	Size    uint64
	Hash    [32]byte
	Created int64
}

// Encode returns the canonical binary encoding of m.
func (m ObjectMeta) Encode() []byte {
	var e encoder
	e.uvarint(m.Size)
	e.fixed(&m.Hash)
	e.varint(m.Created)
	return e.b
}

// DecodeObjectMeta parses a canonical ObjectMeta encoding, returning
// ErrMalformed on truncated, trailing, or non-canonical input.
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

// PutTag conditionally writes an object tag value.
func (s *Store) PutTag(bucket, object, name, value string, p Precondition) (Version, error) {
	return putTag(s, bucket, object, name, value, p)
}

// PutACL conditionally writes a principal's bucket Permission bitmask,
// returning env.ErrInvalidArgument if unknown bits are set.
func (s *Store) PutACL(bucket, principal string, perm Permission, p Precondition) (Version, error) {
	return putACL(s, bucket, principal, perm, p)
}

// PutObjectMeta conditionally writes an object's ObjectMeta record.
func (s *Store) PutObjectMeta(bucket, object string, m ObjectMeta, p Precondition) (Version, error) {
	return putObjectMeta(s, bucket, object, m, p)
}

// PutManifest conditionally writes an object's raw manifest bytes.
func (s *Store) PutManifest(bucket, object string, manifest []byte, p Precondition) (Version, error) {
	return putManifest(s, bucket, object, manifest, p)
}

// SetRoute writes a bucket's routing region unconditionally under LWW.
func (s *Store) SetRoute(bucket string, region env.Region) (Version, error) {
	return setRoute(s, bucket, region)
}

// SetContentType writes an object's content type unconditionally under LWW.
func (s *Store) SetContentType(bucket, object, contentType string) (Version, error) {
	return setContentType(s, bucket, object, contentType)
}

// Tag reads a live object tag value and its ETag, or returns ErrNotFound.
func (r Reader) Tag(bucket, object, name string) (string, ETag, error) {
	b, e, err := r.Get(TagKey(bucket, object, name))
	return string(b), e, err
}

// ACL reads a live bucket ACL permission bitmask and its ETag, returning
// ErrNotFound if absent/deleted or ErrMalformed if the stored byte is invalid.
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

// ObjectMeta reads and decodes a live object's ObjectMeta and ETag, returning
// ErrNotFound or ErrMalformed on failure.
func (r Reader) ObjectMeta(bucket, object string) (ObjectMeta, ETag, error) {
	b, e, err := r.Get(ObjectKey(bucket, object))
	if err != nil {
		return ObjectMeta{}, e, err
	}
	m, err := DecodeObjectMeta(b)
	return m, e, err
}

// Manifest reads a live object's raw manifest bytes and ETag, or ErrNotFound.
func (r Reader) Manifest(bucket, object string) ([]byte, ETag, error) {
	return r.Get(ManifestKey(bucket, object))
}

// Route reads a bucket's live routing region, or returns ErrNotFound.
func (r Reader) Route(bucket string) (env.Region, error) {
	b, _, err := r.Scalar(RouteKey(bucket))
	return env.Region(b), err
}

// ContentType reads an object's live content type, or returns ErrNotFound.
func (r Reader) ContentType(bucket, object string) (string, error) {
	b, _, err := r.Scalar(ContentTypeKey(bucket, object))
	return string(b), err
}

// RecountObjects sums the sizes and count of all live ObjectMeta records in
// bucket using checked uint64 arithmetic. It returns ErrMalformed if any live
// record fails to decode, or ErrUsageOverflow if either total would exceed
// math.MaxUint64.
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
