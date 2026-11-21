package merkle

import (
	"encoding/binary"
	"fmt"
	"math/bits"
)

const (
	msgRequest byte = 0x01
	msgInner   byte = 0x02
	msgLeaves  byte = 0x03
	msgEqual   byte = 0x04
)

const (
	MaxRequestNodes = 1 << 14
	MaxReplyBytes   = 1 << 20
)

type request struct {
	depth int
	level int
	root  Hash
	paths []uint64
}

type response struct {
	kind   byte
	level  int
	inner  [][Fanout]Hash
	leaves [][]Entry
}

func pathBytes(level int) int { return (level + 1) / 2 }

func encodeRequest(r request) []byte {
	b := []byte{msgRequest, byte(r.depth), byte(r.level)}
	if r.level == 0 {
		return append(b, r.root[:]...)
	}
	b = binary.AppendUvarint(b, uint64(len(r.paths)))
	w := pathBytes(r.level)
	for _, p := range r.paths {
		var x [8]byte
		binary.BigEndian.PutUint64(x[:], p)
		b = append(b, x[:w]...)
	}
	return b
}

func encodeInnerNode(b []byte, kids *[Fanout]Hash) []byte {
	var bitmap uint16
	for i, h := range kids {
		if !h.IsZero() {
			bitmap |= 1 << (Fanout - 1 - i)
		}
	}
	b = binary.BigEndian.AppendUint16(b, bitmap)
	for _, h := range kids {
		if !h.IsZero() {
			b = append(b, h[:]...)
		}
	}
	return b
}

func encodeLeaf(b []byte, es []Entry) []byte {
	b = binary.AppendUvarint(b, uint64(len(es)))
	for _, e := range es {
		b = binary.AppendUvarint(b, uint64(len(e.Key)))
		b = append(b, e.Key...)
		b = append(b, e.Hash[:]...)
	}
	return b
}

func leafSize(es []Entry) int {
	n := binary.MaxVarintLen64
	for _, e := range es {
		n += binary.MaxVarintLen64 + len(e.Key) + len(e.Hash)
	}
	return n
}

func encodeResponse(r response) []byte {
	switch r.kind {
	case msgInner:
		b := []byte{msgInner, byte(r.level)}
		b = binary.AppendUvarint(b, uint64(len(r.inner)))
		for i := range r.inner {
			b = encodeInnerNode(b, &r.inner[i])
		}
		return b
	case msgLeaves:
		b := []byte{msgLeaves, byte(r.level)}
		b = binary.AppendUvarint(b, uint64(len(r.leaves)))
		for _, es := range r.leaves {
			b = encodeLeaf(b, es)
		}
		return b
	default:
		return []byte{msgEqual}
	}
}

type reader struct {
	b   []byte
	bad bool
}

func (r *reader) u8() byte {
	if len(r.b) < 1 {
		r.bad = true
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

func (r *reader) take(n int) []byte {
	if n < 0 || len(r.b) < n {
		r.bad = true
		return nil
	}
	v := r.b[:n]
	r.b = r.b[n:]
	return v
}

func (r *reader) uvarint() uint64 {
	v, n := binary.Uvarint(r.b)
	if n <= 0 || n != len(binary.AppendUvarint(nil, v)) {
		r.bad = true
		return 0
	}
	r.b = r.b[n:]
	return v
}

func (r *reader) hash() Hash {
	var h Hash
	copy(h[:], r.take(len(h)))
	return h
}

func malformed(what string) error {
	return fmt.Errorf("merkle: decode: %s: %w", what, ErrMalformed)
}

func decodeRequest(b []byte) (request, error) {
	r := reader{b: b}
	if r.u8() != msgRequest || r.bad {
		return request{}, malformed("not a request")
	}
	depth, level := int(r.u8()), int(r.u8())
	if r.bad {
		return request{}, malformed("short request header")
	}
	if depth < MinDepth || depth > MaxDepth {
		return request{}, fmt.Errorf("merkle: decode: request depth %d: %w", depth, ErrDepth)
	}
	if level > depth {
		return request{}, fmt.Errorf("merkle: decode: request level %d below depth %d: %w", level, depth, ErrDepth)
	}
	req := request{depth: depth, level: level}
	if level == 0 {
		req.root = r.hash()
		if r.bad || len(r.b) != 0 {
			return request{}, malformed("root request length")
		}
		return req, nil
	}
	n := r.uvarint()
	if r.bad || n == 0 || n > MaxRequestNodes {
		return request{}, malformed("request node count")
	}
	w := pathBytes(level)
	if uint64(len(r.b)) != n*uint64(w) {
		return request{}, malformed("request path bytes")
	}
	req.paths = make([]uint64, 0, n)
	for i := uint64(0); i < n; i++ {
		var x [8]byte
		copy(x[:], r.take(w))
		p := binary.BigEndian.Uint64(x[:])
		if p != prefix(p, level) {
			return request{}, malformed("non-canonical path")
		}
		if len(req.paths) > 0 && p <= req.paths[len(req.paths)-1] {
			return request{}, malformed("paths out of order")
		}
		req.paths = append(req.paths, p)
	}
	return req, nil
}

func decodeResponse(b []byte) (response, error) {
	r := reader{b: b}
	kind := r.u8()
	if r.bad {
		return response{}, malformed("empty response")
	}
	if kind == msgEqual {
		if len(r.b) != 0 {
			return response{}, malformed("equal response length")
		}
		return response{kind: msgEqual}, nil
	}
	if kind != msgInner && kind != msgLeaves {
		return response{}, malformed("unknown response kind")
	}
	level := int(r.u8())
	n := r.uvarint()
	if r.bad {
		return response{}, malformed("short response header")
	}
	if level > MaxDepth {
		return response{}, fmt.Errorf("merkle: decode: response level %d: %w", level, ErrDepth)
	}
	if n == 0 || n > MaxRequestNodes {
		return response{}, malformed("response node count")
	}
	resp := response{kind: kind, level: level}
	if kind == msgInner {
		if uint64(len(r.b)) < 2*n {
			return response{}, malformed("inner nodes truncated")
		}
		resp.inner = make([][Fanout]Hash, n)
		for j := range resp.inner {
			bitmap := binary.BigEndian.Uint16(r.take(2))
			if len(r.b) < 32*bits.OnesCount16(bitmap) {
				return response{}, malformed("inner hashes truncated")
			}
			for i := 0; i < Fanout; i++ {
				if bitmap&(1<<(Fanout-1-i)) == 0 {
					continue
				}
				h := r.hash()
				if h.IsZero() {
					return response{}, malformed("present child with the empty hash")
				}
				resp.inner[j][i] = h
			}
		}
	} else {
		resp.leaves = make([][]Entry, 0, min(n, uint64(len(r.b))))
		for j := uint64(0); j < n; j++ {
			m := r.uvarint()
			if r.bad || m > MaxLeafEntries || m > uint64(len(r.b))/33 {
				return response{}, malformed("leaf entry count")
			}
			es := make([]Entry, 0, m)
			for k := uint64(0); k < m; k++ {
				kl := r.uvarint()
				if r.bad || kl > MaxKeyLen {
					return response{}, malformed("leaf key length")
				}
				key := string(r.take(int(kl)))
				h := r.hash()
				if r.bad {
					return response{}, malformed("leaf entry truncated")
				}
				if len(es) > 0 && key <= es[len(es)-1].Key {
					return response{}, malformed("leaf keys out of order")
				}
				es = append(es, Entry{Key: key, Hash: h})
			}
			resp.leaves = append(resp.leaves, es)
		}
	}
	if r.bad || len(r.b) != 0 {
		return response{}, malformed("response length")
	}
	return resp, nil
}
