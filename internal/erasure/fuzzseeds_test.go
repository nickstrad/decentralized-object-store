package erasure_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

const fuzzSeedLen = 4096

func fuzzSeedStream(label string, n int) []byte {
	out := make([]byte, 0, n)
	var ctr [8]byte
	for i := uint64(0); len(out) < n; i++ {
		binary.BigEndian.PutUint64(ctr[:], i)
		sum := sha256.Sum256(append([]byte(label), ctr[:]...))
		out = append(out, sum[:]...)
	}
	return out[:n]
}

func fuzzSeeds(label string) [][]byte {
	seeds := [][]byte{
		make([]byte, fuzzSeedLen),
		bytes.Repeat([]byte{1, 0, 0, 0, 0, 0, 0, 0}, fuzzSeedLen/8),
		bytes.Repeat([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x7f}, fuzzSeedLen/8),
	}
	for i := 0; i < 8; i++ {
		seeds = append(seeds, fuzzSeedStream(fmt.Sprintf("%s/%d", label, i), fuzzSeedLen))
	}
	return seeds
}

func gf256FuzzSeeds() [][]byte { return fuzzSeeds("gf256") }

func codingFuzzSeeds() [][]byte { return fuzzSeeds("coding") }

func TestFuzzSeedsRunToCompletion(t *testing.T) {
	targets := []struct {
		name  string
		prop  func(*rapid.T)
		seeds [][]byte
	}{
		{"FuzzGF256", propFieldAxioms, gf256FuzzSeeds()},
		{"FuzzErasureCoding", propErasureCoding, codingFuzzSeeds()},
	}
	for _, tg := range targets {
		fn := rapid.MakeFuzz(tg.prop)
		for i, seed := range tg.seeds {
			var sub *testing.T
			t.Run(fmt.Sprintf("%s/seed%d", tg.name, i), func(st *testing.T) {
				sub = st
				fn(st, seed)
			})
			if sub.Skipped() {
				t.Errorf("%s seed %d (%d bytes) skipped: rapid ran out of input before the property finished", tg.name, i, len(seed))
			}
		}
	}
}
