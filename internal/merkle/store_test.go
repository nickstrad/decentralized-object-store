package merkle_test

import (
	"encoding/binary"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/merkle"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

const (
	chunkPrefix         = "chunk/"
	tagMerkle      byte = 'M'
	tagGet         byte = 'G'
	tagPut         byte = 'P'
	storeTimeout        = 300 * time.Millisecond
	errStoreClosed      = "store: worker not running"
)

var errNoWorker = errors.New(errStoreClosed)

type tagNet struct {
	inner env.Transport
	tag   byte
}

func (n tagNet) Send(to env.NodeID, msg []byte) error {
	return n.inner.Send(to, append([]byte{n.tag}, msg...))
}

func (n tagNet) Request(to env.NodeID, req []byte, timeout time.Duration, cb func([]byte, error)) {
	n.inner.Request(to, append([]byte{n.tag}, req...), timeout, cb)
}

type storeConfig struct {
	peers     []env.NodeID
	worker    merkle.Config
	replicate bool
	wrapNet   func(env.Transport) env.Transport
}

type chunkStore struct {
	e        env.Env
	cfg      storeConfig
	w        *merkle.Worker
	startErr error
	fetched  uint64
	served   uint64
	writes   uint64
	puts     uint64
	replErrs uint64
}

func storeFactory(cfg storeConfig) sim.Factory {
	return func(e env.Env) env.Process { return &chunkStore{e: e, cfg: cfg} }
}

func (s *chunkStore) Peers() []env.NodeID { return s.cfg.peers }

func (s *chunkStore) Keys() ([]string, error) {
	names, err := s.e.Disk.List(chunkPrefix)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, strings.TrimPrefix(n, chunkPrefix))
	}
	return out, nil
}

func (s *chunkStore) Read(key string) ([]byte, error) { return s.e.Disk.Read(chunkPrefix + key) }

func (s *chunkStore) Valid(key string, h merkle.Hash) bool { return key == h.String() }

func (s *chunkStore) Fetch(peer env.NodeID, key string, done func([]byte, error)) {
	s.e.Net.Request(peer, append([]byte{tagGet}, key...), storeTimeout, func(b []byte, err error) {
		if err == nil {
			s.fetched += uint64(len(b))
		}
		done(b, err)
	})
}

func (s *chunkStore) Put(key string, data []byte) error {
	if err := s.e.Disk.Write(chunkPrefix+key, data); err != nil {
		return err
	}
	s.puts++
	return s.e.Disk.Sync(chunkPrefix + key)
}

func (s *chunkStore) Start() {
	var net env.Transport = tagNet{inner: s.e.Net, tag: tagMerkle}
	if s.cfg.wrapNet != nil {
		net = s.cfg.wrapNet(net)
	}
	w, err := merkle.NewWorker(s.e.WithNet(net), s.cfg.worker, s, s)
	if err != nil {
		s.startErr = err
		return
	}
	s.w = w
	s.startErr = w.Start()
}

func (s *chunkStore) OnDatagram(env.NodeID, []byte) {}

func (s *chunkStore) OnRequest(from env.NodeID, req []byte, reply env.Reply) {
	if len(req) == 0 {
		reply(nil, env.ErrInvalidArgument)
		return
	}
	switch req[0] {
	case tagMerkle:
		if s.w == nil {
			reply(nil, errNoWorker)
			return
		}
		s.w.OnRequest(from, req[1:], reply)
	case tagGet:
		data, err := s.Read(string(req[1:]))
		if err != nil {
			reply(nil, err)
			return
		}
		s.served += uint64(len(data))
		reply(data, nil)
	case tagPut:
		reply(nil, s.store(req[1:]))
	default:
		reply(nil, env.ErrInvalidArgument)
	}
}

func (s *chunkStore) store(data []byte) error {
	h := merkle.Sum(data)
	if err := s.Put(h.String(), data); err != nil {
		return err
	}
	if s.w == nil {
		return errNoWorker
	}
	return s.w.Record(h.String(), h)
}

func (s *chunkStore) ClientWrite(data []byte) error {
	s.writes++
	if err := s.store(data); err != nil {
		return err
	}
	if !s.cfg.replicate {
		return nil
	}
	for _, p := range s.cfg.peers {
		if p == s.e.ID {
			continue
		}
		s.e.Net.Request(p, append([]byte{tagPut}, data...), storeTimeout, func(_ []byte, err error) {
			if err != nil {
				s.replErrs++
			}
		})
	}
	return nil
}

func (s *chunkStore) EncodeState() []byte {
	var out []byte
	if s.w != nil {
		out = s.w.EncodeState()
	}
	for _, v := range []uint64{s.fetched, s.served, s.writes, s.puts, s.replErrs} {
		out = binary.AppendUvarint(out, v)
	}
	return out
}

func storeOf(s *sim.Sim, id env.NodeID) *chunkStore {
	if s.Status(id).Paused {
		return nil
	}
	c, _ := s.Process(id).(*chunkStore)
	return c
}

type liarConfig struct {
	depth int
	known func() []merkle.Entry
}

type liar struct {
	e   env.Env
	cfg liarConfig
	n   uint64
}

func liarFactory(cfg liarConfig) sim.Factory {
	return func(e env.Env) env.Process { return &liar{e: e, cfg: cfg} }
}

func (l *liar) Start()                         {}
func (l *liar) OnDatagram(env.NodeID, []byte)  {}
func (l *liar) EncodeState() []byte            { return binary.AppendUvarint(nil, l.n) }

func (l *liar) tree() (*merkle.Tree, bool) {
	t, err := merkle.New(l.cfg.depth)
	if err != nil {
		return nil, false
	}
	for _, e := range l.cfg.known() {
		if t.Set(e.Key, e.Hash) != nil {
			return nil, false
		}
	}
	for i := 0; i < 64; i++ {
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], l.e.Rand.Uint64())
		k := merkle.Sum(b[:]).String()
		var h merkle.Hash
		binary.BigEndian.PutUint64(h[:], l.e.Rand.Uint64()|1)
		if t.Set(k, h) != nil {
			return nil, false
		}
	}
	return t, true
}

func (l *liar) OnRequest(from env.NodeID, req []byte, reply env.Reply) {
	l.n++
	if len(req) == 0 {
		reply(nil, env.ErrInvalidArgument)
		return
	}
	switch req[0] {
	case tagMerkle:
		switch l.e.Rand.IntN(4) {
		case 0:
			b := make([]byte, l.e.Rand.IntN(80))
			for i := range b {
				b[i] = byte(l.e.Rand.Uint32())
			}
			reply(b, nil)
		case 1:
			reply(nil, merkle.ErrDepth)
		default:
			t, ok := l.tree()
			if !ok {
				reply(nil, env.ErrIO)
				return
			}
			resp, err := t.Answer(req[1:])
			reply(resp, err)
		}
	case tagGet:
		b := make([]byte, 64+l.e.Rand.IntN(64))
		for i := range b {
			b[i] = byte(l.e.Rand.Uint32())
		}
		reply(b, nil)
	default:
		reply(nil, nil)
	}
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
