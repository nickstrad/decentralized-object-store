package node

import (
	"context"
	"fmt"
	"runtime"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

type result[T any] struct {
	v   T
	err error
}

func call[T any](ctx context.Context, rt *Runtime, op string, fn func(n *Node, done func(T, error))) (T, error) {
	var zero T
	rt.mu.Lock()
	state := rt.state
	rt.mu.Unlock()
	switch state {
	case stateClosed:
		return zero, fmt.Errorf("node: %s on %s: %w", op, rt.cfg.ID, env.ErrClosed)
	case stateRunning:
	default:
		return zero, fmt.Errorf("node: %s on %s: %w", op, rt.cfg.ID, ErrNotReady)
	}
	ch := make(chan result[T], 1)
	f := func() {
		once := false
		fn(rt.node, func(v T, err error) {
			if once {
				return
			}
			once = true
			ch <- result[T]{v: v, err: err}
		})
	}
	select {
	case rt.ctrl <- f:
	case <-ctx.Done():
		return zero, fmt.Errorf("node: %s on %s: %w", op, rt.cfg.ID, ctx.Err())
	case <-rt.ctx.Done():
		return zero, fmt.Errorf("node: %s on %s: %w", op, rt.cfg.ID, env.ErrClosed)
	}
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		return zero, fmt.Errorf("node: %s on %s: %w", op, rt.cfg.ID, ctx.Err())
	case <-rt.ctx.Done():
		return zero, fmt.Errorf("node: %s on %s: %w", op, rt.cfg.ID, env.ErrClosed)
	}
}

func syncCall[T any](ctx context.Context, rt *Runtime, op string, fn func(n *Node) (T, error)) (T, error) {
	return call(ctx, rt, op, func(n *Node, done func(T, error)) { done(fn(n)) })
}

var _ storage.Service = (*Runtime)(nil)

func (rt *Runtime) CreateBucket(ctx context.Context, spec storage.BucketSpec) error {
	_, err := syncCall(ctx, rt, "create bucket", func(n *Node) (struct{}, error) { return struct{}{}, n.CreateBucket(spec) })
	return err
}

func (rt *Runtime) ListObjects(ctx context.Context, bucket, prefix string, limit int) ([]storage.ObjectInfo, error) {
	return syncCall(ctx, rt, "list", func(n *Node) ([]storage.ObjectInfo, error) { return n.ListObjects(bucket, prefix, limit) })
}

func (rt *Runtime) PutObject(ctx context.Context, req storage.PutRequest) (storage.ObjectInfo, error) {
	return call(ctx, rt, "put", func(n *Node, done func(storage.ObjectInfo, error)) { n.PutObject(req, done) })
}

type getResult struct {
	info storage.ObjectInfo
	data []byte
}

func (rt *Runtime) GetObject(ctx context.Context, bucket, key string) (storage.ObjectInfo, []byte, error) {
	r, err := call(ctx, rt, "get", func(n *Node, done func(getResult, error)) {
		n.GetObject(bucket, key, func(oi storage.ObjectInfo, data []byte, err error) { done(getResult{info: oi, data: data}, err) })
	})
	return r.info, r.data, err
}

func (rt *Runtime) HeadObject(ctx context.Context, bucket, key string) (storage.ObjectInfo, error) {
	return syncCall(ctx, rt, "head", func(n *Node) (storage.ObjectInfo, error) { return n.HeadObject(bucket, key) })
}

func (rt *Runtime) DeleteObject(ctx context.Context, bucket, key string, cond storage.Condition) (storage.ObjectInfo, error) {
	return syncCall(ctx, rt, "delete", func(n *Node) (storage.ObjectInfo, error) { return n.DeleteObject(bucket, key, cond) })
}

func (rt *Runtime) PutTag(ctx context.Context, bucket, key, name, value string, cond storage.Condition) (storage.TagInfo, error) {
	return syncCall(ctx, rt, "put tag", func(n *Node) (storage.TagInfo, error) { return n.PutTag(bucket, key, name, value, cond) })
}

func (rt *Runtime) Tags(ctx context.Context, bucket, key string) ([]storage.TagInfo, error) {
	return syncCall(ctx, rt, "tags", func(n *Node) ([]storage.TagInfo, error) { return n.Tags(bucket, key) })
}

func (rt *Runtime) PutACL(ctx context.Context, bucket, principal, perm string, cond storage.Condition) (storage.ACLInfo, error) {
	return syncCall(ctx, rt, "put acl", func(n *Node) (storage.ACLInfo, error) { return n.PutACL(bucket, principal, perm, cond) })
}

func (rt *Runtime) ACL(ctx context.Context, bucket, principal string) (storage.ACLInfo, error) {
	return syncCall(ctx, rt, "acl", func(n *Node) (storage.ACLInfo, error) { return n.ACL(bucket, principal) })
}

func (rt *Runtime) Quota(ctx context.Context, bucket string) (storage.QuotaInfo, error) {
	return syncCall(ctx, rt, "quota", func(n *Node) (storage.QuotaInfo, error) { return n.Quota(bucket) })
}

func (rt *Runtime) SetQuota(ctx context.Context, bucket string, bytes, objects uint64) (storage.QuotaInfo, error) {
	return syncCall(ctx, rt, "set quota", func(n *Node) (storage.QuotaInfo, error) { return n.SetQuota(bucket, bytes, objects) })
}

func (rt *Runtime) StartMigration(ctx context.Context, bucket, to string) (storage.MigrationInfo, error) {
	return syncCall(ctx, rt, "migrate", func(n *Node) (storage.MigrationInfo, error) { return n.StartMigration(bucket, to) })
}

func (rt *Runtime) Migration(ctx context.Context, id string) (storage.MigrationInfo, error) {
	return syncCall(ctx, rt, "migration", func(n *Node) (storage.MigrationInfo, error) { return n.Migration(id) })
}

func (rt *Runtime) Members(ctx context.Context) ([]storage.MemberInfo, error) {
	return syncCall(ctx, rt, "members", func(n *Node) ([]storage.MemberInfo, error) { return n.Members(), nil })
}

func (rt *Runtime) Health(ctx context.Context) (storage.Health, error) {
	h, err := syncCall(ctx, rt, "health", func(n *Node) (storage.Health, error) { return n.Health(), nil })
	if err != nil {
		return h, err
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	h.Counters = append(h.Counters,
		storage.Counter{Name: "datagrams_dropped", Value: rt.Dropped()},
		storage.Counter{Name: "connections_refused", Value: rt.Refused()},
		storage.Counter{Name: "bad_frames", Value: rt.BadFrames()},
		storage.Counter{Name: "goroutines", Value: uint64(runtime.NumGoroutine())},
		storage.Counter{Name: "heap_alloc_bytes", Value: ms.HeapAlloc},
	)
	return h, nil
}
