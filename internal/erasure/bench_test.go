package erasure_test

import (
	"math/rand/v2"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/erasure"
)

func benchStripe(b *testing.B) (*erasure.Codec, [][]byte) {
	codec, err := erasure.New(4, 2)
	if err != nil {
		b.Fatal(err)
	}
	r := rand.New(rand.NewPCG(1, 2))
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	shards := codec.Split(data)
	if err := codec.Encode(shards); err != nil {
		b.Fatal(err)
	}
	return codec, shards
}

func BenchmarkEncode1MiBStripe(b *testing.B) {
	codec, shards := benchStripe(b)
	b.SetBytes(1 << 20)
	for b.Loop() {
		if err := codec.Encode(shards); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReconstructTwoDataShards1MiBStripe(b *testing.B) {
	codec, shards := benchStripe(b)
	b.SetBytes(1 << 20)
	for b.Loop() {
		shards[0], shards[2] = nil, nil
		if err := codec.ReconstructData(shards); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkReconstructAllMissing1MiBStripe(b *testing.B) {
	codec, shards := benchStripe(b)
	b.SetBytes(1 << 20)
	for b.Loop() {
		shards[1], shards[3] = nil, nil
		if err := codec.Reconstruct(shards); err != nil {
			b.Fatal(err)
		}
	}
}
