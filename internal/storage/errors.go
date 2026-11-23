package storage

import (
	"errors"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

// Sentinel errors returned by chunk-store, manifest, placement, and node operations:
//   - ErrNotFound: the requested shard does not exist on disk.
//   - ErrCorrupt: a shard, manifest, or reassembled object failed format or checksum verification.
//   - ErrExists: a valid shard with different bytes is already stored under that ShardID.
//   - ErrUnavailable: too few reachable members or verified shards to satisfy the operation.
//   - ErrInvalid: a caller supplied an out-of-range, malformed, or nil argument.
var (
	ErrNotFound    = errors.New("storage: not found")
	ErrCorrupt     = errors.New("storage: corrupt")
	ErrExists      = errors.New("storage: exists")
	ErrUnavailable = errors.New("storage: unavailable")
	ErrInvalid     = errors.New("storage: invalid argument")
)

// ObjectError wraps an error from a Node Put or Get with the operation name
// and target bucket and key.
type ObjectError struct {
	Op     string
	Bucket string
	Key    string
	Err    error
}

// Error formats the operation, bucket/key, and underlying error message.
func (e *ObjectError) Error() string {
	return "storage: " + e.Op + " " + e.Bucket + "/" + e.Key + ": " + e.Err.Error()
}

// Unwrap returns the underlying error for errors.Is and errors.As.
func (e *ObjectError) Unwrap() error { return e.Err }

// Wire status codes for storage sentinels in the application code range
// (env.CodeAppMin+32 through env.CodeAppMin+36).
const (
	CodeNotFound    = env.CodeAppMin + 32
	CodeCorrupt     = env.CodeAppMin + 33
	CodeExists      = env.CodeAppMin + 34
	CodeUnavailable = env.CodeAppMin + 35
	CodeInvalid     = env.CodeAppMin + 36
)

// CodeEntries returns the env.CodeEntry mappings for every storage sentinel so
// callers can merge them into a shared env.CodeTable.
func CodeEntries() []env.CodeEntry {
	return []env.CodeEntry{
		{Code: CodeUnavailable, Err: ErrUnavailable},
		{Code: CodeNotFound, Err: ErrNotFound},
		{Code: CodeCorrupt, Err: ErrCorrupt},
		{Code: CodeExists, Err: ErrExists},
		{Code: CodeInvalid, Err: ErrInvalid},
	}
}

// Codes constructs an env.CodeTable containing the storage sentinels and the
// built-in env sentinels.
func Codes() (*env.CodeTable, error) {
	return env.NewCodeTable(CodeEntries()...)
}

func objectErr(op, bucket, key string, err error) error {
	var e error = &ObjectError{Op: op, Bucket: bucket, Key: key, Err: err}
	return e
}
