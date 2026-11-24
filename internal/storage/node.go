// Package storage implements the local verified chunk store, self-checksummed
// shard blob format, fixed-size object striping, canonical object manifests,
// region-aware rendezvous shard placement, and the single-threaded storage Node
// process.
//
// Corruption detection lives here: every shard read and decode verifies the
// shard header, SHA-256 checksum, and (during object reads) the manifest's
// per-shard checksum, turning corrupted shards into erasures for package
// erasure. Node satisfies env.Process with no goroutines, maps, or wall-clock
// access so it runs identically in the simulator and on a real node loop.
package storage

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/erasure"
)

// Default coordinator timeouts, stripe-window size, and transient-error retry limits.
const (
	DefaultRequestTimeout = time.Second
	DefaultWindow         = 4
	MaxShardAttempts      = 4
	RetryBackoff          = 50 * time.Millisecond
)

// Config configures a storage Node. Zero values for AckQuorum, RequestTimeout,
// and Window select min(K+1, K+M), DefaultRequestTimeout, and DefaultWindow.
// Window caps stripes launched and awaiting quorum (Put) or decode (Get); it
// does not cap shard requests still outstanding to slow holders after a stripe
// reaches quorum or decodes.
type Config struct {
	MetricsEnabled bool
	Layout         Layout
	Members        Membership
	AckQuorum      int
	RequestTimeout time.Duration
	Window         int
}

// Validate checks Layout, non-nil Members, AckQuorum in [K, K+M] (or 0 for
// default), and non-negative RequestTimeout and Window, returning ErrInvalid on failure.
func (c Config) Validate() error {
	if err := c.Layout.Validate(); err != nil {
		return err
	}
	if c.Members == nil {
		return fmt.Errorf("storage: config: no membership: %w", ErrInvalid)
	}
	if c.AckQuorum != 0 && (c.AckQuorum < c.Layout.K || c.AckQuorum > c.Layout.K+c.Layout.M) {
		return fmt.Errorf("storage: config: ack quorum %d outside [k=%d, k+m=%d]: %w", c.AckQuorum, c.Layout.K, c.Layout.K+c.Layout.M, ErrInvalid)
	}
	if c.RequestTimeout < 0 || c.Window < 0 {
		return fmt.Errorf("storage: config: negative timeout or window: %w", ErrInvalid)
	}
	return nil
}

func (c Config) withDefaults() Config {
	if c.AckQuorum == 0 {
		c.AckQuorum = min(c.Layout.K+1, c.Layout.K+c.Layout.M)
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = DefaultRequestTimeout
	}
	if c.Window == 0 {
		c.Window = DefaultWindow
	}
	return c
}

// NodeStats records deterministic operational counters for one Node incarnation,
// included in Node.EncodeState. PutAwaitingPeak is the peak number of stripes
// of one Put awaiting quorum simultaneously; PutOutstanding and
// PutOutstandingBytes track put RPCs still awaiting a reply or timeout.
type NodeStats struct {
	Served              uint64
	Refused             uint64
	Requests            uint64
	PutsDone            uint64
	PutsFail            uint64
	GetsDone            uint64
	GetsFail            uint64
	Datagrams           uint64
	PutAwaitingPeak     uint64
	PutOutstanding      uint64
	PutOutstandingBytes uint64
}

// Node is an env.Process that serves local shard RPCs from its ChunkStore and
// coordinates striped erasure-coded Put and Get operations across peers.
type Node struct {
	metric *metricState
	e      env.Env
	cfg    Config
	codec  StripeCodec
	store  *ChunkStore
	stats  NodeStats
}

// NewNode validates cfg and creates a Node backed by e. It returns ErrInvalid
// if cfg is invalid.
func NewNode(e env.Env, cfg Config) (*Node, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	codec, err := erasure.NewMetered(cfg.Layout.K, cfg.Layout.M, cfg.MetricsEnabled)
	if err != nil {
		return nil, err
	}
	n := &Node{metric: newMetricState(cfg.MetricsEnabled), e: e, cfg: cfg.withDefaults(), codec: codec, store: NewChunkStore(e.Disk)}
	n.store.enableMetrics(n.metric)
	return n, nil
}

// Store returns the node's local ChunkStore.
func (n *Node) Store() *ChunkStore { return n.store }

// Stats returns a snapshot of the node's deterministic counters.
func (n *Node) Stats() NodeStats { return n.stats }

// Start implements env.Process; Node needs no startup work.
func (n *Node) Start() {}

// OnDatagram implements env.Process; storage uses request/response RPCs, so
// datagrams are only counted.
func (n *Node) OnDatagram(env.NodeID, []byte) { n.stats.Datagrams++ }

// OnRequest implements env.Process by serving OpPutShard, OpGetShard, and
// OpDeleteShard against the local ChunkStore and invoking reply synchronously.
func (n *Node) OnRequest(_ env.NodeID, req []byte, reply env.Reply) {
	resp, err := n.serve(req)
	if err != nil {
		n.stats.Refused++
		reply(nil, err)
		return
	}
	n.stats.Served++
	reply(resp, nil)
}

func (n *Node) serve(req []byte) ([]byte, error) {
	op, id, body, err := DecodeShardRequest(req)
	if err != nil {
		return nil, err
	}
	switch op {
	case OpPutShard:
		return nil, n.store.Put(id, body)
	case OpGetShard:
		return n.store.GetBlob(id)
	default:
		return nil, n.store.Delete(id)
	}
}

// EncodeState returns a canonical uvarint encoding of n's counters for
// deterministic state-digest comparison.
func (n *Node) EncodeState() []byte {
	s := n.stats
	var b []byte
	for _, v := range []uint64{s.Served, s.Refused, s.Requests, s.PutsDone, s.PutsFail, s.GetsDone, s.GetsFail, s.Datagrams, s.PutAwaitingPeak, s.PutOutstanding, s.PutOutstandingBytes} {
		b = binary.AppendUvarint(b, v)
	}
	return b
}

func (n *Node) request(to env.NodeID, req []byte, cb func([]byte, error)) {
	n.stats.Requests++
	n.metric.adjust("storage_requests_in_flight", 1)
	n.e.Net.Request(to, req, n.cfg.RequestTimeout, func(b []byte, err error) {
		n.metric.adjust("storage_requests_in_flight", -1)
		if errors.Is(err, env.ErrTimeout) {
			n.metric.add("storage_rpc_timeouts_total", 1)
		}
		cb(b, err)
	})
}

func (n *Node) later(fn func()) {
	n.e.Clock.AfterFunc(0, fn)
}

type putOp struct {
	n        *Node
	man      *Manifest
	data     []byte
	cb       func(*Manifest, error)
	next     int
	inflight int
	acked    int
	stopped  bool
	finished bool
	stripes  []putStripe
}

type putStripe struct {
	acks, replies int
	advanced      bool
	errs          []error
}

// Put erasure-codes data under the node's Config.Layout, places and writes
// shards stripe by stripe with up to Config.Window stripes awaiting quorum at
// once, and invokes cb with the resulting Manifest or an *ObjectError.
// Put succeeds as soon as every stripe reaches AckQuorum durable replies; at
// most N - AckQuorum requests per stripe may remain outstanding to slow holders
// until they reply or hit RequestTimeout. cb never runs synchronously inside Put.
func (n *Node) Put(bucket, key string, data []byte, cb func(*Manifest, error)) {
	man := &Manifest{
		Bucket: bucket, Key: key, Size: int64(len(data)),
		Version: VersionID(n.e.Rand.Uint64()), ContentSHA: sha256.Sum256(data), Layout: n.cfg.Layout,
	}
	man.Stripes = make([]StripeRef, man.Layout.Stripes(man.Size))
	for i := range man.Stripes {
		man.Stripes[i].Shards = make([]ShardRef, n.codec.N())
	}
	op := &putOp{n: n, man: man, data: data, cb: cb, stripes: make([]putStripe, len(man.Stripes))}
	if err := validObject(bucket, key); err != nil {
		n.later(func() { op.finish(err) })
		return
	}
	if len(man.Stripes) == 0 {
		n.later(func() { op.finish(nil) })
		return
	}
	op.fill()
}

func validObject(bucket, key string) error {
	if err := ValidBucket(bucket); err != nil {
		return err
	}
	if len(key) == 0 || len(key) > MaxKeyLen {
		return fmt.Errorf("storage: key length %d outside [1, %d]: %w", len(key), MaxKeyLen, ErrInvalid)
	}
	return nil
}

func (op *putOp) finish(err error) {
	if op.finished {
		return
	}
	op.finished = true
	// Drop the object buffer so late reply closures for slow holders do not pin it.
	op.data = nil
	if err != nil {
		op.n.stats.PutsFail++
		op.cb(nil, objectErr("put", op.man.Bucket, op.man.Key, err))
		return
	}
	op.n.stats.PutsDone++
	op.cb(op.man, nil)
}

func (op *putOp) fill() {
	for !op.finished && !op.stopped && op.inflight < op.n.cfg.Window && op.next < len(op.stripes) {
		i := op.next
		op.next++
		// Launch before incrementing inflight so an immediate placement error
		// does not inflate PutAwaitingPeak; defer finish so cb never runs inside Put.
		if err := op.launch(i); err != nil {
			op.stopped = true
			op.n.later(func() { op.finish(err) })
			return
		}
		op.inflight++
		op.n.stats.PutAwaitingPeak = max(op.n.stats.PutAwaitingPeak, uint64(op.inflight))
	}
}

func (op *putOp) launch(i int) error {
	n, man := op.n, op.man
	stripe := uint32(i)
	lo := int64(i) * int64(man.Layout.StripeSize)
	data := op.data[lo : lo+int64(man.Layout.StripeLen(man.Size, i))]
	blobs, err := EncodeStripeWithCodec(n.codec, man.Version, stripe, data)
	if err != nil {
		return err
	}
	nodes, err := Place(n.cfg.Members, StripeKey(man.Bucket, man.Version, stripe), n.codec.N(), n.codec.M())
	if err != nil {
		return fmt.Errorf("stripe %d: %w", i, err)
	}
	for idx, blob := range blobs {
		man.Stripes[i].Shards[idx] = ShardRef{Node: nodes[idx], Checksum: BlobChecksum(blob)}
	}
	for idx, blob := range blobs {
		target := nodes[idx]
		req := EncodeShardRequest(OpPutShard, man.ShardID(i, idx), blob)
		size := uint64(len(req))
		n.stats.PutOutstanding++
		n.stats.PutOutstandingBytes += size
		n.request(target, req, func(_ []byte, err error) {
			n.stats.PutOutstanding--
			n.stats.PutOutstandingBytes -= size
			op.reply(i, idx, target, err)
		})
	}
	return nil
}

func (op *putOp) reply(i, idx int, target env.NodeID, err error) {
	st := &op.stripes[i]
	st.replies++
	if err == nil {
		st.acks++
		if st.acks == op.n.cfg.AckQuorum {
			op.acked++
		}
	} else {
		st.errs = append(st.errs, fmt.Errorf("shard %d on %s: %w", idx, target, err))
	}
	if op.finished || op.stopped {
		return
	}
	done := st.replies == op.n.codec.N()
	if done && st.acks < op.n.cfg.AckQuorum {
		cause := errors.Join(append([]error{ErrUnavailable}, st.errs...)...)
		op.finish(fmt.Errorf("stripe %d: %d of %d shards durable, need %d: %w", i, st.acks, op.n.codec.N(), op.n.cfg.AckQuorum, cause))
		return
	}
	if !st.advanced && (done || st.acks >= op.n.cfg.AckQuorum) {
		st.advanced = true
		op.inflight--
		op.fill()
	}
	if op.acked == len(op.stripes) {
		op.finish(nil)
	}
}

// GetResult carries the reassembled object bytes (nil on error) along with the
// shard RPC and rejection counts observed during Node.Get.
type GetResult struct {
	Data     []byte
	Requests int
	Rejected int
}

type getOp struct {
	n        *Node
	codec    StripeCodec
	man      *Manifest
	cb       func(GetResult, error)
	res      GetResult
	out      []byte
	next     int
	inflight int
	decoded  int
	finished bool
	stripes  []getStripe
}

type getStripe struct {
	exp      shardExpect
	payloads [][]byte
	attempts []int
	retry    []int
	tried    int
	pending  int
	good     int
	waiting  bool
	done     bool
	causes   []error
}

// Get fetches and verifies shards described by man using man.Layout (even if it
// differs from the node's Config.Layout), decodes up to Config.Window stripes
// concurrently, verifies the reassembled SHA-256 against man.ContentSHA, and
// invokes cb with the result or an *ObjectError.
// Each stripe requests its k data shards first, falls back to parity shards on
// failure or corruption, and retries transient errors (env.ErrIO, env.ErrTimeout,
// env.ErrUnreachable) after RetryBackoff up to MaxShardAttempts per shard.
// Corrupt and missing shards are never retried. cb never runs synchronously inside Get.
func (n *Node) Get(man *Manifest, cb func(GetResult, error)) {
	op := &getOp{n: n, man: man, cb: cb}
	if man == nil {
		n.later(func() { op.finish(fmt.Errorf("storage: get: nil manifest: %w", ErrInvalid)) })
		return
	}
	op.codec = n.codec
	err := man.Validate()
	if err == nil && (man.Layout.K != n.codec.K() || man.Layout.M != n.codec.M()) {
		op.codec, err = n.NewCodec(man.Layout.K, man.Layout.M)
	}
	if err != nil {
		n.later(func() { op.finish(err) })
		return
	}
	op.out = make([]byte, man.Size)
	op.stripes = make([]getStripe, len(man.Stripes))
	if len(man.Stripes) == 0 {
		n.later(func() { op.finish(nil) })
		return
	}
	op.fill()
}

func (op *getOp) finish(err error) {
	if op.finished {
		return
	}
	op.finished = true
	bucket, key := "", ""
	if op.man != nil {
		bucket, key = op.man.Bucket, op.man.Key
	}
	if err == nil && sha256.Sum256(op.out) != op.man.ContentSHA {
		err = fmt.Errorf("content hash differs from the manifest: %w", ErrCorrupt)
	}
	if err != nil {
		op.n.stats.GetsFail++
		op.res.Data = nil
		op.cb(op.res, objectErr("get", bucket, key, err))
		return
	}
	op.n.stats.GetsDone++
	op.res.Data = op.out
	op.cb(op.res, nil)
}

func (op *getOp) fill() {
	for !op.finished && op.inflight < op.n.cfg.Window && op.next < len(op.stripes) {
		i := op.next
		op.next++
		op.inflight++
		st := &op.stripes[i]
		st.exp = expectFor(op.codec, op.man.Version, uint32(i), op.man.Layout.StripeLen(op.man.Size, i))
		st.payloads = make([][]byte, op.codec.N())
		st.attempts = make([]int, op.codec.N())
		for j := 0; j < op.codec.K(); j++ {
			op.fetch(i)
		}
	}
}

func (op *getOp) fetch(i int) {
	st := &op.stripes[i]
	idx := st.tried
	st.tried++
	op.send(i, idx)
}

func (op *getOp) send(i, idx int) {
	st := &op.stripes[i]
	st.pending++
	st.attempts[idx]++
	op.res.Requests++
	ref := op.man.Stripes[i].Shards[idx]
	want := ref.Checksum
	op.n.request(ref.Node, EncodeShardRequest(OpGetShard, op.man.ShardID(i, idx), nil), func(blob []byte, err error) {
		op.reply(i, idx, ref.Node, blob, err, want)
	})
}

func transient(err error) bool {
	return errors.Is(err, env.ErrIO) || errors.Is(err, env.ErrTimeout) || errors.Is(err, env.ErrUnreachable)
}

func (op *getOp) reply(i, idx int, from env.NodeID, blob []byte, err error, want [32]byte) {
	st := &op.stripes[i]
	st.pending--
	if op.finished || st.done {
		return
	}
	var payload []byte
	if err == nil {
		payload, err = st.exp.verify(idx, blob, &want)
	}
	if err != nil {
		op.res.Rejected++
		st.causes = append(st.causes, fmt.Errorf("shard %d on %s attempt %d: %w", idx, from, st.attempts[idx], err))
		if transient(err) && st.attempts[idx] < MaxShardAttempts {
			st.retry = append(st.retry, idx)
		}
		if st.tried < op.codec.N() {
			op.fetch(i)
			return
		}
		op.checkExhausted(i)
		return
	}
	st.payloads[idx] = payload
	st.good++
	if st.good < op.codec.K() {
		op.checkExhausted(i)
		return
	}
	data, derr := decodeVerified(op.codec, uint32(i), st.exp.length, st.payloads, st.causes)
	if derr != nil {
		op.finish(fmt.Errorf("stripe %d: %w", i, derr))
		return
	}
	copy(op.out[int64(i)*int64(op.man.Layout.StripeSize):], data)
	st.done = true
	st.payloads = nil
	op.decoded++
	op.inflight--
	if op.decoded == len(op.stripes) {
		op.finish(nil)
		return
	}
	op.fill()
}

// checkExhausted schedules a backoff retry batch for transient failures once
// all n shards have been tried and no requests remain pending, or fails the Get
// with ErrUnavailable if no retryable shards remain.
func (op *getOp) checkExhausted(i int) {
	st := &op.stripes[i]
	if st.pending > 0 || st.waiting || st.tried < op.codec.N() {
		return
	}
	if len(st.retry) > 0 {
		st.waiting = true
		op.n.e.Clock.AfterFunc(RetryBackoff, func() { op.retry(i) })
		return
	}
	cause := errors.Join(append([]error{ErrUnavailable}, st.causes...)...)
	op.finish(fmt.Errorf("stripe %d: %d of %d shards verified, need %d: %w", i, st.good, op.codec.N(), op.codec.K(), cause))
}

func (op *getOp) retry(i int) {
	st := &op.stripes[i]
	st.waiting = false
	if op.finished || st.done {
		return
	}
	again := st.retry
	st.retry = nil
	for _, idx := range again {
		op.send(i, idx)
	}
}
