package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"hash/crc32"
	"math"
	"math/rand/v2"
	"reflect"
	"time"
)

func enc(rs ...saga.Record) []byte {
	var b []byte
	var e error
	for _, r := range rs {
		b, e = saga.AppendRecord(b, r)
		must(e)
	}
	return b
}
func begin() saga.Record          { return saga.Record{Kind: saga.RecBegin, Def: "d", Data: []byte("input")} }
func intent(i, a int) saga.Record { return saga.Record{Kind: saga.RecIntent, Step: i, Attempt: a} }
func done(i, a int) saga.Record {
	return saga.Record{Kind: saga.RecDone, Step: i, Attempt: a, Data: []byte("output")}
}
func completeLog() []saga.Record {
	return []saga.Record{begin(), intent(0, 1), done(0, 1), {Kind: saga.RecEnd, State: saga.StateCompleted}}
}
func codec() {
	add("C1", "roundtrip-prefixes", func() string {
		r := rand.New(rand.NewPCG(91001, 4))
		for n := 0; n < 100; n++ {
			rs := completeLog()
			rs[0].Data = make([]byte, r.IntN(1000))
			for i := range rs[0].Data {
				rs[0].Data[i] = byte(r.Uint32())
			}
			b := enc(rs...)
			got, v, e := saga.DecodeLog(b)
			must(e)
			check(v == len(b) && reflect.DeepEqual(got, rs), "seed=91001 n=%d roundtrip differs", n)
			for cut := 0; cut < len(b); cut++ {
				_, v, e := saga.DecodeLog(b[:cut])
				check(e == nil && v <= cut, "seed=91001 cut=%d err=%v", cut, e)
			}
		}
		return "100 random records and every byte prefix decoded consistently (seed=91001)"
	})
	add("C1", "bit-corruption", func() string {
		b := enc(completeLog()...)
		for i := range b {
			for bit := 0; bit < 8; bit++ {
				x := bytes.Clone(b)
				x[i] ^= 1 << bit
				_, _, e := saga.DecodeLog(x)
				check(errors.Is(e, saga.ErrLogCorrupt), "seed=0 byte=%d bit=%d err=%v", i, bit, e)
			}
		}
		return "every single-bit mutation of a complete four-record log was corrupt"
	})
	add("C1", "canonical-size-limits", func() string {
		_, e := saga.AppendRecord(nil, saga.Record{Kind: saga.RecDone, Data: make([]byte, saga.MaxRecord)})
		check(errors.Is(e, env.ErrTooLarge), "oversize accepted: %v", e)
		for _, r := range []saga.Record{{Kind: 0}, {Kind: saga.RecDone, Step: -2}, {Kind: saga.RecIntent, Attempt: math.MaxInt32 + 1}, {Kind: saga.RecEnd, State: 99}} {
			_, e := saga.AppendRecord(nil, r)
			check(errors.Is(e, env.ErrInvalidArgument), "invalid record accepted: %+v", r)
		}
		b := enc(begin())
		b = append(b[:14], append([]byte{0x80, 0}, b[15:]...)...)
		binary.BigEndian.PutUint32(b[1:5], uint32(len(b)-13))
		t := crc32.MakeTable(crc32.Castagnoli)
		binary.BigEndian.PutUint32(b[5:9], crc32.Checksum(b[:5], t))
		binary.BigEndian.PutUint32(b[9:13], crc32.Checksum(b[13:], t))
		_, _, e = saga.DecodeLog(b)
		check(errors.Is(e, saga.ErrLogCorrupt), "noncanonical zero accepted: %v", e)
		return "oversized, invalid-field and correctly checksummed noncanonical records were rejected"
	})
	add("C14", "random-byte-decoding", func() string {
		r := rand.New(rand.NewPCG(91002, 9))
		for n := 0; n < 50000; n++ {
			b := make([]byte, r.IntN(512))
			for i := range b {
				b[i] = byte(r.Uint32())
			}
			if n%2 == 0 && len(b) > 0 {
				b[0] = 0xa5
			}
			_, v, e := saga.DecodeLog(b)
			check(v >= 0 && v <= len(b) && (e == nil || errors.Is(e, saga.ErrLogCorrupt)), "seed=91002 case=%d invalid result", n)
		}
		return "50000 deterministic malformed byte strings returned a prefix or corruption without panic (seed=91002)"
	})
	add("C5", "legal-sequences-and-deletions", func() string {
		for n := 1; n <= 12; n++ {
			rs := []saga.Record{begin()}
			for i := 0; i < n; i++ {
				rs = append(rs, intent(i, 1), done(i, 1))
			}
			rs = append(rs, saga.Record{Kind: saga.RecEnd, State: saga.StateCompleted})
			for k := 1; k <= len(rs); k++ {
				s, e := saga.Resume(rs[:k], n, n, 3)
				must(e)
				check(s.Step >= 0 && s.Step <= n, "bad step")
			}
			for k := 1; k < len(rs)-1; k++ {
				x := append([]saga.Record{}, rs[:k]...)
				x = append(x, rs[k+1:]...)
				_, e := saga.Resume(x, n, n, 3)
				check(errors.Is(e, saga.ErrLogCorrupt), "seed=0 n=%d deleted record=%d accepted", n, k)
			}
		}
		return "all prefixes of 1..12-step traces accepted and each removed intent/done rejected"
	})
	add("C5", "retry-bound-and-pivot-plans", func() string {
		for p := 0; p <= 1; p++ {
			for n := 1; n < 20; n++ {
				rs := []saga.Record{begin()}
				for a := 1; a <= n; a++ {
					rs = append(rs, intent(0, a))
				}
				s, e := saga.Resume(rs, 1, p, 3)
				must(e)
				want := saga.ActDo
				if p == 1 && n >= 3 {
					want = saga.ActAbort
				}
				check(s.Next == want, "pivot=%d attempts=%d next=%v", p, n, s.Next)
			}
		}
		return "unknown outcomes abort at the pre-pivot bound and keep retrying at the pivot"
	})
	add("C6", "backoff-extremes", func() string {
		for _, base := range []time.Duration{1, 7, time.Second, time.Duration(math.MaxInt64 / 2), time.Duration(math.MaxInt64)} {
			p := saga.Policy{MaxAttempts: 3, BaseDelay: base, MaxDelay: time.Duration(math.MaxInt64), StepTimeout: 1, JitterPPM: 1000000}
			must(p.Validate())
			a := rand.New(rand.NewPCG(912, 1))
			b := rand.New(rand.NewPCG(912, 1))
			for i := 1; i < 80; i++ {
				d := p.Backoff(i, a)
				check(d >= base && d == p.Backoff(i, b), "seed=912 base=%d attempt=%d d=%d", base, i, d)
			}
		}
		return "large delays saturated without wrap and seeded jitter replayed across 395 draws"
	})
	add("C6", "jitter-bound", func() string {
		p := saga.Policy{BaseDelay: 13 * time.Millisecond, MaxDelay: 107 * time.Millisecond, JitterPPM: 333333}
		r := rand.New(rand.NewPCG(913, 2))
		for i := 0; i < 10000; i++ {
			a := 1 + i%32
			d := p.Backoff(a, nil)
			j := p.Backoff(a, r)
			check(j >= d && j <= d+time.Duration(int64(d)*333333/1000000), "seed=913 draw=%d delay=%v", i, j)
		}
		return "10000 draws stayed in exact integer jitter bounds; no distribution claim"
	})
	add("C8", "key-collision-grid", func() string {
		seen := map[string]string{}
		for _, id := range []saga.ID{"a", "a1", "a-1", "a_1", "a.1", "x#1", "x"} {
			for i := -1; i < 100; i++ {
				k := saga.Key{Saga: id, Step: i}
				b := string(k.Bytes())
				old, ok := seen[b]
				check(!ok, "collision %s / %s", old, k.String())
				seen[b] = k.String()
			}
		}
		return "707 saga/step pairs including delimiter-shaped IDs had distinct binary keys"
	})
	add("C10", "error-wire-roundtrip", func() string {
		t, e := env.NewCodeTable(saga.Codes()...)
		must(e)
		for _, row := range saga.Codes() {
			se := &saga.StepError{Saga: "x", Step: "s", Attempt: 7, Compensation: true, Err: row.Err}
			code, msg := t.Encode(se)
			check(code == row.Code && errors.Is(t.Decode(code, msg), row.Err) && errors.Is(se, row.Err), "wire error %v", se)
		}
		return "wrapped step errors preserved every saga sentinel over wire encoding"
	})
	add("C15", "input-boundaries", func() string {
		for _, n := range []int{1, 127, 128, 1024, 1025} {
			m := saga.MigrationInput{Bucket: string(bytes.Repeat([]byte{'b'}, n)), From: "a", To: "b"}
			got, e := saga.DecodeMigrationInput(m.Encode())
			if n <= 1024 {
				check(e == nil && got == m, "n=%d roundtrip failed", n)
			} else {
				check(errors.Is(e, env.ErrInvalidArgument), "oversize not rejected")
			}
		}
		for _, m := range []saga.MigrationInput{{}, {Bucket: "x", From: "a", To: "a"}} {
			check(errors.Is(m.Validate(), env.ErrInvalidArgument), "bad input accepted")
		}
		return "1..1024 byte fields roundtripped and oversized or invalid inputs were correctly rejected"
	})
	add("C15", "input-truncation-garbage", func() string {
		b := (saga.MigrationInput{Bucket: "bk", From: "a", To: "b"}).Encode()
		for i := 0; i < len(b); i++ {
			_, e := saga.DecodeMigrationInput(b[:i])
			check(errors.Is(e, env.ErrInvalidArgument), "cut=%d accepted", i)
		}
		_, e := saga.DecodeMigrationInput(append(b, 0))
		check(errors.Is(e, env.ErrInvalidArgument), "trailing byte accepted")
		return fmt.Sprintf("all %d truncated encodings and trailing garbage rejected", len(b))
	})
}
