package storage

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/nickstrad/object-store/internal/env"
)

// Manifest field length limits and binary format constants.
const (
	MaxKeyLen       = 1024
	MaxNodeIDLen    = 255
	manifestMagic   = "OMAN"
	manifestVersion = 1
	// Smallest encoded ShardRef: 1-byte length prefix + 1-byte node ID + 32-byte checksum.
	minShardRefSize = 1 + 1 + 32
)

// ShardRef records the node assigned to hold one shard and its expected
// header-and-payload SHA-256 checksum.
type ShardRef struct {
	Node     env.NodeID
	Checksum [32]byte
}

// StripeRef lists the k+m shard placements for one stripe in shard-index order.
type StripeRef struct {
	Shards []ShardRef
}

// Manifest describes one immutable object version: its bucket, key, size,
// whole-object SHA-256, erasure Layout, and per-stripe shard placements.
type Manifest struct {
	Bucket     string
	Key        string
	Version    VersionID
	Size       int64
	ContentSHA [32]byte
	Layout     Layout
	Stripes    []StripeRef
}

// ShardID returns the ShardID for the given stripe and shard index in m.
func (m *Manifest) ShardID(stripe, index int) ShardID {
	return ShardID{Bucket: m.Bucket, Version: m.Version, Stripe: uint32(stripe), Index: uint16(index)}
}

// Validate checks every field of m, including bucket/key bounds, layout
// validity, stripe count matching Size, and shard counts and node IDs. It
// returns ErrInvalid on any violation.
func (m *Manifest) Validate() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("storage: manifest %s/%s: %s: %w", m.Bucket, m.Key, fmt.Sprintf(format, args...), ErrInvalid)
	}
	if err := ValidBucket(m.Bucket); err != nil {
		return fmt.Errorf("storage: manifest: %w", err)
	}
	if len(m.Key) == 0 || len(m.Key) > MaxKeyLen {
		return bad("key length %d outside [1, %d]", len(m.Key), MaxKeyLen)
	}
	if m.Size < 0 {
		return bad("negative size %d", m.Size)
	}
	if err := m.Layout.Validate(); err != nil {
		return fmt.Errorf("storage: manifest %s/%s: %w", m.Bucket, m.Key, err)
	}
	if want := m.Layout.Stripes(m.Size); len(m.Stripes) != want {
		return bad("%d stripes, size %d needs %d", len(m.Stripes), m.Size, want)
	}
	n := m.Layout.K + m.Layout.M
	for i, st := range m.Stripes {
		if len(st.Shards) != n {
			return bad("stripe %d has %d shards, want %d", i, len(st.Shards), n)
		}
		for j, sh := range st.Shards {
			if len(sh.Node) == 0 || len(sh.Node) > MaxNodeIDLen {
				return bad("stripe %d shard %d: node id length %d", i, j, len(sh.Node))
			}
		}
	}
	return nil
}

func appendString(b []byte, s string) []byte {
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

// Encode validates m and serializes it into its canonical binary form
// (magic "OMAN", version 1, length-prefixed strings, uvarints, and shard table).
// It returns ErrInvalid if m fails Validate.
func (m *Manifest) Encode() ([]byte, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	b := append([]byte(manifestMagic), manifestVersion)
	b = appendString(b, m.Bucket)
	b = appendString(b, m.Key)
	b = binary.BigEndian.AppendUint64(b, uint64(m.Version))
	b = binary.AppendUvarint(b, uint64(m.Size))
	b = append(b, m.ContentSHA[:]...)
	b = binary.AppendUvarint(b, uint64(m.Layout.K))
	b = binary.AppendUvarint(b, uint64(m.Layout.M))
	b = binary.AppendUvarint(b, uint64(m.Layout.StripeSize))
	b = binary.AppendUvarint(b, uint64(len(m.Stripes)))
	for _, st := range m.Stripes {
		for _, sh := range st.Shards {
			b = appendString(b, string(sh.Node))
			b = append(b, sh.Checksum[:]...)
		}
	}
	return b, nil
}

// reader is a sticky-error cursor over a byte slice for binary decoding.
type reader struct {
	b   []byte
	err error
}

func (r *reader) fail(why string) {
	if r.err == nil {
		r.err = errors.New(why)
	}
}

func (r *reader) take(n uint64) []byte {
	if r.err != nil || n > uint64(len(r.b)) {
		r.fail("truncated")
		return nil
	}
	out := r.b[:n]
	r.b = r.b[n:]
	return out
}

func (r *reader) uvarint(limit uint64) uint64 {
	if r.err != nil {
		return 0
	}
	v, n := binary.Uvarint(r.b)
	if n <= 0 || v > limit {
		r.fail("bad or oversized number")
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) str(limit uint64) string {
	return string(r.take(r.uvarint(limit)))
}

// DecodeManifest parses data into a validated Manifest. Only the canonical
// encoding is accepted (re-encoding must match data byte for byte); truncated,
// trailing, non-minimal, or invalid inputs return ErrCorrupt.
func DecodeManifest(data []byte) (*Manifest, error) {
	bad := func(err error) (*Manifest, error) {
		return nil, fmt.Errorf("storage: decode manifest: %w", errors.Join(ErrCorrupt, err))
	}
	r := &reader{b: data}
	if !bytes.Equal(r.take(4), []byte(manifestMagic)) {
		return bad(errors.New("bad magic"))
	}
	if v := r.take(1); r.err != nil || v[0] != manifestVersion {
		return bad(errors.New("unknown format version"))
	}
	m := &Manifest{}
	m.Bucket = r.str(MaxBucketLen)
	m.Key = r.str(MaxKeyLen)
	if vb := r.take(8); r.err == nil {
		m.Version = VersionID(binary.BigEndian.Uint64(vb))
	}
	m.Size = int64(r.uvarint(1<<62 - 1))
	copy(m.ContentSHA[:], r.take(32))
	m.Layout.K = int(r.uvarint(MaxStripeSize))
	m.Layout.M = int(r.uvarint(MaxStripeSize))
	m.Layout.StripeSize = int(r.uvarint(MaxStripeSize))
	count := r.uvarint(uint64(len(data)))
	n := uint64(m.Layout.K + m.Layout.M)
	// Bound the stripe table against remaining bytes before allocating to block
	// memory-exhaustion inputs.
	if r.err == nil && (n == 0 || count > uint64(len(r.b))/minShardRefSize/n) {
		r.fail("stripe table larger than the input")
	}
	if r.err == nil {
		m.Stripes = make([]StripeRef, count)
	}
	for i := range m.Stripes {
		m.Stripes[i].Shards = make([]ShardRef, n)
		for j := range m.Stripes[i].Shards {
			sh := &m.Stripes[i].Shards[j]
			sh.Node = env.NodeID(r.str(MaxNodeIDLen))
			copy(sh.Checksum[:], r.take(32))
		}
	}
	if r.err != nil {
		return bad(r.err)
	}
	if len(r.b) != 0 {
		return bad(errors.New("trailing bytes"))
	}
	// Re-encoding rejects non-canonical representations such as overlong uvarints.
	again, err := m.Encode()
	if err != nil {
		return bad(err)
	}
	if !bytes.Equal(again, data) {
		return bad(errors.New("not in canonical form"))
	}
	return m, nil
}
