package erasure_test

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/erasure"
	"pgregory.net/rapid"
)

func mustCodec(t testing.TB, k, m int) *erasure.Codec {
	t.Helper()
	c, err := erasure.New(k, m)
	if err != nil {
		t.Fatalf("New(%d, %d): %v", k, m, err)
	}
	return c
}

func TestNewConfigTable(t *testing.T) {
	cases := []struct {
		name string
		k, m int
		err  error
	}{
		{"k=0 is rejected", 0, 2, erasure.ErrInvalidConfig},
		{"negative k is rejected", -1, 2, erasure.ErrInvalidConfig},
		{"negative m is rejected", 4, -1, erasure.ErrInvalidConfig},
		{"m=0 is a valid code with no parity", 4, 0, nil},
		{"k=1 m=0 is the smallest code", 1, 0, nil},
		{"the default k=4 m=2 is valid", 4, 2, nil},
		{"k+m=255 is valid", 200, 55, nil},
		{"k+m=256 is the largest valid code", 128, 128, nil},
		{"k=256 m=0 is valid", 256, 0, nil},
		{"k=1 m=255 is valid", 1, 255, nil},
		{"k+m=257 is rejected", 129, 128, erasure.ErrInvalidConfig},
		{"k=257 m=0 is rejected", 257, 0, erasure.ErrInvalidConfig},
		{"k=MaxInt m=1 overflows k+m and is rejected", math.MaxInt, 1, erasure.ErrInvalidConfig},
		{"k=1 m=MaxInt overflows k+m and is rejected", 1, math.MaxInt, erasure.ErrInvalidConfig},
		{"k=m=2^62 wraps k+m negative and is rejected", 1 << 62, 1 << 62, erasure.ErrInvalidConfig},
		{"k=m=MaxInt is rejected", math.MaxInt, math.MaxInt, erasure.ErrInvalidConfig},
		{"k=257 m=-1 is rejected", 257, -1, erasure.ErrInvalidConfig},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			codec, err := erasure.New(c.k, c.m)
			if !errors.Is(err, c.err) {
				t.Fatalf("New(%d, %d) error %v, want %v", c.k, c.m, err, c.err)
			}
			if err != nil {
				return
			}
			if codec.K() != c.k || codec.M() != c.m || codec.N() != c.k+c.m {
				t.Fatalf("codec reports k=%d m=%d n=%d", codec.K(), codec.M(), codec.N())
			}
		})
	}
}

func TestEncodeKnownVectors(t *testing.T) {
	cases := []struct {
		name   string
		k, m   int
		data   [][]byte
		parity [][]byte
	}{
		{"k=1 m=1 parity copies the data", 1, 1, [][]byte{{1, 2, 3}}, [][]byte{{1, 2, 3}}},
		{"k=2 m=1", 2, 1, [][]byte{{1, 2}, {3, 4}}, [][]byte{{0x8f, 0xf6}}},
		{"k=4 m=2 default code", 4, 2,
			[][]byte{{0, 1, 2, 3}, {4, 5, 6, 7}, {8, 9, 10, 11}, {12, 13, 14, 15}},
			[][]byte{{0x3a, 0x1a, 0x7a, 0x5a}, {0xba, 0x9a, 0xfa, 0xda}}},
		{"k=3 m=3 single-byte shards", 3, 3, [][]byte{{0xff}, {0x10}, {0x01}}, [][]byte{{0x5c}, {0x2e}, {0x8d}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			codec := mustCodec(t, c.k, c.m)
			shards := make([][]byte, c.k+c.m)
			for i, d := range c.data {
				shards[i] = append([]byte(nil), d...)
			}
			if err := codec.Encode(shards); err != nil {
				t.Fatal(err)
			}
			for i, p := range c.parity {
				if !bytes.Equal(shards[c.k+i], p) {
					t.Fatalf("parity %d = %x, want %x", i, shards[c.k+i], p)
				}
			}
			for i, d := range c.data {
				if !bytes.Equal(shards[i], d) {
					t.Fatalf("data shard %d changed by Encode", i)
				}
			}
			if err := codec.Verify(shards); err != nil {
				t.Fatalf("Verify: %v", err)
			}
		})
	}
}

func TestEncodeReusesParityBuffers(t *testing.T) {
	codec := mustCodec(t, 2, 1)
	buf := []byte{9, 9}
	shards := [][]byte{{1, 2}, {3, 4}, buf}
	if err := codec.Encode(shards); err != nil {
		t.Fatal(err)
	}
	if &shards[2][0] != &buf[0] || !bytes.Equal(buf, []byte{0x8f, 0xf6}) {
		t.Fatalf("parity buffer not reused or not overwritten: %x", shards[2])
	}
}

func TestShardErrors(t *testing.T) {
	codec := mustCodec(t, 2, 1)
	cases := []struct {
		name   string
		op     func([][]byte) error
		shards [][]byte
		err    error
		index  int
	}{
		{"Encode with too few slots", codec.Encode, [][]byte{{1}, {2}}, erasure.ErrShardCount, -1},
		{"Encode with a missing data shard", codec.Encode, [][]byte{{1}, nil, nil}, erasure.ErrTooFewShards, 1},
		{"Encode with unequal data shards", codec.Encode, [][]byte{{1}, {2, 3}, nil}, erasure.ErrShardSize, 1},
		{"Verify with too many slots", codec.Verify, [][]byte{{1}, {2}, {3}, {4}}, erasure.ErrShardCount, -1},
		{"Verify with a missing parity shard", codec.Verify, [][]byte{{1}, {2}, nil}, erasure.ErrTooFewShards, 2},
		{"Verify with a short parity shard", codec.Verify, [][]byte{{1}, {2}, {}}, erasure.ErrShardSize, 2},
		{"Verify with wrong parity", codec.Verify, [][]byte{{1}, {2}, {0}}, erasure.ErrParityMismatch, 2},
		{"Reconstruct with wrong slot count", codec.Reconstruct, [][]byte{{1}}, erasure.ErrShardCount, -1},
		{"Reconstruct with two of three missing", codec.Reconstruct, [][]byte{nil, {2}, nil}, erasure.ErrTooFewShards, -1},
		{"Reconstruct with unequal shards", codec.Reconstruct, [][]byte{{1}, {2, 3}, nil}, erasure.ErrShardSize, 1},
		{"ReconstructData with all missing", codec.ReconstructData, [][]byte{nil, nil, nil}, erasure.ErrTooFewShards, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.op(c.shards)
			if !errors.Is(err, c.err) {
				t.Fatalf("error %v, want %v", err, c.err)
			}
			var se *erasure.ShardError
			if c.index < 0 {
				if errors.As(err, &se) {
					t.Fatalf("unexpected ShardError %v", se)
				}
				return
			}
			if !errors.As(err, &se) || se.Index != c.index || !errors.Is(se.Err, c.err) {
				t.Fatalf("want ShardError{Index: %d, Err: %v}, got %v", c.index, c.err, err)
			}
		})
	}
}

func TestShardErrorMessage(t *testing.T) {
	err := &erasure.ShardError{Index: 3, Err: erasure.ErrShardSize}
	if got, want := err.Error(), "erasure: shard 3: "+erasure.ErrShardSize.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestSplitAndJoin(t *testing.T) {
	codec := mustCodec(t, 4, 2)
	cases := []struct {
		name      string
		size      int
		shardSize int
	}{
		{"empty payload gives empty shards", 0, 0},
		{"one byte pads to one byte per shard", 1, 1},
		{"k-1 bytes", 3, 1},
		{"k bytes fill every shard exactly", 4, 1},
		{"k+1 bytes round the shard size up", 5, 2},
		{"a prime length", 1009, 253},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := make([]byte, c.size)
			for i := range data {
				data[i] = byte(i*7 + 1)
			}
			if got := codec.ShardSize(c.size); got != c.shardSize {
				t.Fatalf("ShardSize(%d) = %d, want %d", c.size, got, c.shardSize)
			}
			shards := codec.Split(data)
			if len(shards) != 6 {
				t.Fatalf("Split gave %d shards", len(shards))
			}
			for i, s := range shards {
				if s == nil || len(s) != c.shardSize {
					t.Fatalf("shard %d has length %d (nil %v)", i, len(s), s == nil)
				}
			}
			if err := codec.Encode(shards); err != nil {
				t.Fatal(err)
			}
			got, err := codec.Join(shards, c.size)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, data) {
				t.Fatal("Join(Split(data)) differs from data")
			}
		})
	}
}

func TestJoinErrors(t *testing.T) {
	codec := mustCodec(t, 2, 1)
	if _, err := codec.Join([][]byte{{1}, {2}}, 2); !errors.Is(err, erasure.ErrShardCount) {
		t.Fatalf("short slice: %v", err)
	}
	if _, err := codec.Join([][]byte{{1}, nil, {3}}, 2); !errors.Is(err, erasure.ErrTooFewShards) {
		t.Fatalf("missing data shard: %v", err)
	}
	if _, err := codec.Join([][]byte{{1}, {2}, {3}}, 3); !errors.Is(err, erasure.ErrShardSize) {
		t.Fatalf("size past the data: %v", err)
	}
	if _, err := codec.Join([][]byte{{1}, {2}, {3}}, -1); !errors.Is(err, erasure.ErrShardSize) {
		t.Fatalf("negative size: %v", err)
	}
	if _, err := codec.Join([][]byte{{1}, {2, 2}, {3}}, 2); !errors.Is(err, erasure.ErrShardSize) {
		t.Fatalf("unequal data shards: %v", err)
	}
}

func TestReconstructDataLeavesParityMissing(t *testing.T) {
	codec := mustCodec(t, 4, 2)
	shards := codec.Split([]byte("systematic reed-solomon"))
	if err := codec.Encode(shards); err != nil {
		t.Fatal(err)
	}
	want := cloneShards(shards)
	shards[1], shards[4] = nil, nil
	if err := codec.ReconstructData(shards); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(shards[1], want[1]) {
		t.Fatal("data shard 1 not rebuilt")
	}
	if shards[4] != nil {
		t.Fatal("ReconstructData rebuilt a parity shard")
	}
	if err := codec.Reconstruct(shards); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(shards[4], want[4]) {
		t.Fatal("parity shard 4 not rebuilt")
	}
}

func cloneShards(s [][]byte) [][]byte {
	out := make([][]byte, len(s))
	for i, b := range s {
		if b != nil {
			out[i] = append([]byte{}, b...)
		}
	}
	return out
}

var payloadSizes = []int{0, 1, 2, 3, 5, 7, 13, 31, 251, 257, 1021, 4093, 4096, 4097, 65521, 65536, 65537}

type codeCase struct {
	k, m    int
	payload []byte
	erased  []int
	trace   string
}

func drawCode(t *rapid.T, allowLarge bool) (int, int) {
	k := rapid.OneOf(rapid.IntRange(1, 8), rapid.SampledFrom([]int{1, 2, 4, 10, 16, 17, 32, 100, 128, 200, 255, 256})).Draw(t, "k")
	maxM := erasure.MaxShards - k
	if !allowLarge {
		maxM = min(maxM, 4)
	}
	m := rapid.OneOf(rapid.IntRange(0, min(4, maxM)), rapid.IntRange(0, maxM), rapid.Just(maxM)).Draw(t, "m")
	return k, m
}

func drawPayload(t *rapid.T, k int, small bool) []byte {
	class := rapid.IntRange(0, 99).Draw(t, "sizeClass")
	var size int
	switch {
	case class < 40:
		size = rapid.SampledFrom(append([]int{max(k-1, 0), k, k + 1, 2*k - 1, 2 * k, 2*k + 1}, payloadSizes...)).Draw(t, "boundarySize")
	case class < 97 || small:
		size = rapid.IntRange(0, 8192).Draw(t, "size")
	default:
		size = rapid.IntRange(1<<20, 3<<20).Draw(t, "multiMBSize")
	}
	if small {
		size = min(size, 64<<10)
	}
	seed := rapid.Uint64().Draw(t, "contentSeed")
	r := rand.New(rand.NewPCG(seed, uint64(size)))
	p := make([]byte, size)
	for i := range p {
		p[i] = byte(r.Uint32())
	}
	return p
}

func drawErasures(t *rapid.T, n, max int) []int {
	if max == 0 {
		return nil
	}
	return rapid.SliceOfNDistinct(rapid.IntRange(0, n-1), 0, max, rapid.ID[int]).Draw(t, "erased")
}

func checkRoundTrip(codec *erasure.Codec, payload []byte, erased []int) error {
	shards := codec.Split(payload)
	if err := codec.Encode(shards); err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	for i := 0; i < codec.K(); i++ {
		lo := min(i*len(shards[0]), len(payload))
		hi := min(lo+len(shards[0]), len(payload))
		if !bytes.Equal(shards[i][:hi-lo], payload[lo:hi]) {
			return fmt.Errorf("data shard %d is not the payload: the code is not systematic", i)
		}
	}
	want := cloneShards(shards)
	for _, e := range erased {
		shards[e] = nil
	}
	if err := codec.Reconstruct(shards); err != nil {
		return fmt.Errorf("reconstruct with %d erasures %v: %w", len(erased), erased, err)
	}
	for i := range shards {
		if !bytes.Equal(shards[i], want[i]) {
			return fmt.Errorf("shard %d rebuilt wrong", i)
		}
	}
	if err := codec.Verify(shards); err != nil {
		return fmt.Errorf("verify after reconstruct: %w", err)
	}
	got, err := codec.Join(shards, len(payload))
	if err != nil {
		return fmt.Errorf("join: %w", err)
	}
	if !bytes.Equal(got, payload) {
		return errors.New("joined bytes differ from the payload")
	}
	return nil
}

func propRoundTrip(small bool) func(*rapid.T) {
	return func(t *rapid.T) {
		k, m := drawCode(t, true)
		payload := drawPayload(t, k, small || k+m > 16)
		codec, err := erasure.New(k, m)
		if err != nil {
			t.Fatal(err)
		}
		erased := drawErasures(t, k+m, m)
		if err := checkRoundTrip(codec, payload, erased); err != nil {
			t.Fatalf("k=%d m=%d size=%d: %v", k, m, len(payload), err)
		}
	}
}

func TestPropRoundTrip(t *testing.T) {
	rapid.Check(t, propRoundTrip(false))
}

func TestRoundTripMultiMB(t *testing.T) {
	codec := mustCodec(t, 4, 2)
	r := rand.New(rand.NewPCG(7, 7))
	for _, size := range []int{1 << 20, 3<<20 + 17} {
		p := make([]byte, size)
		for i := range p {
			p[i] = byte(r.Uint32())
		}
		for _, erased := range [][]int{{0, 1}, {2, 5}, {4, 5}, {3}} {
			if err := checkRoundTrip(codec, p, erased); err != nil {
				t.Fatalf("size %d erased %v: %v", size, erased, err)
			}
		}
	}
}

func TestRoundTripEveryErasureSetSmall(t *testing.T) {
	payload := []byte("every erasure set of size at most m, data and parity alike")
	for k := 1; k <= 5; k++ {
		for m := 0; m <= 4; m++ {
			codec := mustCodec(t, k, m)
			for e := 0; e <= m; e++ {
				if e == 0 {
					if err := checkRoundTrip(codec, payload, nil); err != nil {
						t.Fatalf("k=%d m=%d: %v", k, m, err)
					}
					continue
				}
				combinations(k+m, e, func(erased []int) bool {
					if err := checkRoundTrip(codec, payload, append([]int(nil), erased...)); err != nil {
						t.Fatalf("k=%d m=%d erased %v: %v", k, m, erased, err)
					}
					return true
				})
			}
		}
	}
}

func propFailureBoundary(t *rapid.T) {
	k, m := drawCode(t, false)
	if k > 32 {
		k = 32
	}
	codec, err := erasure.New(k, m)
	if err != nil {
		t.Fatal(err)
	}
	payload := drawPayload(t, k, true)
	shards := codec.Split(payload)
	if err := codec.Encode(shards); err != nil {
		t.Fatal(err)
	}
	erased := rapid.SliceOfNDistinct(rapid.IntRange(0, k+m-1), m+1, k+m, rapid.ID[int]).Draw(t, "erased")
	before := cloneShards(shards)
	for _, e := range erased {
		shards[e] = nil
	}
	for _, op := range []func([][]byte) error{codec.Reconstruct, codec.ReconstructData} {
		err := op(shards)
		if !errors.Is(err, erasure.ErrTooFewShards) {
			t.Fatalf("k=%d m=%d with %d erasures: error %v, want ErrTooFewShards", k, m, len(erased), err)
		}
		for i := range shards {
			if (shards[i] == nil) != (before[i] == nil || contains(erased, i)) {
				t.Fatalf("shard %d presence changed on a failed reconstruct", i)
			}
			if shards[i] != nil && !bytes.Equal(shards[i], before[i]) {
				t.Fatalf("shard %d changed on a failed reconstruct", i)
			}
		}
	}
	if _, err := codec.Join(shards, len(payload)); len(payload) > 0 && err == nil && anyDataErased(erased, k) {
		t.Fatal("Join succeeded with a data shard missing")
	}
}

func contains(s []int, v int) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func anyDataErased(erased []int, k int) bool {
	for _, e := range erased {
		if e < k {
			return true
		}
	}
	return false
}

func TestPropFailureBoundary(t *testing.T) {
	rapid.Check(t, propFailureBoundary)
}

func propErasureCoding(t *rapid.T) {
	k, m := drawCode(t, true)
	payload := drawPayload(t, k, true)
	codec, err := erasure.New(k, m)
	if err != nil {
		t.Fatal(err)
	}
	n := k + m
	shards := codec.Split(payload)
	if err := codec.Encode(shards); err != nil {
		t.Fatal(err)
	}
	want := cloneShards(shards)
	lost := rapid.SliceOfNDistinct(rapid.IntRange(0, n-1), 0, n, rapid.ID[int]).Draw(t, "lost")
	for _, e := range lost {
		shards[e] = nil
	}
	err = codec.Reconstruct(shards)
	if len(lost) > m {
		if !errors.Is(err, erasure.ErrTooFewShards) {
			t.Fatalf("k=%d m=%d lost %d: error %v, want ErrTooFewShards", k, m, len(lost), err)
		}
		return
	}
	if err != nil {
		t.Fatalf("k=%d m=%d lost %d: %v", k, m, len(lost), err)
	}
	for i := range shards {
		if !bytes.Equal(shards[i], want[i]) {
			t.Fatalf("k=%d m=%d lost %v: shard %d rebuilt wrong", k, m, lost, i)
		}
	}
	got, err := codec.Join(shards, len(payload))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("join: %v", err)
	}
}

func FuzzErasureCoding(f *testing.F) {
	for _, seed := range codingFuzzSeeds() {
		f.Add(seed)
	}
	f.Fuzz(rapid.MakeFuzz(propErasureCoding))
}

func TestPropErasureCodingAnyLossSet(t *testing.T) {
	rapid.Check(t, propErasureCoding)
}

func TestDecodingIsTotalOnHostileShards(t *testing.T) {
	codec := mustCodec(t, 3, 2)
	inputs := [][][]byte{
		nil,
		{},
		{nil, nil, nil, nil, nil},
		{{}, {}, {}, {}, {}},
		{{1}, {}, {1}, {1}, {1}},
		{{1, 2}, {3, 4}, {5, 6}, {7}, nil},
		{{1}, {2}, {3}, {4}, {5}, {6}},
	}
	for i, in := range inputs {
		for _, op := range []func([][]byte) error{codec.Reconstruct, codec.ReconstructData, codec.Verify, codec.Encode} {
			err := op(cloneShards(in))
			if err == nil {
				continue
			}
			if !errors.Is(err, erasure.ErrShardCount) && !errors.Is(err, erasure.ErrShardSize) &&
				!errors.Is(err, erasure.ErrTooFewShards) && !errors.Is(err, erasure.ErrParityMismatch) {
				t.Fatalf("input %d: error %v is not one of the package sentinels", i, err)
			}
		}
	}
}

func TestShardSizeAtTheIntLimit(t *testing.T) {
	for _, k := range []int{1, 2, 4, 255, 256} {
		codec := mustCodec(t, k, 0)
		for _, size := range []int{math.MaxInt, math.MaxInt - 1, math.MaxInt - k + 1, math.MaxInt - k/2} {
			want := (size-1)/k + 1
			if got := codec.ShardSize(size); got != want || got <= 0 {
				t.Fatalf("k=%d ShardSize(%d) = %d, want %d", k, size, got, want)
			}
		}
	}
}
