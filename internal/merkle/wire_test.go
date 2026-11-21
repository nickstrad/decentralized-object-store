package merkle

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
)

func h32(b byte) Hash {
	var h Hash
	for i := range h {
		h[i] = b
	}
	return h
}

func TestWireTable(t *testing.T) {
	inner := [Fanout]Hash{}
	inner[0] = h32(0xaa)
	inner[15] = h32(0xbb)
	cases := []struct {
		name string
		msg  any
		hex  string
	}{
		{"a root request carries the root hash", request{depth: 2, level: 0, root: h32(0x11)},
			"010200" + hex.EncodeToString(bytes.Repeat([]byte{0x11}, 32))},
		{"a level-one request packs one nibble per path in the high half", request{depth: 2, level: 1, paths: []uint64{0x1 << 60, 0xf << 60}},
			"0102010210f0"},
		{"a level-two request packs two nibbles per byte", request{depth: 3, level: 2, paths: []uint64{0xab << 56}},
			"01030201ab"},
		{"a level-three request rounds up to two bytes", request{depth: 3, level: 3, paths: []uint64{0xabc << 52}},
			"01030301abc0"},
		{"an equal reply is one byte", response{kind: msgEqual}, "04"},
		{"an inner reply lists only non-empty children", response{kind: msgInner, level: 0, inner: [][Fanout]Hash{inner}},
			"02000180 01" + hex.EncodeToString(bytes.Repeat([]byte{0xaa}, 32)) + hex.EncodeToString(bytes.Repeat([]byte{0xbb}, 32))},
		{"a leaf reply carries length-prefixed keys", response{kind: msgLeaves, level: 1, leaves: [][]Entry{{{Key: "ab", Hash: h32(0x01)}}, nil}},
			"03010201026162" + hex.EncodeToString(bytes.Repeat([]byte{0x01}, 32)) + "00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			want, err := hex.DecodeString(stripSpaces(c.hex))
			if err != nil {
				t.Fatal(err)
			}
			switch m := c.msg.(type) {
			case request:
				got := encodeRequest(m)
				if !bytes.Equal(got, want) {
					t.Fatalf("encode = %x, want %x", got, want)
				}
				back, err := decodeRequest(got)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(encodeRequest(back), got) {
					t.Fatal("decode then encode is not the identity")
				}
			case response:
				got := encodeResponse(m)
				if !bytes.Equal(got, want) {
					t.Fatalf("encode = %x, want %x", got, want)
				}
				back, err := decodeResponse(got)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(encodeResponse(back), got) {
					t.Fatal("decode then encode is not the identity")
				}
			}
		})
	}
}

func stripSpaces(s string) string {
	return string(bytes.ReplaceAll([]byte(s), []byte(" "), nil))
}

func TestWireMalformed(t *testing.T) {
	zero := make([]byte, 32)
	nonzero := bytes.Repeat([]byte{7}, 32)
	reqs := []struct {
		name string
		b    []byte
		want error
	}{
		{"empty", nil, ErrMalformed},
		{"wrong type", []byte{0x02, 1, 0}, ErrMalformed},
		{"truncated header", []byte{0x01, 1}, ErrMalformed},
		{"depth zero", []byte{0x01, 0, 0}, ErrDepth},
		{"depth over the maximum", []byte{0x01, MaxDepth + 1, 0}, ErrDepth},
		{"level over the depth", []byte{0x01, 2, 3, 1, 0, 0}, ErrDepth},
		{"root request without a root", []byte{0x01, 2, 0, 1}, ErrMalformed},
		{"root request with trailing bytes", append(append([]byte{0x01, 2, 0}, zero...), 0), ErrMalformed},
		{"no paths", []byte{0x01, 2, 1, 0}, ErrMalformed},
		{"too many paths", append([]byte{0x01, 2, 1}, 0x81, 0x80, 0x01), ErrMalformed},
		{"count larger than the bytes", []byte{0x01, 2, 1, 2, 0x10}, ErrMalformed},
		{"trailing path byte", []byte{0x01, 2, 1, 1, 0x10, 0x20}, ErrMalformed},
		{"odd level with a low nibble set", []byte{0x01, 2, 1, 1, 0x11}, ErrMalformed},
		{"paths out of order", []byte{0x01, 2, 1, 2, 0x20, 0x10}, ErrMalformed},
		{"duplicate paths", []byte{0x01, 2, 1, 2, 0x20, 0x20}, ErrMalformed},
		{"varint overflow", []byte{0x01, 2, 1, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01}, ErrMalformed},
	}
	for _, c := range reqs {
		t.Run("request/"+c.name, func(t *testing.T) {
			if _, err := decodeRequest(c.b); !errors.Is(err, c.want) {
				t.Fatalf("decodeRequest(% x) = %v, want %v", c.b, err, c.want)
			}
		})
	}
	resps := []struct {
		name string
		b    []byte
		want error
	}{
		{"empty", nil, ErrMalformed},
		{"unknown kind", []byte{0x09}, ErrMalformed},
		{"equal with trailing bytes", []byte{0x04, 0}, ErrMalformed},
		{"inner without a level", []byte{0x02}, ErrMalformed},
		{"inner level over the maximum", []byte{0x02, MaxDepth + 1, 1, 0, 0}, ErrDepth},
		{"inner with no nodes", []byte{0x02, 0, 0}, ErrMalformed},
		{"inner with a short bitmap", []byte{0x02, 0, 1, 0x80}, ErrMalformed},
		{"inner with a missing hash", []byte{0x02, 0, 1, 0x80, 0x00}, ErrMalformed},
		{"inner with a present zero hash", append([]byte{0x02, 0, 1, 0x80, 0x00}, zero...), ErrMalformed},
		{"inner with trailing bytes", append(append([]byte{0x02, 0, 1, 0x80, 0x00}, nonzero...), 1), ErrMalformed},
		{"inner count larger than the bytes", []byte{0x02, 0, 3, 0, 0}, ErrMalformed},
		{"inner bitmap truncated after a full node", append(append([]byte{0x02, 0, 2, 0x80, 0x00}, nonzero...), 0x00), ErrMalformed},
		{"leaves with no nodes", []byte{0x03, 1, 0}, ErrMalformed},
		{"leaves over the entry limit", []byte{0x03, 1, 1, 0x81, 0x40}, ErrMalformed},
		{"leaf entry count larger than the bytes", []byte{0x03, 1, 1, 2, 0}, ErrMalformed},
		{"leaf key over the length limit", append([]byte{0x03, 1, 1, 1, 0x81, 0x08}, nonzero...), ErrMalformed},
		{"leaf key truncated", []byte{0x03, 1, 1, 1, 5, 'a'}, ErrMalformed},
		{"leaf hash truncated", []byte{0x03, 1, 1, 1, 1, 'a', 1}, ErrMalformed},
		{"leaf keys out of order", append(append(append([]byte{0x03, 1, 1, 2, 1, 'b'}, nonzero...), 1, 'a'), nonzero...), ErrMalformed},
		{"leaf keys duplicated", append(append(append([]byte{0x03, 1, 1, 2, 1, 'a'}, nonzero...), 1, 'a'), nonzero...), ErrMalformed},
		{"leaves with trailing bytes", []byte{0x03, 1, 1, 0, 0}, ErrMalformed},
	}
	for _, c := range resps {
		t.Run("response/"+c.name, func(t *testing.T) {
			if _, err := decodeResponse(c.b); !errors.Is(err, c.want) {
				t.Fatalf("decodeResponse(% x) = %v, want %v", c.b, err, c.want)
			}
		})
	}
}

func FuzzMerkleWire(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0x04})
	f.Add(encodeRequest(request{depth: 2, level: 0, root: h32(1)}))
	f.Add(encodeRequest(request{depth: 2, level: 1, paths: []uint64{0, 1 << 60}}))
	f.Add(encodeRequest(request{depth: 2, level: 2, paths: []uint64{0x12 << 56}}))
	f.Add(encodeResponse(response{kind: msgInner, level: 0, inner: [][Fanout]Hash{{h32(1), h32(2)}}}))
	f.Add(encodeResponse(response{kind: msgLeaves, level: 2, leaves: [][]Entry{{{Key: "a", Hash: h32(3)}}}}))
	f.Add([]byte{0x02, 0, 0xff, 0xff, 0x03})
	tr, err := New(2)
	if err != nil {
		f.Fatal(err)
	}
	for i := 0; i < 64; i++ {
		if err := tr.Set(string(rune('a'+i%26))+string(rune('a'+i/26)), Sum([]byte{byte(i)})); err != nil {
			f.Fatal(err)
		}
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			return
		}
		if req, err := decodeRequest(data); err == nil {
			if !bytes.Equal(encodeRequest(req), data) {
				t.Fatalf("request % x does not re-encode to itself", data)
			}
		} else if !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrDepth) {
			t.Fatalf("decodeRequest error %v", err)
		}
		if resp, err := decodeResponse(data); err == nil {
			if !bytes.Equal(encodeResponse(resp), data) {
				t.Fatalf("response % x does not re-encode to itself", data)
			}
		} else if !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrDepth) {
			t.Fatalf("decodeResponse error %v", err)
		}
		if out, err := tr.Answer(data); err == nil {
			if _, err := decodeResponse(out); err != nil {
				t.Fatalf("Answer produced an undecodable reply: %v", err)
			}
		} else if !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrDepth) {
			t.Fatalf("Answer error %v", err)
		}
		for steps := 0; steps < 3; steps++ {
			s := tr.Diff(Limits{Frontier: 4})
			for i := 0; i < steps; i++ {
				req, ok := s.Request()
				if !ok {
					break
				}
				other := tr.Snapshot()
				other.Delete("aa")
				resp, err := other.Answer(req)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.Feed(resp); err != nil {
					t.Fatal(err)
				}
			}
			if _, ok := s.Request(); !ok {
				continue
			}
			if err := s.Feed(data); err != nil && !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrDepth) {
				t.Fatalf("Feed error %v", err)
			}
			for n := 0; n < 16; n++ {
				if _, ok := s.Request(); !ok {
					break
				}
				s.Fail(ErrMalformed)
			}
			if !s.Done() {
				t.Fatal("session not done")
			}
		}
	})
}
