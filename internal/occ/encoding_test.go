package occ_test

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/occ"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata golden files")

type raw struct{ b []byte }

func (r *raw) u(x uint64) *raw             { r.b = binary.AppendUvarint(r.b, x); return r }
func (r *raw) i(x int64) *raw              { r.b = binary.AppendVarint(r.b, x); return r }
func (r *raw) s(x string) *raw             { r.u(uint64(len(x))); r.b = append(r.b, x...); return r }
func (r *raw) by(x ...byte) *raw           { r.b = append(r.b, x...); return r }
func (r *raw) magic() *raw                 { return r.by('O', 'C', 'C', 1) }
func (r *raw) stamp() *raw                 { return r.i(0).u(0).s("") }
func (r *raw) dot(n string, c uint64) *raw { return r.s(n).u(1).u(c) }

func goldenDoc() *occ.Doc {
	a := occ.Actor{Node: "node-a", Epoch: 0x1111}
	b := occ.Actor{Node: "node-b", Epoch: 0x2222}
	base := occ.NewVersion(occ.Dot{Actor: a, Counter: 1}, occ.VersionVector{}, false, []byte("base"), occ.Stamp{Wall: 1000, Logical: 0, Node: "node-a"})
	left := occ.NewVersion(occ.Dot{Actor: a, Counter: 2}, base.Clock(), false, []byte("left"), occ.Stamp{Wall: 2000, Logical: 1, Node: "node-a"})
	right := occ.NewVersion(occ.Dot{Actor: b, Counter: 1}, base.Clock(), true, nil, occ.Stamp{Wall: -5, Logical: 3, Node: "node-b"})
	d := occ.NewDoc()
	d.MergeRecord("tags/x", occ.NewRecord(base))
	d.MergeRecord("obj/y", occ.NewRecord(left))
	d.MergeRecord("obj/y", occ.NewRecord(right))
	d.MergeScalar("route/b", occ.Scalar{Value: []byte("region-a"), Stamp: occ.Stamp{Wall: 7, Logical: 0, Node: "node-b"}})
	d.MergeScalar("ctype/b/o", occ.Scalar{Value: nil, Stamp: occ.Stamp{Wall: 8, Logical: 2, Node: "node-a"}})
	return d
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.WriteFile(path, []byte(hex.EncodeToString(got)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s (run with -update-golden to create it): %v", path, err)
	}
	if hex.EncodeToString(got) != strings.TrimSpace(string(want)) {
		t.Fatalf("encoding of %s changed:\n got %x\nwant %s", name, got, want)
	}
}

func TestCanonicalEncoding(t *testing.T) {
	d := goldenDoc()
	enc := d.Encode()
	golden(t, "doc-v1.golden", enc)
	golden(t, "state-v1.golden", occ.EncodeStateBlob(0x1111, enc))
	golden(t, "op-v1.golden", occ.EncodeOpRequest(occ.OpPut, "obj/y", occ.IfMatch(d.Record("obj/y").ETag()), []byte("v")))
	back, err := occ.Decode(enc)
	if err != nil || !bytes.Equal(back.Encode(), enc) || back.Digest() != d.Digest() {
		t.Fatalf("round trip: %v", err)
	}
	other := occ.NewDoc()
	other.MergeScalar("ctype/b/o", occ.Scalar{Stamp: occ.Stamp{Wall: 8, Logical: 2, Node: "node-a"}})
	other.Merge(back)
	other.Merge(d)
	if !bytes.Equal(other.Encode(), enc) || !other.Equal(d) {
		t.Fatal("equal states built in a different order encode differently")
	}
	if occ.NewDoc().Len() != 0 || d.Len() != 4 {
		t.Fatal("len")
	}
	enc[len(enc)-1] ^= 0xff
	if bytes.Equal(enc, d.Encode()) {
		t.Fatal("Encode exposed its cached buffer")
	}
	epoch, sdoc, err := occ.DecodeStateBlob(occ.EncodeStateBlob(0x1111, d.Encode()))
	if err != nil || epoch != 0x1111 || !sdoc.Equal(d) {
		t.Fatalf("state blob round trip: %v", err)
	}
}

func versionBytes(r *raw, node string, counter uint64, ctx [][2]uint64, flag byte, value string) *raw {
	r.dot(node, counter)
	r.u(uint64(len(ctx)))
	for _, e := range ctx {
		r.s(string(rune('a' + e[0]))).u(1).u(e[1])
	}
	r.by(flag).stamp()
	return r.s(value)
}

func TestDecodeMalformedTable(t *testing.T) {
	one := func(build func(r *raw)) []byte {
		r := (&raw{}).magic().u(1).s("k").u(1)
		build(r)
		return r.u(0).b
	}
	valid := one(func(r *raw) { versionBytes(r, "a", 1, nil, 0, "v") })
	if _, err := occ.Decode(valid); err != nil {
		t.Fatalf("the base case must decode: %v", err)
	}
	v1 := occ.NewVersion(occ.Dot{Actor: occ.Actor{Node: "a", Epoch: 1}, Counter: 1}, occ.VersionVector{}, false, []byte("x"), occ.Stamp{})
	v2 := occ.NewVersion(occ.Dot{Actor: occ.Actor{Node: "b", Epoch: 1}, Counter: 1}, occ.VersionVector{}, false, []byte("y"), occ.Stamp{})
	first, second := "a", "b"
	if v2.ID().Compare(v1.ID()) < 0 {
		first, second = "b", "a"
	}
	vals := map[string]string{"a": "x", "b": "y"}
	rows := []struct {
		name string
		in   []byte
	}{
		{"empty input", nil},
		{"wrong magic", []byte("OCC\x02\x00\x00")},
		{"magic only", (&raw{}).magic().b},
		{"trailing byte", append(append([]byte(nil), valid...), 0)},
		{"empty record", (&raw{}).magic().u(1).s("k").u(0).u(0).b},
		{"zero dot counter", one(func(r *raw) { versionBytes(r, "a", 0, nil, 0, "v") })},
		{"tombstone carrying a value", one(func(r *raw) { versionBytes(r, "a", 1, nil, 1, "v") })},
		{"flag byte out of range", one(func(r *raw) { versionBytes(r, "a", 1, nil, 2, "") })},
		{"context entry with a zero counter", one(func(r *raw) { versionBytes(r, "a", 1, [][2]uint64{{1, 0}}, 0, "") })},
		{"context entries out of order", one(func(r *raw) { versionBytes(r, "a", 1, [][2]uint64{{2, 1}, {1, 1}}, 0, "") })},
		{"context entry repeated", one(func(r *raw) { versionBytes(r, "a", 1, [][2]uint64{{1, 1}, {1, 2}}, 0, "") })},
		{"non-minimal uvarint", (&raw{}).magic().by(0x81, 0x00).s("k").u(1).b},
		{"record count larger than the input", (&raw{}).magic().u(1 << 40).b},
		{"logical counter beyond 32 bits", one(func(r *raw) { r.dot("a", 1).u(0).by(0).i(0).u(1 << 33).s("").s("") })},
		{"keys out of order", func() []byte {
			r := (&raw{}).magic().u(2).s("b").u(1)
			versionBytes(r, "a", 1, nil, 0, "")
			r.s("a").u(1)
			versionBytes(r, "a", 1, nil, 0, "")
			return r.u(0).b
		}()},
		{"key repeated", func() []byte {
			r := (&raw{}).magic().u(2).s("a").u(1)
			versionBytes(r, "a", 1, nil, 0, "")
			r.s("a").u(1)
			versionBytes(r, "a", 1, nil, 0, "")
			return r.u(0).b
		}()},
		{"siblings out of id order", func() []byte {
			r := (&raw{}).magic().u(1).s("k").u(2)
			versionBytes(r, second, 1, nil, 0, vals[second])
			versionBytes(r, first, 1, nil, 0, vals[first])
			return r.u(0).b
		}()},
		{"sibling repeated", func() []byte {
			r := (&raw{}).magic().u(1).s("k").u(2)
			versionBytes(r, "a", 1, nil, 0, "x")
			versionBytes(r, "a", 1, nil, 0, "x")
			return r.u(0).b
		}()},
		{"a sibling superseded by another", func() []byte {
			old := occ.NewVersion(occ.Dot{Actor: occ.Actor{Node: "a", Epoch: 1}, Counter: 1}, occ.VersionVector{}, false, nil, occ.Stamp{})
			nw := occ.NewVersion(occ.Dot{Actor: occ.Actor{Node: "b", Epoch: 1}, Counter: 1}, old.Clock(), false, nil, occ.Stamp{})
			r := (&raw{}).magic().u(1).s("k").u(2)
			o := func() { versionBytes(r, "a", 1, nil, 0, "") }
			n := func() { versionBytes(r, "b", 1, [][2]uint64{{0, 1}}, 0, "") }
			if old.ID().Compare(nw.ID()) < 0 {
				o()
				n()
			} else {
				n()
				o()
			}
			return r.u(0).b
		}()},
		{"scalars out of order", (&raw{}).magic().u(0).u(2).s("b").s("v").stamp().s("a").s("v").stamp().b},
		{"scalar truncated", (&raw{}).magic().u(0).u(1).s("b").s("v").i(0).b},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			if d, err := occ.Decode(r.in); !errors.Is(err, occ.ErrMalformed) || d != nil {
				t.Fatalf("Decode = %v, want ErrMalformed", err)
			}
		})
	}
	t.Run("every strict prefix of a valid document", func(t *testing.T) {
		enc := goldenDoc().Encode()
		for n := 0; n < len(enc); n++ {
			if _, err := occ.Decode(enc[:n]); !errors.Is(err, occ.ErrMalformed) {
				t.Fatalf("prefix of %d bytes: %v", n, err)
			}
		}
	})
	t.Run("state blobs", func(t *testing.T) {
		good := occ.EncodeStateBlob(5, goldenDoc().Encode())
		flipped := append([]byte(nil), good...)
		flipped[len(flipped)-1] ^= 1
		for name, in := range map[string][]byte{
			"empty":             nil,
			"wrong magic":       append([]byte("XXXX"), good[4:]...),
			"short header":      good[:10],
			"checksum mismatch": flipped,
			"zero epoch":        occ.EncodeStateBlob(0, goldenDoc().Encode()),
			"truncated epoch":   occ.EncodeStateBlob(0, nil)[:36],
			"malformed doc":     occ.EncodeStateBlob(5, []byte("OCC\x01\x05")),
		} {
			if _, _, err := occ.DecodeStateBlob(in); !errors.Is(err, occ.ErrMalformed) {
				t.Fatalf("%s: %v", name, err)
			}
		}
	})
	t.Run("results", func(t *testing.T) {
		if _, err := occ.DecodeResult([]byte{1, 2}); !errors.Is(err, occ.ErrMalformed) {
			t.Fatal(err)
		}
	})
}

func hostileCycle() *occ.Doc {
	a := occ.Actor{Node: "a", Epoch: 1}
	b := occ.Actor{Node: "b", Epoch: 1}
	v1 := occ.NewVersion(occ.Dot{Actor: a, Counter: 1}, occ.NewVersionVector(occ.Entry{Actor: b, Counter: 1}), false, []byte("one"), occ.Stamp{})
	v2 := occ.NewVersion(occ.Dot{Actor: b, Counter: 1}, occ.NewVersionVector(occ.Entry{Actor: a, Counter: 1}), false, []byte("two"), occ.Stamp{})
	v3 := occ.NewVersion(occ.Dot{Actor: a, Counter: 2}, occ.NewVersionVector(occ.Entry{Actor: a, Counter: 9}), true, nil, occ.Stamp{Wall: -1 << 62})
	d := occ.NewDoc()
	d.MergeRecord("k", occ.NewRecord(v1, v2))
	d.MergeRecord("j", occ.NewRecord(v3))
	return d
}

func TestMergeIsTotalOnDecodedStates(t *testing.T) {
	cycle, err := occ.Decode(hostileCycle().Encode())
	if err != nil {
		t.Fatalf("a hostile but canonical state must decode: %v", err)
	}
	if cycle.Record("k").Len() != 2 {
		t.Fatal("mutually covering versions with equal event sets must both be kept")
	}
	g := goldenDoc()
	s, _ := newTestStore(t, "a", 1, 1)
	mustPut(t, s, "k", "honest", occ.IfNoneMatch())
	states := []*occ.Doc{cycle, g, s.Doc().Clone(), occ.NewDoc()}
	for _, a := range states {
		for _, b := range states {
			for _, c := range states {
				if err := checkLaws(a, b, c); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

func FuzzOCCDecode(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("OCC\x01"))
	f.Add([]byte("OCC\x01\x00\x00"))
	f.Add(goldenDoc().Encode())
	f.Add(hostileCycle().Encode())
	f.Add(occ.EncodeStateBlob(3, goldenDoc().Encode()))
	f.Add(occ.EncodeOpRequest(occ.OpPut, "k", occ.IfMatch(occ.ETag{1}), []byte("v")))
	honest := goldenDoc()
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			return
		}
		_, _, _ = occ.DecodeStateBlob(data)
		_, _, _ = occ.ParseKey(string(data))
		_, _ = occ.DecodeUsage(data)
		_, _ = occ.DecodeObjectMeta(data)
		_, _ = occ.DecodeResult(data)
		d, err := occ.Decode(data)
		if err != nil {
			if !errors.Is(err, occ.ErrMalformed) || d != nil {
				t.Fatalf("decode error %v is not ErrMalformed", err)
			}
			return
		}
		if !bytes.Equal(d.Encode(), data) {
			t.Fatal("encode(decode(b)) != b")
		}
		again, err := occ.Decode(d.Encode())
		if err != nil || !again.Equal(d) {
			t.Fatalf("decode(encode(d)) != d: %v", err)
		}
		if err := checkLaws(d, honest, d); err != nil {
			t.Fatal(err)
		}
		if err := checkLaws(honest, d, hostileCycle()); err != nil {
			t.Fatal(err)
		}
	})
}
