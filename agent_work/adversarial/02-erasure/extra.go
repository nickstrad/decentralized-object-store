package main

import (
	"errors"
	"fmt"
	e "github.com/nickstrad/object-store/internal/erasure"
	"math/rand"
	"reflect"
)

func extraAlgebra() {
	attack("C6,C9,C10", "random-loss-differential", func() string {
		r := rand.New(rand.NewSource(220211))
		for trial := 0; trial < 300; trial++ {
			k := 1 + r.Intn(64)
			m := r.Intn(65)
			c := codec(k, m)
			data := payload(r.Intn(509))
			s := c.Split(data)
			must(c.Encode(s))
			base := clone(s)
			loss := r.Intn(m + 1)
			for _, i := range r.Perm(k + m)[:loss] {
				s[i] = nil
			}
			ref := refDecode(k, s)
			must(c.ReconstructData(s))
			for j := 0; j < k; j++ {
				same(s[j], ref[j])
			}
			must(c.Reconstruct(s))
			check(reflect.DeepEqual(s, base), "trial=%d k=%d m=%d loss=%d", trial, k, m, loss)
		}
		return "300 independently seeded midrange layouts and partial loss sets match augmented-elimination oracle"
	})
	attack("C11", "codec-sentinels", func() string {
		c := codec(4, 2)
		base := c.Split(payload(13))
		must(c.Encode(base))
		for _, index := range []int{0, 3, 4, 5} {
			s := clone(base)
			s[index][0] ^= 1
			err := c.Verify(s)
			check(errors.Is(err, e.ErrParityMismatch), "modified shard=%d verify=%v", index, err)
		}
		for _, index := range []int{0, 3, 4, 5} {
			s := clone(base)
			s[index] = append(s[index], 0)
			err := c.Reconstruct(s)
			check(errors.Is(err, e.ErrShardSize), "wrong length index=%d err=%v", index, err)
			var se *e.ShardError
			check(errors.As(err, &se), "no indexed error")
			check(se.Error() != "", "empty indexed error")
		}
		for _, size := range []int{-1, 17, 1000000} {
			_, err := c.Join(base, size)
			check(errors.Is(err, e.ErrShardSize), "invalid join size=%d err=%v", size, err)
		}
		for _, f := range []func([][]byte) error{c.Encode, c.Verify, c.Reconstruct, c.ReconstructData} {
			err := f(nil)
			check(errors.Is(err, e.ErrShardCount), "wrong count err=%v", err)
		}
		return fmt.Sprint("parity mismatch, unequal lengths, invalid Join sizes and counts return package sentinels")
	})
}
