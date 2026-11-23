package occ_test

import (
	"errors"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/sim"
)

func TestNodeMergeAppliesLastWriterWins(t *testing.T) {
	s := newSim(t, 3)
	var discards []occ.Discard
	for _, id := range []env.NodeID{"a", "b"} {
		cfg := occ.Config{Period: time.Hour, OnDiscard: func(key string, w, l occ.Version) {
			discards = append(discards, occ.Discard{Key: key, Winner: w, Loser: l})
		}}
		if err := s.AddNode(sim.NodeSpec{ID: id, Region: "r"}, occ.NewFactory(cfg)); err != nil {
			t.Fatal(err)
		}
	}
	run(t, s, 10*time.Millisecond)
	a, b := occNode(s, "a"), occNode(s, "b")
	key := occ.ManifestKey("bk", "o")
	old, err := a.Put(key, []byte("old"), occ.Unconditional())
	if err != nil {
		t.Fatal(err)
	}
	run(t, s, 5*time.Millisecond)
	newer, err := b.Put(key, []byte("new"), occ.Unconditional())
	if err != nil {
		t.Fatal(err)
	}
	single := func(v occ.Version) *occ.Doc {
		d := occ.NewDoc()
		d.MergeVersion(key, v)
		return d
	}
	changed, err := a.Merge(single(newer))
	if err != nil || !changed {
		t.Fatalf("merge of the later version: changed=%v err=%v", changed, err)
	}
	if got, _, err := a.Get(key); err != nil || string(got) != "new" {
		t.Fatalf("after merge a holds %q, %v", got, err)
	}
	changed, err = a.Merge(single(old))
	if err != nil || changed {
		t.Fatalf("merge of the replaced version: changed=%v err=%v", changed, err)
	}
	if len(discards) == 0 || discards[0].Loser.ID() != old.ID() {
		t.Fatalf("discards %v, want the concurrent old version on record", discards)
	}
}

func TestNodeMergeBeforeReadyIsNotReady(t *testing.T) {
	n := occ.NewNode(env.Env{ID: "x"}, occ.Config{})
	if _, err := n.Merge(occ.NewDoc()); !errors.Is(err, occ.ErrNotReady) {
		t.Fatalf("got %v, want ErrNotReady", err)
	}
}
