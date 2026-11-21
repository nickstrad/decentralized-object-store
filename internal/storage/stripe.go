package storage

import (
	"errors"
	"fmt"

	"github.com/nickstrad/decentralized-object-store/internal/erasure"
)

const (
	DefaultK          = 4
	DefaultM          = 2
	DefaultStripeSize = 1 << 20
	MaxStripeSize     = 64 << 20
)

type Layout struct {
	K, M       int
	StripeSize int
}

func DefaultLayout() Layout {
	return Layout{K: DefaultK, M: DefaultM, StripeSize: DefaultStripeSize}
}

func (l Layout) Validate() error {
	if _, err := erasure.New(l.K, l.M); err != nil {
		return fmt.Errorf("storage: layout: %w", errors.Join(ErrInvalid, err))
	}
	if l.StripeSize < l.K || l.StripeSize > MaxStripeSize {
		return fmt.Errorf("storage: layout: stripe size %d outside [k=%d, %d]: %w", l.StripeSize, l.K, MaxStripeSize, ErrInvalid)
	}
	return nil
}

func (l Layout) Codec() (*erasure.Codec, error) {
	if err := l.Validate(); err != nil {
		return nil, err
	}
	return erasure.New(l.K, l.M)
}

func (l Layout) Stripes(size int64) int {
	if size <= 0 || l.StripeSize <= 0 {
		return 0
	}
	return int((size-1)/int64(l.StripeSize) + 1)
}

func (l Layout) StripeLen(size int64, i int) int {
	if i < 0 || i >= l.Stripes(size) {
		return 0
	}
	lo := int64(i) * int64(l.StripeSize)
	return int(min(size-lo, int64(l.StripeSize)))
}

func EncodeStripe(codec *erasure.Codec, v VersionID, stripe uint32, data []byte) ([][]byte, error) {
	if len(data) > MaxStripeSize {
		return nil, fmt.Errorf("storage: encode stripe %d: %d bytes over %d: %w", stripe, len(data), MaxStripeSize, ErrInvalid)
	}
	shards := codec.Split(data)
	if err := codec.Encode(shards); err != nil {
		return nil, fmt.Errorf("storage: encode stripe %d: %w", stripe, err)
	}
	blobs := make([][]byte, len(shards))
	for i, p := range shards {
		b, err := EncodeShard(ShardHeader{K: codec.K(), M: codec.M(), Index: i, Stripe: stripe, Version: v}, p)
		if err != nil {
			return nil, fmt.Errorf("storage: encode stripe %d: %w", stripe, err)
		}
		blobs[i] = b
	}
	return blobs, nil
}

type shardExpect struct {
	k, m      int
	version   VersionID
	stripe    uint32
	shardSize int
	length    int
}

func expectFor(codec *erasure.Codec, v VersionID, stripe uint32, dataLen int) shardExpect {
	return shardExpect{k: codec.K(), m: codec.M(), version: v, stripe: stripe, shardSize: codec.ShardSize(dataLen), length: dataLen}
}

func (e shardExpect) verify(index int, blob []byte, want *[32]byte) ([]byte, error) {
	h, payload, err := DecodeShard(blob)
	if err != nil {
		return nil, err
	}
	switch {
	case h.K != e.k || h.M != e.m:
		return nil, fmt.Errorf("storage: shard %d: code k=%d m=%d, want k=%d m=%d: %w", index, h.K, h.M, e.k, e.m, ErrCorrupt)
	case h.Index != index || h.Stripe != e.stripe || h.Version != e.version:
		return nil, fmt.Errorf("storage: shard %d: belongs to version %s stripe %d index %d: %w", index, h.Version, h.Stripe, h.Index, ErrCorrupt)
	case h.PayloadLen != e.shardSize:
		return nil, fmt.Errorf("storage: shard %d: payload %d bytes, want %d: %w", index, h.PayloadLen, e.shardSize, ErrCorrupt)
	case want != nil && h.Checksum != *want:
		return nil, fmt.Errorf("storage: shard %d: checksum differs from the manifest: %w", index, ErrCorrupt)
	}
	return payload, nil
}

func decodeVerified(codec *erasure.Codec, stripe uint32, dataLen int, payloads [][]byte, causes []error) ([]byte, error) {
	shards := make([][]byte, len(payloads))
	copy(shards, payloads)
	if err := codec.ReconstructData(shards); err != nil {
		return nil, fmt.Errorf("storage: decode stripe %d: %w", stripe, errors.Join(append([]error{ErrUnavailable, err}, causes...)...))
	}
	data, err := codec.Join(shards, dataLen)
	if err != nil {
		return nil, fmt.Errorf("storage: decode stripe %d: %w", stripe, errors.Join(ErrCorrupt, err))
	}
	return data, nil
}

func DecodeStripe(codec *erasure.Codec, v VersionID, stripe uint32, dataLen int, blobs [][]byte, want [][32]byte) ([]byte, []int, error) {
	n := codec.N()
	if len(blobs) != n || want != nil && len(want) != n || dataLen < 0 || dataLen > MaxStripeSize {
		return nil, nil, fmt.Errorf("storage: decode stripe %d: %d blobs, %d checksums, length %d: %w", stripe, len(blobs), len(want), dataLen, ErrInvalid)
	}
	exp := expectFor(codec, v, stripe, dataLen)
	payloads := make([][]byte, n)
	var bad []int
	var causes []error
	for i, b := range blobs {
		if b == nil {
			continue
		}
		var w *[32]byte
		if want != nil {
			w = &want[i]
		}
		p, err := exp.verify(i, b, w)
		if err != nil {
			bad = append(bad, i)
			causes = append(causes, err)
			continue
		}
		payloads[i] = p
	}
	data, err := decodeVerified(codec, stripe, dataLen, payloads, causes)
	if err != nil {
		return nil, bad, err
	}
	return data, bad, nil
}
