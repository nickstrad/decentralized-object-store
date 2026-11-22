package noderuntime

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync/atomic"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
	"github.com/nickstrad/decentralized-object-store/internal/storage/httpapi"
)

type result[T any] struct {
	v   T
	err error
}

const (
	callQueued int32 = iota
	callRunning
	callCanceled
	callDelivered
	callLeft
)

// call dispatches one operation onto the single-threaded node loop and waits
// for its completion or context cancellation. Five CAS states (callQueued..
// callLeft) separate calls canceled before the loop takes them (never applied,
// returns ctx.Err) from calls canceled after the loop took them (applied once,
// returns ErrOutcomeUnknown joined with the cause, and logs/counts
// abandoned_calls when done).
func call[T any](ctx context.Context, rt *Runtime, op string, fn func(n *node.Node, done func(T, error))) (T, error) {
	var zero T
	rt.mu.Lock()
	state := rt.state
	rt.mu.Unlock()
	switch state {
	case stateClosed:
		return zero, fmt.Errorf("node: %s on %s: %w", op, rt.cfg.ID, env.ErrClosed)
	case stateRunning:
	default:
		return zero, fmt.Errorf("node: %s on %s: %w", op, rt.cfg.ID, node.ErrNotReady)
	}
	if err := ctx.Err(); err != nil {
		return zero, fmt.Errorf("node: %s on %s: not applied: %w", op, rt.cfg.ID, err)
	}
	var st atomic.Int32
	left := make(chan struct{})
	ch := make(chan result[T], 1)
	f := func() {
		if !st.CompareAndSwap(callQueued, callRunning) {
			return
		}
		if h := rt.dequeueHook; h != nil {
			h(op, left)
		}
		once := false
		fn(rt.nd, func(v T, err error) {
			if once {
				return
			}
			once = true
			if st.CompareAndSwap(callRunning, callDelivered) {
				ch <- result[T]{v: v, err: err}
				return
			}
			rt.abandoned.Add(1)
			rt.logf("node %s: %s finished after its caller left: err=%v", rt.cfg.ID, op, err)
		})
	}
	abandon := func(cause error) (T, error) {
		if st.CompareAndSwap(callQueued, callCanceled) {
			return zero, fmt.Errorf("node: %s on %s: not applied: %w", op, rt.cfg.ID, cause)
		}
		if st.CompareAndSwap(callRunning, callLeft) {
			close(left)
			return zero, fmt.Errorf("node: %s on %s: %w", op, rt.cfg.ID, errors.Join(ErrOutcomeUnknown, cause))
		}
		r := <-ch
		return r.v, r.err
	}
	select {
	case rt.ctrl <- f:
	case <-ctx.Done():
		return zero, fmt.Errorf("node: %s on %s: not applied: %w", op, rt.cfg.ID, ctx.Err())
	case <-rt.ctx.Done():
		return zero, fmt.Errorf("node: %s on %s: %w", op, rt.cfg.ID, env.ErrClosed)
	}
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		return abandon(ctx.Err())
	case <-rt.ctx.Done():
		v, err := abandon(env.ErrClosed)
		if err != nil && !errors.Is(err, env.ErrClosed) {
			err = errors.Join(err, env.ErrClosed)
		}
		return v, err
	}
}

func syncCall[T any](ctx context.Context, rt *Runtime, op string, fn func(n *node.Node) (T, error)) (T, error) {
	return call(ctx, rt, op, func(n *node.Node, done func(T, error)) { done(fn(n)) })
}

var _ httpapi.Service = (*Runtime)(nil)

// Inspect runs fn synchronously on the node loop with direct access to the
// underlying Node, returning env.ErrClosed, node.ErrNotReady, or ctx.Err() if
// the loop is unavailable or ctx ends first.
func (rt *Runtime) Inspect(ctx context.Context, fn func(*node.Node)) error {
	_, err := syncCall(ctx, rt, "inspect", func(n *node.Node) (struct{}, error) { fn(n); return struct{}{}, nil })
	return err
}

// CreateBucket creates a bucket on the node loop, implementing httpapi.Service.
func (rt *Runtime) CreateBucket(ctx context.Context, spec storage.BucketSpec) error {
	_, err := syncCall(ctx, rt, "create bucket", func(n *node.Node) (struct{}, error) { return struct{}{}, n.CreateBucket(spec) })
	return err
}

// ListObjects lists live objects in bucket matching prefix up to limit.
func (rt *Runtime) ListObjects(ctx context.Context, bucket, prefix string, limit int) ([]storage.ObjectInfo, error) {
	return syncCall(ctx, rt, "list", func(n *node.Node) ([]storage.ObjectInfo, error) { return n.ListObjects(bucket, prefix, limit) })
}

// PutObject writes an object through the node loop, honoring req.Cond.
func (rt *Runtime) PutObject(ctx context.Context, req storage.PutRequest) (storage.ObjectInfo, error) {
	return call(ctx, rt, "put", func(n *node.Node, done func(storage.ObjectInfo, error)) { n.PutObject(req, done) })
}

type getResult struct {
	info storage.ObjectInfo
	data []byte
}

// GetObject fetches metadata and full payload bytes for bucket/key.
func (rt *Runtime) GetObject(ctx context.Context, bucket, key string) (storage.ObjectInfo, []byte, error) {
	r, err := call(ctx, rt, "get", func(n *node.Node, done func(getResult, error)) {
		n.GetObject(bucket, key, func(oi storage.ObjectInfo, data []byte, err error) { done(getResult{info: oi, data: data}, err) })
	})
	return r.info, r.data, err
}

// HeadObject returns metadata for bucket/key without fetching shards.
func (rt *Runtime) HeadObject(ctx context.Context, bucket, key string) (storage.ObjectInfo, error) {
	return syncCall(ctx, rt, "head", func(n *node.Node) (storage.ObjectInfo, error) { return n.HeadObject(bucket, key) })
}

// DeleteObject tombstones bucket/key subject to cond.
func (rt *Runtime) DeleteObject(ctx context.Context, bucket, key string, cond storage.Condition) (storage.ObjectInfo, error) {
	return syncCall(ctx, rt, "delete", func(n *node.Node) (storage.ObjectInfo, error) { return n.DeleteObject(bucket, key, cond) })
}

// PutTag sets one user tag on bucket/key subject to cond.
func (rt *Runtime) PutTag(ctx context.Context, bucket, key, name, value string, cond storage.Condition) (storage.TagInfo, error) {
	return syncCall(ctx, rt, "put tag", func(n *node.Node) (storage.TagInfo, error) { return n.PutTag(bucket, key, name, value, cond) })
}

// Tags returns all user tags on bucket/key.
func (rt *Runtime) Tags(ctx context.Context, bucket, key string) ([]storage.TagInfo, error) {
	return syncCall(ctx, rt, "tags", func(n *node.Node) ([]storage.TagInfo, error) { return n.Tags(bucket, key) })
}

// PutACL sets a principal's permission string on bucket subject to cond.
func (rt *Runtime) PutACL(ctx context.Context, bucket, principal, perm string, cond storage.Condition) (storage.ACLInfo, error) {
	return syncCall(ctx, rt, "put acl", func(n *node.Node) (storage.ACLInfo, error) { return n.PutACL(bucket, principal, perm, cond) })
}

// ACL returns a principal's permission entry on bucket.
func (rt *Runtime) ACL(ctx context.Context, bucket, principal string) (storage.ACLInfo, error) {
	return syncCall(ctx, rt, "acl", func(n *node.Node) (storage.ACLInfo, error) { return n.ACL(bucket, principal) })
}

// Quota returns a bucket's configured limits and recorded usage.
func (rt *Runtime) Quota(ctx context.Context, bucket string) (storage.QuotaInfo, error) {
	return syncCall(ctx, rt, "quota", func(n *node.Node) (storage.QuotaInfo, error) { return n.Quota(bucket) })
}

// SetQuota updates a bucket's byte and object quota limits.
func (rt *Runtime) SetQuota(ctx context.Context, bucket string, bytes, objects uint64) (storage.QuotaInfo, error) {
	return syncCall(ctx, rt, "set quota", func(n *node.Node) (storage.QuotaInfo, error) { return n.SetQuota(bucket, bytes, objects) })
}

// StartMigration starts a bucket migration saga coordinated by this node.
func (rt *Runtime) StartMigration(ctx context.Context, bucket, to string) (storage.MigrationInfo, error) {
	return syncCall(ctx, rt, "migrate", func(n *node.Node) (storage.MigrationInfo, error) { return n.StartMigration(bucket, to) })
}

// Migration returns the status of a migration saga coordinated by this node, or
// node.ErrUnknownSaga if unknown locally.
func (rt *Runtime) Migration(ctx context.Context, id string) (storage.MigrationInfo, error) {
	return syncCall(ctx, rt, "migration", func(n *node.Node) (storage.MigrationInfo, error) { return n.Migration(id) })
}

// Members returns the node's current gossip membership view.
func (rt *Runtime) Members(ctx context.Context) ([]storage.MemberInfo, error) {
	return syncCall(ctx, rt, "members", func(n *node.Node) ([]storage.MemberInfo, error) { return n.Members(), nil })
}

// Health returns node health supplemented with runtime transport, call,
// goroutine, and heap counters.
func (rt *Runtime) Health(ctx context.Context) (storage.Health, error) {
	h, err := syncCall(ctx, rt, "health", func(n *node.Node) (storage.Health, error) { return n.Health(), nil })
	if err != nil {
		return h, err
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	h.Counters = append(h.Counters,
		storage.Counter{Name: "datagrams_dropped", Value: rt.Dropped()},
		storage.Counter{Name: "connections_refused", Value: rt.Refused()},
		storage.Counter{Name: "bad_frames", Value: rt.BadFrames()},
		storage.Counter{Name: "abandoned_calls", Value: rt.Abandoned()},
		storage.Counter{Name: "unresolved_sends", Value: rt.Unresolved()},
		storage.Counter{Name: "goroutines", Value: uint64(runtime.NumGoroutine())},
		storage.Counter{Name: "heap_alloc_bytes", Value: ms.HeapAlloc},
	)
	return h, nil
}
