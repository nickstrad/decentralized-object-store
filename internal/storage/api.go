package storage

import (
	"errors"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

// Sentinel errors for object-API bucket, migration, and HTTP method failures:
//   - ErrNoSuchBucket: the named bucket does not exist.
//   - ErrBucketExists: CreateBucket targeted an already-existing bucket.
//   - ErrBusy: the bucket is currently migrating or not yet writable.
//   - ErrMethod: the HTTP method is not allowed on the matched route.
var (
	ErrNoSuchBucket = errors.New("storage: no such bucket")
	ErrBucketExists = errors.New("storage: bucket exists")
	ErrBusy         = errors.New("storage: bucket busy")
	ErrMethod       = errors.New("storage: method not allowed")
)

// Wire error codes for object-API sentinels (block 160–175).
const (
	CodeNoSuchBucket = env.CodeAppMin + 96
	CodeBucketExists = env.CodeAppMin + 97
	CodeBusy         = env.CodeAppMin + 98
	CodeMethod       = env.CodeAppMin + 99
)

// APICodeEntries returns the env.CodeEntry rows for the object-API sentinels.
func APICodeEntries() []env.CodeEntry {
	return []env.CodeEntry{
		{Code: CodeNoSuchBucket, Err: ErrNoSuchBucket},
		{Code: CodeBucketExists, Err: ErrBucketExists},
		{Code: CodeBusy, Err: ErrBusy},
		{Code: CodeMethod, Err: ErrMethod},
	}
}

// Condition expresses HTTP-style optimistic concurrency preconditions for a
// write or delete. When IfMatchList is true, an If-Match header was present
// (with strong tags in IfMatchAny, or "*" in IfMatch) and the request never
// degrades to an unconditional write even if IfMatchAny is empty.
type Condition struct {
	IfMatch     string
	IfMatchList bool
	IfMatchAny  []string
	IfNoneMatch bool
}

// ObjectInfo describes the metadata of a single object version returned by
// put, get, head, list, and metadata queries.
type ObjectInfo struct {
	Bucket      string
	Key         string
	Size        int64
	ETag        string
	Version     string
	ContentSHA  string
	ContentType string
	Deleted     bool
}

// PutRequest carries the key, full payload bytes, content type, and
// precondition for an object write.
type PutRequest struct {
	Bucket      string
	Key         string
	Data        []byte
	ContentType string
	Cond        Condition
}

// BucketSpec defines the name, optional home region, and optional quota limits
// for creating a bucket.
type BucketSpec struct {
	Name         string
	Region       string
	QuotaBytes   uint64
	QuotaObjects uint64
}

// QuotaInfo reports a bucket's configured byte and object limits alongside its
// currently recorded usage.
type QuotaInfo struct {
	Bucket       string
	LimitBytes   uint64
	LimitObjects uint64
	UsedBytes    uint64
	UsedObjects  uint64
}

// TagInfo represents one user-defined tag on an object and its record ETag.
type TagInfo struct {
	Name  string
	Value string
	ETag  string
}

// ACLInfo represents one principal's permission string ("r", "w", "a" subset)
// on a bucket and its record ETag.
type ACLInfo struct {
	Principal string
	Perm      string
	ETag      string
}

// MigrationInfo reports the coordinator-local state and progress of a bucket
// migration saga.
type MigrationInfo struct {
	ID      string
	Bucket  string
	From    string
	To      string
	State   string
	Step    int
	Attempt int
	Outcome string
	Error   string
}

// MemberInfo describes one peer in the node's current gossip membership view.
type MemberInfo struct {
	ID          string
	Region      string
	State       string
	Incarnation uint32
}

// DiscardInfo records one last-writer-wins discard observed on a metadata key,
// naming the winning and losing version IDs.
type DiscardInfo struct {
	Key    string
	Winner string
	Loser  string
}

// Health summarizes a node's readiness, membership count, persisted discard
// total, recent discards tail, and diagnostic counters.
type Health struct {
	Node           string
	Region         string
	Ready          bool
	LiveMembers    int
	Discards       uint64
	RecentDiscards []DiscardInfo
	Counters       []Counter
}

// Counter is a named monotonic diagnostic count reported in Health.
type Counter struct {
	Name  string
	Value uint64
}

// StatusRow maps one sentinel error to the HTTP status code served for it.
type StatusRow struct {
	Err    error
	Status int
}
