package merkle

import (
	"encoding/binary"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

const (
	DefaultPeriod     = time.Second
	DefaultTimeout    = 500 * time.Millisecond
	DefaultScrubBatch = 64
	DefaultMaxRepairs = 256
	DeadlinePeriods   = 10
)

type Membership interface {
	Peers() []env.NodeID
}

type Repairer interface {
	Keys() ([]string, error)
	Read(key string) ([]byte, error)
	Valid(key string, sum Hash) bool
	Fetch(peer env.NodeID, key string, done func(data []byte, err error))
	Put(key string, data []byte) error
}

type Config struct {
	Depth         int
	Period        time.Duration
	Timeout       time.Duration
	RoundDeadline time.Duration
	ScrubBatch    int
	MaxRepairs    int
	Limits        Limits
}

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

type Worker struct {
	e        env.Env
	cfg      Config
	m        Membership
	r        Repairer
	tree     *Tree
	cursor   string
	gen      uint64
	busy     bool
	deadline env.Timer
	timer    env.Timer
	session  *Session
	stats    Stats
	last     error
	started  bool
}

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
	return &Worker{e: e, cfg: cfg, m: m, r: r, tree: t}, nil
}

func (w *Worker) Start() error {
	if w.started {
		return nil
	}
	w.started = true
	err := w.scrub(true)
	w.arm()
	return err
}

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
	w.gen++
}

func (w *Worker) Record(key string, sum Hash) error {
	if err := w.tree.Set(key, sum); err != nil {
		return fmt.Errorf("merkle: record: %w", err)
	}
	return nil
}

func (w *Worker) Forget(key string) bool { return w.tree.Delete(key) }

func (w *Worker) Tree() *Tree { return w.tree.Snapshot() }

func (w *Worker) LastErr() error { return w.last }

func (w *Worker) Stats() Stats {
	s := w.stats
	s.InFlight = w.busy
	return s
}

func (w *Worker) OnRequest(from env.NodeID, req []byte, reply env.Reply) {
	w.stats.Answered++
	resp, err := w.tree.Answer(req)
	if err != nil {
		reply(nil, err)
		return
	}
	reply(resp, nil)
}

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
	if w.started && !w.busy {
		w.startRound()
	}
	if w.started {
		w.arm()
	}
}

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

func (w *Worker) account() {
	if w.session == nil {
		return
	}
	c := w.session.Result().Cost
	w.session = nil
	w.stats.RoundTrips += uint64(c.RoundTrips)
	w.stats.Hashes += uint64(c.Hashes)
	w.stats.DiffBytes += uint64(c.BytesSent + c.BytesReceived)
	w.stats.MaxRoundTrips = max(w.stats.MaxRoundTrips, uint64(c.RoundTrips))
}

func (w *Worker) repair(g uint64, peer env.NodeID, res Result) {
	if res.Truncated {
		w.stats.Truncated++
	}
	if !res.Truncated && len(res.Diffs) == 0 {
		w.stats.InSync++
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
		if cur, ok := w.tree.Get(d.Key); ok && cur == d.Remote {
			continue
		}
		if len(todo) >= w.cfg.MaxRepairs {
			w.stats.Deferred++
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
		return fmt.Errorf("merkle: repair %q from %s: %w", key, peer, err)
	}
	h := Sum(data)
	if !w.r.Valid(key, h) {
		w.stats.Rejected++
		return fmt.Errorf("merkle: repair %q from %s: %w", key, peer, ErrRejected)
	}
	if cur, ok := w.tree.Get(key); ok && cur == h {
		return nil
	}
	if err := w.r.Put(key, data); err != nil {
		w.stats.PutFailed++
		return fmt.Errorf("merkle: repair %q: put: %w", key, err)
	}
	if err := w.tree.Set(key, h); err != nil {
		w.stats.Skipped++
		return fmt.Errorf("merkle: repair %q: %w", key, err)
	}
	w.stats.Repaired++
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
		w.last = err
		return
	}
	w.stats.Completed++
}
