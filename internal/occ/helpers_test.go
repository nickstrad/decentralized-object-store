package occ_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
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

func newTestStore(t fataler, node string, epoch, seed uint64) (*occ.Store, *fakeClock) {
	t.Helper()
	c := &fakeClock{now: env.Time(1_000_000)}
	s, err := occ.NewStore(occ.StoreConfig{Node: env.NodeID(node), Epoch: epoch, Clock: c, Rand: rand.New(rand.NewPCG(seed, 7))})
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	return s, c
}

func mustPut(t fataler, s *occ.Store, key, value string, p occ.Precondition) occ.Version {
	t.Helper()
	v, err := s.Put(key, []byte(value), p)
	if err != nil {
		t.Fatalf("put %q=%q (%s): %v", key, value, p.String(), err)
	}
	return v
}

func ids(r occ.Record) []string {
	var out []string
	for _, id := range r.IDs() {
		out = append(out, id.String())
	}
	return out
}

func values(r occ.Record) []string {
	var out []string
	for _, v := range r.Siblings() {
		if v.Tombstone() {
			out = append(out, "<tombstone>")
		} else {
			out = append(out, string(v.Value()))
		}
	}
	slices.Sort(out)
	return out
}

type ack struct {
	key string
	v   occ.Version
}

type oracle struct {
	acks []ack
	seen map[occ.VersionID]map[occ.VersionID]bool
}

func newOracle() *oracle { return &oracle{seen: map[occ.VersionID]map[occ.VersionID]bool{}} }

func (o *oracle) record(key string, v occ.Version, covered []occ.Version) {
	s := map[occ.VersionID]bool{}
	for _, w := range covered {
		s[w.ID()] = true
		for id := range o.seen[w.ID()] {
			s[id] = true
		}
	}
	o.seen[v.ID()] = s
	o.acks = append(o.acks, ack{key: key, v: v})
}

func (o *oracle) check(doc *occ.Doc) error {
	for _, a := range o.acks {
		ok := false
		for _, w := range doc.Record(a.key).Siblings() {
			if w.ID() == a.v.ID() || o.seen[w.ID()][a.v.ID()] {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("acknowledged version %s of key %q (dot %s:%d) is neither a sibling nor seen by a sibling's writer; record holds %v",
				a.v.ID().String()[:12], a.key, a.v.Dot().Actor.String(), a.v.Dot().Counter, ids(doc.Record(a.key)))
		}
	}
	return nil
}
