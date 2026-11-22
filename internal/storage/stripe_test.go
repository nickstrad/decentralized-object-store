package storage_test

import (
	"bytes"
	"errors"
	"flag"
	"math"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/erasure"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
	"pgregory.net/rapid"
)

// TestLayoutTable proves Layout.Validate, Codec, Stripes, and StripeLen handle
// valid, boundary, invalid, and overflowing configurations.
func TestLayoutTable(t *testing.T) {
	cases := []struct {
		name    string
		l       storage.Layout
		size    int64
		stripes int
		last    int
		err     error
	}{
		{"default layout, empty object has no stripes", storage.DefaultLayout(), 0, 0, 0, nil},
		{"default layout, one byte", storage.DefaultLayout(), 1, 1, 1, nil},
		{"default layout, exactly one stripe", storage.DefaultLayout(), 1 << 20, 1, 1 << 20, nil},
		{"default layout, one stripe plus one byte", storage.DefaultLayout(), 1<<20 + 1, 2, 1, nil},
		{"small stripes, prime size", storage.Layout{K: 2, M: 1, StripeSize: 10}, 31, 4, 1, nil},
		{"k=0 is ErrInvalid", storage.Layout{K: 0, M: 1, StripeSize: 10}, 1, 0, 0, storage.ErrInvalid},
		{"k+m=257 is ErrInvalid", storage.Layout{K: 129, M: 128, StripeSize: 1024}, 1, 0, 0, storage.ErrInvalid},
		{"stripe smaller than k is ErrInvalid", storage.Layout{K: 4, M: 2, StripeSize: 3}, 1, 0, 0, storage.ErrInvalid},
		{"stripe over the limit is ErrInvalid", storage.Layout{K: 4, M: 2, StripeSize: storage.MaxStripeSize + 1}, 1, 0, 0, storage.ErrInvalid},
		{"k=MaxInt m=1 overflows k+m and is ErrInvalid", storage.Layout{K: math.MaxInt, M: 1, StripeSize: 1024}, 1, 0, 0, storage.ErrInvalid},
		{"k=m=2^62 wraps k+m and is ErrInvalid", storage.Layout{K: 1 << 62, M: 1 << 62, StripeSize: 1024}, 1, 0, 0, storage.ErrInvalid},
		{"m=MaxInt is ErrInvalid", storage.Layout{K: 4, M: math.MaxInt, StripeSize: 1024}, 1, 0, 0, storage.ErrInvalid},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.l.Validate()
			if c.err != nil {
				if !errors.Is(err, c.err) {
					t.Fatalf("Validate error %v, want %v", err, c.err)
				}
				if _, err := c.l.Codec(); !errors.Is(err, c.err) {
					t.Fatalf("Codec error %v", err)
				}
				var cfgErr error = erasure.ErrInvalidConfig
				if c.l.K == 0 && !errors.Is(err, cfgErr) {
					t.Fatalf("Validate error %v does not carry the erasure cause", err)
				}
				return
			}
			mustNil(t, err)
			if got := c.l.Stripes(c.size); got != c.stripes {
				t.Fatalf("Stripes(%d) = %d, want %d", c.size, got, c.stripes)
			}
			if c.stripes > 0 {
				if got := c.l.StripeLen(c.size, c.stripes-1); got != c.last {
					t.Fatalf("last stripe length %d, want %d", got, c.last)
				}
			}
			total := 0
			for i := 0; i < c.stripes; i++ {
				total += c.l.StripeLen(c.size, i)
			}
			if int64(total) != c.size {
				t.Fatalf("stripe lengths add to %d, size %d", total, c.size)
			}
			if c.l.StripeLen(c.size, c.stripes) != 0 || c.l.StripeLen(c.size, -1) != 0 {
				t.Fatal("a stripe outside the object has a length")
			}
		})
	}
}

func encodeStripe(t rapid.TB, codec *erasure.Codec, v storage.VersionID, stripe uint32, data []byte) ([][]byte, [][32]byte) {
	t.Helper()
	blobs, err := storage.EncodeStripe(codec, v, stripe, data)
	if err != nil {
		t.Fatal(err)
	}
	sums := make([][32]byte, len(blobs))
	for i, b := range blobs {
		sums[i] = storage.BlobChecksum(b)
	}
	return blobs, sums
}

// TestEncodeDecodeStripe proves EncodeStripe and DecodeStripe round-trip clean
// and missing-shard stripes and reject wrong slice lengths or fewer than k shards.
func TestEncodeDecodeStripe(t *testing.T) {
	codec, err := erasure.New(4, 2)
	mustNil(t, err)
	data := []byte("the quick brown fox jumps over the lazy dog")
	blobs, sums := encodeStripe(t, codec, 5, 3, data)
	if len(blobs) != 6 {
		t.Fatalf("%d blobs", len(blobs))
	}
	for i, b := range blobs {
		h, _, err := storage.DecodeShard(b)
		mustNil(t, err)
		if h.Index != i || h.Stripe != 3 || h.Version != 5 || h.K != 4 || h.M != 2 {
			t.Fatalf("blob %d header %+v", i, h)
		}
	}
	got, bad, err := storage.DecodeStripe(codec, 5, 3, len(data), blobs, sums)
	mustNil(t, err)
	if !bytes.Equal(got, data) || len(bad) != 0 {
		t.Fatalf("DecodeStripe = %q, bad %v", got, bad)
	}
	blobs[0], blobs[5] = nil, nil
	got, _, err = storage.DecodeStripe(codec, 5, 3, len(data), blobs, nil)
	mustNil(t, err)
	if !bytes.Equal(got, data) {
		t.Fatal("decode with two missing shards differs")
	}
	if _, _, err := storage.DecodeStripe(codec, 5, 3, len(data), blobs[:5], nil); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("wrong blob count: %v", err)
	}
	if _, _, err := storage.DecodeStripe(codec, 5, 3, len(data), blobs, sums[:2]); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("wrong checksum count: %v", err)
	}
	if _, _, err := storage.DecodeStripe(codec, 5, 3, -1, blobs, nil); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("negative length: %v", err)
	}
	blobs[1] = nil
	_, _, err = storage.DecodeStripe(codec, 5, 3, len(data), blobs, nil)
	if !errors.Is(err, storage.ErrUnavailable) || !errors.Is(err, erasure.ErrTooFewShards) {
		t.Fatalf("three missing shards: %v", err)
	}
}

// TestEncodeStripeRejectsOversizedData proves EncodeStripe returns ErrInvalid
// when data exceeds MaxStripeSize.
func TestEncodeStripeRejectsOversizedData(t *testing.T) {
	codec, err := erasure.New(1, 0)
	mustNil(t, err)
	if _, err := storage.EncodeStripe(codec, 1, 0, make([]byte, storage.MaxStripeSize+1)); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("oversized stripe: %v", err)
	}
}

type corruption struct {
	name  string
	apply func(r *rand.Rand, blobs [][]byte, i int, other [][]byte, otherVersion [][]byte) []byte
}

var corruptions = []corruption{
	{"flip one bit anywhere", func(r *rand.Rand, b [][]byte, i int, _, _ [][]byte) []byte {
		out := slices.Clone(b[i])
		pos := r.IntN(len(out))
		out[pos] ^= 1 << r.IntN(8)
		return out
	}},
	{"flip one payload bit", func(r *rand.Rand, b [][]byte, i int, _, _ [][]byte) []byte {
		out := slices.Clone(b[i])
		if len(out) == storage.HeaderSize {
			out[r.IntN(len(out))] ^= 0x10
			return out
		}
		out[storage.HeaderSize+r.IntN(len(out)-storage.HeaderSize)] ^= 1 << r.IntN(8)
		return out
	}},
	{"truncate", func(r *rand.Rand, b [][]byte, i int, _, _ [][]byte) []byte { return b[i][:r.IntN(len(b[i]))] }},
	{"extend", func(r *rand.Rand, b [][]byte, i int, _, _ [][]byte) []byte {
		return append(slices.Clone(b[i]), byte(r.Uint32()))
	}},
	{"empty", func(*rand.Rand, [][]byte, int, [][]byte, [][]byte) []byte { return []byte{} }},
	{"missing", func(*rand.Rand, [][]byte, int, [][]byte, [][]byte) []byte { return nil }},
	{"swap with another index of the stripe", func(r *rand.Rand, b [][]byte, i int, _, ov [][]byte) []byte {
		if len(b) == 1 {
			return slices.Clone(ov[i])
		}
		return slices.Clone(b[(i+1+r.IntN(len(b)-1))%len(b)])
	}},
	{"swap with the same index of another stripe", func(_ *rand.Rand, _ [][]byte, i int, other, _ [][]byte) []byte {
		return slices.Clone(other[i])
	}},
	{"swap with the same index of another object version", func(_ *rand.Rand, _ [][]byte, i int, _, ov [][]byte) []byte {
		return slices.Clone(ov[i])
	}},
}

func propCorruptionBecomesErasure(t *rapid.T) {
	k := rapid.IntRange(1, 8).Draw(t, "k")
	m := rapid.IntRange(0, 4).Draw(t, "m")
	n := k + m
	codec, err := erasure.New(k, m)
	if err != nil {
		t.Fatal(err)
	}
	size := rapid.SampledFrom([]int{0, 1, 2, 7, k - 1, k, k + 1, 97, 1000, 4096}).Draw(t, "size")
	seed := rapid.Uint64().Draw(t, "seed")
	r := rand.New(rand.NewPCG(seed, 1))
	data := make([]byte, max(size, 0))
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	blobs, sums := encodeStripe(t, codec, 11, 2, data)
	other, _ := encodeStripe(t, codec, 11, 3, data)
	otherVersion, _ := encodeStripe(t, codec, 12, 2, data)
	victims := rapid.SliceOfNDistinct(rapid.IntRange(0, n-1), 0, n, rapid.ID[int]).Draw(t, "victims")
	useSums := rapid.Bool().Draw(t, "useManifestChecksums")
	damaged := slices.Clone(blobs)
	for _, v := range victims {
		c := corruptions[rapid.IntRange(0, len(corruptions)-1).Draw(t, "corruption")]
		damaged[v] = c.apply(r, blobs, v, other, otherVersion)
	}
	want := sums
	if !useSums {
		want = nil
	}
	got, bad, err := storage.DecodeStripe(codec, 11, 2, len(data), damaged, want)
	if len(victims) > m {
		if err == nil {
			t.Fatalf("k=%d m=%d: decoded with %d damaged shards", k, m, len(victims))
		}
		if !errors.Is(err, storage.ErrUnavailable) || got != nil {
			t.Fatalf("k=%d m=%d: %d damaged shards gave %v and %d bytes, want ErrUnavailable", k, m, len(victims), err, len(got))
		}
		return
	}
	if err != nil {
		t.Fatalf("k=%d m=%d victims %v: %v", k, m, victims, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("k=%d m=%d victims %v: decoded wrong bytes", k, m, victims)
	}
	for _, b := range bad {
		if !slices.Contains(victims, b) {
			t.Fatalf("an intact shard %d was reported bad", b)
		}
	}
	for _, v := range victims {
		if damaged[v] != nil && !slices.Contains(bad, v) {
			t.Fatalf("damaged shard %d was not reported bad (bad %v)", v, bad)
		}
	}
}

// TestPropCorruptionBecomesErasure proves DecodeStripe detects bit flips,
// truncation, extension, empty, missing, and swapped shards (by index, stripe,
// or version), recovers exact bytes for <= m damaged shards, reports every
// damaged present shard, and returns ErrUnavailable for > m.
func TestPropCorruptionBecomesErasure(t *testing.T) {
	rapid.Check(t, propCorruptionBecomesErasure)
}

type recordingTB struct {
	testing.TB
	failed bool
	logs   []string
}

func (r *recordingTB) Helper()               {}
func (r *recordingTB) Name() string          { return "CanaryInner" }
func (r *recordingTB) Logf(string, ...any)   {}
func (r *recordingTB) Log(...any)            {}
func (r *recordingTB) Errorf(string, ...any) { r.failed = true }
func (r *recordingTB) Error(...any)          { r.failed = true }
func (r *recordingTB) Fatalf(string, ...any) { r.failed = true }
func (r *recordingTB) Fatal(...any)          { r.failed = true }
func (r *recordingTB) Fail()                 { r.failed = true }
func (r *recordingTB) FailNow()              { r.failed = true }
func (r *recordingTB) Failed() bool          { return r.failed }
func (r *recordingTB) Skipf(string, ...any)  {}
func (r *recordingTB) Skip(...any)           {}
func (r *recordingTB) SkipNow()              {}

// runPlanted runs a rapid property against a recording TB with rapid fail-file
// output suppressed and reports whether the property failed.
func runPlanted(t *testing.T, prop func(*rapid.T)) bool {
	t.Helper()
	old := flag.Lookup("rapid.nofailfile").Value.String()
	mustNil(t, flag.Set("rapid.nofailfile", "true"))
	t.Cleanup(func() { _ = flag.Set("rapid.nofailfile", old) })
	inner := &recordingTB{TB: t}
	rapid.Check(inner, prop)
	return inner.failed
}

// TestCanaryChecksumSkipBreaksCorruptionProperty plants a bug that skips
// SHA-256 verification in DecodeShard and proves propCorruptionBecomesErasure
// passes without the plant and fails with it.
func TestCanaryChecksumSkipBreaksCorruptionProperty(t *testing.T) {
	if runPlanted(t, propCorruptionBecomesErasure) {
		t.Fatal("the property fails without the planted bug")
	}
	restore := storage.PlantSkipChecksum()
	defer restore()
	codec, err := erasure.New(2, 1)
	mustNil(t, err)
	blobs, _ := encodeStripe(t, codec, 1, 0, []byte("abcd"))
	blobs[0][storage.HeaderSize] ^= 1
	if _, _, err := storage.DecodeShard(blobs[0]); err != nil {
		t.Fatalf("the planted bug did not take effect: %v", err)
	}
	if !runPlanted(t, propCorruptionBecomesErasure) {
		t.Fatal("TestPropCorruptionBecomesErasure passes with checksum verification skipped: the property cannot see corruption")
	}
}

// TestDecodeStripeRejectsForgedShards proves header and manifest-checksum
// checks reject a self-consistent forged shard, a shard from another code, and
// a wrong-length shard, and shows that without manifest checksums a forged
// shard would decode wrong bytes.
func TestDecodeStripeRejectsForgedShards(t *testing.T) {
	codec, err := erasure.New(2, 1)
	mustNil(t, err)
	data := []byte("forged shards agree with their own checksum")
	blobs, sums := encodeStripe(t, codec, 4, 1, data)
	forged, err := storage.EncodeShard(storage.ShardHeader{K: 2, M: 1, Index: 0, Stripe: 1, Version: 4}, bytes.Repeat([]byte{'z'}, len(blobs[0])-storage.HeaderSize))
	mustNil(t, err)
	other, err := erasure.New(1, 2)
	mustNil(t, err)
	otherCode, _ := encodeStripe(t, other, 4, 1, data[:len(blobs[0])-storage.HeaderSize])
	shorter, _ := encodeStripe(t, codec, 4, 1, data[:10])
	cases := []struct {
		name  string
		blob  []byte
		index int
	}{
		{"a re-encoded shard with the right header and different payload", forged, 0},
		{"a shard written by a different code", otherCode[1], 1},
		{"a shard of the right stripe with another payload length", shorter[1], 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			damaged := slices.Clone(blobs)
			damaged[c.index] = c.blob
			got, bad, err := storage.DecodeStripe(codec, 4, 1, len(data), damaged, sums)
			mustNil(t, err)
			if !bytes.Equal(got, data) || !slices.Equal(bad, []int{c.index}) {
				t.Fatalf("decoded %q, bad %v", got, bad)
			}
		})
	}
	damaged := slices.Clone(blobs)
	damaged[0] = forged
	got, _, err := storage.DecodeStripe(codec, 4, 1, len(data), damaged, nil)
	mustNil(t, err)
	if bytes.Equal(got, data) {
		t.Fatal("without manifest checksums a self-consistent forged shard is expected to decode to different bytes; the manifest checksum is the defence")
	}
}
