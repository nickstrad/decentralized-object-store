package merkle

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nickstrad/object-store/internal/env"
)

// Worker defaults used when the corresponding Config fields are zero:
//   - DefaultPeriod: mean interval between periodic scrub-and-diff ticks.
//   - DefaultTimeout: per-request transport timeout for diff exchanges.
//   - DefaultScrubBatch: keys re-read and re-hashed per tick.
//   - DefaultMaxRepairs: maximum keys fetched in one repair round.
//   - DeadlinePeriods: multiplier on Period for the default RoundDeadline.
const (
	DefaultPeriod     = time.Second
	DefaultTimeout    = 500 * time.Millisecond
	DefaultScrubBatch = 64
	DefaultMaxRepairs = 256
	DeadlinePeriods   = 10
)

// Membership supplies the candidate peer IDs for anti-entropy rounds.
type Membership interface {
	// Peers returns the current peer list; duplicates and the local node ID
	// are filtered out by Worker.
	Peers() []env.NodeID
}

// Repairer bridges Worker to the underlying local store and peer fetch path.
// Valid defines truth: Worker only fetches keys whose advertised remote hash
// passes Valid and only stores fetched bytes whose SHA-256 passes Valid.
type Repairer interface {
	// Keys lists all keys currently present in the local store.
	Keys() ([]string, error)
	// Read returns the raw bytes currently stored for key, or env.ErrNotFound
	// if absent.
	Read(key string) ([]byte, error)
	// Valid reports whether sum is an acceptable SHA-256 digest for key.
	Valid(key string, sum Hash) bool
	// Fetch retrieves key's bytes from peer and invokes done with the payload
	// or error.
	Fetch(peer env.NodeID, key string, done func(data []byte, err error))
	// Put durably stores validated bytes for key locally.
	Put(key string, data []byte) error
}

// Config configures a Worker. Depth is required; zero values for Period,
// Timeout, RoundDeadline, ScrubBatch, and MaxRepairs select their defaults.
type Config struct {
	MetricsEnabled bool
	Depth          int
	Period         time.Duration
	Timeout        time.Duration
	RoundDeadline  time.Duration
	ScrubBatch     int
	MaxRepairs     int
	Limits         Limits
}

// Stats holds cumulative counters and in-flight status for a Worker.
// Rounds always equals Completed + Failed + (1 if InFlight else 0).
type Stats struct {
	Rounds        uint64
	Completed     uint64
	Failed        uint64
	InSync        uint64
	Truncated     uint64
	Repaired      uint64
	RepairedBytes uint64
	Rejected      uint64
	FetchFailed   uint64
	PutFailed     uint64
	Invalid       uint64
	Deferred      uint64
	Scrubbed      uint64
	Changed       uint64
	Dropped       uint64
	ReadErrors    uint64
	Skipped       uint64
	RoundTrips    uint64
	Hashes        uint64
	DiffBytes     uint64
	MaxRoundTrips uint64
	Answered      uint64
	InFlight      bool
}

// Worker runs periodic local scrubbing and pull-only Merkle anti-entropy
// rounds over an env.Env. Its tree is kept in memory only and reflects the
// actual SHA-256 of local bytes. A failed round is abandoned whole and the
// next tick is the retry. Worker is single-threaded and driven by the node's
// event loop.
type Worker struct {
	metric        *metricState
	roundRepaired bool
	roundEqual    bool
	e             env.Env
	cfg           Config
	m             Membership
	r             Repairer
	tree          *Tree
	cursor        string
	gen           uint64
	busy          bool
	deadline      env.Timer
	timer         env.Timer
	session       *Session
	stats         Stats
	last          error
	started       bool
}

// NewWorker validates cfg, m, and r and constructs a stopped Worker with an
// empty tree of depth cfg.Depth. It returns env.ErrInvalidArgument if m or r is
// nil or any duration/count setting is negative, and ErrDepth if cfg.Depth is
// out of range.
func NewWorker(e env.Env, cfg Config, m Membership, r Repairer) (*Worker, error) {
	if m == nil || r == nil {
		return nil, fmt.Errorf("merkle: new worker: membership and repairer are required: %w", env.ErrInvalidArgument)
	}
	if cfg.Period < 0 || cfg.Timeout < 0 || cfg.RoundDeadline < 0 || cfg.ScrubBatch < 0 || cfg.MaxRepairs < 0 {
		return nil, fmt.Errorf("merkle: new worker: negative setting: %w", env.ErrInvalidArgument)
	}
	if cfg.Period == 0 {
		cfg.Period = DefaultPeriod
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.RoundDeadline == 0 {
		cfg.RoundDeadline = DeadlinePeriods * cfg.Period
	}
	if cfg.ScrubBatch == 0 {
		cfg.ScrubBatch = DefaultScrubBatch
	}
	if cfg.MaxRepairs == 0 {
		cfg.MaxRepairs = DefaultMaxRepairs
	}
	t, err := New(cfg.Depth)
	if err != nil {
		return nil, fmt.Errorf("merkle: new worker: %w", err)
	}
	return &Worker{metric: newMetricState(cfg.MetricsEnabled), e: e, cfg: cfg, m: m, r: r, tree: t}, nil
}

// Start performs a full scrub of Repairer to rebuild the in-memory tree and
// arms the periodic tick timer. Calling Start on an already-running Worker is
// a no-op; calling Start after Stop rescans and resumes rounds. It returns any
// error from Repairer.Keys during the initial scan.
func (w *Worker) Start() error {
	if w.started {
		return nil
	}
	w.started = true
	err := w.scrub(true)
	w.arm()
	return err
}

// Stop halts periodic ticks, fails any in-flight round with env.ErrClosed
// (preserving its completed diff cost in Stats), and invalidates late callbacks.
// Calling Stop before Start or more than once is a no-op. OnRequest, Record,
// and Forget continue to operate on the tree while stopped.
func (w *Worker) Stop() {
	if !w.started {
		return
	}
	w.started = false
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.finish(w.gen, fmt.Errorf("merkle: round %d: worker stopped: %w", w.gen, env.ErrClosed))
	// Advance gen so any callback already queued for the abandoned round sees
	// live(g) == false even after a later Start.
	w.gen++
}

// Record updates the worker's tree incrementally when the local store writes
// key with hash sum. It returns ErrKey or ErrLeafFull if the tree rejects key.
func (w *Worker) Record(key string, sum Hash) error {
	if err := w.tree.Set(key, sum); err != nil {
		return fmt.Errorf("merkle: record: %w", err)
	}
	return nil
}

// Forget removes key from the worker's tree and reports whether it was present.
func (w *Worker) Forget(key string) bool { return w.tree.Delete(key) }

// Tree returns an immutable O(1) Snapshot of the worker's current tree.
func (w *Worker) Tree() *Tree { return w.tree.Snapshot() }

// LastErr returns the most recent error recorded by a scrub or failed round,
// or nil if none has occurred.
func (w *Worker) LastErr() error { return w.last }

// Stats returns a snapshot of the worker's cumulative counters and InFlight
// flag.
func (w *Worker) Stats() Stats {
	s := w.stats
	s.InFlight = w.busy
	return s
}

// OnRequest answers an inbound Merkle diff request against the live tree and
// passes the reply or error to reply.
func (w *Worker) OnRequest(from env.NodeID, req []byte, reply env.Reply) {
	w.stats.Answered++
	resp, err := w.tree.Answer(req)
	if err != nil {
		reply(nil, err)
		return
	}
	reply(resp, nil)
}

// EncodeState returns a deterministic binary summary of the worker's root,
// counters, generation, and scrub cursor for simulator state-digest checks.
func (w *Worker) EncodeState() []byte {
	root := w.tree.Root()
	out := append([]byte(nil), root[:]...)
	out = binary.AppendUvarint(out, uint64(w.tree.Len()))
	out = binary.AppendUvarint(out, w.gen)
	s := w.Stats()
	for _, v := range []uint64{
		s.Rounds, s.Completed, s.Failed, s.InSync, s.Truncated, s.Repaired, s.RepairedBytes,
		s.Rejected, s.FetchFailed, s.PutFailed, s.Invalid, s.Deferred, s.Scrubbed, s.Changed,
		s.Dropped, s.ReadErrors, s.Skipped, s.RoundTrips, s.Hashes, s.DiffBytes, s.MaxRoundTrips,
		s.Answered,
	} {
		out = binary.AppendUvarint(out, v)
	}
	if s.InFlight {
		out = append(out, 1)
	} else {
		out = append(out, 0)
	}
	return append(out, w.cursor...)
}

// arm schedules the next tick with uniform jitter in [Period/2, 3*Period/2)
// from env.Rand so peers do not synchronize their rounds.
func (w *Worker) arm() {
	d := w.cfg.Period/2 + time.Duration(w.e.Rand.Int64N(int64(w.cfg.Period)))
	w.timer = w.e.Clock.AfterFunc(d, w.tick)
}

func (w *Worker) tick() {
	if !w.started {
		return
	}
	if err := w.scrub(false); err != nil {
		w.last = err
	}
	// Re-check w.started after scrub and startRound in case a synchronous
	// Repairer or Transport call invoked Stop during this tick.
	if w.started && !w.busy {
		w.startRound()
	}
	if w.started {
		w.arm()
	}
}

// scrub re-reads and re-hashes either all keys (on Start) or the next
// ScrubBatch keys past w.cursor in the sorted union of store and tree keys,
// so vanished or wiped blobs are dropped from the tree even when Keys() is empty.
func (w *Worker) scrub(all bool) error {
	keys, err := w.r.Keys()
	if err != nil {
		w.stats.ReadErrors++
		return fmt.Errorf("merkle: scrub: list keys: %w", err)
	}
	union := append(slices.Clone(keys), w.tree.Keys()...)
	slices.Sort(union)
	union = slices.Compact(union)
	if len(union) == 0 {
		return nil
	}
	batch := union
	if !all && len(union) > w.cfg.ScrubBatch {
		i, found := slices.BinarySearch(union, w.cursor)
		if found {
			i++
		}
		batch = make([]string, 0, w.cfg.ScrubBatch)
		for len(batch) < w.cfg.ScrubBatch {
			batch = append(batch, union[i%len(union)])
			i++
		}
	}
	for _, k := range batch {
		w.check(k)
	}
	w.cursor = batch[len(batch)-1]
	return nil
}

// check hashes the bytes currently on disk for key and updates the tree to
// match reality, dropping keys that are missing or unreadable so a diff with a
// healthy peer will pull a good copy.
func (w *Worker) check(key string) {
	data, err := w.r.Read(key)
	if err != nil {
		if !errors.Is(err, env.ErrNotFound) {
			w.stats.ReadErrors++
		}
		if w.tree.Delete(key) {
			w.stats.Dropped++
		}
		return
	}
	w.stats.Scrubbed++
	h := Sum(data)
	old, ok := w.tree.Get(key)
	if ok && old == h {
		return
	}
	if ok {
		w.stats.Changed++
	}
	if err := w.tree.Set(key, h); err != nil {
		w.stats.Skipped++
		w.last = fmt.Errorf("merkle: scrub: %w", err)
	}
}

func (w *Worker) pickPeer() (env.NodeID, bool) {
	peers := slices.Clone(w.m.Peers())
	slices.Sort(peers)
	peers = slices.Compact(peers)
	peers = slices.DeleteFunc(peers, func(p env.NodeID) bool { return p == w.e.ID })
	if len(peers) == 0 {
		return "", false
	}
	return peers[w.e.Rand.IntN(len(peers))], true
}

func (w *Worker) startRound() {
	peer, ok := w.pickPeer()
	if !ok {
		return
	}
	w.gen++
	g := w.gen
	w.busy = true
	w.stats.Rounds++
	w.roundRepaired = false
	w.roundEqual = false
	s := w.tree.Diff(w.cfg.Limits)
	w.session = s
	w.deadline = w.e.Clock.AfterFunc(w.cfg.RoundDeadline, func() {
		w.finish(g, fmt.Errorf("merkle: round %d with %s: deadline %v passed: %w", g, peer, w.cfg.RoundDeadline, env.ErrTimeout))
	})
	w.step(g, peer, s)
}

func (w *Worker) live(g uint64) bool { return w.busy && w.gen == g }

func (w *Worker) step(g uint64, peer env.NodeID, s *Session) {
	req, ok := s.Request()
	if !ok {
		w.account()
		w.repair(g, peer, s.Result())
		return
	}
	w.e.Net.Request(peer, req, w.cfg.Timeout, func(resp []byte, err error) {
		if !w.live(g) {
			return
		}
		if err != nil {
			s.Fail(err)
			w.finish(g, fmt.Errorf("merkle: diff with %s: %w", peer, err))
			return
		}
		if err := s.Feed(resp); err != nil {
			w.finish(g, fmt.Errorf("merkle: diff with %s: %w", peer, err))
			return
		}
		w.step(g, peer, s)
	})
}

// account folds the active Session's cost into Stats once, so rounds ended by
// timeout, malformed reply, deadline, or Stop still record completed work.
func (w *Worker) account() {
	if w.session == nil {
		return
	}
	c := w.session.Result().Cost
	w.session = nil
	w.stats.RoundTrips += uint64(c.RoundTrips)
	w.stats.Hashes += uint64(c.Hashes)
	w.metric.add("merkle_hashes_compared_total", uint64(c.Hashes))
	w.stats.DiffBytes += uint64(c.BytesSent + c.BytesReceived)
	w.stats.MaxRoundTrips = max(w.stats.MaxRoundTrips, uint64(c.RoundTrips))
}

func (w *Worker) repair(g uint64, peer env.NodeID, res Result) {
	w.metric.add("merkle_differences_total", uint64(len(res.Diffs)))
	if res.Truncated {
		w.stats.Truncated++
		w.metric.add("merkle_budget_exhaustions_total", 1)
	}
	// A truncated round dropped unchecked branches, so zero diffs does not mean
	// the replicas are in sync.
	if !res.Truncated && len(res.Diffs) == 0 {
		w.stats.InSync++
		w.roundEqual = true
	}
	var todo []string
	for _, d := range res.Diffs {
		if !d.HasRemote {
			continue
		}
		if !w.r.Valid(d.Key, d.Remote) {
			w.stats.Invalid++
			continue
		}
		// Skip keys already written locally with the same valid hash while the
		// diff was in flight.
		if cur, ok := w.tree.Get(d.Key); ok && cur == d.Remote {
			continue
		}
		if len(todo) >= w.cfg.MaxRepairs {
			w.stats.Deferred++
			w.metric.add("merkle_budget_exhaustions_total", 1)
			continue
		}
		todo = append(todo, d.Key)
	}
	if len(todo) == 0 {
		w.finish(g, nil)
		return
	}
	pending := len(todo)
	var errs []error
	for _, key := range todo {
		// Stop issuing fetches if a synchronous callback or Stop ended the round.
		if !w.live(g) {
			return
		}
		w.r.Fetch(peer, key, func(data []byte, err error) {
			if !w.live(g) {
				return
			}
			if err := w.apply(peer, key, data, err); err != nil {
				errs = append(errs, err)
			}
			pending--
			if pending == 0 {
				w.finish(g, errors.Join(errs...))
			}
		})
	}
}

func (w *Worker) apply(peer env.NodeID, key string, data []byte, err error) error {
	if err != nil {
		w.stats.FetchFailed++
		w.metric.add("merkle_repairs_total", 1, "error")
		return fmt.Errorf("merkle: repair %q from %s: %w", key, peer, err)
	}
	h := Sum(data)
	if !w.r.Valid(key, h) {
		w.stats.Rejected++
		w.metric.add("merkle_repairs_total", 1, "error")
		return fmt.Errorf("merkle: repair %q from %s: %w", key, peer, ErrRejected)
	}
	if cur, ok := w.tree.Get(key); ok && cur == h {
		return nil
	}
	if err := w.r.Put(key, data); err != nil {
		w.stats.PutFailed++
		w.metric.add("merkle_repairs_total", 1, "error")
		return fmt.Errorf("merkle: repair %q: put: %w", key, err)
	}
	if err := w.tree.Set(key, h); err != nil {
		w.stats.Skipped++
		return fmt.Errorf("merkle: repair %q: %w", key, err)
	}
	w.stats.Repaired++
	w.roundRepaired = true
	w.metric.add("merkle_repairs_total", 1, "ok")
	w.stats.RepairedBytes += uint64(len(data))
	return nil
}

func (w *Worker) finish(g uint64, err error) {
	if !w.live(g) {
		return
	}
	w.account()
	w.busy = false
	if w.deadline != nil {
		w.deadline.Stop()
		w.deadline = nil
	}
	if err != nil {
		w.stats.Failed++
		w.metric.add("merkle_rounds_total", 1, "error")
		w.last = err
		return
	}
	w.stats.Completed++
	outcome := "repaired"
	if w.roundEqual {
		outcome = "equal"
	}
	w.metric.add("merkle_rounds_total", 1, outcome)
}
