package storage

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type ChunkStore struct {
	disk env.Disk
}

func NewChunkStore(d env.Disk) *ChunkStore { return &ChunkStore{disk: d} }

func matches(id ShardID, h ShardHeader) bool {
	return h.Version == id.Version && h.Stripe == id.Stripe && h.Index == int(id.Index)
}

func verifyFor(id ShardID, blob []byte) (ShardHeader, []byte, error) {
	h, payload, err := DecodeShard(blob)
	if err != nil {
		return ShardHeader{}, nil, err
	}
	if !matches(id, h) {
		return ShardHeader{}, nil, fmt.Errorf("storage: shard %s carries version %s stripe %d index %d: %w", id.BlobName(), h.Version, h.Stripe, h.Index, ErrCorrupt)
	}
	return h, payload, nil
}

func (s *ChunkStore) read(id ShardID) ([]byte, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	name := id.BlobName()
	blob, err := s.disk.Read(name)
	if errors.Is(err, env.ErrNotFound) {
		return nil, fmt.Errorf("storage: read shard %s: %w", name, errors.Join(ErrNotFound, err))
	}
	if err != nil {
		return nil, fmt.Errorf("storage: read shard %s: %w", name, err)
	}
	return blob, nil
}

func (s *ChunkStore) Get(id ShardID) (ShardHeader, []byte, error) {
	blob, err := s.read(id)
	if err != nil {
		return ShardHeader{}, nil, err
	}
	h, payload, err := verifyFor(id, blob)
	if err != nil {
		return ShardHeader{}, nil, fmt.Errorf("storage: get shard %s: %w", id.BlobName(), err)
	}
	return h, payload, nil
}

func (s *ChunkStore) GetBlob(id ShardID) ([]byte, error) {
	blob, err := s.read(id)
	if err != nil {
		return nil, err
	}
	if _, _, err := verifyFor(id, blob); err != nil {
		return nil, fmt.Errorf("storage: get shard %s: %w", id.BlobName(), err)
	}
	return blob, nil
}

func (s *ChunkStore) Put(id ShardID, blob []byte) error {
	if err := id.Validate(); err != nil {
		return err
	}
	name := id.BlobName()
	if _, _, err := verifyFor(id, blob); err != nil {
		return fmt.Errorf("storage: put shard %s: rejected: %w", name, err)
	}
	old, err := s.read(id)
	switch {
	case err == nil:
		if bytes.Equal(old, blob) {
			return nil
		}
		if _, _, verr := verifyFor(id, old); verr == nil {
			return fmt.Errorf("storage: put shard %s: different content stored: %w", name, ErrExists)
		}
	case !errors.Is(err, ErrNotFound):
		return fmt.Errorf("storage: put shard %s: %w", name, err)
	}
	if err := s.disk.Write(name, blob); err != nil {
		return fmt.Errorf("storage: put shard %s: write: %w", name, err)
	}
	if err := s.disk.Sync(name); err != nil {
		return fmt.Errorf("storage: put shard %s: sync: %w", name, err)
	}
	return nil
}

func (s *ChunkStore) Delete(id ShardID) error {
	if err := id.Validate(); err != nil {
		return err
	}
	name := id.BlobName()
	if err := s.disk.Delete(name); err != nil {
		if errors.Is(err, env.ErrNotFound) {
			return fmt.Errorf("storage: delete shard %s: %w", name, errors.Join(ErrNotFound, err))
		}
		return fmt.Errorf("storage: delete shard %s: %w", name, err)
	}
	if err := s.disk.Sync(name); err != nil {
		return fmt.Errorf("storage: delete shard %s: sync: %w", name, err)
	}
	return nil
}

func (s *ChunkStore) list(prefix string) ([]ShardID, error) {
	names, err := s.disk.List(prefix)
	if err != nil {
		return nil, fmt.Errorf("storage: list %s: %w", prefix, err)
	}
	out := make([]ShardID, 0, len(names))
	for _, n := range names {
		if id, err := ParseBlobName(n); err == nil {
			out = append(out, id)
		}
	}
	return out, nil
}

func (s *ChunkStore) List(bucket string) ([]ShardID, error) {
	if err := ValidBucket(bucket); err != nil {
		return nil, err
	}
	return s.list(blobPrefix + bucket + "/")
}

func (s *ChunkStore) ListVersion(bucket string, v VersionID) ([]ShardID, error) {
	if err := ValidBucket(bucket); err != nil {
		return nil, err
	}
	return s.list(versionDir(bucket, v))
}
