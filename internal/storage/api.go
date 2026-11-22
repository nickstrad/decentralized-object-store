package storage

import (
	"errors"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

var (
	ErrNoSuchBucket = errors.New("storage: no such bucket")
	ErrBucketExists = errors.New("storage: bucket exists")
	ErrBusy         = errors.New("storage: bucket busy")
	ErrMethod       = errors.New("storage: method not allowed")
)

const (
	CodeNoSuchBucket = env.CodeAppMin + 96
	CodeBucketExists = env.CodeAppMin + 97
	CodeBusy         = env.CodeAppMin + 98
	CodeMethod       = env.CodeAppMin + 99
)

func APICodeEntries() []env.CodeEntry {
	return []env.CodeEntry{
		{Code: CodeNoSuchBucket, Err: ErrNoSuchBucket},
		{Code: CodeBucketExists, Err: ErrBucketExists},
		{Code: CodeBusy, Err: ErrBusy},
		{Code: CodeMethod, Err: ErrMethod},
	}
}

type Condition struct {
	IfMatch     string
	IfMatchList bool
	IfMatchAny  []string
	IfNoneMatch bool
}

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

type PutRequest struct {
	Bucket      string
	Key         string
	Data        []byte
	ContentType string
	Cond        Condition
}

type BucketSpec struct {
	Name         string
	Region       string
	QuotaBytes   uint64
	QuotaObjects uint64
}

type QuotaInfo struct {
	Bucket       string
	LimitBytes   uint64
	LimitObjects uint64
	UsedBytes    uint64
	UsedObjects  uint64
}

type TagInfo struct {
	Name  string
	Value string
	ETag  string
}

type ACLInfo struct {
	Principal string
	Perm      string
	ETag      string
}

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

type MemberInfo struct {
	ID          string
	Region      string
	State       string
	Incarnation uint32
}

type DiscardInfo struct {
	Key    string
	Winner string
	Loser  string
}

type Health struct {
	Node           string
	Region         string
	Ready          bool
	LiveMembers    int
	Discards       uint64
	RecentDiscards []DiscardInfo
	Counters       []Counter
}

type Counter struct {
	Name  string
	Value uint64
}

type StatusRow struct {
	Err    error
	Status int
}
