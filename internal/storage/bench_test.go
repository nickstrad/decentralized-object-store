package storage_test

import (
	"bytes"
	"math/rand/v2"
	"runtime"
	"testing"

	"github.com/nickstrad/object-store/internal/storage"
)

// BenchmarkStripedPutGet8MiB and BenchmarkStripedPutGet32MiB measure simulated
// PUT+GET throughput, peak live heap above baseline, and durable disk usage for
// 8 MiB and 32 MiB objects on 9 nodes under the default 4+2 layout.
func BenchmarkStripedPutGet8MiB(b *testing.B)  { benchPutGet(b, 8<<20) }
func BenchmarkStripedPutGet32MiB(b *testing.B) { benchPutGet(b, 32<<20) }

func benchPutGet(b *testing.B, size int) {
	r := rand.New(rand.NewPCG(3, 4))
	data := make([]byte, size)
	for i := range data {
		data[i] = byte(r.Uint32())
	}
	b.SetBytes(int64(size))
	var peak, disk uint64
	for b.Loop() {
		s, ids, _ := newCluster(b, 1, 9, 3, storage.DefaultLayout())
		var ms runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&ms)
		base := ms.HeapAlloc
		s.AddInvariant("heap", 10, func() error {
			runtime.GC()
			runtime.ReadMemStats(&ms)
			if ms.HeapAlloc > base {
				peak = max(peak, ms.HeapAlloc-base)
			}
			return nil
		})
		p := put(b, s, nodeOf(b, s, ids[0]), "b", "big", data)
		if p.err != nil {
			b.Fatal(p.err)
		}
		g := get(b, s, nodeOf(b, s, ids[1]), p.man)
		if g.err != nil || !bytes.Equal(g.res.Data, data) {
			b.Fatal(g.err)
		}
		disk = 0
		for _, id := range ids {
			for _, name := range s.Disk(id).Names() {
				blob, _ := s.Disk(id).PeekDurable(name)
				disk += uint64(len(blob))
			}
		}
	}
	b.ReportMetric(float64(peak)/(1<<20), "peakLiveHeapMiB")
	b.ReportMetric(float64(disk)/(1<<20), "durableDiskMiB")
}
