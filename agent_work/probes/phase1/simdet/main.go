// Phase 1 feasibility probe (not product code): is a single-threaded discrete-event loop
// with named PCG streams and a (time, seq) heap bit-reproducible from one int64 seed?
// Prints "<seed> <events> <sha256 of event log + final state>". With -leak it iterates a
// Go map when picking gossip targets, to show the entropy leak the plan must lint against.
package main

import (
	"container/heap"
	"crypto/sha256"
	"encoding/binary"
	"flag"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
)

type event struct {
	at, seq  uint64
	from, to int
	val      uint64
}
type queue []event

func (q queue) Len() int { return len(q) }
func (q queue) Less(i, j int) bool {
	return q[i].at < q[j].at || (q[i].at == q[j].at && q[i].seq < q[j].seq)
}
func (q queue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *queue) Push(x any)   { *q = append(*q, x.(event)) }
func (q *queue) Pop() any     { o := *q; e := o[len(o)-1]; *q = o[:len(o)-1]; return e }

// stream derives an independent PCG stream from the root seed and a label.
func stream(seed int64, label string) *rand.Rand {
	h := fnv.New64a()
	h.Write([]byte(label))
	return rand.New(rand.NewPCG(uint64(seed), h.Sum64()))
}

func main() {
	seed := flag.Int64("seed", 1, "root seed")
	n := flag.Int("nodes", 50, "node count")
	steps := flag.Int("events", 200000, "events to process")
	leak := flag.Bool("leak", false, "pick targets by map iteration (deliberate entropy leak)")
	flag.Parse()

	netR, faultR, nodeR := stream(*seed, "net"), stream(*seed, "fault"), stream(*seed, "node")
	state := make([]uint64, *n) // each node keeps the max value it has seen (a tiny CRDT)
	peers := map[int]bool{}
	for i := 0; i < *n; i++ {
		peers[i] = true
	}
	var q queue
	var seq, now uint64
	send := func(from, to int, val uint64) {
		if from%5 == 0 && to%5 == 1 && now < 5000 { // asymmetric partition, heals at t=5000
			return
		}
		if faultR.IntN(100) < 10 { // 10% drop
			return
		}
		copies := 1
		if faultR.IntN(100) < 5 { // 5% duplicate
			copies = 2
		}
		for c := 0; c < copies; c++ {
			seq++
			heap.Push(&q, event{at: now + 1 + netR.Uint64N(50), seq: seq, from: from, to: to, val: val}) // random delay = reorder
		}
	}
	for i := 0; i < *n; i++ {
		state[i] = nodeR.Uint64N(1 << 20)
		send(i, (i+1)%*n, state[i])
	}
	log := sha256.New()
	var buf [40]byte
	done := 0
	for ; done < *steps && q.Len() > 0; done++ {
		e := heap.Pop(&q).(event)
		now = e.at
		if e.val > state[e.to] {
			state[e.to] = e.val
		}
		binary.LittleEndian.PutUint64(buf[0:], e.at)
		binary.LittleEndian.PutUint64(buf[8:], e.seq)
		binary.LittleEndian.PutUint64(buf[16:], uint64(e.from))
		binary.LittleEndian.PutUint64(buf[24:], uint64(e.to))
		binary.LittleEndian.PutUint64(buf[32:], state[e.to])
		log.Write(buf[:])
		fan := 1
		if q.Len() < 4**n { // keep the queue alive without unbounded growth
			fan = 2
		}
		for f := 0; f < fan; f++ {
			to := nodeR.IntN(*n)
			if *leak {
				for p := range peers { // map order is randomized per process: the leak
					to = p
					break
				}
			}
			send(e.to, to, state[e.to])
		}
	}
	for _, s := range state {
		binary.LittleEndian.PutUint64(buf[0:], s)
		log.Write(buf[:8])
	}
	fmt.Printf("%d %d %x\n", *seed, done, log.Sum(nil))
}
