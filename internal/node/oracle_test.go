package node_test

import (
	"bytes"
	"cmp"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

type commitRec struct {
	key     string
	node    env.NodeID
	v       occ.Version
	content string
}

// ackOracle tracks per-key commit ancestry independently of OCC's classifier so
// tests can verify every acknowledged write is current, a migration copy of the
// current content, causally superseded on that key, or named on a discard record.
type ackOracle struct {
	acks      []commitRec
	seen      map[occ.VersionID]map[occ.VersionID]bool
	seenAny   map[occ.VersionID]bool
	byContent map[string]occ.VersionID
	lost      map[occ.VersionID]bool
	lostWhy   map[occ.VersionID]string
}

func newAckOracle() *ackOracle {
	return &ackOracle{seen: map[occ.VersionID]map[occ.VersionID]bool{}, seenAny: map[occ.VersionID]bool{},
		byContent: map[string]occ.VersionID{}, lost: map[occ.VersionID]bool{}, lostWhy: map[occ.VersionID]string{}}
}

func contentOf(key string, v occ.Version) string {
	if v.Tombstone() || len(key) == 0 || occ.Kind(key[0]) != occ.KindManifest {
		return ""
	}
	man, err := manifestOf(v.Value())
	if err != nil {
		return ""
	}
	return hex.EncodeToString(man.ContentSHA[:]) + fmt.Sprintf(":%d", man.Size)
}

func (o *ackOracle) ancestors(key string, held occ.Record) map[occ.VersionID]bool {
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
	if c := contentOf(key, h); c != "" {
		if id, ok := o.byContent[key+"\x00"+c]; ok {
			add(id)
		}
	}
	return out
}

func (o *ackOracle) commit(id env.NodeID, key string, held occ.Record, v occ.Version) {
	anc := o.ancestors(key, held)
	for a := range anc {
		o.seenAny[a] = true
	}
	o.seen[v.ID()] = anc
	c := contentOf(key, v)
	if c != "" {
		if _, dup := o.byContent[key+"\x00"+c]; !dup {
			o.byContent[key+"\x00"+c] = v.ID()
		}
	}
	o.acks = append(o.acks, commitRec{key: key, node: id, v: v, content: c})
}

func (o *ackOracle) discard(loser occ.Version) { o.lost[loser.ID()] = true }

func (o *ackOracle) explain(a commitRec, final occ.Record) string {
	if f, ok := final.Version(); ok {
		if f.ID() == a.v.ID() {
			return "current"
		}
		if a.content != "" && contentOf(a.key, f) == a.content {
			return "current-copy"
		}
	}
	switch {
	case o.seenAny[a.v.ID()]:
		return "superseded"
	case o.lost[a.v.ID()]:
		return "discarded"
	case o.lostWhy[a.v.ID()] != "":
		return o.lostWhy[a.v.ID()]
	}
	return ""
}

func (o *ackOracle) check(rd occ.Reader, keep func(key string) bool) (map[string]int, error) {
	tally := map[string]int{}
	for _, a := range o.acks {
		if keep != nil && !keep(a.key) {
			continue
		}
		why := o.explain(a, rd.Record(a.key))
		if why == "" {
			id := a.v.ID()
			return tally, fmt.Errorf("acknowledged version %s of %q (written on %s, stamp %v) is not current, not superseded by a later acknowledged write and on no discard record",
				hex.EncodeToString(id[:4]), a.key, a.node, a.v.Stamp())
		}
		tally[why]++
	}
	return tally, nil
}

func lwwAfter(a, b occ.Version) bool {
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

func covers(x, w occ.Version) bool {
	return x.ID() != w.ID() && x.Clock().Get(w.Dot().Actor) >= w.Dot().Counter
}

// gcOracle intercepts every simulated disk Delete via watchDisk, independent of
// Node.Collected, and fails if any deleted shard belongs to a version that can
// still win on any node's metadata (up, down, or paused) or is committed later.
type gcOracle struct {
	h        *harness
	off      bool
	residual map[string]bool
	onWrite  func(id env.NodeID, name string)
	deleted  map[storage.VersionID][]string
	refs     map[occ.VersionID]storage.VersionID
	found    []string
}

func newGCOracle(h *harness) *gcOracle {
	return &gcOracle{h: h, deleted: map[storage.VersionID][]string{}, refs: map[occ.VersionID]storage.VersionID{}, residual: map[string]bool{}}
}

type watchDisk struct {
	env.Disk
	id env.NodeID
	o  *gcOracle
}

func (d watchDisk) Delete(name string) error {
	if sid, err := storage.ParseBlobName(name); err == nil {
		d.o.onDelete(d.id, name, sid.Version)
	}
	return d.Disk.Delete(name)
}

func (d watchDisk) Write(name string, data []byte) error {
	err := d.Disk.Write(name, data)
	if d.o.onWrite != nil {
		d.o.onWrite(d.id, name)
	}
	return err
}

func (o *gcOracle) ref(key string, v occ.Version) (storage.VersionID, bool) {
	if id, ok := o.refs[v.ID()]; ok {
		return id, id != 0
	}
	var out storage.VersionID
	if !v.Tombstone() && len(key) > 0 {
		switch occ.Kind(key[0]) {
		case occ.KindManifest:
			if man, err := manifestOf(v.Value()); err == nil {
				out = man.Version
			}
		case occ.KindTag:
			if _, parts, err := occ.ParseKey(key); err == nil && strings.HasPrefix(parts[2], "~stage/") {
				if man, err := node.ManifestOfStaged(v.Value()); err == nil {
					out = man.Version
				}
			}
		}
	}
	o.refs[v.ID()] = out
	return out, out != 0
}

func (o *gcOracle) readers() []occ.Reader {
	var out []occ.Reader
	for _, id := range o.h.ids {
		n, _ := o.h.s.Process(id).(*node.Node)
		if n == nil {
			continue
		}
		if rd, err := n.OCC().Reader(); err == nil {
			out = append(out, rd)
		}
	}
	return out
}

func (o *gcOracle) protector(v storage.VersionID, rds []occ.Reader) (string, bool) {
	for _, rd := range rds {
		for _, k := range rd.Keys() {
			x, ok := rd.Record(k).Version()
			if !ok {
				continue
			}
			if got, ok := o.ref(k, x); !ok || got != v {
				continue
			}
			beaten := false
			for _, other := range rds {
				y, ok := other.Record(k).Version()
				if ok && y.ID() != x.ID() && (covers(y, x) || lwwAfter(y, x)) {
					beaten = true
					break
				}
			}
			if !beaten {
				return k, true
			}
		}
	}
	return "", false
}

func (o *gcOracle) onDelete(id env.NodeID, name string, v storage.VersionID) {
	o.deleted[v] = append(o.deleted[v], string(id)+"|"+name)
	if o.off {
		return
	}
	if k, ok := o.protector(v, o.readers()); ok && !o.residual[k] {
		o.fail(fmt.Errorf("node %s deleted shard %s of version %s, which %q can still make the winner", id, name, v, k))
	}
}

func (o *gcOracle) fail(err error) {
	o.found = append(o.found, err.Error())
	o.h.s.Fail(err)
}

func (o *gcOracle) onCommit(key string, v occ.Version) {
	if o.off {
		return
	}
	if sv, ok := o.ref(key, v); ok && len(o.deleted[sv]) > 0 {
		o.fail(fmt.Errorf("version %s committed as %q after its shards were deleted (%s)", sv, key, o.deleted[sv][0]))
	}
}

func (o *gcOracle) check() error {
	if o.off || len(o.deleted) == 0 {
		return nil
	}
	rds := o.readers()
	type cand struct {
		key string
		v   occ.Version
	}
	byVersion := map[storage.VersionID][]cand{}
	for _, rd := range rds {
		for _, k := range rd.Keys() {
			x, ok := rd.Record(k).Version()
			if !ok {
				continue
			}
			if sv, ok := o.ref(k, x); ok && len(o.deleted[sv]) > 0 {
				byVersion[sv] = append(byVersion[sv], cand{key: k, v: x})
			}
		}
	}
	vs := make([]storage.VersionID, 0, len(byVersion))
	for v := range byVersion {
		vs = append(vs, v)
	}
	slices.Sort(vs)
	for _, v := range vs {
		for _, c := range byVersion[v] {
			if o.residual[c.key] {
				continue
			}
			beaten := false
			for _, other := range rds {
				y, ok := other.Record(c.key).Version()
				if ok && y.ID() != c.v.ID() && (covers(y, c.v) || lwwAfter(y, c.v)) {
					beaten = true
					break
				}
			}
			if !beaten {
				return fmt.Errorf("version %s can still win %q but its shard %s was deleted", v, c.key, o.deleted[v][0])
			}
		}
	}
	return nil
}
