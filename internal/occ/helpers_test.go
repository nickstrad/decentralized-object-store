package occ_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/occ"
)

type fakeClock struct{ now env.Time }

func (c *fakeClock) Now() env.Time { return c.now }

func (c *fakeClock) AfterFunc(time.Duration, func()) env.Timer { return nopTimer{} }

type nopTimer struct{}

func (nopTimer) Stop() bool               { return false }
func (nopTimer) Reset(time.Duration) bool { return false }

type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

type discardLog struct {
	lines    []string
	discards []occ.Discard
}

func (l *discardLog) hook(key string, winner, loser occ.Version) {
	l.discards = append(l.discards, occ.Discard{Key: key, Winner: winner, Loser: loser})
}

func (l *discardLog) sink(line string) { l.lines = append(l.lines, line) }

func storeConfig(node string, epoch, seed uint64, c env.Clock) occ.StoreConfig {
	return occ.StoreConfig{Node: env.NodeID(node), Epoch: epoch, Clock: c, Rand: rand.New(rand.NewPCG(seed, 7))}
}

func newTestStore(t fataler, node string, epoch, seed uint64) (*occ.Store, *fakeClock) {
	t.Helper()
	c := &fakeClock{now: env.Time(1_000_000)}
	s, err := occ.NewStore(storeConfig(node, epoch, seed, c))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s, c
}

func newLoggedStore(t fataler, node string, epoch, seed uint64) (*occ.Store, *fakeClock, *discardLog) {
	t.Helper()
	c := &fakeClock{now: env.Time(1_000_000)}
	l := &discardLog{}
	cfg := storeConfig(node, epoch, seed, c)
	cfg.Log, cfg.OnDiscard = l.sink, l.hook
	s, err := occ.NewStore(cfg)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s, c, l
}

func mustPut(t fataler, s *occ.Store, key, value string, p occ.Precondition) occ.Version {
	t.Helper()
	v, err := s.Put(key, []byte(value), p)
	if err != nil {
		t.Fatalf("put %q=%q (%s): %v", key, value, p.String(), err)
	}
	return v
}

func mustMerge(t fataler, s *occ.Store, d *occ.Doc) bool {
	t.Helper()
	changed, err := s.Merge(d)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	return changed
}

func short(id occ.VersionID) string { return id.String()[:12] }

func value(r occ.Record) string {
	v, ok := r.Version()
	switch {
	case !ok:
		return "<absent>"
	case v.Tombstone():
		return "<tombstone>"
	}
	return string(v.Value())
}

// lwwLess is an independent test-side implementation of the LWW total order
// (Wall, Logical, Node, then VersionID bytes) used to cross-check product code.
func lwwLess(a, b occ.Version) bool {
	sa, sb := a.Stamp(), b.Stamp()
	switch {
	case sa.Wall != sb.Wall:
		return sa.Wall < sb.Wall
	case sa.Logical != sb.Logical:
		return sa.Logical < sb.Logical
	case sa.Node != sb.Node:
		return sa.Node < sb.Node
	}
	ia, ib := a.ID(), b.ID()
	return slices.Compare(ia[:], ib[:]) < 0
}

type ack struct {
	key string
	v   occ.Version
}

// oracle independently tracks acknowledged versions, transitive seen ancestry
// from each write's held record (without consulting product version vectors),
// emitted discard callbacks, and per-incarnation holders vs. lost disks.
type oracle struct {
	acks    []ack
	seen    map[occ.VersionID]map[occ.VersionID]bool
	seenAny map[occ.VersionID]bool
	lost    map[occ.VersionID]bool
	logged  int
	gen     map[string]int
	holders map[occ.VersionID]map[string]bool
	died    map[occ.VersionID]map[string]bool
}

func newOracle() *oracle {
	return &oracle{seen: map[occ.VersionID]map[occ.VersionID]bool{}, seenAny: map[occ.VersionID]bool{}, lost: map[occ.VersionID]bool{},
		gen: map[string]int{}, holders: map[occ.VersionID]map[string]bool{}, died: map[occ.VersionID]map[string]bool{}}
}

func (o *oracle) incarnation(node string) string { return fmt.Sprintf("%s#%d", node, o.gen[node]) }

func mark(m map[occ.VersionID]map[string]bool, id occ.VersionID, who string) {
	if m[id] == nil {
		m[id] = map[string]bool{}
	}
	m[id][who] = true
}

func (o *oracle) record(key string, v occ.Version, held occ.Record) {
	s := map[occ.VersionID]bool{}
	if h, ok := held.Version(); ok {
		s[h.ID()] = true
		for id := range o.seen[h.ID()] {
			s[id] = true
		}
	}
	for id := range s {
		o.seenAny[id] = true
	}
	o.seen[v.ID()] = s
	o.acks = append(o.acks, ack{key: key, v: v})
	mark(o.holders, v.ID(), o.incarnation(string(v.Dot().Actor.Node)))
}

func (o *oracle) reach(node string, d *occ.Doc) {
	who := o.incarnation(node)
	for _, k := range d.Keys() {
		if v, ok := d.Record(k).Version(); ok {
			mark(o.holders, v.ID(), who)
		}
	}
}

func (o *oracle) diskLost(node string, d *occ.Doc) {
	who := o.incarnation(node)
	if d != nil {
		for _, k := range d.Keys() {
			if v, ok := d.Record(k).Version(); ok {
				mark(o.died, v.ID(), who)
			}
		}
	}
	o.gen[node]++
}

func (o *oracle) lostWithDisk(id occ.VersionID) bool {
	hs := o.holders[id]
	if len(hs) == 0 {
		return false
	}
	for who := range hs {
		if !o.died[id][who] {
			return false
		}
	}
	return true
}

func (o *oracle) discard(_ string, _, loser occ.Version) {
	o.lost[loser.ID()] = true
	o.logged++
}

func (o *oracle) explain(a ack, docs []*occ.Doc, everywhere bool) (string, bool) {
	held := 0
	for _, d := range docs {
		if v, ok := d.Record(a.key).Version(); ok && v.ID() == a.v.ID() {
			held++
		}
	}
	switch {
	case len(docs) > 0 && held == len(docs):
		return "current", true
	case held > 0 && !everywhere:
		return "held", true
	case o.seenAny[a.v.ID()]:
		return "superseded", true
	case o.lost[a.v.ID()]:
		return "discarded", true
	case o.lostWithDisk(a.v.ID()):
		return "lost-with-disk", true
	}
	return "", false
}

func (o *oracle) check(docs []*occ.Doc, everywhere bool) error {
	for _, a := range o.acks {
		if _, ok := o.explain(a, docs, everywhere); !ok {
			return fmt.Errorf("acknowledged version %s of key %q (dot %s:%d, stamp %s) is lost with no record: not current, not seen by a later acknowledged write, not named in any discard log",
				short(a.v.ID()), a.key, a.v.Dot().Actor.String(), a.v.Dot().Counter, a.v.Stamp().String())
		}
	}
	return nil
}

func (o *oracle) tally(docs []*occ.Doc) map[string]int {
	out := map[string]int{}
	for _, a := range o.acks {
		why, _ := o.explain(a, docs, true)
		out[why]++
	}
	return out
}
