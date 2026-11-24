// Package node implements the single-threaded protocol core of one object-store
// and metadata-sync node over internal/env.
//
// It wires SWIM gossip membership, Reed-Solomon striped shard storage, OCC
// last-writer-wins metadata replication, Merkle-tree anti-entropy (one tree
// over all metadata keys plus local per-bucket shard inventories), and
// compensating bucket-migration sagas into one env.Process that runs unchanged
// inside the simulator and inside internal/noderuntime.
//
// Concurrent writes settle by last-writer-wins (never kept side by side); every
// discard is counted and logged. Every node holds all metadata, tombstones and
// system markers are retained indefinitely, and callers must serialize all calls
// onto the node's single event loop.
package node

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/gossip"
	"github.com/nickstrad/object-store/internal/merkle"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/saga"
	"github.com/nickstrad/object-store/internal/storage"
)

// Default cadences, grace windows, size caps, and concurrency bounds for Node.
const (
	DefaultOCCPeriod      = 3 * time.Second
	DefaultMetaPeriod     = time.Second
	DefaultMetaDepth      = 3
	DefaultMaintainPeriod = 500 * time.Millisecond
	DefaultScrubBatch     = 64
	DefaultInventoryScrub = 16
	DefaultGCGrace        = 10 * time.Minute
	DefaultOrphanGrace    = time.Hour
	DefaultRecountSettle  = 3 * time.Second
	DefaultMaxObjectSize  = 32 << 20
	DefaultRPCTimeout     = time.Second
	DefaultViewSettle     = 5 * time.Second
	MaxObjectSizeLimit    = 1 << 30
	MaxRebuilds           = 8
	QuotaAttempts         = 5
	CollectedLogLen       = 4096
)

// Config configures a Node. Zero durations, counts, Layout, and SagaPolicy fall
// back to defaults via WithDefaults. Layout applies to new writes coordinated
// by this node; reads honor each manifest's recorded layout.
type Config struct {
	MetricsEnabled bool
	Seeds          []env.NodeID
	Layout         storage.Layout
	AckQuorum      int
	RequestTimeout time.Duration
	Gossip         gossip.Config
	OCCPeriod      time.Duration
	OCCFanout      int
	MetaPeriod     time.Duration
	MetaDepth      int
	MaintainPeriod time.Duration
	ScrubBatch     int
	InventoryScrub int
	GCGrace        time.Duration
	OrphanGrace    time.Duration
	RecountSettle  time.Duration
	MaxObjectSize  int64
	RPCTimeout     time.Duration
	ViewSettle     time.Duration
	SagaPolicy     saga.Policy
	WrapMigration  func(saga.Definition) saga.Definition
	ConfigureSaga  func(*saga.Config)
	Log            func(string)
	DiscardSink    func(key string, winner, loser occ.Version)
	CommitSink     func(key string, held occ.Record, written occ.Version)
}

// WithDefaults returns a copy of c with zero-valued fields replaced by package
// defaults.
func (c Config) WithDefaults() Config {
	if c.Layout == (storage.Layout{}) {
		c.Layout = storage.DefaultLayout()
	}
	if c.RequestTimeout == 0 {
		c.RequestTimeout = storage.DefaultRequestTimeout
	}
	if c.OCCPeriod == 0 {
		c.OCCPeriod = DefaultOCCPeriod
	}
	if c.MetaPeriod == 0 {
		c.MetaPeriod = DefaultMetaPeriod
	}
	if c.MetaDepth == 0 {
		c.MetaDepth = DefaultMetaDepth
	}
	if c.MaintainPeriod == 0 {
		c.MaintainPeriod = DefaultMaintainPeriod
	}
	if c.ScrubBatch == 0 {
		c.ScrubBatch = DefaultScrubBatch
	}
	if c.InventoryScrub == 0 {
		c.InventoryScrub = DefaultInventoryScrub
	}
	if c.GCGrace == 0 {
		c.GCGrace = DefaultGCGrace
	}
	if c.OrphanGrace == 0 {
		c.OrphanGrace = DefaultOrphanGrace
	}
	if c.RecountSettle == 0 {
		c.RecountSettle = DefaultRecountSettle
	}
	if c.MaxObjectSize == 0 {
		c.MaxObjectSize = DefaultMaxObjectSize
	}
	if c.RPCTimeout == 0 {
		c.RPCTimeout = DefaultRPCTimeout
	}
	if c.ViewSettle == 0 {
		c.ViewSettle = DefaultViewSettle
	}
	if c.SagaPolicy == (saga.Policy{}) {
		c.SagaPolicy = saga.DefaultPolicy()
	}
	return c
}

// Validate checks c's erasure layout (including that a shard RPC fits MaxFrame),
// durations, Merkle depth, scrub counts, object size cap, ack quorum, saga
// policy, and gossip configuration, returning ErrConfig on failure.
func (c Config) Validate() error {
	c = c.WithDefaults()
	if err := c.Layout.Validate(); err != nil {
		return fmt.Errorf("node: config: layout: %w: %w", err, ErrConfig)
	}
	if ShardRequestSize(c.Layout) > MaxFrame {
		return fmt.Errorf("node: config: shard request of %d bytes over the %d-byte frame: %w", ShardRequestSize(c.Layout), MaxFrame, ErrConfig)
	}
	for _, d := range []time.Duration{c.RequestTimeout, c.OCCPeriod, c.MetaPeriod, c.MaintainPeriod, c.GCGrace, c.OrphanGrace, c.RecountSettle, c.RPCTimeout, c.ViewSettle} {
		if d < 0 {
			return fmt.Errorf("node: config: negative duration %v: %w", d, ErrConfig)
		}
	}
	if c.MetaDepth < merkle.MinDepth || c.MetaDepth > merkle.MaxDepth {
		return fmt.Errorf("node: config: merkle depth %d: %w", c.MetaDepth, ErrConfig)
	}
	if c.ScrubBatch < 0 || c.InventoryScrub < 0 || c.OCCFanout < 0 {
		return fmt.Errorf("node: config: scrub batch %d, inventory scrub %d or occ fanout %d: %w", c.ScrubBatch, c.InventoryScrub, c.OCCFanout, ErrConfig)
	}
	if c.MaxObjectSize < 0 || c.MaxObjectSize > MaxObjectSizeLimit {
		return fmt.Errorf("node: config: max object size %d outside [0, %d]: %w", c.MaxObjectSize, MaxObjectSizeLimit, ErrConfig)
	}
	if c.AckQuorum != 0 && (c.AckQuorum < c.Layout.K || c.AckQuorum > c.Layout.K+c.Layout.M) {
		return fmt.Errorf("node: config: ack quorum %d: %w", c.AckQuorum, ErrConfig)
	}
	if err := c.SagaPolicy.Validate(); err != nil {
		return fmt.Errorf("node: config: saga policy: %w: %w", err, ErrConfig)
	}
	g := c.Gossip
	g.Seeds = c.Seeds
	if err := g.WithDefaults().Validate(); err != nil {
		return fmt.Errorf("node: config: gossip: %w: %w", err, ErrConfig)
	}
	return nil
}

// ShardRequestSize returns an upper bound in bytes for a channel-prefixed
// OpPutShard RPC carrying one shard under layout l.
func ShardRequestSize(l storage.Layout) int {
	shard := (l.StripeSize + l.K - 1) / max(l.K, 1)
	return 1 + 1 + 10 + 512 + storage.HeaderSize + shard
}

// Stats holds monotonic per-node operational, repair, GC, discard, and
// loss-declaration counters.
type Stats struct {
	Malformed       uint64
	Stopped         uint64
	Puts            uint64
	PutFailures     uint64
	Gets            uint64
	GetFailures     uint64
	Deletes         uint64
	Discards        uint64
	DiscardedObject uint64
	QuotaDiscards   uint64
	Recounts        uint64
	RecountWrites   uint64
	RecountErrors   uint64
	UsageErrors     uint64
	MetaStale       uint64
	MetaMerged      uint64
	Scrubbed        uint64
	ScrubCorrupt    uint64
	Rebuilds        uint64
	RebuildFailures uint64
	RebuiltBytes    uint64
	Collected       uint64
	CollectedLoser  uint64
	Purged          uint64
	InventoryStart  uint64
	InventoryStop   uint64
	SagasEnded      uint64
	MemberDeaths    uint64
	GCMarkers       uint64
	OrphanRecords   uint64
	AbsentRecords   uint64
	AbsentRefused   uint64
	LostDeclared    uint64
	PurgeRefused    uint64
	QuotaAudits     uint64
}

type collectRecord struct {
	Bucket  string
	Version storage.VersionID
	At      env.Time
	Loser   bool
}

// Node is the single-threaded env.Process combining gossip, storage, OCC
// metadata, Merkle anti-entropy, shard inventories, and bucket migration sagas.
type Node struct {
	metric *metricState
	e      env.Env
	cfg    Config
	net    chanNet
	view   *gossipView
	gossip *gossip.Node
	store  *storage.Node
	scoped map[env.Region]*storage.Node
	occ    *occ.Node
	meta   *merkle.Worker
	engine *saga.Engine
	mig    *migrator

	started     bool
	viewChanged env.Time
	stopped     bool
	maintTimer  env.Timer
	inv         map[string]*inventory
	absent      map[storage.VersionID]map[[2]int]map[env.NodeID]bool
	liveRefs    []liveRef
	losers      map[storage.VersionID]bool
	purged      map[storage.VersionID]bool
	recountDue  map[string]env.Time
	audits      map[string]quotaAudit
	auditCursor string
	rebuilding  int
	cache       map[occ.ETag]objectRecord
	collected   []collectRecord
	stats       Stats
}

type failed struct{ err error }

func (f *failed) Start()                                        {}
func (f *failed) OnDatagram(env.NodeID, []byte)                 {}
func (f *failed) OnRequest(_ env.NodeID, _ []byte, r env.Reply) { r(nil, f.err) }
func (f *failed) EncodeState() []byte                           { return []byte(f.err.Error()) }
func (f *failed) Err() error                                    { return f.err }

// NewFactory returns a simulator Process factory that constructs a Node with
// cfg. If construction fails, the returned placeholder Process answers requests
// with the construction error.
func NewFactory(cfg Config) func(env.Env) env.Process {
	return func(e env.Env) env.Process {
		n, err := New(e, cfg)
		if err != nil {
			return &failed{err: err}
		}
		return n
	}
}

// New validates cfg and e and constructs an unstarted Node wiring all protocol
// subsystems onto one channel-multiplexed env.Transport.
func New(e env.Env, cfg Config) (*Node, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if e.Clock == nil || e.Net == nil || e.Disk == nil || e.Rand == nil || e.ID == "" {
		return nil, fmt.Errorf("node: new %q: incomplete environment: %w", e.ID, ErrConfig)
	}
	cfg = cfg.WithDefaults()
	n := &Node{
		metric: newMetricState(cfg.MetricsEnabled), e: e, cfg: cfg, net: chanNet{net: e.Net, ch: chNode},
		scoped: map[env.Region]*storage.Node{}, inv: map[string]*inventory{},
		absent: map[storage.VersionID]map[[2]int]map[env.NodeID]bool{}, losers: map[storage.VersionID]bool{}, purged: map[storage.VersionID]bool{},
		recountDue: map[string]env.Time{}, audits: map[string]quotaAudit{}, cache: map[occ.ETag]objectRecord{},
	}
	n.view = &gossipView{self: e.ID}
	gcfg := cfg.Gossip
	gcfg.Seeds = cfg.Seeds
	gcfg.MetricsEnabled = cfg.MetricsEnabled
	g, err := gossip.New(e.WithNet(chanNet{net: e.Net, ch: chGossip}), gcfg)
	if err != nil {
		return nil, fmt.Errorf("node: new %s: gossip: %w", e.ID, err)
	}
	n.gossip = g
	n.view.g = g
	g.Subscribe(n.onMember)
	st, err := storage.NewNode(e.WithNet(chanNet{net: e.Net, ch: chStorage}), storage.Config{MetricsEnabled: cfg.MetricsEnabled,
		Layout: cfg.Layout, Members: n.view, AckQuorum: cfg.AckQuorum, RequestTimeout: cfg.RequestTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("node: new %s: storage: %w", e.ID, err)
	}
	n.store = st
	n.occ = occ.NewNode(e.WithNet(chanNet{net: e.Net, ch: chOCC}), occ.Config{MetricsEnabled: cfg.MetricsEnabled,
		Membership: n.view, Period: cfg.OCCPeriod, Fanout: cfg.OCCFanout, Log: cfg.Log, OnDiscard: n.onDiscard,
	})
	w, err := merkle.NewWorker(e.WithNet(chanNet{net: e.Net, ch: chMeta}), merkle.Config{MetricsEnabled: cfg.MetricsEnabled,
		Depth: cfg.MetaDepth, Period: cfg.MetaPeriod, ScrubBatch: cfg.ScrubBatch,
	}, n.view, &metaRepairer{n: n})
	if err != nil {
		return nil, fmt.Errorf("node: new %s: merkle: %w", e.ID, err)
	}
	n.meta = w
	n.mig = newMigrator(n)
	def := saga.Migration(n.mig)
	if cfg.WrapMigration != nil {
		def = cfg.WrapMigration(def)
	}
	scfg := saga.Config{Policy: cfg.SagaPolicy, OnEnd: n.onSagaEnd}
	if cfg.ConfigureSaga != nil {
		cfg.ConfigureSaga(&scfg)
	}
	scfg.MetricsEnabled = cfg.MetricsEnabled
	eng, err := saga.New(e, scfg, def)
	if err != nil {
		return nil, fmt.Errorf("node: new %s: saga: %w", e.ID, err)
	}
	n.engine = eng
	return n, nil
}

// Subsystem, identity, lifecycle, and inspection accessors on Node:
//   - ID, Region: node identity and placement zone from env.Env.
//   - Gossip, OCC, Storage, Meta, Engine: underlying protocol subsystem handles.
//   - Stats: snapshot of monotonic node counters.
//   - Ready, Stopped: lifecycle readiness flags.
//   - Collected: copy of the bounded in-memory collected-shard ring buffer.
//   - Inventories: count of active per-bucket shard inventories.
func (n *Node) ID() env.NodeID             { return n.e.ID }
func (n *Node) Region() env.Region         { return n.e.Region }
func (n *Node) Gossip() *gossip.Node       { return n.gossip }
func (n *Node) OCC() *occ.Node             { return n.occ }
func (n *Node) Storage() *storage.Node     { return n.store }
func (n *Node) Meta() *merkle.Worker       { return n.meta }
func (n *Node) Engine() *saga.Engine       { return n.engine }
func (n *Node) Stats() Stats               { return n.stats }
func (n *Node) Ready() bool                { return n.started && !n.stopped }
func (n *Node) Stopped() bool              { return n.stopped }
func (n *Node) Collected() []collectRecord { return append([]collectRecord(nil), n.collected...) }
func (n *Node) Inventories() int           { return len(n.inv) }
func (n *Node) later(fn func())            { n.e.Clock.AfterFunc(0, fn) }
func (n *Node) logf(f string, a ...any) {
	if n.cfg.Log != nil {
		n.cfg.Log(fmt.Sprintf("node %s: ", n.e.ID) + fmt.Sprintf(f, a...))
	}
}

// Start initializes OCC metadata, SWIM gossip, metadata Merkle anti-entropy,
// saga recovery, and the periodic maintenance timer.
func (n *Node) Start() {
	n.viewChanged = n.e.Clock.Now()
	n.occ.Start()
	n.gossip.Start()
	n.ensureStarted()
	n.maintTimer = n.e.Clock.AfterFunc(n.cfg.MaintainPeriod, n.maintain)
}

func (n *Node) ensureStarted() {
	if n.started || n.stopped || !n.occ.Ready() {
		return
	}
	if err := n.meta.Start(); err != nil {
		n.logf("metadata anti-entropy start: %v", err)
		return
	}
	n.started = true
	if err := n.engine.Recover(); err != nil {
		n.logf("saga recovery: %v", err)
	}
}

// Stop marks the node stopped, announces gossip leave, halts metadata
// anti-entropy, drops all bucket inventories, and cancels the maintenance timer.
func (n *Node) Stop() {
	if n.stopped {
		return
	}
	n.stopped = true
	if err := n.gossip.Leave(); err != nil {
		n.logf("leave: %v", err)
	}
	n.meta.Stop()
	for _, b := range sortedKeys(n.inv) {
		delete(n.inv, b)
		n.stats.InventoryStop++
	}
	if n.maintTimer != nil {
		n.maintTimer.Stop()
	}
}

// RunningWorkers returns the number of active background workers (one for
// metadata anti-entropy while running, plus one per active bucket inventory).
func (n *Node) RunningWorkers() int {
	c := len(n.inv)
	if n.started && !n.stopped {
		c++
	}
	return c
}

// OnDatagram demultiplexes an inbound datagram by its leading channel byte to
// gossip, OCC, or storage, dropping traffic after Stop or with unknown channels.
func (n *Node) OnDatagram(from env.NodeID, msg []byte) {
	if n.stopped {
		n.stats.Stopped++
		return
	}
	if len(msg) == 0 {
		n.stats.Malformed++
		return
	}
	body := msg[1:]
	switch msg[0] {
	case chGossip:
		n.gossip.OnDatagram(from, body)
	case chOCC:
		n.occ.OnDatagram(from, body)
	case chStorage:
		n.store.OnDatagram(from, body)
	default:
		n.stats.Malformed++
	}
}

// OnRequest demultiplexes an inbound request by its leading channel byte to
// gossip, storage (via serveStorage), OCC, Merkle anti-entropy, or node RPCs,
// replying with ErrStopped after Stop or ErrMalformed on bad headers.
func (n *Node) OnRequest(from env.NodeID, req []byte, reply env.Reply) {
	if n.stopped {
		n.stats.Stopped++
		reply(nil, fmt.Errorf("node %s: request from %s: %w", n.e.ID, from, ErrStopped))
		return
	}
	if len(req) == 0 {
		n.stats.Malformed++
		reply(nil, fmt.Errorf("node %s: empty request from %s: %w", n.e.ID, from, ErrMalformed))
		return
	}
	body := req[1:]
	switch req[0] {
	case chGossip:
		n.gossip.OnRequest(from, body, reply)
	case chStorage:
		n.serveStorage(from, body, reply)
	case chOCC:
		n.occ.OnRequest(from, body, reply)
	case chMeta:
		n.meta.OnRequest(from, body, reply)
	case chNode:
		n.serveNode(from, body, reply)
	default:
		n.stats.Malformed++
		reply(nil, fmt.Errorf("node %s: channel %d from %s: %w", n.e.ID, req[0], from, ErrMalformed))
	}
}

// ViewSettled reports whether at least one live peer is known and no join event
// has occurred within Config.ViewSettle. Migrations require a settled view
// before pointer agreement and shard purges.
func (n *Node) ViewSettled() bool {
	return n.e.Clock.Now().Sub(n.viewChanged) >= n.cfg.ViewSettle && len(n.view.Peers()) > 0
}

// EncodeState returns a deterministic byte encoding of all subsystem volatile
// states and node counters for simulator replay digest checks.
func (n *Node) EncodeState() []byte {
	var w wbuf
	w.bytes(n.gossip.EncodeState())
	w.bytes(n.store.EncodeState())
	w.bytes(n.occ.EncodeState())
	w.bytes(n.meta.EncodeState())
	w.bytes(n.engine.EncodeState())
	s := n.stats
	for _, v := range []uint64{s.Malformed, s.Stopped, s.Puts, s.PutFailures, s.Gets, s.GetFailures, s.Deletes,
		s.Discards, s.DiscardedObject, s.QuotaDiscards, s.Recounts, s.RecountWrites, s.RecountErrors, s.UsageErrors,
		s.MetaStale, s.MetaMerged, s.Scrubbed, s.ScrubCorrupt, s.Rebuilds, s.RebuildFailures, s.RebuiltBytes,
		s.Collected, s.CollectedLoser, s.Purged, s.InventoryStart, s.InventoryStop, s.SagasEnded, s.MemberDeaths, s.GCMarkers,
		s.OrphanRecords, s.LostDeclared, s.PurgeRefused, s.QuotaAudits, s.AbsentRecords, s.AbsentRefused} {
		w.uvarint(v)
	}
	if n.started {
		w.byte(1)
	} else {
		w.byte(0)
	}
	return w.b
}

func (n *Node) onMember(ev gossip.Event) {
	if ev.Kind == gossip.EventJoined {
		n.viewChanged = n.e.Clock.Now()
	}
	if ev.Kind == gossip.EventDied || ev.Kind == gossip.EventLeft {
		n.stats.MemberDeaths++
	}
}

func (n *Node) onSagaEnd(st saga.Status) {
	n.stats.SagasEnded++
	outcome := "ok"
	if st.State != saga.StateCompleted {
		outcome = "error"
	}
	n.metric.add("node_migrations_ended_total", 1, outcome)
	n.logf("saga %s ended %v", st.ID, st.State)
}

func (n *Node) reader() (occ.Reader, error) {
	if n.stopped {
		return occ.Reader{}, fmt.Errorf("node %s: %w", n.e.ID, ErrStopped)
	}
	if !n.started {
		return occ.Reader{}, fmt.Errorf("node %s: %w", n.e.ID, ErrNotReady)
	}
	return n.occ.Reader()
}

func (n *Node) scope(region string) *storage.Node {
	if region == "" {
		return n.store
	}
	r := env.Region(region)
	if s, ok := n.scoped[r]; ok {
		return s
	}
	s, err := storage.NewNodeSharingMetrics(n.e.WithNet(chanNet{net: n.e.Net, ch: chStorage}), storage.Config{MetricsEnabled: n.cfg.MetricsEnabled,
		Layout: n.cfg.Layout, Members: regionView{v: n.view, region: r}, AckQuorum: n.cfg.AckQuorum, RequestTimeout: n.cfg.RequestTimeout,
	}, n.store)
	if err != nil {
		return n.store
	}
	n.scoped[r] = s
	return s
}

func (n *Node) maintain() {
	if n.stopped {
		return
	}
	n.maintTimer = n.e.Clock.AfterFunc(n.cfg.MaintainPeriod, n.maintain)
	n.ensureStarted()
	if !n.started {
		return
	}
	rd, err := n.occ.Reader()
	if err != nil {
		return
	}
	n.runRecounts()
	n.auditQuotas(rd)
	n.runInventories(rd)
}

// AuditBatch bounds how many buckets auditQuotas inspects per maintenance tick.
const AuditBatch = 4

type quotaAudit struct {
	state [32]byte
	since env.Time
}

func (n *Node) buckets(rd occ.Reader) []string {
	var out []string
	for _, k := range rd.Keys() {
		if len(k) == 0 || occ.Kind(k[0]) != occ.KindTag || !rd.Record(k).Live() {
			continue
		}
		if _, parts, err := occ.ParseKey(k); err == nil && parts[1] == "" && parts[2] == tagBucket {
			out = append(out, parts[0])
		}
	}
	return out
}

// auditQuotas checks up to AuditBatch buckets per tick to repair quota drift
// that outlives discard-triggered recounts (such as a partition-era recount
// that became causally newer than earlier quota writes). Once stored usage
// disagrees with live manifests and neither the quota version nor the bucket's
// manifest digest has changed for RecountSettle, the lowest-ID live node writes
// a fresh recount.
func (n *Node) auditQuotas(rd occ.Reader) {
	bs := n.buckets(rd)
	if len(bs) == 0 || canaryNoQuotaAudit {
		return
	}
	i, _ := slices.BinarySearch(bs, n.auditCursor)
	now := n.e.Clock.Now()
	for c := range min(AuditBatch, len(bs)) {
		b := bs[(i+c)%len(bs)]
		n.auditCursor = b + "\x00"
		stored, _, err := rd.ReadQuota(occ.QuotaKey(b))
		if err != nil && !isNotFound(err) {
			continue
		}
		var truth occ.Usage
		for _, o := range n.liveObjects(rd, b) {
			truth.Bytes += uint64(o.rec.Manifest.Size)
			truth.Objects++
		}
		if stored == truth {
			delete(n.audits, b)
			continue
		}
		state := n.pointerView(rd, b).digest
		qv, _ := rd.Record(occ.QuotaKey(b)).Version()
		qid := qv.ID()
		for j := range state {
			state[j] ^= qid[j]
		}
		a, ok := n.audits[b]
		if !ok || a.state != state {
			n.audits[b] = quotaAudit{state: state, since: now}
			continue
		}
		if now.Sub(a.since) < n.cfg.RecountSettle || !n.designated() {
			continue
		}
		n.stats.QuotaAudits++
		delete(n.audits, b)
		_, wrote, err := n.occ.RecountQuota(occ.QuotaKey(b), QuotaAttempts, n.recount)
		if err != nil {
			n.stats.RecountErrors++
			continue
		}
		if wrote {
			n.stats.RecountWrites++
			n.RecordMeta(occ.QuotaKey(b))
		}
	}
}

// onDiscard receives every last-writer-wins discard from OCC, counts it,
// forwards it to DiscardSink, and defers handleDiscard via n.later so it never
// re-enters OCC mid-merge.
func (n *Node) onDiscard(key string, winner, loser occ.Version) {
	n.stats.Discards++
	if n.cfg.DiscardSink != nil {
		n.cfg.DiscardSink(key, winner, loser)
	}
	n.later(func() { n.handleDiscard(key, loser) })
}

func (n *Node) committed(key string, held occ.Record, v occ.Version) {
	if n.cfg.CommitSink != nil {
		n.cfg.CommitSink(key, held, v)
	}
}

// handleDiscard schedules a settled quota recount on quota or manifest discards
// (including tombstone losers) and marks any discarded live manifest's storage
// version dead so its shards are collected after GCGrace. Pending recounts and
// losers are in-memory; the ~gc/ marker is durable and auditQuotas repairs any
// dropped recount after a crash.
func (n *Node) handleDiscard(key string, loser occ.Version) {
	kind, parts, err := occ.ParseKey(key)
	if err != nil {
		return
	}
	switch kind {
	case occ.KindQuota:
		n.stats.QuotaDiscards++
		if !canaryNoDiscardRecount {
			n.recountDue[parts[0]] = n.e.Clock.Now().Add(n.cfg.RecountSettle)
		}
	case occ.KindManifest:
		if !canaryNoDiscardRecount {
			n.recountDue[parts[0]] = n.e.Clock.Now().Add(n.cfg.RecountSettle)
		}
		if loser.Tombstone() {
			return
		}
		rec, err := decodeObject(loser.Value())
		if err != nil {
			return
		}
		n.stats.DiscardedObject++
		n.losers[rec.Manifest.Version] = true
		n.markDead(rec.Manifest.Bucket, rec.Manifest.Version)
	}
}

func (n *Node) runRecounts() {
	now := n.e.Clock.Now()
	for _, b := range sortedKeys(n.recountDue) {
		if n.recountDue[b] > now {
			continue
		}
		delete(n.recountDue, b)
		n.stats.Recounts++
		_, wrote, err := n.occ.RecountQuota(occ.QuotaKey(b), QuotaAttempts, n.recount)
		if err != nil {
			n.stats.RecountErrors++
			n.recountDue[b] = now.Add(n.cfg.RecountSettle)
			continue
		}
		if wrote {
			n.stats.RecountWrites++
		}
	}
}

func (n *Node) recount(key string) (occ.Usage, error) {
	kind, parts, err := occ.ParseKey(key)
	if err != nil || kind != occ.KindQuota {
		return occ.Usage{}, fmt.Errorf("node: recount %q: %w", key, ErrMalformed)
	}
	rd, err := n.occ.Reader()
	if err != nil {
		return occ.Usage{}, err
	}
	var u occ.Usage
	for _, o := range n.liveObjects(rd, parts[0]) {
		u.Bytes += uint64(o.rec.Manifest.Size)
		u.Objects++
	}
	return u, nil
}

func (n *Node) usage(bucket string, dBytes, dObjects int64) {
	if dBytes == 0 && dObjects == 0 {
		return
	}
	if _, err := n.occ.UpdateQuota(occ.QuotaKey(bucket), QuotaAttempts, occ.AddUsage(dBytes, dObjects, occ.Usage{})); err != nil {
		n.stats.UsageErrors++
		n.recountDue[bucket] = n.e.Clock.Now().Add(n.cfg.RecountSettle)
	}
}

func gcKey(bucket string, v storage.VersionID) string {
	return bucketKey(bucket, tagGC+v.String())
}

func (n *Node) markDead(bucket string, v storage.VersionID) {
	rd, err := n.occ.Reader()
	if err != nil {
		return
	}
	k := gcKey(bucket, v)
	if rd.Record(k).Live() {
		return
	}
	if _, err := n.occ.SetScalar(k, []byte{1}); err != nil {
		n.logf("gc marker %s: %v", k, err)
		return
	}
	n.stats.GCMarkers++
	n.RecordMeta(k)
}

// RecordMeta updates the metadata Merkle tree entry for key from its current
// encoded OCC record.
func (n *Node) RecordMeta(key string) {
	rd, err := n.occ.Reader()
	if err != nil {
		return
	}
	if b, err := encodeRecord(rd, key); err == nil {
		if err := n.meta.Record(key, merkle.Sum(b)); err != nil {
			n.logf("record %q: %v", key, err)
		}
	}
}

func encodeRecord(rd occ.Reader, key string) ([]byte, error) {
	rec := rd.Record(key)
	v, ok := rec.Version()
	if !ok {
		return nil, fmt.Errorf("node: record %q: %w", key, env.ErrNotFound)
	}
	d := occ.NewDoc()
	d.MergeVersion(key, v)
	return d.Encode(), nil
}

func isNotFound(err error) bool {
	return errors.Is(err, occ.ErrNotFound) || errors.Is(err, env.ErrNotFound) || errors.Is(err, storage.ErrNotFound)
}
