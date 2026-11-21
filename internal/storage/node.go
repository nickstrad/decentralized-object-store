package storage

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/erasure"
)

const (
	DefaultRequestTimeout = time.Second
	DefaultWindow         = 4
	MaxShardAttempts      = 4
	RetryBackoff          = 50 * time.Millisecond
)

type Config struct {
	Layout         Layout
	Members        Membership
	AckQuorum      int
	RequestTimeout time.Duration
	Window         int
}

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

type Node struct {
	e     env.Env
	cfg   Config
	codec *erasure.Codec
	store *ChunkStore
	stats NodeStats
}

func NewNode(e env.Env, cfg Config) (*Node, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	codec, err := cfg.Layout.Codec()
	if err != nil {
		return nil, err
	}
	return &Node{e: e, cfg: cfg.withDefaults(), codec: codec, store: NewChunkStore(e.Disk)}, nil
}

func (n *Node) Store() *ChunkStore { return n.store }

func (n *Node) Stats() NodeStats { return n.stats }

func (n *Node) Start() {}

func (n *Node) OnDatagram(env.NodeID, []byte) { n.stats.Datagrams++ }

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
	n.e.Net.Request(to, req, n.cfg.RequestTimeout, cb)
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
		op.inflight++
		op.n.stats.PutAwaitingPeak = max(op.n.stats.PutAwaitingPeak, uint64(op.inflight))
		if err := op.launch(i); err != nil {
			op.stopped = true
			op.n.later(func() { op.finish(err) })
			return
		}
	}
}

func (op *putOp) launch(i int) error {
	n, man := op.n, op.man
	stripe := uint32(i)
	lo := int64(i) * int64(man.Layout.StripeSize)
	data := op.data[lo : lo+int64(man.Layout.StripeLen(man.Size, i))]
	blobs, err := EncodeStripe(n.codec, man.Version, stripe, data)
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

type GetResult struct {
	Data     []byte
	Requests int
	Rejected int
}

type getOp struct {
	n        *Node
	codec    *erasure.Codec
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

func (n *Node) Get(man *Manifest, cb func(GetResult, error)) {
	op := &getOp{n: n, man: man, cb: cb}
	if man == nil {
		n.later(func() { op.finish(fmt.Errorf("storage: get: nil manifest: %w", ErrInvalid)) })
		return
	}
	op.codec = n.codec
	err := man.Validate()
	if err == nil && (man.Layout.K != n.codec.K() || man.Layout.M != n.codec.M()) {
		op.codec, err = man.Layout.Codec()
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
