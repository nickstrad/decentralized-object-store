package merkle

import (
	"encoding/binary"
	"fmt"
	"math/bits"
)

// Wire message type tags for the diff protocol:
//   - msgRequest (0x01): depth, level, and either root hash (level 0) or
//     strictly increasing paths plus an optional leaf resume key.
//   - msgInner (0x02): non-empty child hashes for requested inner nodes,
//     packed behind a 16-bit big-endian child bitmap per node.
//   - msgLeaves (0x03): complete entry lists for one or more requested leaves.
//   - msgEqual (0x04): single-byte reply meaning the peers' roots match.
//   - msgCut (0x05): like msgLeaves, but the last leaf was cut at MaxReplyBytes
//     and continues strictly after its final key.
const (
	msgRequest byte = 0x01
	msgInner   byte = 0x02
	msgLeaves  byte = 0x03
	msgEqual   byte = 0x04
	msgCut     byte = 0x05
)

// Wire size limits shared by Tree.Answer, Session, and the decoders:
//   - MaxReplyBytes: hard byte ceiling on every reply Tree.Answer emits (1 MiB).
//   - MaxRequestNodes: maximum nodes per request or reply, derived so a full
//     inner reply (4-byte header + 514 bytes per node) never exceeds MaxReplyBytes.
const (
	MaxReplyBytes   = 1 << 20
	MaxRequestNodes = (MaxReplyBytes - 4) / (2 + Fanout*32)
	maxEntryBytes   = 2 + MaxKeyLen + 32
	minCutReply     = MaxReplyBytes - 2*maxEntryBytes
)

type request struct {
	depth  int
	level  int
	root   Hash
	paths  []uint64
	resume bool
	after  string
}

type response struct {
	kind    byte
	level   int
	more    bool
	bitmaps []uint16
	hashes  []Hash
	leaves  [][]Entry
}

// pathBytes returns the byte width holding level 4-bit nibbles (rounded up).
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
	// Only leaf-level requests that resume a cut leaf carry the suffix, keeping
	// unresumed requests free of extra flag bytes.
	if r.level != r.depth || !r.resume {
		return b
	}
	b = binary.AppendUvarint(b, uint64(len(r.after)))
	return append(b, r.after...)
}

// appendKids packs one inner node's non-zero children into r's flat bitmap and
// hash slices, using bit 15-i for child i.
func appendKids(r *response, kids *[Fanout]Hash) {
	var bitmap uint16
	for i, h := range kids {
		if !h.IsZero() {
			bitmap |= 1 << (Fanout - 1 - i)
			r.hashes = append(r.hashes, h)
		}
	}
	r.bitmaps = append(r.bitmaps, bitmap)
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

func uvarintLen(n int) int { return uvarintLen64(uint64(n)) }

// uvarintLen64 computes the canonical unsigned varint byte length without
// allocating.
func uvarintLen64(v uint64) int {
	l := 1
	for ; v >= 0x80; v >>= 7 {
		l++
	}
	return l
}

// entryBytes returns the exact wire size of one leaf entry.
func entryBytes(e Entry) int { return uvarintLen(len(e.Key)) + len(e.Key) + len(e.Hash) }

func encodeResponse(r response) []byte {
	switch r.kind {
	case msgInner:
		b := []byte{msgInner, byte(r.level)}
		b = binary.AppendUvarint(b, uint64(len(r.bitmaps)))
		k := 0
		for _, bm := range r.bitmaps {
			b = binary.BigEndian.AppendUint16(b, bm)
			for i := 0; i < bits.OnesCount16(bm); i++ {
				b = append(b, r.hashes[k][:]...)
				k++
			}
		}
		return b
	case msgLeaves:
		kind := msgLeaves
		if r.more {
			kind = msgCut
		}
		b := []byte{kind, byte(r.level)}
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
	// Reject non-minimal (overlong) varints so every accepted message has a
	// single canonical byte representation.
	if n <= 0 || n != uvarintLen64(v) {
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

// decodeRequest parses and canonically validates a wire request, returning
// ErrDepth for out-of-range depths or levels and ErrMalformed otherwise.
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
	// Non-leaf requests have no optional suffix, so their remaining length must
	// match n*w exactly; leaf requests may append a resume key.
	if uint64(len(r.b)) < n*uint64(w) || (level != depth && uint64(len(r.b)) != n*uint64(w)) {
		return request{}, malformed("request path bytes")
	}
	req.paths = make([]uint64, 0, n)
	for i := uint64(0); i < n; i++ {
		var x [8]byte
		copy(x[:], r.take(w))
		p := binary.BigEndian.Uint64(x[:])
		// For odd levels, the unused low nibble of the last path byte must be zero.
		if p != prefix(p, level) {
			return request{}, malformed("non-canonical path")
		}
		if len(req.paths) > 0 && p <= req.paths[len(req.paths)-1] {
			return request{}, malformed("paths out of order")
		}
		req.paths = append(req.paths, p)
	}
	if len(r.b) == 0 {
		return req, nil
	}
	kl := r.uvarint()
	if r.bad || kl > MaxKeyLen {
		return request{}, malformed("resume key length")
	}
	req.resume = true
	req.after = string(r.take(int(kl)))
	if r.bad || len(r.b) != 0 {
		return request{}, malformed("request length")
	}
	return req, nil
}

// decodeResponse parses and canonically validates a wire reply while keeping
// total allocations bounded by 4*len(b) + 64 KiB. It returns ErrDepth for a
// level above MaxDepth and ErrMalformed for any structural violation.
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
	if kind != msgInner && kind != msgLeaves && kind != msgCut {
		return response{}, malformed("unknown response kind")
	}
	more := kind == msgCut
	if more {
		kind = msgLeaves
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
	resp := response{kind: kind, level: level, more: more}
	if kind == msgInner {
		// Pre-check minimum byte width (2 bytes per bitmap) and size flat slices
		// directly from the remaining input so empty nodes cannot amplify memory.
		if uint64(len(r.b)) < 2*n {
			return response{}, malformed("inner nodes truncated")
		}
		resp.bitmaps = make([]uint16, 0, n)
		resp.hashes = make([]Hash, 0, (uint64(len(r.b))-2*n)/uint64(len(Hash{})))
		for j := uint64(0); j < n; j++ {
			raw := r.take(2)
			if r.bad {
				return response{}, malformed("inner bitmap truncated")
			}
			bitmap := binary.BigEndian.Uint16(raw)
			if len(r.b) < 32*bits.OnesCount16(bitmap) {
				return response{}, malformed("inner hashes truncated")
			}
			for i := 0; i < bits.OnesCount16(bitmap); i++ {
				h := r.hash()
				// Empty subtrees are omitted from the bitmap; a set bit carrying
				// the zero hash would break canonical encoding.
				if h.IsZero() {
					return response{}, malformed("present child with the empty hash")
				}
				resp.hashes = append(resp.hashes, h)
			}
			resp.bitmaps = append(resp.bitmaps, bitmap)
		}
	} else {
		// Copy the leaf payload into one string so decoded Entry keys share a
		// single backing buffer; Session clones keys it retains across replies.
		keys := string(r.b)
		base := len(r.b)
		resp.leaves = make([][]Entry, 0, n)
		for j := uint64(0); j < n; j++ {
			m := r.uvarint()
			// Each entry requires at least 33 bytes (1-byte length + 32-byte hash).
			if r.bad || m > MaxLeafEntries || m > uint64(len(r.b))/33 {
				return response{}, malformed("leaf entry count")
			}
			var es []Entry
			if m > 0 {
				es = make([]Entry, 0, m)
			}
			for k := uint64(0); k < m; k++ {
				kl := r.uvarint()
				if r.bad || kl > MaxKeyLen {
					return response{}, malformed("leaf key length")
				}
				off := base - len(r.b)
				r.take(int(kl))
				h := r.hash()
				if r.bad {
					return response{}, malformed("leaf entry truncated")
				}
				key := keys[off : off+int(kl)]
				if len(es) > 0 && key <= es[len(es)-1].Key {
					return response{}, malformed("leaf keys out of order")
				}
				es = append(es, Entry{Key: key, Hash: h})
			}
			resp.leaves = append(resp.leaves, es)
		}
		if resp.more && len(resp.leaves[len(resp.leaves)-1]) == 0 {
			return response{}, malformed("cut at an empty leaf")
		}
	}
	if r.bad || len(r.b) != 0 {
		return response{}, malformed("response length")
	}
	return resp, nil
}
