package storage

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/erasure"
)

const (
	HeaderSize         = 64
	ShardFormatVersion = 1
	MaxBucketLen       = 63
	shardMagic         = "OSSH"
	fieldsSize         = 32
	blobPrefix         = "shards/"
)

var plantSkipChecksum bool

type VersionID uint64

func (v VersionID) String() string {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	return hex.EncodeToString(b[:])
}

type ShardID struct {
	Bucket  string
	Version VersionID
	Stripe  uint32
	Index   uint16
}

type ShardHeader struct {
	K, M       int
	Index      int
	Stripe     uint32
	Version    VersionID
	PayloadLen int
	Checksum   [32]byte
}

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

func (id ShardID) Validate() error { return ValidBucket(id.Bucket) }

func versionDir(bucket string, v VersionID) string {
	vh := v.String()
	sum := sha256.Sum256([]byte(bucket + "/" + vh))
	return blobPrefix + bucket + "/" + hex.EncodeToString(sum[:1]) + "/" + vh + "/"
}

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
	if id.BlobName() != name {
		return bad("not canonical")
	}
	return id, nil
}

func StripeGroup(_ env.NodeID, blob string) string {
	id, err := ParseBlobName(blob)
	if err != nil {
		return ""
	}
	return id.Bucket + "/" + id.Version.String() + "/" + strconv.FormatUint(uint64(id.Stripe), 10)
}

func validShape(k, m, index int) error {
	if k < 1 || m < 0 || k+m > erasure.MaxShards {
		return fmt.Errorf("k=%d m=%d", k, m)
	}
	if index < 0 || index >= k+m {
		return fmt.Errorf("index %d of %d", index, k+m)
	}
	return nil
}

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

func checksum(blob []byte) [32]byte {
	d := sha256.New()
	_, _ = d.Write(blob[:fieldsSize])
	_, _ = d.Write(blob[HeaderSize:])
	var out [32]byte
	d.Sum(out[:0])
	return out
}

func BlobChecksum(blob []byte) [32]byte {
	var out [32]byte
	if len(blob) >= HeaderSize {
		copy(out[:], blob[fieldsSize:HeaderSize])
	}
	return out
}

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
	if h.PayloadLen != len(blob)-HeaderSize {
		return bad(fmt.Sprintf("payload length %d, blob carries %d", h.PayloadLen, len(blob)-HeaderSize))
	}
	if !plantSkipChecksum && checksum(blob) != h.Checksum {
		return bad("checksum mismatch")
	}
	return h, blob[HeaderSize:], nil
}
