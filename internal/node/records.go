package node

import (
	"encoding/binary"
	"fmt"
	"slices"
	"strings"

	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

// Reserved metadata tag names (prefixed with "~", rejected on user tags).
// Bucket-scoped records store under an empty object key; stage and lost records
// attach to an object key. Tombstones, GC markers, orphan, absence, and loss
// records are retained indefinitely.
const (
	recordVersion = 1
	sysPrefix     = "~"
	tagBucket     = "~bucket"
	tagLive       = "~live"
	tagReg        = "~reg/"
	tagRetired    = "~retired"
	tagClaim      = "~claim/"
	tagStage      = "~stage/"
	tagGC         = "~gc/"
	tagFlip       = "~flip"
	tagIntent     = "~cv/"
	tagOrphan     = "~orphan/"
	tagLost       = "~lost/"
	tagAbsent     = "~absent/"
	maxField      = 1 << 16
)

type wbuf struct{ b []byte }

func (w *wbuf) byte(v byte)      { w.b = append(w.b, v) }
func (w *wbuf) uvarint(v uint64) { w.b = binary.AppendUvarint(w.b, v) }
func (w *wbuf) bytes(p []byte) {
	w.uvarint(uint64(len(p)))
	w.b = append(w.b, p...)
}
func (w *wbuf) str(s string) { w.bytes([]byte(s)) }

type rbuf struct {
	b   []byte
	err error
}

func (r *rbuf) fail(what string) {
	if r.err == nil {
		r.err = fmt.Errorf("node: decode %s: %w", what, ErrMalformed)
	}
}

func (r *rbuf) byte() byte {
	if r.err != nil || len(r.b) < 1 {
		r.fail("byte")
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

func (r *rbuf) uvarint() uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b)
	if n <= 0 || n != len(binary.AppendUvarint(nil, v)) {
		r.fail("uvarint")
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *rbuf) bytes() []byte {
	n := r.uvarint()
	if r.err != nil || n > maxField<<8 || n > uint64(len(r.b)) {
		r.fail("bytes")
		return nil
	}
	out := slices.Clone(r.b[:n])
	r.b = r.b[n:]
	return out
}

func (r *rbuf) str() string { return string(r.bytes()) }

func (r *rbuf) rest() []byte {
	out := slices.Clone(r.b)
	r.b = nil
	return out
}

func (r *rbuf) end(what string) error {
	if r.err == nil && len(r.b) != 0 {
		r.fail(what + ": trailing bytes")
	}
	return r.err
}

// objectRecord pairs an object's Content-Type with its storage manifest in one
// OCC record so concurrent PUTs cannot leave one write's metadata over another's
// bytes.
type objectRecord struct {
	ContentType string
	Manifest    *storage.Manifest
}

func encodeObject(o objectRecord) ([]byte, error) {
	mb, err := o.Manifest.Encode()
	if err != nil {
		return nil, err
	}
	var w wbuf
	w.byte(recordVersion)
	w.str(o.ContentType)
	w.b = append(w.b, mb...)
	return w.b, nil
}

func decodeObject(b []byte) (objectRecord, error) {
	r := rbuf{b: b}
	if r.byte() != recordVersion {
		r.fail("object record version")
	}
	ct := r.str()
	rest := r.rest()
	if r.err != nil {
		return objectRecord{}, r.err
	}
	m, err := storage.DecodeManifest(rest)
	if err != nil {
		return objectRecord{}, fmt.Errorf("node: decode object record: %w", err)
	}
	return objectRecord{ContentType: ct, Manifest: m}, nil
}

// bucketRecord stores a bucket's initial region (empty for unrouted buckets)
// and configured byte and object quota limits.
type bucketRecord struct {
	Region       string
	QuotaBytes   uint64
	QuotaObjects uint64
}

func encodeBucket(b bucketRecord) []byte {
	var w wbuf
	w.byte(recordVersion)
	w.str(b.Region)
	w.uvarint(b.QuotaBytes)
	w.uvarint(b.QuotaObjects)
	return w.b
}

func decodeBucket(b []byte) (bucketRecord, error) {
	r := rbuf{b: b}
	if r.byte() != recordVersion {
		r.fail("bucket record version")
	}
	out := bucketRecord{Region: r.str(), QuotaBytes: r.uvarint(), QuotaObjects: r.uvarint()}
	return out, r.end("bucket record")
}

// stagedRecord holds a migration saga's target-region copy of an object along
// with the source ETag it was copied from.
type stagedRecord struct {
	Saga    string
	SrcETag occ.ETag
	Object  []byte
}

func encodeStaged(s stagedRecord) []byte {
	var w wbuf
	w.byte(recordVersion)
	w.str(s.Saga)
	w.b = append(w.b, s.SrcETag[:]...)
	w.bytes(s.Object)
	return w.b
}

func decodeStaged(b []byte) (stagedRecord, error) {
	r := rbuf{b: b}
	if r.byte() != recordVersion {
		r.fail("staged record version")
	}
	var s stagedRecord
	s.Saga = r.str()
	if r.err == nil && len(r.b) < len(s.SrcETag) {
		r.fail("staged etag")
	}
	if r.err == nil {
		copy(s.SrcETag[:], r.b)
		r.b = r.b[len(s.SrcETag):]
	}
	s.Object = r.bytes()
	return s, r.end("staged record")
}

type sysTag struct {
	key    string
	object string
	rest   string
}

func sysTags(rd occ.Reader, bucket, prefix string) []sysTag {
	var out []sysTag
	for _, k := range prefixKeys(rd.Keys(), occ.BucketPrefix(occ.KindTag, bucket)) {
		if !rd.Record(k).Live() {
			continue
		}
		_, parts, err := occ.ParseKey(k)
		if err != nil || !strings.HasPrefix(parts[2], prefix) {
			continue
		}
		out = append(out, sysTag{key: k, object: parts[1], rest: strings.TrimPrefix(parts[2], prefix)})
	}
	return out
}

func bucketKey(bucket, name string) string { return occ.TagKey(bucket, "", name) }

func stageKey(bucket, object, saga string) string {
	return occ.TagKey(bucket, object, tagStage+saga)
}

func systemName(name string) bool { return strings.HasPrefix(name, sysPrefix) }
