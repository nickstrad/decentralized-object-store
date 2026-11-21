package merkle

import (
	"fmt"
	"slices"
	"strings"
)

const DefaultFrontier = 1024

var skipLastBranch bool

type Limits struct {
	Frontier int
}

func (l Limits) frontier() int {
	if l.Frontier <= 0 {
		return DefaultFrontier
	}
	return min(l.Frontier, MaxRequestNodes)
}

type Difference struct {
	Key       string
	Local     Hash
	Remote    Hash
	HasLocal  bool
	HasRemote bool
}

type Cost struct {
	RoundTrips    int
	Hashes        int
	Nodes         int
	BytesSent     int
	BytesReceived int
}

type Result struct {
	Diffs     []Difference
	Truncated bool
	Cost      Cost
}

type Session struct {
	local     *Tree
	frontier  int
	level     int
	todo      []uint64
	sent      []uint64
	next      []uint64
	resume    bool
	after     string
	taken     int
	req       []byte
	diffs     []Difference
	truncated bool
	cost      Cost
	done      bool
	err       error
}

func (t *Tree) Diff(l Limits) *Session {
	return &Session{local: t.Snapshot(), frontier: l.frontier(), todo: []uint64{0}}
}

func (t *Tree) Answer(b []byte) ([]byte, error) {
	req, err := decodeRequest(b)
	if err != nil {
		return nil, fmt.Errorf("merkle: answer: %w", err)
	}
	if req.depth != t.depth {
		return nil, fmt.Errorf("merkle: answer: peer depth %d, local depth %d: %w", req.depth, t.depth, ErrDepth)
	}
	if req.level == 0 {
		if req.root == t.Root() {
			return encodeResponse(response{kind: msgEqual}), nil
		}
		req.paths = []uint64{0}
	}
	if req.level < t.depth {
		resp := response{kind: msgInner, level: req.level}
		for _, p := range req.paths {
			n := t.nodeAt(req.level, p)
			var kids [Fanout]Hash
			for i := 0; i < Fanout; i++ {
				kids[i] = n.kid(i).sum()
			}
			appendKids(&resp, &kids)
		}
		return encodeResponse(resp), nil
	}
	return encodeResponse(t.packLeaves(req)), nil
}

func (t *Tree) packLeaves(req request) response {
	resp := response{kind: msgLeaves, level: req.level}
	size := 2 + uvarintLen(len(req.paths))
	for j, p := range req.paths {
		var es []Entry
		if n := t.nodeAt(req.level, p); n != nil {
			es = n.entries
		}
		if j == 0 && req.resume {
			i, found := slices.BinarySearchFunc(es, req.after, byKey)
			if found {
				i++
			}
			es = es[i:]
		}
		full := uvarintLen(len(es))
		for _, e := range es {
			full += entryBytes(e)
		}
		if size+full <= MaxReplyBytes {
			size += full
			resp.leaves = append(resp.leaves, es)
			continue
		}
		k, used := 0, uvarintLen(0)
		for k < len(es) {
			grow := entryBytes(es[k]) + uvarintLen(k+1) - uvarintLen(k)
			if size+used+grow > MaxReplyBytes {
				break
			}
			used += grow
			k++
		}
		if k > 0 {
			resp.leaves = append(resp.leaves, es[:k])
			resp.more = true
		}
		break
	}
	return resp
}

func (s *Session) Request() ([]byte, bool) {
	if s.done {
		return nil, false
	}
	if s.req == nil {
		s.sent = s.todo
		s.todo = nil
		r := request{depth: s.local.depth, level: s.level, paths: s.sent, resume: s.resume, after: s.after}
		if s.level == 0 {
			r.root = s.local.Root()
			s.cost.Hashes++
		}
		s.req = encodeRequest(r)
		s.cost.BytesSent += len(s.req)
	}
	return s.req, true
}

func (s *Session) Feed(b []byte) error {
	if s.done || s.req == nil {
		return fmt.Errorf("merkle: feed at level %d: %w", s.level, ErrSession)
	}
	s.cost.RoundTrips++
	s.cost.BytesReceived += len(b)
	resp, err := decodeResponse(b)
	if err == nil && resp.more && len(b) <= minCutReply {
		err = malformed("a cut leaf reply that is not full")
	}
	if err != nil {
		return s.fail(fmt.Errorf("merkle: diff level %d: %w", s.level, err))
	}
	if err := s.apply(resp); err != nil {
		return s.fail(fmt.Errorf("merkle: diff level %d: %w", s.level, err))
	}
	s.req = nil
	s.advance()
	return nil
}

func (s *Session) apply(r response) error {
	switch r.kind {
	case msgEqual:
		if s.level != 0 {
			return malformed("equal reply below the root")
		}
		s.sent = nil
		return nil
	case msgInner:
		if r.level != s.level || s.level >= s.local.depth || len(r.bitmaps) != len(s.sent) {
			return malformed("inner reply does not match the request")
		}
		k := 0
		for j, p := range s.sent {
			ln := s.local.nodeAt(s.level, p)
			for i := 0; i < Fanout; i++ {
				var rh Hash
				if r.bitmaps[j]&(1<<(Fanout-1-i)) != 0 {
					rh = r.hashes[k]
					k++
					s.cost.Hashes++
				}
				if rh == ln.kid(i).sum() {
					continue
				}
				if skipLastBranch && i == Fanout-1 {
					continue
				}
				if len(s.next) >= s.frontier {
					s.truncated = true
					continue
				}
				s.next = append(s.next, child(p, s.level, i))
			}
		}
		s.cost.Nodes += len(s.sent)
		s.sent = nil
		return nil
	default:
		return s.applyLeaves(r)
	}
}

func (s *Session) applyLeaves(r response) error {
	if r.level != s.level || s.level != s.local.depth || len(r.leaves) > len(s.sent) {
		return malformed("leaf reply does not match the request")
	}
	last := len(r.leaves) - 1
	for j, es := range r.leaves {
		for _, e := range es {
			if prefix(pathOf(e.Key), s.level) != s.sent[j] {
				return malformed("leaf entry outside its leaf")
			}
		}
		if j == 0 && s.resume && len(es) > 0 && es[0].Key <= s.after {
			return malformed("resumed leaf repeats a key")
		}
	}
	if s.resume && s.taken+len(r.leaves[0]) > MaxLeafEntries {
		return malformed("leaf pieces over the entry limit")
	}
	for j, es := range r.leaves {
		var mine []Entry
		if n := s.local.nodeAt(s.level, s.sent[j]); n != nil {
			mine = n.entries
		}
		if j == 0 && s.resume {
			i, found := slices.BinarySearchFunc(mine, s.after, byKey)
			if found {
				i++
			}
			mine = mine[i:]
		}
		if j == last && r.more {
			i, found := slices.BinarySearchFunc(mine, es[len(es)-1].Key, byKey)
			if found {
				i++
			}
			mine = mine[:i]
		}
		s.diffs = diffLeaf(mine, es, s.diffs)
		s.cost.Hashes += len(es)
	}
	s.cost.Nodes += len(r.leaves)
	if r.more {
		taken := len(r.leaves[last])
		if last == 0 && s.resume {
			taken += s.taken
		}
		s.todo = slices.Clone(s.sent[last:])
		s.resume, s.after, s.taken = true, strings.Clone(r.leaves[last][len(r.leaves[last])-1].Key), taken
	} else {
		s.todo = slices.Clone(s.sent[len(r.leaves):])
		s.resume, s.after, s.taken = false, "", 0
	}
	s.sent = nil
	return nil
}

func (s *Session) advance() {
	if len(s.todo) > 0 {
		return
	}
	if len(s.next) > 0 {
		s.level++
		s.todo, s.next = s.next, nil
		return
	}
	s.done = true
	slices.SortFunc(s.diffs, func(a, b Difference) int { return strings.Compare(a.Key, b.Key) })
}

func (s *Session) fail(err error) error {
	if !s.done {
		s.done, s.err = true, err
	}
	return err
}

func (s *Session) Fail(err error) { _ = s.fail(err) }

func (s *Session) Done() bool { return s.done }

func (s *Session) Err() error { return s.err }

func (s *Session) Result() Result {
	return Result{Diffs: slices.Clone(s.diffs), Truncated: s.truncated, Cost: s.cost}
}

func diffLeaf(local, remote []Entry, out []Difference) []Difference {
	i, j := 0, 0
	for i < len(local) || j < len(remote) {
		switch {
		case j == len(remote) || (i < len(local) && local[i].Key < remote[j].Key):
			out = append(out, Difference{Key: local[i].Key, Local: local[i].Hash, HasLocal: true})
			i++
		case i == len(local) || remote[j].Key < local[i].Key:
			out = append(out, Difference{Key: strings.Clone(remote[j].Key), Remote: remote[j].Hash, HasRemote: true})
			j++
		default:
			if local[i].Hash != remote[j].Hash {
				out = append(out, Difference{Key: local[i].Key, Local: local[i].Hash, Remote: remote[j].Hash, HasLocal: true, HasRemote: true})
			}
			i++
			j++
		}
	}
	return out
}

func Compare(local, remote *Tree, l Limits) (Result, error) {
	s := local.Diff(l)
	for {
		req, ok := s.Request()
		if !ok {
			break
		}
		resp, err := remote.Answer(req)
		if err != nil {
			s.Fail(err)
			break
		}
		if err := s.Feed(resp); err != nil {
			break
		}
	}
	return s.Result(), s.Err()
}
