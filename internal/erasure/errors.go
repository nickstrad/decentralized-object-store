package erasure

import (
	"errors"
	"strconv"
)

var (
	ErrInvalidConfig  = errors.New("erasure: invalid configuration")
	ErrTooFewShards   = errors.New("erasure: too few shards")
	ErrShardSize      = errors.New("erasure: shard size mismatch")
	ErrShardCount     = errors.New("erasure: wrong number of shards")
	ErrSingular       = errors.New("erasure: singular matrix")
	ErrParityMismatch = errors.New("erasure: parity does not match data")
)

type ShardError struct {
	Index int
	Err   error
}

func (e *ShardError) Error() string {
	return "erasure: shard " + strconv.Itoa(e.Index) + ": " + e.Err.Error()
}

func (e *ShardError) Unwrap() error { return e.Err }
