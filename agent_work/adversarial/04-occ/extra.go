package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/sim"
	"math"
	"time"
)

func extras() {
	check("C17", "recount-stale-noop", func() string {
		s := store("a", 0)
		_, e := s.WriteQuota("q", occ.Usage{Bytes: 5}, occ.IfNoneMatch())
		must(e)
		calls := 0
		want := occ.Usage{Bytes: 5}
		_, _, e = s.RecountQuota("q", 3, func(string) (occ.Usage, error) {
			calls++
			if calls == 1 {
				_, p, e := s.ReadQuota("q")
				must(e)
				_, e = s.WriteQuota("q", occ.Usage{Bytes: 7}, p)
				must(e)
			}
			return want, nil
		})
		must(e)
		got, _, e := s.ReadQuota("q")
		must(e)
		require(got == want, "initial quota=5; recount hook writes quota=7 then returns 5; calls=%d final bytes=%d without retry", calls, got.Bytes)
		return "recount rechecked unchanged-result precondition after intervening update"
	})
	check("C3,C7", "single-replica-model", func() string {
		r := sim.NewStream(4701, "cas-model")
		s := store("a", 0)
		live, present := false, false
		var tag occ.ETag
		var val []byte
		for i := 0; i < 5000; i++ {
			kind := r.IntN(4)
			p := occ.Unconditional()
			allowed := true
			switch kind {
			case 1:
				p = occ.IfNoneMatch()
				allowed = !live
			case 2:
				p = occ.IfMatch(tag)
				allowed = present
			case 3:
				p = occ.IfMatch(occ.ETag{99})
				allowed = false
			}
			del := r.IntN(3) == 0
			next := []byte(fmt.Sprint(i))
			before := s.EncodeState()
			var v occ.Version
			var e error
			if del {
				v, e = s.Delete("k", p)
			} else {
				v, e = s.Put("k", next, p)
			}
			if !allowed {
				require(errors.Is(e, occ.ErrPreconditionFailed), "step=%d wrong refusal", i)
			} else if del && !live {
				require(errors.Is(e, occ.ErrNotFound), "step=%d missing delete", i)
			} else {
				must(e)
				require(tag != v.ETag(), "step=%d reused etag", i)
				tag = v.ETag()
				live = !del
				present = true
				val = next
			}
			if e != nil {
				require(bytes.Equal(before, s.EncodeState()), "step=%d refusal mutation", i)
			}
			got, t, e := s.Get("k")
			require(t == tag, "step=%d etag mismatch", i)
			if live {
				must(e)
				require(bytes.Equal(got, val), "step=%d value mismatch", i)
			} else {
				require(errors.Is(e, occ.ErrNotFound), "step=%d live mismatch", i)
			}
		}
		require(s.LWWDiscards() == 0, "causal-only writes discarded")
		return "5000 mixed put/delete/CAS operations matched independent present/live/value/etag state"
	})
	check("C6,C8", "saturating-count-and-dot-rotation", func() string {
		hooks := 0
		s, e := occ.NewStore(occ.StoreConfig{Node: "a", Epoch: 1, Counter: math.MaxUint64, Discards: math.MaxUint64, Clock: &clock{}, Rand: sim.NewStream(4701, "overflow"), OnDiscard: func(string, occ.Version, occ.Version) { hooks++ }})
		must(e)
		v := put(s, "k", nil)
		require(v.Dot().Actor.Epoch != 1 && v.Dot().Counter == 1 && s.Rotations() == 1, "dot failed to rotate")
		merge(s, doc(ver("z", 1, occ.Stamp{Wall: 100, Node: "z"}, true)))
		require(s.LWWDiscards() == math.MaxUint64 && hooks == 1 && len(s.DiscardTail()) == 1, "saturation suppressed log or wrapped")
		return "maximum dot counter rotated epoch and maximum discard count saturated while retaining the callback/tail"
	})
	check("C4,C18", "encoding-copy-isolation", func() string {
		s := store("a", 0)
		v := put(s, "k", []byte("value"))
		d := s.Doc()
		orig := d.Encode()
		enc := d.Encode()
		enc[0] ^= 255
		value := v.Value()
		value[0] ^= 255
		keys := d.Keys()
		keys[0] = "changed"
		clone := d.Clone()
		clone.MergeVersion("other", ver("b", 1, occ.Stamp{Wall: 9, Node: "b"}, false))
		require(bytes.Equal(d.Encode(), orig) && s.Record("k").ETag() == v.ETag(), "returned slices/clone mutate original")
		round, e := occ.Decode(orig)
		must(e)
		require(round.Digest() == d.Digest() && round.Record("k").ETag() == v.ETag(), "roundtrip changed tag/digest")
		return "encoded bytes, value slices, key lists and cloned docs could be mutated without changing source state"
	})
	check("C19,C20,C21", "repeated-key-large-input", func() string {
		base := doc(ver("a", 1, occ.Stamp{Wall: 1, Node: "a"}, false)).Encode()
		record := base[5:]
		for _, size := range []int{2 << 20, 8 << 20, 16 << 20} {
			n := size / len(record)
			b := append([]byte("OCC\x02"), binary.AppendUvarint(nil, uint64(n))...)
			b = append(b, bytes.Repeat(record, n)...)
			_, e := occ.Decode(b)
			require(errors.Is(e, occ.ErrMalformed), "size=%d repeated key accepted", size)
		}
		return "2, 8 and 16 MiB canonical-record repetition inputs were rejected; operation counts are checked separately by existing instrumented tests"
	})
	check("C10,C14", "hlc-observe-many", func() string {
		c := &clock{t: -200}
		h := occ.NewHLC(c, "a")
		var last occ.Stamp
		for i := 0; i < 10000; i++ {
			c.t += env.Time(i % 3)
			st := occ.Stamp{Wall: int64(c.t) + int64(i%10), Logical: uint32(i % 4), Node: "b"}
			must(h.Observe(st))
			v, e := h.Now()
			must(e)
			require(v.After(st) && (i == 0 || v.After(last)), "step=%d clock went backward", i)
			last = v
		}
		before := h.Last()
		e := h.Observe(occ.Stamp{Wall: int64(c.t) + int64(2*time.Hour), Node: "b"})
		require(errors.Is(e, occ.ErrClockAhead) && before == h.Last(), "ahead stamp mutated clock")
		return "10000 negative-to-positive clock observations and writes remained strictly monotone; ahead rejection preserved state"
	})
}
