package nodetest

import (
	"bytes"
	"cmp"
	"encoding/hex"
	"fmt"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/node"
	"github.com/nickstrad/object-store/internal/occ"
)

type Commit struct {
	Key     string
	Node    env.NodeID
	Version occ.Version
	Content string
}

type Lineage struct {
	acks      []Commit
	seen      map[occ.VersionID]map[occ.VersionID]bool
	seenAny   map[occ.VersionID]bool
	byContent map[string]occ.VersionID
	lost      map[occ.VersionID]bool
}

func NewLineage() *Lineage {
	return &Lineage{seen: map[occ.VersionID]map[occ.VersionID]bool{}, seenAny: map[occ.VersionID]bool{},
		byContent: map[string]occ.VersionID{}, lost: map[occ.VersionID]bool{}}
}

func ContentOf(key string, v occ.Version) string {
	if v.Tombstone() || len(key) == 0 || occ.Kind(key[0]) != occ.KindManifest {
		return ""
	}
	man, err := node.ManifestOfRecord(v.Value())
	if err != nil {
		return ""
	}
	return hex.EncodeToString(man.ContentSHA[:]) + fmt.Sprintf(":%d", man.Size)
}

func (o *Lineage) ancestors(key string, held occ.Record) map[occ.VersionID]bool {
	out := map[occ.VersionID]bool{}
	h, ok := held.Version()
	if !ok {
		return out
	}
	add := func(id occ.VersionID) {
		out[id] = true
		for a := range o.seen[id] {
			out[a] = true
		}
	}
	add(h.ID())
	if c := ContentOf(key, h); c != "" {
		if id, ok := o.byContent[key+"\x00"+c]; ok {
			add(id)
		}
	}
	return out
}

func (o *Lineage) Commit(id env.NodeID, key string, held occ.Record, v occ.Version) {
	anc := o.ancestors(key, held)
	for a := range anc {
		o.seenAny[a] = true
	}
	o.seen[v.ID()] = anc
	c := ContentOf(key, v)
	if c != "" {
		if _, dup := o.byContent[key+"\x00"+c]; !dup {
			o.byContent[key+"\x00"+c] = v.ID()
		}
	}
	o.acks = append(o.acks, Commit{Key: key, Node: id, Version: v, Content: c})
}

func (o *Lineage) Discard(loser occ.Version) { o.lost[loser.ID()] = true }

func (o *Lineage) Forget() { o.lost = map[occ.VersionID]bool{} }

func (o *Lineage) Commits() []Commit { return append([]Commit(nil), o.acks...) }

func (o *Lineage) Explain(a Commit, final occ.Record) string {
	if f, ok := final.Version(); ok {
		if f.ID() == a.Version.ID() {
			return "current"
		}
		if a.Content != "" && ContentOf(a.Key, f) == a.Content {
			return "current-copy"
		}
	}
	switch {
	case o.seenAny[a.Version.ID()]:
		return "superseded"
	case o.lost[a.Version.ID()]:
		return "discarded"
	}
	return ""
}

func (o *Lineage) Check(rd occ.Reader, keep func(key string) bool) (map[string]int, error) {
	tally := map[string]int{}
	for _, a := range o.acks {
		if keep != nil && !keep(a.Key) {
			continue
		}
		why := o.Explain(a, rd.Record(a.Key))
		if why == "" {
			id := a.Version.ID()
			return tally, fmt.Errorf("acknowledged version %s of %q (written on %s, stamp %v) is not current, not superseded by a later acknowledged write and on no discard record",
				hex.EncodeToString(id[:4]), a.Key, a.Node, a.Version.Stamp())
		}
		tally[why]++
	}
	return tally, nil
}

func LWWAfter(a, b occ.Version) bool {
	sa, sb := a.Stamp(), b.Stamp()
	if c := cmp.Compare(sa.Wall, sb.Wall); c != 0 {
		return c > 0
	}
	if c := cmp.Compare(sa.Logical, sb.Logical); c != 0 {
		return c > 0
	}
	if c := cmp.Compare(sa.Node, sb.Node); c != 0 {
		return c > 0
	}
	ia, ib := a.ID(), b.ID()
	return bytes.Compare(ia[:], ib[:]) > 0
}

func Covers(x, w occ.Version) bool {
	return x.ID() != w.ID() && x.Clock().Get(w.Dot().Actor) >= w.Dot().Counter
}
