// Package occ implements optimistic concurrency control for replicated metadata.
//
// Each key holds at most one version on every replica. Local writes check an
// optional precondition (If-Match ETag or If-None-Match) atomically and advance
// the version's causal context and hybrid logical clock (HLC) stamp so a write
// always beats the version it replaced. Concurrent writes accepted on opposite
// sides of a partition converge by last-writer-wins: the higher HLC stamp wins
// (with node ID and version ID breaking ties), independent of arrival order.
//
// Version vectors detect concurrency rather than pick winners. Whenever a merge
// drops a version that the winner never saw — either replacing a held version
// or rejecting an incoming one — the store records the discard in a persisted
// counter, ring tail, and crash-safe outbox emitted to the log and callback.
// Every Store and Node is single-threaded and acts only through internal/env.
package occ

import (
	"bytes"
	"crypto/sha256"
	"slices"
	"strings"
)

// docMagic tags the canonical single-version-per-key document encoding.
var docMagic = []byte("OCC\x02")

// minRecordSize is the smallest encoded (key, version) pair, used to bound
// declared record counts against remaining input bytes.
const minRecordSize = 10

// canaryMergePrefersLocal is a test-only switch that breaks commutativity by
// keeping the local version during merges.
var canaryMergePrefersLocal bool

// Doc is an in-memory map from metadata key to its single winning Version.
// It caches its sorted key list, canonical encoding, and SHA-256 digest.
type Doc struct {
	records map[string]Version
	keys    []string
	sorted  bool
	enc     []byte
	dig     [32]byte
}

// NewDoc returns an empty Doc.
func NewDoc() *Doc {
	return &Doc{records: map[string]Version{}}
}

// Record returns the Record stored at key, or the zero absent Record if missing.
func (d *Doc) Record(key string) Record {
	v, ok := d.records[key]
	return Record{v: v, ok: ok}
}

// Len returns the number of keys present in d, including tombstones.
func (d *Doc) Len() int { return len(d.records) }

// Keys returns a sorted copy of all keys present in d, including tombstones.
func (d *Doc) Keys() []string { return slices.Clone(d.sortedKeys()) }

// sortedKeys returns d's cached sorted key slice, rebuilding it after key-set
// changes so map iteration order never leaks into outputs.
func (d *Doc) sortedKeys() []string {
	if !d.sorted {
		keys := make([]string, 0, len(d.records))
		for k := range d.records {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		d.keys, d.sorted = keys, true
	}
	return d.keys
}

// KeysWithPrefix returns all keys in d starting with prefix, in sorted order.
func (d *Doc) KeysWithPrefix(prefix string) []string {
	var out []string
	for _, k := range d.sortedKeys() {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

// set installs or removes r at key and invalidates cached encoding and key order.
func (d *Doc) set(key string, r Record) {
	_, had := d.records[key]
	if r.ok {
		d.records[key] = r.v
	} else {
		delete(d.records, key)
	}
	if had != r.ok {
		d.sorted, d.keys = false, nil
	}
	d.enc = nil
}

// MergeVersion merges v into key using last-writer-wins and reports whether
// the stored version changed.
func (d *Doc) MergeVersion(key string, v Version) bool {
	return d.mergeVersion(key, v, nil, nil)
}

type replaceFunc func(key string, held, next Record)

type rejectFunc func(key string, held, incoming Version)

// mergeVersion applies last-writer-wins for one key and notifies either
// replace (when the stored version changes) or reject (when an existing version
// wins over or equals incoming).
func (d *Doc) mergeVersion(key string, v Version, replace replaceFunc, reject rejectFunc) bool {
	cur := d.Record(key)
	next := cur.Merge(NewRecord(v))
	if canaryMergePrefersLocal && cur.ok {
		next = cur
	}
	if next.Equal(cur) {
		if reject != nil && cur.ok {
			reject(key, cur.v, v)
		}
		return false
	}
	if replace != nil {
		replace(key, cur, next)
	}
	d.set(key, next)
	return true
}

// Merge folds every key of o into d under last-writer-wins and reports whether
// any key in d changed. Merge is commutative, associative, idempotent, and total.
func (d *Doc) Merge(o *Doc) bool {
	return d.merge(o, nil, nil)
}

// merge iterates o in sorted key order so discard callbacks and side effects
// run deterministically.
func (d *Doc) merge(o *Doc, replace replaceFunc, reject rejectFunc) bool {
	changed := false
	for _, k := range o.sortedKeys() {
		if d.mergeVersion(k, o.records[k], replace, reject) {
			changed = true
		}
	}
	return changed
}

// Clone returns an independent copy of d sharing immutable cached bytes.
func (d *Doc) Clone() *Doc {
	c := &Doc{records: make(map[string]Version, len(d.records))}
	for _, k := range d.sortedKeys() {
		c.records[k] = d.records[k]
	}
	c.keys, c.sorted = d.keys, true
	c.enc, c.dig = d.enc, d.dig
	return c
}

// Subset returns a new Doc containing only the requested keys that exist in d.
func (d *Doc) Subset(keys ...string) *Doc {
	c := NewDoc()
	for _, k := range keys {
		if v, ok := d.records[k]; ok {
			c.records[k] = v
		}
	}
	return c
}

// Stamps returns the HLC stamp of every version in d, in sorted key order.
func (d *Doc) Stamps() []Stamp {
	keys := d.sortedKeys()
	out := make([]Stamp, len(keys))
	for i, k := range keys {
		out[i] = d.records[k].stamp
	}
	return out
}

// MaxStamp returns the highest HLC stamp in d and true, or the zero Stamp and
// false when d is empty.
func (d *Doc) MaxStamp() (Stamp, bool) {
	var m Stamp
	found := false
	for _, s := range d.Stamps() {
		if !found || s.Compare(m) > 0 {
			m, found = s, true
		}
	}
	return m, found
}

// Encode returns a copy of d's canonical binary encoding. Replicas holding the
// same versions always produce byte-identical encodings.
func (d *Doc) Encode() []byte {
	if d.enc == nil {
		var e encoder
		e.raw(docMagic)
		keys := d.sortedKeys()
		e.uvarint(uint64(len(keys)))
		for _, k := range keys {
			e.str(k)
			e.version(d.records[k])
		}
		d.enc = e.b
		d.dig = sha256.Sum256(d.enc)
	}
	return slices.Clone(d.enc)
}

// Digest returns the SHA-256 hash of d's canonical encoding.
func (d *Doc) Digest() [32]byte {
	if d.enc == nil {
		d.Encode()
	}
	return d.dig
}

// Equal reports whether d and o have identical canonical encodings.
func (d *Doc) Equal(o *Doc) bool {
	return bytes.Equal(d.Encode(), o.Encode())
}

// Decode parses a canonical Doc encoding. It rejects out-of-order or repeated
// keys, non-canonical varints, malformed versions, and trailing bytes with
// ErrMalformed.
func Decode(b []byte) (*Doc, error) {
	doc, _, err := decodeDoc(b)
	return doc, err
}

// decodeDoc parses b strictly and returns the operation count used to verify
// linear work bounds on hostile inputs.
func decodeDoc(b []byte) (*Doc, int, error) {
	if !bytes.HasPrefix(b, docMagic) {
		d := &decoder{b: b}
		return nil, d.ops, d.failed("magic")
	}
	d := &decoder{b: b, off: len(docMagic)}
	doc := NewDoc()
	n := d.count(minRecordSize)
	prev := ""
	for i := 0; i < n && d.err == nil; i++ {
		k := d.str()
		// Enforce strictly increasing keys inline so duplicate or unsorted keys
		// fail before decoding further versions.
		if d.err == nil && i > 0 && k <= prev {
			d.fail("key order: a key repeated or out of order")
		}
		v := d.version()
		if d.err == nil {
			doc.records[k] = v
			doc.keys = append(doc.keys, k)
			prev = k
		}
	}
	doc.sorted = true
	d.end()
	if d.err != nil {
		return nil, d.ops, d.err
	}
	d.ops += len(b)
	// Final round-trip check guarantees canonical encoding (for example,
	// rejecting non-minimal varints) so Decode and Encode are strict inverses.
	if !bytes.Equal(doc.Encode(), b) {
		return nil, d.ops, d.failed("non-canonical document")
	}
	return doc, d.ops, nil
}

// failed records what as a decode error and returns it.
func (d *decoder) failed(what string) error {
	d.fail(what)
	return d.err
}
