package occ

import (
	"bytes"
	"crypto/sha256"
	"slices"
	"strings"
)

var docMagic = []byte("OCC\x01")

type Doc struct {
	records map[string]Record
	scalars map[string]Scalar
	enc     []byte
	dig     [32]byte
}

func NewDoc() *Doc {
	return &Doc{records: map[string]Record{}, scalars: map[string]Scalar{}}
}

func (d *Doc) Record(key string) Record { return d.records[key] }

func (d *Doc) Scalar(key string) (Scalar, bool) {
	s, ok := d.scalars[key]
	if !ok {
		return Scalar{}, false
	}
	return Scalar{Value: slices.Clone(s.Value), Stamp: s.Stamp}, true
}

func (d *Doc) Len() int { return len(d.records) + len(d.scalars) }

func (d *Doc) Keys() []string {
	keys := make([]string, 0, len(d.records))
	for k := range d.records {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (d *Doc) ScalarKeys() []string {
	keys := make([]string, 0, len(d.scalars))
	for k := range d.scalars {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func (d *Doc) KeysWithPrefix(prefix string) []string {
	var out []string
	for _, k := range d.Keys() {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

func (d *Doc) setRecord(key string, r Record) {
	if r.Len() == 0 {
		delete(d.records, key)
	} else {
		d.records[key] = r
	}
	d.enc = nil
}

func (d *Doc) MergeRecord(key string, r Record) bool {
	cur := d.records[key]
	next := cur.Merge(r)
	if next.Equal(cur) {
		return false
	}
	d.setRecord(key, next)
	return true
}

func (d *Doc) MergeScalar(key string, s Scalar) bool {
	cur, ok := d.scalars[key]
	if ok {
		next := cur.Merge(s)
		if next.Compare(cur) == 0 {
			return false
		}
		d.scalars[key] = next
	} else {
		d.scalars[key] = Scalar{Value: slices.Clone(s.Value), Stamp: s.Stamp}
	}
	d.enc = nil
	return true
}

func (d *Doc) Merge(o *Doc) bool {
	changed := false
	for _, k := range o.Keys() {
		if d.MergeRecord(k, o.records[k]) {
			changed = true
		}
	}
	for _, k := range o.ScalarKeys() {
		if d.MergeScalar(k, o.scalars[k]) {
			changed = true
		}
	}
	return changed
}

func (d *Doc) Clone() *Doc {
	c := NewDoc()
	for _, k := range d.Keys() {
		c.records[k] = d.records[k]
	}
	for _, k := range d.ScalarKeys() {
		c.scalars[k] = d.scalars[k]
	}
	return c
}

func (d *Doc) Subset(keys ...string) *Doc {
	c := NewDoc()
	for _, k := range keys {
		if r, ok := d.records[k]; ok {
			c.records[k] = r
		}
		if s, ok := d.scalars[k]; ok {
			c.scalars[k] = s
		}
	}
	return c
}

func (d *Doc) MaxStamp() Stamp {
	var m Stamp
	for _, k := range d.Keys() {
		for _, v := range d.records[k].siblings {
			if v.stamp.Compare(m) > 0 {
				m = v.stamp
			}
		}
	}
	for _, k := range d.ScalarKeys() {
		if s := d.scalars[k].Stamp; s.Compare(m) > 0 {
			m = s
		}
	}
	return m
}

func (d *Doc) Encode() []byte {
	if d.enc == nil {
		var e encoder
		e.raw(docMagic)
		keys := d.Keys()
		e.uvarint(uint64(len(keys)))
		for _, k := range keys {
			e.str(k)
			e.record(d.records[k])
		}
		skeys := d.ScalarKeys()
		e.uvarint(uint64(len(skeys)))
		for _, k := range skeys {
			s := d.scalars[k]
			e.str(k)
			e.bytes(s.Value)
			e.stamp(s.Stamp)
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
	if !bytes.HasPrefix(b, docMagic) {
		return nil, (&decoder{b: b}).failed("magic")
	}
	d := &decoder{b: b, off: len(docMagic)}
	doc := NewDoc()
	n := d.count(2)
	for i := 0; i < n && d.err == nil; i++ {
		k := d.str()
		r := d.record()
		if d.err == nil {
			doc.records[k] = r
		}
	}
	m := d.count(3)
	for i := 0; i < m && d.err == nil; i++ {
		k := d.str()
		v := d.bytes()
		st := d.stamp()
		if d.err == nil {
			doc.scalars[k] = Scalar{Value: slices.Clone(v), Stamp: st}
		}
	}
	d.end()
	if d.err != nil {
		return nil, d.err
	}
	if len(doc.records) != n || len(doc.scalars) != m || !bytes.Equal(doc.Encode(), b) {
		return nil, d.failed("non-canonical document")
	}
	return doc, nil
}

func (d *decoder) failed(what string) error {
	d.fail(what)
	return d.err
}
