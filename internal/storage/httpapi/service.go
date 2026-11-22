package httpapi

import (
	"context"

	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

type Service interface {
	CreateBucket(ctx context.Context, spec storage.BucketSpec) error
	ListObjects(ctx context.Context, bucket, prefix string, limit int) ([]storage.ObjectInfo, error)
	PutObject(ctx context.Context, req storage.PutRequest) (storage.ObjectInfo, error)
	GetObject(ctx context.Context, bucket, key string) (storage.ObjectInfo, []byte, error)
	HeadObject(ctx context.Context, bucket, key string) (storage.ObjectInfo, error)
	DeleteObject(ctx context.Context, bucket, key string, cond storage.Condition) (storage.ObjectInfo, error)
	PutTag(ctx context.Context, bucket, key, name, value string, cond storage.Condition) (storage.TagInfo, error)
	Tags(ctx context.Context, bucket, key string) ([]storage.TagInfo, error)
	PutACL(ctx context.Context, bucket, principal, perm string, cond storage.Condition) (storage.ACLInfo, error)
	ACL(ctx context.Context, bucket, principal string) (storage.ACLInfo, error)
	Quota(ctx context.Context, bucket string) (storage.QuotaInfo, error)
	SetQuota(ctx context.Context, bucket string, bytes, objects uint64) (storage.QuotaInfo, error)
	StartMigration(ctx context.Context, bucket, to string) (storage.MigrationInfo, error)
	Migration(ctx context.Context, id string) (storage.MigrationInfo, error)
	Members(ctx context.Context) ([]storage.MemberInfo, error)
	Health(ctx context.Context) (storage.Health, error)
}
