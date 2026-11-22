package occ_test

import (
	"fmt"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/occ"
)

func benchDocs(b *testing.B, n int) (*occ.Doc, *occ.Doc) {
	x, _ := newTestStore(b, "x", 1, 1)
	y, _ := newTestStore(b, "y", 1, 2)
	for i := range n {
		k := fmt.Sprintf("key-%06d", i)
		mustPut(b, x, k, "base", occ.IfNoneMatch())
	}
	y.Merge(x.Doc())
	for i := range n {
		k := fmt.Sprintf("key-%06d", i)
		if i%10 == 0 {
			mustPut(b, x, k, "left", occ.IfMatch(x.Record(k).ETag()))
			mustPut(b, y, k, "right", occ.IfMatch(y.Record(k).ETag()))
		}
	}
	return x.Doc(), y.Doc()
}

// BenchmarkMergeDoc measures Doc.Merge, Store.Merge with 10% concurrent
// discards, and canonical encode+decode at 1,000 and 10,000 records.
func BenchmarkMergeDoc(b *testing.B) {
	for _, n := range []int{1_000, 10_000} {
		b.Run(fmt.Sprintf("records=%d", n), func(b *testing.B) {
			x, y := benchDocs(b, n)
			b.ResetTimer()
			for range b.N {
				z := x.Clone()
				z.Merge(y)
			}
		})
		b.Run(fmt.Sprintf("store merge with discards records=%d", n), func(b *testing.B) {
			x, y := benchDocs(b, n)
			b.ResetTimer()
			for range b.N {
				b.StopTimer()
				cfg := storeConfig("z", 1, 3, &fakeClock{now: 1_000_000})
				cfg.Doc = x.Clone()
				s, err := occ.NewStore(cfg)
				if err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if _, err := s.Merge(y); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run(fmt.Sprintf("encode+decode records=%d", n), func(b *testing.B) {
			x, y := benchDocs(b, n)
			x.Merge(y)
			b.ReportMetric(float64(len(x.Encode())), "bytes")
			b.ResetTimer()
			for range b.N {
				if _, err := occ.Decode(x.Clone().Encode()); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
