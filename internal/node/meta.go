package node

import (
	"fmt"
	"slices"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/merkle"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

var (
	canaryNoDiscardRecount bool
	canaryIgnoreTombstones bool
	canaryNoQuotaAudit     bool
)

// metaRepairer implements merkle.Repairer over the node's entire OCC document
// (including tombstones and system keys) so one Merkle tree discovers newly
// created buckets as well as missed updates.
type metaRepairer struct{ n *Node }

func (m *metaRepairer) Keys() ([]string, error) {
	rd, err := m.n.occ.Reader()
	if err != nil {
		return nil, err
	}
	keys := rd.Keys()
	if canaryIgnoreTombstones {
		keys = slices.DeleteFunc(keys, func(k string) bool { return !rd.Record(k).Live() })
	}
	return keys, nil
}

func (m *metaRepairer) Read(key string) ([]byte, error) {
	rd, err := m.n.occ.Reader()
	if err != nil {
		return nil, err
	}
	if canaryIgnoreTombstones && !rd.Record(key).Live() {
		return nil, fmt.Errorf("node: read %q: %w", key, env.ErrNotFound)
	}
	return encodeRecord(rd, key)
}

func (m *metaRepairer) Valid(key string, _ merkle.Hash) bool {
	_, _, err := occ.ParseKey(key)
	return err == nil
}

func (m *metaRepairer) Fetch(peer env.NodeID, key string, done func([]byte, error)) {
	var w wbuf
	w.byte(opMetaFetch)
	w.str(key)
	m.n.net.Request(peer, w.b, m.n.cfg.RPCTimeout, done)
}

// Put merges one fetched single-key OCC document using last-writer-wins. If the
// fetched version loses to the local version, Put returns ErrStale so the
// Merkle worker does not record the losing hash.
func (m *metaRepairer) Put(key string, data []byte) error {
	n := m.n
	doc, err := occ.Decode(data)
	if err != nil {
		return fmt.Errorf("node: repair %q: %w", key, err)
	}
	if keys := doc.Keys(); len(keys) != 1 || keys[0] != key {
		return fmt.Errorf("node: repair %q: document holds %d keys: %w", key, len(keys), ErrMalformed)
	}
	incoming := doc.Record(key)
	rd, err := n.occ.Reader()
	if err != nil {
		return err
	}
	if canaryIgnoreTombstones && incoming.Live() && rd.Record(key).Present() && !rd.Record(key).Live() {
		v, _ := incoming.Version()
		if _, err := n.occ.Put(key, v.Value(), occ.Unconditional()); err != nil {
			return err
		}
		return nil
	}
	changed, err := n.occ.Merge(doc)
	if err != nil {
		return fmt.Errorf("node: repair %q: %w", key, err)
	}
	if !changed && !rd.Record(key).Equal(incoming) {
		n.stats.MetaStale++
		return fmt.Errorf("node: repair %q: %w", key, ErrStale)
	}
	n.stats.MetaMerged++
	return nil
}

const (
	opMetaFetch    byte = 1
	opPointer      byte = 2
	opPurge        byte = 3
	opVerifyShards byte = 4
)

// serveNode dispatches node-level RPCs on chNode: metadata record fetches for
// Merkle repair, migration pointer-view queries, and explicit dead-version
// shard purges.
func (n *Node) serveNode(from env.NodeID, req []byte, reply env.Reply) {
	r := rbuf{b: req}
	op := r.byte()
	switch op {
	case opMetaFetch:
		key := r.str()
		if err := r.end("meta fetch"); err != nil {
			reply(nil, err)
			return
		}
		b, err := (&metaRepairer{n: n}).Read(key)
		reply(b, err)
	case opPointer:
		bucket := r.str()
		if err := r.end("pointer"); err != nil {
			reply(nil, err)
			return
		}
		rd, err := n.reader()
		if err != nil {
			reply(nil, err)
			return
		}
		reply(n.pointerView(rd, bucket).encode(), nil)
	case opPurge:
		bucket := r.str()
		cnt := r.uvarint()
		if cnt > uint64(len(req)) {
			r.fail("purge count")
		}
		var vs []storage.VersionID
		for i := uint64(0); i < cnt && r.err == nil; i++ {
			vs = append(vs, storage.VersionID(r.uvarint()))
		}
		if err := r.end("purge"); err != nil {
			reply(nil, err)
			return
		}
		c, err := n.purge(bucket, vs)
		if err != nil {
			reply(nil, err)
			return
		}
		reply(encodeCount(c), nil)
	case opVerifyShards:
		bucket, reqExp, docBytes, reqs, err := decodeVerifyShardsReq(&r)
		if err != nil {
			reply(nil, err)
			return
		}
		res, err := n.verifyShards(bucket, reqExp, docBytes, reqs)
		if err != nil {
			reply(nil, err)
			return
		}
		reply(encodeVerifyShardsResp(res), nil)
	default:
		n.stats.Malformed++
		reply(nil, fmt.Errorf("node %s: op %d from %s: %w", n.e.ID, op, from, ErrMalformed))
	}
}

func encodeCount(c int) []byte {
	var w wbuf
	w.uvarint(uint64(c))
	return w.b
}
