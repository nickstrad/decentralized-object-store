package occ

import (
	"bytes"
	"crypto/sha256"
	"slices"
	"strings"
)

var docMagic = []byte("OCC\x02")

const minRecordSize = 10

var canaryMergePrefersLocal bool

type Doc struct {
	records map[string]Version
	keys    []string
	sorted  bool
	enc     []byte
	dig     [32]byte
}

func NewDoc() *Doc {
	return &Doc{records: map[string]Version{}}
}

func (d *Doc) Record(key string) Record {
	v, ok := d.records[key]
	return Record{v: v, ok: ok}
}

func (d *Doc) Len() int { return len(d.records) }

func (d *Doc) Keys() []string { return slices.Clone(d.sortedKeys()) }

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

func (d *Doc) KeysWithPrefix(prefix string) []string {
	var out []string
	for _, k := range d.sortedKeys() {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

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

func (d *Doc) MergeVersion(key string, v Version) bool {
	return d.mergeVersion(key, v, nil, nil)
}

type replaceFunc func(key string, held, next Record)

type rejectFunc func(key string, held, incoming Version)

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

func (d *Doc) Merge(o *Doc) bool {
	return d.merge(o, nil, nil)
}

func (d *Doc) merge(o *Doc, replace replaceFunc, reject rejectFunc) bool {
	changed := false
	for _, k := range o.sortedKeys() {
		if d.mergeVersion(k, o.records[k], replace, reject) {
			changed = true
		}
	}
	return changed
}

func (d *Doc) Clone() *Doc {
	c := &Doc{records: make(map[string]Version, len(d.records))}
	for _, k := range d.sortedKeys() {
		c.records[k] = d.records[k]
	}
	c.keys, c.sorted = d.keys, true
	c.enc, c.dig = d.enc, d.dig
	return c
}

func (d *Doc) Subset(keys ...string) *Doc {
	c := NewDoc()
	for _, k := range keys {
		if v, ok := d.records[k]; ok {
			c.records[k] = v
		}
	}
	return c
}

func (d *Doc) Stamps() []Stamp {
	keys := d.sortedKeys()
	out := make([]Stamp, len(keys))
	for i, k := range keys {
		out[i] = d.records[k].stamp
	}
	return out
}

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

func (d *Doc) Digest() [32]byte {
	if d.enc == nil {
		d.Encode()
	}
	return d.dig
}

func (d *Doc) Equal(o *Doc) bool {
	return bytes.Equal(d.Encode(), o.Encode())
}

func Decode(b []byte) (*Doc, error) {
	doc, _, err := decodeDoc(b)
	return doc, err
}

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
	if !bytes.Equal(doc.Encode(), b) {
		return nil, d.ops, d.failed("non-canonical document")
	}
	return doc, d.ops, nil
}

func (d *decoder) failed(what string) error {
	d.fail(what)
	return d.err
}
