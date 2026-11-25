package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/erasure"
)

// Shard blob format constants. A shard blob starts with a 64-byte header:
// bytes 0..3 magic "OSSH", 4 version (1), 5 reserved (0), 6..7 k, 8..9 m,
// 10..11 index, 12..15 stripe, 16..19 payload length, 20..27 version ID,
// 28..31 reserved (0), 32..63 SHA-256(bytes 0..31 || payload).
const (
	HeaderSize         = 64
	ShardFormatVersion = 1
	MaxBucketLen       = 63
	shardMagic         = "OSSH"
	fieldsSize         = 32
	blobPrefix         = "shards/"
)

// plantSkipChecksum is flipped only by canary tests via export_test.go.
var plantSkipChecksum bool

// VersionID identifies one immutable version of an object.
type VersionID uint64

// String formats v as 16 lowercase hexadecimal digits in big-endian order.
func (v VersionID) String() string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	return hex.EncodeToString(b[:])
}

// ShardID uniquely names one shard of one stripe of one object version.
type ShardID struct {
	Bucket  string
	Version VersionID
	Stripe  uint32
	Index   uint16
}

// ShardHeader holds the decoded metadata fields and checksum of a shard blob.
type ShardHeader struct {
	K, M       int
	Index      int
	Stripe     uint32
	Version    VersionID
	PayloadLen int
	Checksum   [32]byte
}

// ValidBucket checks that b is 1 to MaxBucketLen bytes of [a-z0-9.-] starting
// with an ASCII lowercase letter or digit. It returns ErrInvalid otherwise.
func ValidBucket(b string) error {
	if len(b) == 0 || len(b) > MaxBucketLen {
		return fmt.Errorf("storage: bucket %q: length %d outside [1, %d]: %w", b, len(b), MaxBucketLen, ErrInvalid)
	}
	for i := 0; i < len(b); i++ {
		c := b[i]
		alnum := c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		if !alnum && (i == 0 || c != '-' && c != '.') {
			return fmt.Errorf("storage: bucket %q: byte %d: %w", b, i, ErrInvalid)
		}
	}
	return nil
}

// ValidKey checks that an object key is 1 to MaxKeyLen bytes. It returns
// ErrInvalid otherwise.
func ValidKey(key string) error {
	if key == "" || len(key) > MaxKeyLen {
		return fmt.Errorf("storage: object key length %d: %w", len(key), ErrInvalid)
	}
	return nil
}

// Validate checks that id's bucket name is valid, returning ErrInvalid if not.
func (id ShardID) Validate() error { return ValidBucket(id.Bucket) }

// versionDir returns the disk prefix "shards/<bucket>/<hh>/<version-hex16>/",
// where hh is the first byte of SHA-256(bucket + "/" + version-hex16).
func versionDir(bucket string, v VersionID) string {
	vh := v.String()
	sum := sha256.Sum256([]byte(bucket + "/" + vh))
	return blobPrefix + bucket + "/" + hex.EncodeToString(sum[:1]) + "/" + vh + "/"
}

// BlobName returns the canonical disk blob name for id:
// shards/<bucket>/<hh>/<version-hex16>/<stripe>.<index>.
func (id ShardID) BlobName() string {
	return versionDir(id.Bucket, id.Version) + strconv.FormatUint(uint64(id.Stripe), 10) + "." + strconv.FormatUint(uint64(id.Index), 10)
}

func parseCanonicalUint(s string, bits int) (uint64, bool) {
	if s == "" || len(s) > 1 && s[0] == '0' {
		return 0, false
	}
	v, err := strconv.ParseUint(s, 10, bits)
	return v, err == nil
}

// ParseBlobName parses a canonical shard blob name into a ShardID. Non-canonical
// forms (wrong hash prefix, uppercase hex, leading zeros, invalid bucket) return
// ErrInvalid.
func ParseBlobName(name string) (ShardID, error) {
	bad := func(why string) (ShardID, error) {
		return ShardID{}, fmt.Errorf("storage: blob name %q: %s: %w", name, why, ErrInvalid)
	}
	rest, ok := strings.CutPrefix(name, blobPrefix)
	if !ok {
		return bad("not a shard")
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 4 {
		return bad("wrong number of parts")
	}
	if err := ValidBucket(parts[0]); err != nil {
		return ShardID{}, fmt.Errorf("storage: blob name %q: %w", name, err)
	}
	if len(parts[2]) != 16 || strings.ToLower(parts[2]) != parts[2] {
		return bad("version is not 16 lowercase hex digits")
	}
	vb, err := hex.DecodeString(parts[2])
	if err != nil {
		return bad("version is not hex")
	}
	id := ShardID{Bucket: parts[0], Version: VersionID(binary.BigEndian.Uint64(vb))}
	stripe, idx, found := strings.Cut(parts[3], ".")
	if !found {
		return bad("no stripe.index")
	}
	sv, ok1 := parseCanonicalUint(stripe, 32)
	iv, ok2 := parseCanonicalUint(idx, 16)
	if !ok1 || !ok2 {
		return bad("stripe or index is not a canonical number")
	}
	id.Stripe, id.Index = uint32(sv), uint16(iv)
	// Re-deriving BlobName verifies the hh directory hash and canonical formatting.
	if id.BlobName() != name {
		return bad("not canonical")
	}
	return id, nil
}

// StripeGroup maps a shard blob name to its stripe group key
// "<bucket>/<version-hex16>/<stripe>" for fault-budget grouping, or "" if blob
// is not a canonical shard name.
func StripeGroup(_ env.NodeID, blob string) string {
	id, err := ParseBlobName(blob)
	if err != nil {
		return ""
	}
	return id.Bucket + "/" + id.Version.String() + "/" + strconv.FormatUint(uint64(id.Stripe), 10)
}

func validShape(k, m, index int) error {
	// Check bounds before adding to prevent signed overflow on hostile headers.
	if k < 1 || m < 0 || k > erasure.MaxShards || m > erasure.MaxShards-k {
		return fmt.Errorf("k=%d m=%d", k, m)
	}
	if index < 0 || index >= k+m {
		return fmt.Errorf("index %d of %d", index, k+m)
	}
	return nil
}

// EncodeShard packs h and payload into a self-checksummed shard blob.
// h.PayloadLen and h.Checksum are ignored and computed from payload. It returns
// ErrInvalid if (K, M, Index) is invalid or len(payload) > MaxStripeSize.
func EncodeShard(h ShardHeader, payload []byte) ([]byte, error) {
	if err := validShape(h.K, h.M, h.Index); err != nil {
		return nil, fmt.Errorf("storage: encode shard: %v: %w", err, ErrInvalid)
	}
	if uint64(len(payload)) > uint64(MaxStripeSize) {
		return nil, fmt.Errorf("storage: encode shard: payload %d bytes over %d: %w", len(payload), MaxStripeSize, ErrInvalid)
	}
	blob := make([]byte, HeaderSize+len(payload))
	copy(blob, shardMagic)
	blob[4] = ShardFormatVersion
	binary.BigEndian.PutUint16(blob[6:], uint16(h.K))
	binary.BigEndian.PutUint16(blob[8:], uint16(h.M))
	binary.BigEndian.PutUint16(blob[10:], uint16(h.Index))
	binary.BigEndian.PutUint32(blob[12:], h.Stripe)
	binary.BigEndian.PutUint32(blob[16:], uint32(len(payload)))
	binary.BigEndian.PutUint64(blob[20:], uint64(h.Version))
	copy(blob[HeaderSize:], payload)
	sum := checksum(blob)
	copy(blob[fieldsSize:HeaderSize], sum[:])
	return blob, nil
}

// checksum hashes the 32-byte header fields together with the payload so header
// tampering is detected even when the payload is intact.
func checksum(blob []byte) [32]byte {
	d := sha256.New()
	_, _ = d.Write(blob[:fieldsSize])
	_, _ = d.Write(blob[HeaderSize:])
	var out [32]byte
	d.Sum(out[:0])
	return out
}

// BlobChecksum returns the 32-byte checksum field stored in blob's header, or
// the zero array if blob is shorter than HeaderSize. It does not verify the hash.
func BlobChecksum(blob []byte) [32]byte {
	var out [32]byte
	if len(blob) >= HeaderSize {
		copy(out[:], blob[fieldsSize:HeaderSize])
	}
	return out
}

// DecodeShard parses and verifies a shard blob, returning its header and a
// subslice aliasing blob's payload. Any format violation, reserved non-zero
// byte, invalid shape, length mismatch, payload over MaxStripeSize, or checksum
// mismatch returns ErrCorrupt.
func DecodeShard(blob []byte) (ShardHeader, []byte, error) {
	bad := func(why string) (ShardHeader, []byte, error) {
		return ShardHeader{}, nil, fmt.Errorf("storage: decode shard: %s: %w", why, ErrCorrupt)
	}
	if len(blob) < HeaderSize {
		return bad("shorter than the header")
	}
	if string(blob[:4]) != shardMagic {
		return bad("bad magic")
	}
	if blob[4] != ShardFormatVersion {
		return bad("unknown format version")
	}
	if blob[5] != 0 || !bytes.Equal(blob[28:fieldsSize], []byte{0, 0, 0, 0}) {
		return bad("reserved bytes set")
	}
	h := ShardHeader{
		K:          int(binary.BigEndian.Uint16(blob[6:])),
		M:          int(binary.BigEndian.Uint16(blob[8:])),
		Index:      int(binary.BigEndian.Uint16(blob[10:])),
		Stripe:     binary.BigEndian.Uint32(blob[12:]),
		PayloadLen: int(binary.BigEndian.Uint32(blob[16:])),
		Version:    VersionID(binary.BigEndian.Uint64(blob[20:])),
		Checksum:   BlobChecksum(blob),
	}
	if err := validShape(h.K, h.M, h.Index); err != nil {
		return bad(err.Error())
	}
	if h.PayloadLen > MaxStripeSize {
		return bad(fmt.Sprintf("payload length %d over %d", h.PayloadLen, MaxStripeSize))
	}
	if h.PayloadLen != len(blob)-HeaderSize {
		return bad(fmt.Sprintf("payload length %d, blob carries %d", h.PayloadLen, len(blob)-HeaderSize))
	}
	if !plantSkipChecksum && checksum(blob) != h.Checksum {
		return bad("checksum mismatch")
	}
	return h, blob[HeaderSize:], nil
}
