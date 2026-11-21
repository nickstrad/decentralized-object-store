package occ

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

type Membership interface {
	Peers() []env.NodeID
}

type StaticMembership []env.NodeID

func (m StaticMembership) Peers() []env.NodeID { return slices.Clone(m) }

type Config struct {
	Membership Membership
	Period     time.Duration
	Timeout    time.Duration
	Fanout     int
	MaxDelta   int
	Log        func(line string)
	OnDiscard  func(key string, winner, loser Version)
}

const (
	StateBlob       = "occ/state"
	DefaultPeriod   = 500 * time.Millisecond
	DefaultTimeout  = 300 * time.Millisecond
	DefaultFanout   = 2
	DefaultMaxDelta = 60000
	LoadRetry       = 100 * time.Millisecond
)

var hazardReuseEpoch bool

func (c Config) withDefaults() Config {
	if c.Period <= 0 {
		c.Period = DefaultPeriod
	}
	if c.Timeout <= 0 {
		c.Timeout = DefaultTimeout
	}
	if c.Fanout <= 0 {
		c.Fanout = DefaultFanout
	}
	if c.MaxDelta <= 0 {
		c.MaxDelta = DefaultMaxDelta
	}
	return c
}

type Stats struct {
	Rounds     uint64
	Matches    uint64
	Pulls      uint64
	Pushes     uint64
	Deltas     uint64
	Malformed  uint64
	NetErrors  uint64
	DiskErrors uint64
	LoadErrors uint64
	Corrupt    uint64
	Fresh      uint64
}

type Node struct {
	e     env.Env
	cfg   Config
	store *Store
	stats Stats
}

func NewFactory(cfg Config) func(env.Env) env.Process {
	cfg = cfg.withDefaults()
	return func(e env.Env) env.Process { return NewNode(e, cfg) }
}

func NewNode(e env.Env, cfg Config) *Node {
	return &Node{e: e, cfg: cfg.withDefaults()}
}

func (n *Node) Ready() bool  { return n.store != nil }
func (n *Node) Stats() Stats { return n.stats }

func (n *Node) Digest() [32]byte {
	if n.store == nil {
		return [32]byte{}
	}
	return n.store.doc.Digest()
}

func (n *Node) Reader() (Reader, error) {
	if n.store == nil {
		return Reader{}, fmt.Errorf("occ: read on %s: %w", n.e.ID, ErrNotReady)
	}
	return n.store.Reader, nil
}

func (n *Node) Record(key string) Record {
	if n.store == nil {
		return Record{}
	}
	return n.store.Record(key)
}

func (n *Node) Doc() *Doc {
	if n.store == nil {
		return nil
	}
	return n.store.Doc()
}

func (n *Node) Actor() Actor {
	if n.store == nil {
		return Actor{}
	}
	return n.store.actor
}

func (n *Node) LWWDiscards() uint64 {
	if n.store == nil {
		return 0
	}
	return n.store.discards
}

func (n *Node) DiscardTail() []Discard {
	if n.store == nil {
		return nil
	}
	return n.store.DiscardTail()
}

func (n *Node) AheadStamps() int {
	if n.store == nil {
		return 0
	}
	return n.store.ahead
}

func (n *Node) Start() { n.load() }

func (n *Node) load() {
	b, err := n.e.Disk.Read(StateBlob)
	switch {
	case err == nil:
		st, derr := decodeState(b)
		if derr == nil {
			n.open(st, false)
			return
		}
		n.stats.Corrupt++
		n.open(persisted{doc: NewDoc()}, true)
	case errors.Is(err, env.ErrNotFound):
		n.open(persisted{doc: NewDoc()}, true)
	case errors.Is(err, env.ErrClosed):
	default:
		n.stats.LoadErrors++
		n.e.Clock.AfterFunc(LoadRetry, n.load)
	}
}

func (n *Node) open(st persisted, fresh bool) {
	if fresh {
		n.stats.Fresh++
		if hazardReuseEpoch {
			st.epoch = 1
		}
	}
	n.store = newStore(StoreConfig{
		Node:      n.e.ID,
		Epoch:     st.epoch,
		Counter:   st.counter,
		Discards:  st.discards,
		Clock:     n.e.Clock,
		Rand:      n.e.Rand,
		Doc:       st.doc,
		Persist:   n.persist,
		Log:       n.cfg.Log,
		OnDiscard: n.cfg.OnDiscard,
	})
	if fresh {
		_ = n.persist(n.store.EncodeState())
	}
	n.arm()
}

func (n *Node) persist(blob []byte) error {
	if err := n.e.Disk.Write(StateBlob, blob); err != nil {
		n.stats.DiskErrors++
		return fmt.Errorf("occ: persist %s: %w", StateBlob, err)
	}
	if err := n.e.Disk.Sync(StateBlob); err != nil {
		n.stats.DiskErrors++
		return fmt.Errorf("occ: sync %s: %w", StateBlob, err)
	}
	return nil
}

func (n *Node) arm() {
	d := n.cfg.Period/2 + time.Duration(n.e.Rand.Int64N(int64(n.cfg.Period)))
	n.e.Clock.AfterFunc(d, n.round)
}

func (n *Node) peers() []env.NodeID {
	if n.cfg.Membership == nil {
		return nil
	}
	var out []env.NodeID
	for _, p := range n.cfg.Membership.Peers() {
		if p != n.e.ID {
			out = append(out, p)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

func (n *Node) round() {
	n.arm()
	ps := n.peers()
	if len(ps) == 0 {
		return
	}
	peer := ps[n.e.Rand.IntN(len(ps))]
	n.stats.Rounds++
	n.e.Net.Request(peer, digestRequest(n.Digest()), n.cfg.Timeout, func(reply []byte, err error) {
		n.onDigestReply(peer, reply, err)
	})
}

func (n *Node) onDigestReply(peer env.NodeID, reply []byte, err error) {
	if n.store == nil {
		return
	}
	if err != nil {
		n.stats.NetErrors++
		return
	}
	if len(reply) == 1 && reply[0] == replyMatch {
		n.stats.Matches++
		return
	}
	if len(reply) == 0 || reply[0] != replyState {
		n.stats.Malformed++
		return
	}
	state := reply[1:]
	doc, err := Decode(state)
	if err != nil {
		n.stats.Malformed++
		return
	}
	n.stats.Pulls++
	if _, err := n.store.Merge(doc); err != nil {
		return
	}
	if n.Digest() != stateDigest(state) {
		n.stats.Pushes++
		n.e.Net.Request(peer, append([]byte{msgPush}, n.store.doc.Encode()...), n.cfg.Timeout, func(_ []byte, err error) {
			if err != nil {
				n.stats.NetErrors++
			}
		})
	}
}

func (n *Node) OnDatagram(from env.NodeID, msg []byte) {
	if n.store == nil {
		return
	}
	if len(msg) == 0 || msg[0] != msgDelta {
		n.stats.Malformed++
		return
	}
	doc, err := Decode(msg[1:])
	if err != nil {
		n.stats.Malformed++
		return
	}
	n.stats.Deltas++
	_, _ = n.store.Merge(doc)
}

func (n *Node) OnRequest(from env.NodeID, req []byte, reply env.Reply) {
	if n.store == nil {
		reply(nil, fmt.Errorf("occ: request from %s: %w", from, ErrNotReady))
		return
	}
	if len(req) == 0 {
		reply(nil, fmt.Errorf("occ: empty request from %s: %w", from, ErrMalformed))
		return
	}
	switch req[0] {
	case msgDigest:
		if len(req) != 1+sha256.Size {
			reply(nil, fmt.Errorf("occ: digest request from %s: %w", from, ErrMalformed))
			return
		}
		if digestEqual(req[1:], n.store.doc.Digest()) {
			reply([]byte{replyMatch}, nil)
			return
		}
		reply(append([]byte{replyState}, n.store.doc.Encode()...), nil)
	case msgPush:
		doc, err := Decode(req[1:])
		if err != nil {
			n.stats.Malformed++
			reply(nil, fmt.Errorf("occ: push from %s: %w", from, err))
			return
		}
		if _, err := n.store.Merge(doc); err != nil {
			reply(nil, err)
			return
		}
		reply([]byte{replyAck}, nil)
	case msgOp:
		n.serveOp(from, req, reply)
	default:
		reply(nil, fmt.Errorf("occ: request kind %d from %s: %w", req[0], from, ErrMalformed))
	}
}

func (n *Node) serveOp(from env.NodeID, req []byte, reply env.Reply) {
	o, err := decodeOp(req)
	if err != nil {
		reply(nil, fmt.Errorf("occ: op from %s: %w", from, err))
		return
	}
	var r Result
	switch o.code {
	case opGet:
		r.Value, r.ETag, err = n.Get(o.key)
	case opPut:
		var v Version
		v, err = n.Put(o.key, o.value, o.pre)
		r.Version, r.ETag = v.ID(), v.ETag()
	default:
		var v Version
		v, err = n.Delete(o.key, o.pre)
		r.Version, r.ETag = v.ID(), v.ETag()
	}
	if err != nil {
		reply(nil, err)
		return
	}
	reply(encodeResult(r), nil)
}

func (n *Node) broadcast(key string) {
	delta := append([]byte{msgDelta}, n.store.doc.Subset(key).Encode()...)
	if len(delta) > n.cfg.MaxDelta {
		return
	}
	ps := n.peers()
	for i := 0; i < n.cfg.Fanout && len(ps) > 0; i++ {
		if err := n.e.Net.Send(ps[n.e.Rand.IntN(len(ps))], delta); err != nil {
			n.stats.NetErrors++
		}
	}
}

func (n *Node) notReady(op, key string) error {
	return fmt.Errorf("occ: %s %q on %s: %w", op, key, n.e.ID, ErrNotReady)
}

func (n *Node) Get(key string) ([]byte, ETag, error) {
	if n.store == nil {
		return nil, ETag{}, n.notReady("get", key)
	}
	return n.store.Get(key)
}

func (n *Node) Put(key string, value []byte, p Precondition) (Version, error) {
	if n.store == nil {
		return Version{}, n.notReady("put", key)
	}
	v, err := n.store.Put(key, value, p)
	if err != nil {
		return Version{}, err
	}
	n.broadcast(key)
	return v, nil
}

func (n *Node) Delete(key string, p Precondition) (Version, error) {
	if n.store == nil {
		return Version{}, n.notReady("delete", key)
	}
	v, err := n.store.Delete(key, p)
	if err != nil {
		return Version{}, err
	}
	n.broadcast(key)
	return v, nil
}

func (n *Node) SetScalar(key string, value []byte) (Version, error) {
	return n.Put(key, value, Unconditional())
}

func (n *Node) UpdateQuota(key string, attempts int, fn func(Usage) (Usage, error)) (Usage, error) {
	if n.store == nil {
		return Usage{}, n.notReady("update quota", key)
	}
	return updateQuota(n, n.store.Reader, key, attempts, fn)
}

func (n *Node) RecountQuota(key string, attempts int, recount Recount) (Usage, bool, error) {
	if n.store == nil {
		return Usage{}, false, n.notReady("recount quota", key)
	}
	return recountQuota(n, n.store.Reader, key, attempts, recount)
}

func (n *Node) PutTag(bucket, object, name, value string, p Precondition) (Version, error) {
	return putTag(n, bucket, object, name, value, p)
}

func (n *Node) PutACL(bucket, principal string, perm Permission, p Precondition) (Version, error) {
	return putACL(n, bucket, principal, perm, p)
}

func (n *Node) PutObjectMeta(bucket, object string, m ObjectMeta, p Precondition) (Version, error) {
	return putObjectMeta(n, bucket, object, m, p)
}

func (n *Node) PutManifest(bucket, object string, manifest []byte, p Precondition) (Version, error) {
	return putManifest(n, bucket, object, manifest, p)
}

func (n *Node) SetRoute(bucket string, region env.Region) (Version, error) {
	return setRoute(n, bucket, region)
}

func (n *Node) SetContentType(bucket, object, contentType string) (Version, error) {
	return setContentType(n, bucket, object, contentType)
}

func (n *Node) EncodeState() []byte {
	var e encoder
	if n.store == nil {
		e.byte(0)
	} else {
		e.byte(1)
		e.actor(n.store.actor)
		e.uvarint(n.store.counter)
		e.uvarint(n.store.discards)
		e.uvarint(uint64(n.store.ahead))
		e.bytes(n.store.doc.Encode())
	}
	s := n.stats
	for _, x := range []uint64{s.Rounds, s.Matches, s.Pulls, s.Pushes, s.Deltas, s.Malformed, s.NetErrors, s.DiskErrors, s.LoadErrors, s.Corrupt, s.Fresh} {
		e.uvarint(x)
	}
	return e.b
}

type Client struct {
	net     env.Transport
	timeout time.Duration
}

func NewClient(net env.Transport, timeout time.Duration) *Client {
	return &Client{net: net, timeout: timeout}
}

func (c *Client) call(node env.NodeID, o op, cb func(Result, error)) {
	c.net.Request(node, encodeOp(o), c.timeout, func(reply []byte, err error) {
		if err != nil {
			cb(Result{}, err)
			return
		}
		cb(decodeResult(reply))
	})
}

func (c *Client) Get(node env.NodeID, key string, cb func(Result, error)) {
	c.call(node, op{code: opGet, key: key}, cb)
}

func (c *Client) Put(node env.NodeID, key string, value []byte, p Precondition, cb func(Result, error)) {
	c.call(node, op{code: opPut, key: key, pre: p, value: value}, cb)
}

func (c *Client) Delete(node env.NodeID, key string, p Precondition, cb func(Result, error)) {
	c.call(node, op{code: opDelete, key: key, pre: p}, cb)
}

func digestEqual(b []byte, d [32]byte) bool { return bytes.Equal(b, d[:]) }
