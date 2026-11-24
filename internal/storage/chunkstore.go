package storage

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/nickstrad/object-store/internal/env"
)

// ChunkStore stores verified, immutable shard blobs on an env.Disk.
// Every read verifies the shard header, checksum, and ShardID match; every
// successful Put or Delete syncs before returning so its result survives a crash.
type ChunkStore struct {
	disk   env.Disk
	metric *metricState
	sizes  map[string]uint64
}

// NewChunkStore returns a ChunkStore backed by d.
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

// Get reads and verifies shard id, returning its header and payload. It returns
// ErrInvalid for a bad bucket, ErrNotFound (joined with env.ErrNotFound) if
// absent, ErrCorrupt if verification fails, or the underlying disk error.
func (s *ChunkStore) Get(id ShardID) (hout ShardHeader, bout []byte, eout error) {
	defer func() {
		s.observedOperation("get", eout)
		if eout == nil {
			s.metric.add("storage_shard_bytes_total", uint64(HeaderSize+len(bout)), "read")
		}
	}()
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

// GetBlob reads and verifies shard id, returning the complete encoded blob.
// Error semantics match Get.
func (s *ChunkStore) GetBlob(id ShardID) (bout []byte, eout error) {
	defer func() {
		s.observedOperation("get", eout)
		if eout == nil {
			s.metric.add("storage_shard_bytes_total", uint64(len(bout)), "read")
		}
	}()
	blob, err := s.read(id)
	if err != nil {
		return nil, err
	}
	if _, _, err := verifyFor(id, blob); err != nil {
		return nil, fmt.Errorf("storage: get shard %s: %w", id.BlobName(), err)
	}
	return blob, nil
}

// Put verifies blob against id, writes it to disk, and syncs before returning.
// Identical bytes already on disk are synced and accepted (idempotent); a valid
// shard with different bytes returns ErrExists; a corrupt existing blob is
// overwritten (repaired). A malformed or mismatched blob returns ErrCorrupt
// without writing.
func (s *ChunkStore) Put(id ShardID, blob []byte) (eout error) {
	defer func() { s.observedOperation("put", eout) }()
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
			// Sync even on a match: the existing bytes may only be in the
			// unsynced written layer after an earlier failed Sync.
			if err := s.disk.Sync(name); err != nil {
				return fmt.Errorf("storage: put shard %s: sync: %w", name, err)
			}
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
	s.observedWrite(id, blob)
	if err := s.disk.Sync(name); err != nil {
		return fmt.Errorf("storage: put shard %s: sync: %w", name, err)
	}
	return nil
}

// Delete removes shard id and syncs the removal so it stays deleted across a
// crash. If the shard is absent from the written layer, Delete still syncs any
// pending deletion before returning ErrNotFound (joined with env.ErrNotFound).
func (s *ChunkStore) Delete(id ShardID) (eout error) {
	defer func() { s.observedOperation("delete", eout) }()
	if err := id.Validate(); err != nil {
		return err
	}
	name := id.BlobName()
	if err := s.disk.Delete(name); err != nil {
		if !errors.Is(err, env.ErrNotFound) {
			return fmt.Errorf("storage: delete shard %s: %w", name, err)
		}
		s.observedDelete(id)
		// Flush a prior unsynced deletion; env.ErrNotFound from Sync means
		// nothing existed in the durable layer either.
		if serr := s.disk.Sync(name); serr != nil && !errors.Is(serr, env.ErrNotFound) {
			return fmt.Errorf("storage: delete shard %s: sync: %w", name, serr)
		}
		return fmt.Errorf("storage: delete shard %s: %w", name, errors.Join(ErrNotFound, err))
	}
	s.observedDelete(id)
	if err := s.disk.Sync(name); err != nil && !errors.Is(err, env.ErrNotFound) {
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
		// Skip stray or non-canonical entries on disk rather than failing the listing.
		if id, err := ParseBlobName(n); err == nil {
			out = append(out, id)
		}
	}
	return out, nil
}

// List returns all canonical ShardIDs stored under bucket in disk name order.
// It returns ErrInvalid for an invalid bucket name.
func (s *ChunkStore) List(bucket string) ([]ShardID, error) {
	if err := ValidBucket(bucket); err != nil {
		return nil, err
	}
	return s.list(blobPrefix + bucket + "/")
}

// ListVersion returns all canonical ShardIDs for version v in bucket in disk
// name order. It returns ErrInvalid for an invalid bucket name.
func (s *ChunkStore) ListVersion(bucket string, v VersionID) ([]ShardID, error) {
	if err := ValidBucket(bucket); err != nil {
		return nil, err
	}
	return s.list(versionDir(bucket, v))
}
