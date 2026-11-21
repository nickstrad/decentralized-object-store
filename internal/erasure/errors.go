package erasure

import (
	"errors"
	"strconv"
)

// Sentinel errors returned by Codec and Matrix operations:
//   - ErrInvalidConfig: out-of-range (k, m), ragged or empty matrix, or incompatible dimensions.
//   - ErrTooFewShards: fewer than k present shards, or a required data/parity shard is nil.
//   - ErrShardSize: present shards differ in length, or Join's target size exceeds k*shardSize.
//   - ErrShardCount: the shard slice length does not equal k+m.
//   - ErrSingular: Invert encountered a zero pivot column.
//   - ErrParityMismatch: Verify recomputed a parity shard that differed from the supplied bytes.
var (
	ErrInvalidConfig  = errors.New("erasure: invalid configuration")
	ErrTooFewShards   = errors.New("erasure: too few shards")
	ErrShardSize      = errors.New("erasure: shard size mismatch")
	ErrShardCount     = errors.New("erasure: wrong number of shards")
	ErrSingular       = errors.New("erasure: singular matrix")
	ErrParityMismatch = errors.New("erasure: parity does not match data")
)

// ShardError pairs a shard slot index with the underlying sentinel error.
type ShardError struct {
	Index int
	Err   error
}

// Error formats the shard index and underlying error message.
func (e *ShardError) Error() string {
	return "erasure: shard " + strconv.Itoa(e.Index) + ": " + e.Err.Error()
}

// Unwrap returns the underlying sentinel error for errors.Is and errors.As.
func (e *ShardError) Unwrap() error { return e.Err }
