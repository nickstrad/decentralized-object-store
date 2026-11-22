package occ_test

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
)

var updateGolden = flag.Bool("update-golden", false, "rewrite testdata golden files")

type raw struct{ b []byte }

func (r *raw) u(x uint64) *raw             { r.b = binary.AppendUvarint(r.b, x); return r }
func (r *raw) i(x int64) *raw              { r.b = binary.AppendVarint(r.b, x); return r }
func (r *raw) s(x string) *raw             { r.u(uint64(len(x))); r.b = append(r.b, x...); return r }
func (r *raw) by(x ...byte) *raw           { r.b = append(r.b, x...); return r }
func (r *raw) magic() *raw                 { return r.by('O', 'C', 'C', 2) }
func (r *raw) stamp() *raw                 { return r.i(0).u(0).s("") }
func (r *raw) dot(n string, c uint64) *raw { return r.s(n).u(1).u(c) }

func (r *raw) version(node string, counter uint64, ctx [][2]uint64, flag byte, value string) *raw {
	r.dot(node, counter)
	r.u(uint64(len(ctx)))
	for _, e := range ctx {
		r.s(string(rune('a' + e[0]))).u(1).u(e[1])
	}
	r.by(flag).stamp()
	return r.s(value)
}

func goldenDoc() *occ.Doc {
	a := occ.Actor{Node: "node-a", Epoch: 0x1111}
	b := occ.Actor{Node: "node-b", Epoch: 0x2222}
	base := occ.NewVersion(occ.Dot{Actor: a, Counter: 1}, occ.VersionVector{}, false, []byte("base"), st(1000, 0, "node-a"))
	left := occ.NewVersion(occ.Dot{Actor: a, Counter: 2}, base.Clock(), false, []byte("left"), st(2000, 1, "node-a"))
	right := occ.NewVersion(occ.Dot{Actor: b, Counter: 1}, base.Clock(), true, nil, st(-5, 3, "node-b"))
	d := occ.NewDoc()
	d.MergeVersion("tags/x", base)
	d.MergeVersion("obj/y", left)
	d.MergeVersion("obj/y", right)
	d.MergeVersion("route/b", occ.NewVersion(occ.Dot{Actor: b, Counter: 2}, occ.VersionVector{}, false, []byte("region-a"), st(7, 0, "node-b")))
	d.MergeVersion("ctype/b/o", occ.NewVersion(occ.Dot{Actor: a, Counter: 3}, occ.VersionVector{}, false, nil, st(8, 2, "node-a")))
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

// TestCanonicalEncoding proves golden byte stability for documents, state blobs,
// and client ops, round-trip equality, order-independent encoding, and defensive
// copying of Doc's cached bytes.
func TestCanonicalEncoding(t *testing.T) {
	d := goldenDoc()
	enc := d.Encode()
	golden(t, "doc-v2.golden", enc)
	golden(t, "state-v3.golden", occ.EncodeStateBlob(0x1111, 5, 2, enc))
	golden(t, "op-v1.golden", occ.EncodeOpRequest(occ.OpPut, "obj/y", occ.IfMatch(d.Record("obj/y").ETag()), []byte("v")))
	if value(d.Record("obj/y")) != "left" || d.Len() != 4 {
		t.Fatal("the golden document holds the wrong winner")
	}
	back, err := occ.Decode(enc)
	if err != nil || !bytes.Equal(back.Encode(), enc) || back.Digest() != d.Digest() {
		t.Fatalf("round trip: %v", err)
	}
	other := occ.NewDoc()
	other.Merge(back)
	other.Merge(goldenDoc())
	other.Merge(d)
	if !bytes.Equal(other.Encode(), enc) || !other.Equal(d) {
		t.Fatal("equal states built in a different order encode differently")
	}
	if occ.NewDoc().Len() != 0 {
		t.Fatal("len")
	}
	enc[len(enc)-1] ^= 0xff
	if bytes.Equal(enc, d.Encode()) {
		t.Fatal("Encode exposed its cached buffer")
	}
	st, err := occ.DecodeStateBlob(occ.EncodeStateBlob(0x1111, 5, 2, d.Encode()))
	if err != nil || st.Epoch != 0x1111 || st.Counter != 5 || st.Discards != 2 || !st.Doc.Equal(d) {
		t.Fatalf("state blob round trip: %v", err)
	}
}

func oneRecord(build func(r *raw)) []byte {
	r := (&raw{}).magic().u(1).s("k")
	build(r)
	return r.b
}

// TestDecodeMalformedTable proves that Decode, DecodeStateBlob, and
// DecodeResult reject malformed, repeated-key, out-of-order, non-canonical,
// and truncated inputs with ErrMalformed.
func TestDecodeMalformedTable(t *testing.T) {
	valid := oneRecord(func(r *raw) { r.version("a", 1, nil, 0, "v") })
	if _, err := occ.Decode(valid); err != nil {
		t.Fatalf("the base case must decode: %v", err)
	}
	twoOf := func(key1, key2 string, v1, v2 string) []byte {
		r := (&raw{}).magic().u(2).s(key1)
		r.version("a", 1, nil, 0, v1)
		r.s(key2)
		r.version("b", 1, nil, 0, v2)
		return r.b
	}
	rows := []struct {
		name string
		in   []byte
	}{
		{"empty input", nil},
		{"the sibling-era magic", append([]byte("OCC\x01"), valid[4:]...)},
		{"magic only", (&raw{}).magic().b},
		{"trailing byte", append(append([]byte(nil), valid...), 0)},
		{"zero dot counter", oneRecord(func(r *raw) { r.version("a", 0, nil, 0, "v") })},
		{"tombstone carrying a value", oneRecord(func(r *raw) { r.version("a", 1, nil, 1, "v") })},
		{"flag byte out of range", oneRecord(func(r *raw) { r.version("a", 1, nil, 2, "") })},
		{"context entry with a zero counter", oneRecord(func(r *raw) { r.version("a", 1, [][2]uint64{{1, 0}}, 0, "") })},
		{"context entries out of order", oneRecord(func(r *raw) { r.version("a", 1, [][2]uint64{{2, 1}, {1, 1}}, 0, "") })},
		{"context entry repeated", oneRecord(func(r *raw) { r.version("a", 1, [][2]uint64{{1, 1}, {1, 2}}, 0, "") })},
		{"context count larger than the input", oneRecord(func(r *raw) { r.dot("a", 1).u(1 << 30) })},
		{"non-minimal uvarint", (&raw{}).magic().by(0x81, 0x00).s("k").b},
		{"record count larger than the input", (&raw{}).magic().u(1 << 40).b},
		{"logical counter beyond 32 bits", oneRecord(func(r *raw) { r.dot("a", 1).u(0).by(0).i(0).u(1 << 33).s("").s("") })},
		{"version truncated", oneRecord(func(r *raw) { r.dot("a", 1).u(0).by(0).i(0) })},
		{"keys out of order", twoOf("b", "a", "x", "y")},
		{"a record with two versions (the key repeated)", twoOf("k", "k", "x", "y")},
		{"a record with the same version twice", func() []byte {
			r := (&raw{}).magic().u(2).s("k")
			r.version("a", 1, nil, 0, "x")
			r.s("k")
			r.version("a", 1, nil, 0, "x")
			return r.b
		}()},
		{"the reviewer's 28 KiB shape: 400 versions of one key", reviewerShape()},
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
		good := occ.EncodeStateBlob(5, 1, 0, goldenDoc().Encode())
		flipped := append([]byte(nil), good...)
		flipped[len(flipped)-1] ^= 1
		for name, in := range map[string][]byte{
			"empty":             nil,
			"wrong magic":       append([]byte("XXXX"), good[4:]...),
			"short header":      good[:10],
			"checksum mismatch": flipped,
			"zero epoch":        occ.EncodeStateBlob(0, 1, 0, goldenDoc().Encode()),
			"truncated header":  occ.EncodeStateBlob(5, 1, 0, nil)[:37],
			"malformed doc":     occ.EncodeStateBlob(5, 1, 0, []byte("OCC\x02\x05")),
		} {
			if _, err := occ.DecodeStateBlob(in); !errors.Is(err, occ.ErrMalformed) {
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

// reviewerShape builds a ~28 KiB malformed document packing 400 versions under
// one key to test early rejection of multi-version records.
func reviewerShape() []byte {
	r := (&raw{}).magic().u(400)
	for i := range 400 {
		r.s("k").s("").u(0).u(1).u(20)
		for a := range 20 {
			r.s("").u(uint64(a)).u(64)
		}
		r.by(0).stamp().s(string([]byte{byte(i >> 8), byte(i)}))
	}
	return r.b
}

func fuzzSeed(data []byte) []byte {
	return fmt.Appendf(nil, "go test fuzz v1\n[]byte(%q)\n", data)
}

// TestReviewerHostileInputIsRejectedEarly proves that a ~28 KiB hostile input
// repeating one key 400 times fails at the second version with ErrMalformed
// and matches the committed fuzz corpus seed.
func TestReviewerHostileInputIsRejectedEarly(t *testing.T) {
	in := reviewerShape()
	if len(in) < 28<<10 || len(in) > 32<<10 {
		t.Fatalf("the shape is %d bytes, want about 28 KiB", len(in))
	}
	d, work, err := occ.DecodeWork(in)
	if !errors.Is(err, occ.ErrMalformed) || d != nil || !strings.Contains(err.Error(), "key order") {
		t.Fatalf("the reviewer's input must be rejected as a multi-version record: %v", err)
	}
	if work > 4*len(in)/400*2 {
		t.Fatalf("rejection took %d operations; it must stop at the second version", work)
	}
	path := filepath.Join("testdata", "fuzz", "FuzzOCCDecode", "reviewer-28k-multi-version")
	if *updateGolden {
		if err := os.WriteFile(path, fuzzSeed(in), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, fuzzSeed(in)) {
		t.Fatalf("the committed fuzz seed %s differs from the reviewer's shape (run with -update-golden): %v", path, err)
	}
}

func manyKeys(n int, wall int64) []byte {
	r := (&raw{}).magic().u(uint64(n))
	var key [8]byte
	for i := range n {
		binary.BigEndian.PutUint64(key[:], uint64(i))
		r.s(string(key[:])).s("").u(1).u(1<<21 + uint64(i)).u(0).by(0).i(wall).u(0).s("").s("")
	}
	return r.b
}

func bigContexts(entries int, keys int, counter uint64, wall int64) []byte {
	r := (&raw{}).magic().u(uint64(keys))
	for k := range keys {
		r.s(string(rune('a' + k))).s("").u(1).u(1).u(uint64(entries))
		for a := range entries {
			r.s("").u(1<<21 + uint64(a)).u(counter)
		}
		r.by(0).i(wall).u(0).s("").s("")
	}
	return r.b
}

func raceBuild() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "-race" {
			return s.Value == "true"
		}
	}
	return false
}

func fitCount(limit, per int) int { return (limit - 64) / per }

// TestHostileMessagesBoundedWork proves that decoding and merging maximum-size
// hostile messages with many keys or large contexts do linear work per byte.
func TestHostileMessagesBoundedWork(t *testing.T) {
	limit := sim.DefaultMaxMessage - 1
	if raceBuild() {
		limit = 1 << 20
	}
	perKey := len(manyKeys(2, 0)) - len(manyKeys(1, 0))
	perEntry := len(bigContexts(2, 1, 1, 0)) - len(bigContexts(1, 1, 1, 0))
	shapes := []struct {
		name       string
		held, push func(scale int) []byte
	}{
		{"many keys", func(scale int) []byte { return manyKeys(fitCount(limit/scale, perKey), 1) },
			func(scale int) []byte { return manyKeys(fitCount(limit/scale, perKey), 2) }},
		{"big contexts", func(scale int) []byte { return bigContexts(fitCount(limit/scale/4, perEntry), 4, 1, 1) },
			func(scale int) []byte { return bigContexts(fitCount(limit/scale/4, perEntry), 4, 1, 2) }},
	}
	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			var perByte [2]float64
			for i, scale := range []int{8, 1} {
				held, push := sh.held(scale), sh.push(scale)
				if scale == 1 && (len(push) > sim.DefaultMaxMessage || len(push) < limit*9/10) {
					t.Fatalf("the hostile message is %d bytes, want close to %d", len(push), limit)
				}
				hd, hwork, err := occ.DecodeWork(held)
				if err != nil {
					t.Fatal(err)
				}
				pd, pwork, err := occ.DecodeWork(push)
				if err != nil {
					t.Fatal(err)
				}
				if hwork > 4*len(held) || pwork > 4*len(push) {
					t.Fatalf("decode work %d / %d for %d / %d bytes is not linear", hwork, pwork, len(held), len(push))
				}
				c := &fakeClock{now: 1_000_000}
				cfg := storeConfig("a", 1, 1, c)
				discards := 0
				cfg.OnDiscard = func(string, occ.Version, occ.Version) { discards++ }
				s, err := occ.NewStore(cfg)
				if err != nil {
					t.Fatal(err)
				}
				mustMerge(t, s, hd)
				w0 := s.Work()
				mustMerge(t, s, pd)
				merge := s.Work() - w0
				if merge > len(held)+len(push) {
					t.Fatalf("merge work %d for %d + %d bytes is not linear", merge, len(held), len(push))
				}
				if discards != pd.Len() || s.LWWDiscards() != uint64(pd.Len()) || !s.Doc().Equal(pd) {
					t.Fatalf("every forged replacement must be decided and recorded: %d of %d", discards, pd.Len())
				}
				perByte[i] = float64(pwork+merge) / float64(len(push))
				t.Logf("MEASURE %s scale 1/%d: %d bytes, decode work %d, merge work %d", sh.name, scale, len(push), pwork, merge)
			}
			if perByte[1] > 1.5*perByte[0] {
				t.Fatalf("work per byte grew from %.2f to %.2f with message size", perByte[0], perByte[1])
			}
		})
	}
}

func hostileDocs() []*occ.Doc {
	a := occ.Actor{Node: "a", Epoch: 1}
	b := occ.Actor{Node: "b", Epoch: 1}
	mutual1 := occ.NewVersion(occ.Dot{Actor: a, Counter: 1}, occ.NewVersionVector(occ.Entry{Actor: b, Counter: 1}), false, []byte("one"), st(5, 0, "a"))
	mutual2 := occ.NewVersion(occ.Dot{Actor: b, Counter: 1}, occ.NewVersionVector(occ.Entry{Actor: a, Counter: 1}), false, []byte("two"), st(5, 0, "a"))
	past := occ.NewVersion(occ.Dot{Actor: a, Counter: 2}, occ.NewVersionVector(occ.Entry{Actor: a, Counter: 9}), true, nil, st(math.MinInt64, 0, ""))
	end := occ.NewVersion(occ.Dot{Actor: b, Counter: 7}, occ.VersionVector{}, false, nil, st(math.MaxInt64, math.MaxUint32, "zz"))
	d1, d2, d3 := occ.NewDoc(), occ.NewDoc(), occ.NewDoc()
	d1.MergeVersion("k", mutual1)
	d1.MergeVersion("j", past)
	d2.MergeVersion("k", mutual2)
	d2.MergeVersion("e", end)
	d3.MergeVersion("j", end)
	d3.MergeVersion("k", past)
	return []*occ.Doc{d1, d2, d3}
}

// TestMergeIsTotalOnDecodedStates proves that Doc.Merge and Store.Merge satisfy
// all merge laws and never error across honest and hand-built hostile decoded states.
func TestMergeIsTotalOnDecodedStates(t *testing.T) {
	var states []*occ.Doc
	for _, h := range hostileDocs() {
		d, err := occ.Decode(h.Encode())
		if err != nil {
			t.Fatalf("a hostile but canonical state must decode: %v", err)
		}
		states = append(states, d)
	}
	s, _ := newTestStore(t, "a", 1, 1)
	mustPut(t, s, "k", "honest", occ.IfNoneMatch())
	negative := occ.NewDoc()
	negative.MergeVersion("route/b", version("x", 1, occ.VersionVector{}, false, "", st(-25, 0, "x")))
	if m := mergeDocs(occ.NewDoc(), negative); !m.Equal(negative) {
		t.Fatal("a version stamped before the epoch must not lose to an absent one")
	}
	states = append(states, goldenDoc(), s.Doc(), occ.NewDoc(), negative)
	for _, a := range states {
		for _, b := range states {
			for _, c := range states {
				if err := checkLaws(a, b, c); err != nil {
					t.Fatal(err)
				}
			}
			st, _ := newTestStore(t, "m", 1, 9)
			for _, d := range []*occ.Doc{a, b} {
				if _, err := st.Merge(d); err != nil {
					t.Fatalf("a store merge failed on a decoded state: %v", err)
				}
			}
			if !st.Doc().Equal(mergeDocs(a, b)) {
				t.Fatal("the store merge differs from the document merge")
			}
		}
	}
}

// FuzzOCCDecode proves that decoding arbitrary bytes never panics, runs in
// linear work, rejects invalid input with ErrMalformed, round-trips valid docs,
// and preserves merge laws and total Store.Merge on every decoded document.
func FuzzOCCDecode(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte("OCC\x02"))
	f.Add([]byte("OCC\x02\x00"))
	f.Add(goldenDoc().Encode())
	for _, h := range hostileDocs() {
		f.Add(h.Encode())
	}
	f.Add(occ.EncodeStateBlob(3, 1, 0, goldenDoc().Encode()))
	f.Add(occ.EncodeOpRequest(occ.OpPut, "k", occ.IfMatch(occ.ETag{1}), []byte("v")))
	honest := goldenDoc()
	hostile := hostileDocs()[0]
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64<<10 {
			return
		}
		_, _ = occ.DecodeStateBlob(data)
		_, _, _ = occ.ParseKey(string(data))
		_, _ = occ.DecodeUsage(data)
		_, _ = occ.DecodeObjectMeta(data)
		_, _ = occ.DecodeResult(data)
		d, work, err := occ.DecodeWork(data)
		if work > 4*len(data)+8 {
			t.Fatalf("decode work %d for %d bytes", work, len(data))
		}
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
		if err := checkLaws(honest, d, hostile); err != nil {
			t.Fatal(err)
		}
		s, _ := newTestStore(t, "a", 1, 1)
		mustPut(t, s, "k", "mine", occ.IfNoneMatch())
		if _, err := s.Merge(d); err != nil {
			t.Fatal(err)
		}
	})
}
