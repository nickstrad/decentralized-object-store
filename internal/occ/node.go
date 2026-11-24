package occ

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nickstrad/object-store/internal/env"
)

// Membership supplies the peer list used by Node for anti-entropy sync and
// delta broadcasts. Peers need not be sorted or deduplicated.
type Membership interface {
	Peers() []env.NodeID
}

// StaticMembership is a fixed Membership backed by a slice of node IDs.
type StaticMembership []env.NodeID

// Peers returns a defensive copy of the configured peer IDs.
func (m StaticMembership) Peers() []env.NodeID { return slices.Clone(m) }

// Config configures a Node's peer view, anti-entropy cadence, delta fanout,
// and optional discard reporting hooks. Zero timing or fanout fields fall back
// to the Default* constants.
type Config struct {
	MetricsEnabled bool
	Membership     Membership
	Period         time.Duration
	Timeout        time.Duration
	Fanout         int
	MaxDelta       int
	Log            func(line string)
	OnDiscard      func(key string, winner, loser Version)
}

// Storage and replication defaults for Node:
//   - StateBlob: disk blob name holding the node's checksummed local state.
//   - DefaultPeriod: base interval between anti-entropy digest rounds (jittered ±50%).
//   - DefaultTimeout: request timeout for digest and push exchanges.
//   - DefaultFanout: number of random peers sent a delta datagram after a local write.
//   - DefaultMaxDelta: maximum encoded delta datagram size in bytes.
//   - LoadRetry: retry delay when reading StateBlob fails with a transient disk error.
const (
	StateBlob       = "occ/state"
	DefaultPeriod   = 500 * time.Millisecond
	DefaultTimeout  = 300 * time.Millisecond
	DefaultFanout   = 2
	DefaultMaxDelta = 60000
	LoadRetry       = 100 * time.Millisecond
)

// hazardReuseEpoch is a test-only switch that pins a freshly initialized node
// to epoch 1 to reproduce the wiped-node counter-reuse hazard.
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

// Stats holds diagnostic counters for a Node's replication and disk activity.
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

// Node is a single-threaded env.Process hosting a durable Store and epidemic
// push-pull anti-entropy replication. Local writes persist synchronously inside
// the store transaction and broadcast a single-key delta to Fanout peers.
// Node does not schedule quota recounts automatically; callers drive
// RecountQuota after syncing (for example, from Config.OnDiscard).
type Node struct {
	metric *metricState
	e      env.Env
	cfg    Config
	store  *Store
	stats  Stats
}

// NewFactory returns a process constructor suitable for sim.Sim that creates
// a Node configured with cfg for each node incarnation.
func NewFactory(cfg Config) func(env.Env) env.Process {
	cfg = cfg.withDefaults()
	return func(e env.Env) env.Process { return NewNode(e, cfg) }
}

// NewNode constructs an unstarted Node bound to environment e and cfg.
// Call Start before issuing reads, writes, or inbound messages.
func NewNode(e env.Env, cfg Config) *Node {
	return &Node{metric: newMetricState(cfg.MetricsEnabled), e: e, cfg: cfg.withDefaults()}
}

// Node lifecycle and diagnostic accessors:
//   - Ready: reports whether durable state has finished loading.
//   - Stats: returns a snapshot of the node's replication and disk counters.
func (n *Node) Ready() bool  { return n.store != nil }
func (n *Node) Stats() Stats { return n.stats }

// Digest returns the SHA-256 digest of the replicated document, or the zero
// hash before Ready is true.
func (n *Node) Digest() [32]byte {
	if n.store == nil {
		return [32]byte{}
	}
	return n.store.doc.Digest()
}

// Reader returns a read-only view of the node's store, or ErrNotReady before
// durable state has loaded.
func (n *Node) Reader() (Reader, error) {
	if n.store == nil {
		return Reader{}, fmt.Errorf("occ: read on %s: %w", n.e.ID, ErrNotReady)
	}
	return n.store.Reader, nil
}

// Record returns the Record at key, or the zero Record before Ready is true.
func (n *Node) Record(key string) Record {
	if n.store == nil {
		return Record{}
	}
	return n.store.Record(key)
}

// Doc returns a clone of the node's replicated Doc, or nil before Ready is true.
func (n *Node) Doc() *Doc {
	if n.store == nil {
		return nil
	}
	return n.store.Doc()
}

// Actor returns the node's current writer Actor, or the zero Actor before Ready.
func (n *Node) Actor() Actor {
	if n.store == nil {
		return Actor{}
	}
	return n.store.actor
}

// LWWDiscards returns the persisted saturating count of discard events
// observed by this node, or 0 before Ready is true.
func (n *Node) LWWDiscards() uint64 {
	if n.store == nil {
		return 0
	}
	return n.store.discards
}

// DiscardTail returns up to DiscardTailLen most recent discards recorded by
// this node, oldest first, or nil before Ready is true.
func (n *Node) DiscardTail() []Discard {
	if n.store == nil {
		return nil
	}
	return n.store.DiscardTail()
}

// AheadStamps returns the number of incoming or loaded stamps refused by the
// HLC for exceeding MaxClockLead, or 0 before Ready is true.
func (n *Node) AheadStamps() int {
	if n.store == nil {
		return 0
	}
	return n.store.ahead
}

// Start begins loading durable state from Disk and arms periodic anti-entropy
// once the store is open.
func (n *Node) Start() { n.load() }

// load reads StateBlob. Missing or corrupt blobs open a fresh store with a new
// actor epoch; transient disk read errors keep the node not-ready and retry
// after LoadRetry rather than guessing.
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
		n.open(persisted{}, true)
	case errors.Is(err, env.ErrNotFound):
		n.open(persisted{}, true)
	case errors.Is(err, env.ErrClosed):
	default:
		n.stats.LoadErrors++
		n.e.Clock.AfterFunc(LoadRetry, n.load)
	}
}

func (n *Node) open(st persisted, fresh bool) {
	cfg := StoreConfig{MetricsEnabled: n.cfg.MetricsEnabled,
		Node:      n.e.ID,
		Clock:     n.e.Clock,
		Rand:      n.e.Rand,
		Persist:   n.persist,
		Log:       n.cfg.Log,
		OnDiscard: n.cfg.OnDiscard,
	}
	if fresh {
		n.stats.Fresh++
		if hazardReuseEpoch {
			cfg.Epoch = 1
		}
		n.store = newStore(cfg)
		// Best-effort initial write: if it fails, the first mutation persists
		// the epoch with its data, and a restart without a blob safely draws a
		// new epoch.
		_ = n.persist(n.store.EncodeState())
	} else {
		n.store = openStore(cfg, st)
		n.store.replay()
	}
	n.arm()
}

func (n *Node) persist(blob []byte) error {
	if err := n.e.Disk.Write(StateBlob, blob); err != nil {
		n.stats.DiskErrors++
		n.metric.add("occ_replication_errors_total", 1, "disk")
		return fmt.Errorf("occ: persist %s: %w", StateBlob, err)
	}
	if err := n.e.Disk.Sync(StateBlob); err != nil {
		n.stats.DiskErrors++
		n.metric.add("occ_replication_errors_total", 1, "disk")
		return fmt.Errorf("occ: sync %s: %w", StateBlob, err)
	}
	return nil
}

// arm schedules the next anti-entropy round after Period jittered by ±50%.
func (n *Node) arm() {
	d := n.cfg.Period/2 + time.Duration(n.e.Rand.Int64N(int64(n.cfg.Period)))
	n.e.Clock.AfterFunc(d, n.round)
}

// peers returns all remote peers sorted and deduplicated so random peer choice
// does not depend on the order returned by Membership.
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
	outcome := "error"
	defer func() { n.metric.add("occ_replication_rounds_total", 1, outcome) }()
	if n.store == nil {
		return
	}
	if err != nil {
		n.stats.NetErrors++
		n.metric.add("occ_replication_errors_total", 1, "network")
		return
	}
	if len(reply) == 1 && reply[0] == replyMatch {
		n.stats.Matches++
		outcome = "ok"
		return
	}
	if len(reply) == 0 || reply[0] != replyState {
		n.stats.Malformed++
		n.metric.add("occ_replication_errors_total", 1, "malformed")
		return
	}
	state := reply[1:]
	doc, err := Decode(state)
	if err != nil {
		n.stats.Malformed++
		n.metric.add("occ_replication_errors_total", 1, "malformed")
		return
	}
	n.stats.Pulls++
	if _, err := n.store.Merge(doc); err != nil {
		return
	}
	outcome = "ok"
	// Push back only when our merged state still differs from what peer sent,
	// meaning we hold newer or additional versions peer has not seen.
	if n.Digest() != stateDigest(state) {
		n.stats.Pushes++
		n.e.Net.Request(peer, append([]byte{msgPush}, n.store.doc.Encode()...), n.cfg.Timeout, func(_ []byte, err error) {
			if err != nil {
				n.stats.NetErrors++
				n.metric.add("occ_replication_errors_total", 1, "network")
			}
		})
	}
}

// OnDatagram handles an inbound single-key delta datagram from peer from.
// Malformed datagrams are counted and dropped; failed persists roll back
// inside Store.Merge and are retried on the next anti-entropy round.
func (n *Node) OnDatagram(from env.NodeID, msg []byte) {
	if n.store == nil {
		return
	}
	if len(msg) == 0 || msg[0] != msgDelta {
		n.stats.Malformed++
		n.metric.add("occ_replication_errors_total", 1, "malformed")
		return
	}
	doc, err := Decode(msg[1:])
	if err != nil {
		n.stats.Malformed++
		n.metric.add("occ_replication_errors_total", 1, "malformed")
		return
	}
	n.stats.Deltas++
	// Persist failures roll back inside Merge and increment Stats.DiskErrors;
	// periodic anti-entropy will pull the missing update later.
	_, _ = n.store.Merge(doc)
}

// OnRequest handles inbound digest pulls, state pushes, and remote Client ops.
// Before Ready is true it replies with ErrNotReady.
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
			n.metric.add("occ_replication_errors_total", 1, "malformed")
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

// broadcast sends a single-key delta datagram to Fanout random peers when it
// fits within MaxDelta; larger updates propagate via periodic digest sync.
func (n *Node) broadcast(key string) {
	delta := append([]byte{msgDelta}, n.store.doc.Subset(key).Encode()...)
	if len(delta) > n.cfg.MaxDelta {
		return
	}
	ps := n.peers()
	for i := 0; i < n.cfg.Fanout && len(ps) > 0; i++ {
		if err := n.e.Net.Send(ps[n.e.Rand.IntN(len(ps))], delta); err != nil {
			n.stats.NetErrors++
			n.metric.add("occ_replication_errors_total", 1, "network")
		}
	}
}

func (n *Node) notReady(op, key string) error {
	return fmt.Errorf("occ: %s %q on %s: %w", op, key, n.e.ID, ErrNotReady)
}

// Get returns a copy of the live value at key and its ETag, or ErrNotReady /
// ErrNotFound.
func (n *Node) Get(key string) ([]byte, ETag, error) {
	if n.store == nil {
		return nil, ETag{}, n.notReady("get", key)
	}
	return n.store.Get(key)
}

// Put conditionally writes value at key, persists the state blob, and
// broadcasts a delta on success. It returns ErrNotReady if state is not loaded.
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

// Delete conditionally tombstones key, persists the state blob, and broadcasts
// a delta on success. It returns ErrNotReady if state is not loaded.
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

// SetScalar writes value unconditionally at key, persists, and broadcasts a delta.
func (n *Node) SetScalar(key string, value []byte) (Version, error) {
	return n.Put(key, value, Unconditional())
}

// UpdateQuota runs a bounded check-and-set update on quota key through Node.Put
// so successful updates persist and broadcast. Returns ErrNotReady before Ready.
func (n *Node) UpdateQuota(key string, attempts int, fn func(Usage) (Usage, error)) (Usage, error) {
	if n.store == nil {
		return Usage{}, n.notReady("update quota", key)
	}
	return updateQuota(n, n.store.Reader, key, attempts, fn)
}

// RecountQuota recomputes quota key via recount and conditionally writes it
// through Node.Put only if the stored value differs or is malformed.
func (n *Node) RecountQuota(key string, attempts int, recount Recount) (Usage, bool, error) {
	if n.store == nil {
		return Usage{}, false, n.notReady("recount quota", key)
	}
	return recountQuota(n, n.store.Reader, key, attempts, recount)
}

// PutTag conditionally writes an object tag, persisting and broadcasting a delta.
func (n *Node) PutTag(bucket, object, name, value string, p Precondition) (Version, error) {
	return putTag(n, bucket, object, name, value, p)
}

// PutACL conditionally writes a bucket ACL entry, persisting and broadcasting.
func (n *Node) PutACL(bucket, principal string, perm Permission, p Precondition) (Version, error) {
	return putACL(n, bucket, principal, perm, p)
}

// PutObjectMeta conditionally writes an object's ObjectMeta, persisting and broadcasting.
func (n *Node) PutObjectMeta(bucket, object string, m ObjectMeta, p Precondition) (Version, error) {
	return putObjectMeta(n, bucket, object, m, p)
}

// PutManifest conditionally writes an object's manifest bytes, persisting and broadcasting.
func (n *Node) PutManifest(bucket, object string, manifest []byte, p Precondition) (Version, error) {
	return putManifest(n, bucket, object, manifest, p)
}

// SetRoute writes a bucket's routing region unconditionally, persisting and broadcasting.
func (n *Node) SetRoute(bucket string, region env.Region) (Version, error) {
	return setRoute(n, bucket, region)
}

// SetContentType writes an object's content type unconditionally, persisting and broadcasting.
func (n *Node) SetContentType(bucket, object, contentType string) (Version, error) {
	return setContentType(n, bucket, object, contentType)
}

// EncodeState returns a canonical byte representation of the node's volatile
// state and counters for deterministic simulation state-digest comparison.
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

// Client issues remote Get, Put, and Delete requests to a Node over env.Transport.
type Client struct {
	net     env.Transport
	timeout time.Duration
}

// NewClient constructs a Client using net with per-request timeout.
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

// Get sends a remote read for key to node and passes the decoded Result or
// transport/remote error to cb.
func (c *Client) Get(node env.NodeID, key string, cb func(Result, error)) {
	c.call(node, op{code: opGet, key: key}, cb)
}

// Put sends a conditional write for (key, value, p) to node and passes the
// decoded Result or error to cb.
func (c *Client) Put(node env.NodeID, key string, value []byte, p Precondition, cb func(Result, error)) {
	c.call(node, op{code: opPut, key: key, pre: p, value: value}, cb)
}

// Delete sends a conditional delete for (key, p) to node and passes the
// decoded Result or error to cb.
func (c *Client) Delete(node env.NodeID, key string, p Precondition, cb func(Result, error)) {
	c.call(node, op{code: opDelete, key: key, pre: p}, cb)
}

func digestEqual(b []byte, d [32]byte) bool { return bytes.Equal(b, d[:]) }
