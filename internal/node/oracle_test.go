package node_test

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/node"
	"github.com/nickstrad/object-store/internal/nodetest"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/storage"
)

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
				if ok && y.ID() != x.ID() && (nodetest.Covers(y, x) || nodetest.LWWAfter(y, x)) {
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
				if ok && y.ID() != c.v.ID() && (nodetest.Covers(y, c.v) || nodetest.LWWAfter(y, c.v)) {
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
