package main

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"math"
	"os"
	"strings"
	"time"
)

var failed bool

func require(ok bool, f string, a ...any) {
	if !ok {
		panic(fmt.Sprintf(f, a...))
	}
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
func check(c, name string, fn func() string) {
	if filter := os.Getenv("CHECK"); filter != "" && filter != name {
		return
	}
	defer func() {
		if e := recover(); e != nil {
			failed = true
			fmt.Printf("BROKEN %s %s: seed=4701 %s\n", c, name, strings.ReplaceAll(fmt.Sprint(e), "\n", " "))
		}
	}()
	s := fn()
	fmt.Printf("HELD %s %s: %s\n", c, name, s)
}

type clock struct{ t env.Time }

func (c *clock) Now() env.Time                             { return c.t }
func (c *clock) AfterFunc(time.Duration, func()) env.Timer { panic("unused timer") }
func store(id string, t int64) *occ.Store {
	s, e := occ.NewStore(occ.StoreConfig{Node: env.NodeID(id), Epoch: 1, Clock: &clock{env.Time(t)}, Rand: sim.NewStream(4701, id)})
	must(e)
	return s
}
func put(s *occ.Store, k string, b []byte) occ.Version {
	v, e := s.Put(k, b, occ.Unconditional())
	must(e)
	return v
}
func doc(v occ.Version) *occ.Doc     { d := occ.NewDoc(); d.MergeVersion("k", v); return d }
func merge(s *occ.Store, d *occ.Doc) { _, e := s.Merge(d); must(e) }
func ver(id string, n uint64, st occ.Stamp, tomb bool) occ.Version {
	return occ.NewVersion(occ.Dot{Actor: occ.Actor{Node: env.NodeID(id), Epoch: 1}, Counter: n}, occ.VersionVector{}, tomb, []byte(fmt.Sprint(n)), st)
}
func core() {
	check("C1", "vector-model", func() string {
		r := sim.NewStream(4701, "vectors")
		for range 10000 {
			var es, fs []occ.Entry
			a, b := map[occ.Actor]uint64{}, map[occ.Actor]uint64{}
			for range 30 {
				x := occ.Actor{Node: env.NodeID(fmt.Sprint(r.IntN(5))), Epoch: uint64(r.IntN(3))}
				n := r.Uint64() % 20
				es = append(es, occ.Entry{Actor: x, Counter: n})
				a[x] = max(a[x], n)
				m := r.Uint64() % 20
				fs = append(fs, occ.Entry{Actor: x, Counter: m})
				b[x] = max(b[x], m)
			}
			v, w := occ.NewVersionVector(es...), occ.NewVersionVector(fs...)
			lo, hi := false, false
			for x, n := range a {
				lo = lo || n < b[x]
				hi = hi || n > b[x]
				require(v.Join(w).Get(x) == max(n, b[x]), "join mismatch")
			}
			want := occ.Equal
			if lo && hi {
				want = occ.Concurrent
			} else if lo {
				want = occ.Before
			} else if hi {
				want = occ.After
			}
			require(v.Compare(w) == want, "compare mismatch")
		}
		return "10000 duplicate/zero/epoch vector pairs matched pointwise maxima and ordering"
	})
	for _, del := range []bool{false, true} {
		check("C2", fmt.Sprintf("precondition-table-delete-%v", del), func() string {
			for state := 0; state < 3; state++ {
				for p := 0; p < 5; p++ {
					s := store("a", 0)
					if state > 0 {
						put(s, "k", []byte("old"))
					}
					if state == 2 {
						_, e := s.Delete("k", occ.Unconditional())
						must(e)
					}
					old := s.EncodeState()
					tag := s.Record("k").ETag()
					ps := []occ.Precondition{occ.Unconditional(), occ.IfNoneMatch(), occ.IfMatch(tag), occ.IfMatch(occ.ETag{1}), occ.IfMatch(occ.ETag{})}
					allowed := p == 0 || p == 1 && state != 1 || p == 2 && state != 0
					var e error
					var v occ.Version
					if del {
						v, e = s.Delete("k", ps[p])
					} else {
						v, e = s.Put("k", []byte("new"), ps[p])
					}
					if !allowed {
						require(errors.Is(e, occ.ErrPreconditionFailed), "state=%d pre=%d got=%v", state, p, e)
					} else if del && state != 1 {
						require(errors.Is(e, occ.ErrNotFound), "missing delete: %v", e)
					} else {
						must(e)
						require(v.ETag() != tag && s.Record("k").ETag() == v.ETag(), "success tag mismatch")
					}
					if e != nil {
						require(bytes.Equal(old, s.EncodeState()), "failed call changed state")
					}
					require(s.LWWDiscards() == 0, "local discard")
				}
			}
			return "all 15 state/precondition rows matched independent acceptance table and rollback bytes"
		})
	}
	check("C3,C4,C8", "etag-sequence", func() string {
		s := store("a", 0)
		seen := map[occ.ETag]bool{}
		dots := map[occ.Dot]bool{}
		for i := 0; i < 12000; i++ {
			v := put(s, fmt.Sprint(i%7), []byte("same"))
			require(!seen[v.ETag()] && !dots[v.Dot()], "reused etag or dot at %d", i)
			seen[v.ETag()] = true
			dots[v.Dot()] = true
			t, e := occ.ParseETag(v.ETag().String())
			must(e)
			require(t == v.ETag(), "parse mismatch")
			old := s.EncodeState()
			_, e = s.Put(fmt.Sprint(i%7), nil, occ.IfMatch(occ.ETag{9}))
			require(errors.Is(e, occ.ErrPreconditionFailed) && bytes.Equal(old, s.EncodeState()), "stale accepted")
		}
		return "12000 same-value writes had unique ETags/dots, parse round trips and stale-write rejection"
	})
	check("C5,C11,C12,C18", "decoded-permutations", func() string {
		r := sim.NewStream(4701, "laws")
		for i := 0; i < 1500; i++ {
			vs := make([]occ.Version, 3)
			ds := make([]*occ.Doc, 3)
			for j := range vs {
				st := occ.Stamp{Wall: int64(r.IntN(7)) - 3, Logical: uint32(r.IntN(3)), Node: env.NodeID(fmt.Sprint(r.IntN(2)))}
				vs[j] = ver(fmt.Sprint(j), uint64(i+1), st, r.IntN(2) == 0)
				var e error
				ds[j], e = occ.Decode(doc(vs[j]).Encode())
				must(e)
			}
			winner := vs[0]
			for _, v := range vs[1:] {
				a, b := v.Stamp(), winner.Stamp()
				ai, bi := v.ID(), winner.ID()
				if a.Wall > b.Wall || a.Wall == b.Wall && (a.Logical > b.Logical || a.Logical == b.Logical && (a.Node > b.Node || a.Node == b.Node && bytes.Compare(ai[:], bi[:]) > 0)) {
					winner = v
				}
			}
			for _, p := range [][3]int{{0, 1, 2}, {0, 2, 1}, {1, 0, 2}, {1, 2, 0}, {2, 0, 1}, {2, 1, 0}} {
				d := occ.NewDoc()
				for _, j := range p {
					d.Merge(ds[j])
					d.Merge(ds[j])
				}
				require(bytes.Equal(d.Encode(), doc(winner).Encode()), "iteration=%d order=%v", i, p)
				s := store("x", 0)
				merge(s, d)
				require(s.Doc().Equal(d), "store merge differs")
			}
		}
		return "1500 decoded triples in all six orders with duplicate deliveries matched an independent stamp/ID maximum"
	})
	check("C6,C7", "discard-accounting", func() string {
		logs := []string{}
		hooks := 0
		s, e := occ.NewStore(occ.StoreConfig{Node: "a", Epoch: 1, Clock: &clock{}, Rand: sim.NewStream(4701, "tail"), Log: func(x string) { logs = append(logs, x) }, OnDiscard: func(string, occ.Version, occ.Version) { hooks++ }})
		must(e)
		put(s, "k", nil)
		for i := 1; i <= 90; i++ {
			v := ver(fmt.Sprint(i), 1, occ.Stamp{Wall: int64(i), Node: env.NodeID(fmt.Sprint(i))}, i%2 == 0)
			merge(s, doc(v))
			merge(s, doc(v))
		}
		require(s.LWWDiscards() == 90 && len(logs) == 90 && hooks == 90 && len(s.DiscardTail()) == 64, "count/tail/log mismatch")
		for _, x := range s.DiscardTail() {
			require(strings.Contains(logs[len(logs)-1], "winner="), "missing log fields")
			require(x.Key == "k", "wrong key")
		}
		put(s, "k", nil)
		require(s.LWWDiscards() == 90, "causal overwrite counted")
		return "90 concurrent replacements produced 90 logs/hooks, 64 retained entries, no duplicate or causal count"
	})
	check("C10,C13,C14", "clock-boundaries", func() string {
		for _, st := range []occ.Stamp{{Wall: int64(sim.MaxSkew), Node: "z"}, {Wall: math.MaxInt64 - 1, Logical: math.MaxUint32, Node: "z"}, {Wall: math.MaxInt64, Logical: math.MaxUint32, Node: "z"}, {Wall: -100, Node: "z"}} {
			s := store("a", -1000)
			v := ver("z", 1, st, false)
			merge(s, doc(v))
			before := s.EncodeState()
			next, e := s.Put("k", nil, occ.IfMatch(v.ETag()))
			if st.Wall == math.MaxInt64 && st.Logical == math.MaxUint32 {
				require(errors.Is(e, occ.ErrClockExhausted) && bytes.Equal(before, s.EncodeState()), "end-time rejection changed state")
			} else {
				must(e)
				require(next.Stamp().After(st) && next.Supersedes(v), "causal successor did not win")
			}
			normal := put(s, "other", nil)
			require(normal.Stamp().Wall < 100000, "far future pinned unrelated key")
		}
		return "negative skew, maximum skew, logical carry and end-of-time rejection obeyed key-local clock rules"
	})
	check("C15", "persist-rollback", func() string {
		fail := false
		hooks := 0
		s, e := occ.NewStore(occ.StoreConfig{Node: "a", Clock: &clock{}, Rand: sim.NewStream(4701, "persist"), Persist: func([]byte) error {
			if fail {
				return env.ErrIO
			}
			return nil
		}, OnDiscard: func(string, occ.Version, occ.Version) { hooks++ }})
		must(e)
		put(s, "k", nil)
		before, st := s.EncodeState(), s.HLC().Last()
		fail = true
		_, e = s.Merge(doc(ver("z", 1, occ.Stamp{Wall: 100, Node: "z"}, false)))
		require(errors.Is(e, env.ErrIO) && bytes.Equal(before, s.EncodeState()) && st == s.HLC().Last() && hooks == 0 && len(s.DiscardTail()) == 0, "merge rollback leaked")
		_, e = s.Delete("k", occ.Unconditional())
		require(errors.Is(e, env.ErrIO) && bytes.Equal(before, s.EncodeState()), "delete rollback leaked")
		fail = false
		_, e = s.Delete("k", occ.Unconditional())
		must(e)
		return "failed merge and delete left state, clock, hook and tail unchanged; retry succeeded"
	})
	check("C16,C17", "quota-retry-recount", func() string {
		s := store("a", 0)
		calls := 0
		u, e := s.UpdateQuota("q", 3, func(u occ.Usage) (occ.Usage, error) {
			calls++
			if calls == 1 {
				_, e := s.WriteQuota("q", occ.Usage{Bytes: 5}, occ.IfNoneMatch())
				must(e)
			}
			u.Bytes++
			return u, nil
		})
		must(e)
		require(calls == 2 && u.Bytes == 6, "stale update applied")
		_, w, e := s.RecountQuota("q", 2, func(string) (occ.Usage, error) { return u, nil })
		must(e)
		require(!w, "identical recount wrote")
		_, e = s.WriteQuota("q", u, occ.Unconditional())
		require(errors.Is(e, env.ErrInvalidArgument), "blind quota allowed")
		_, e = occ.AddUsage(1, 0, occ.Usage{})(occ.Usage{Bytes: math.MaxUint64})
		require(errors.Is(e, env.ErrInvalidArgument), "quota overflow accepted")
		return "stale callback retried on fresh usage, identical recount did not write and invalid updates were rejected"
	})
	check("C19,C20,C22", "decode-corpus", func() string {
		r := sim.NewStream(4701, "bytes")
		base := doc(ver("a", 1, occ.Stamp{Wall: 5, Node: "a"}, false)).Encode()
		for n := 0; n < len(base); n++ {
			_, e := occ.Decode(base[:n])
			require(errors.Is(e, occ.ErrMalformed), "prefix %d accepted", n)
		}
		for i := 0; i < 20000; i++ {
			b := append([]byte{}, base...)
			if i%2 == 0 {
				b = make([]byte, r.IntN(2048))
				for j := range b {
					b[j] = byte(r.Uint64())
				}
			} else {
				b[r.IntN(len(b))] ^= byte(1 + r.IntN(255))
			}
			d, e := occ.Decode(b)
			if e != nil {
				require(errors.Is(e, occ.ErrMalformed), "wrong decode error")
			} else {
				require(bytes.Equal(b, d.Encode()), "accepted noncanonical bytes")
				s := store("a", 0)
				merge(s, d)
				x := d.Clone()
				x.Merge(d)
				require(x.Equal(d), "decoded idempotence failed")
			}
		}
		return "20000 random/mutated byte strings and every valid-document prefix rejected correctly or round-tripped canonically"
	})
	check("C23", "typed-roundtrips", func() string {
		s := store("a", 0)
		for _, b := range []string{"", "a/b", "a\x00b", "☃"} {
			_, e := s.PutObjectMeta(b, "x", occ.ObjectMeta{Size: 7, Created: -1}, occ.Unconditional())
			must(e)
			_, e = s.PutTag(b, "x", "n", "v", occ.Unconditional())
			must(e)
			_, e = s.PutACL(b, "p", occ.PermRead|occ.PermWrite, occ.Unconditional())
			must(e)
			u, e := s.RecountObjects(b)
			must(e)
			require(u == (occ.Usage{Bytes: 7, Objects: 1}), "bucket prefix collision")
			for _, k := range s.Keys() {
				_, _, e := occ.ParseKey(k)
				must(e)
			}
		}
		return "empty, slash, NUL and Unicode bucket names retained distinct keys and exact recounts"
	})
	check("C23", "recount-overflow", func() string {
		s := store("a", 0)
		_, e := s.PutObjectMeta("b", "x", occ.ObjectMeta{Size: math.MaxUint64}, occ.Unconditional())
		must(e)
		_, e = s.PutObjectMeta("b", "y", occ.ObjectMeta{Size: 1}, occ.Unconditional())
		must(e)
		u, e := s.RecountObjects("b")
		require(e != nil || u.Bytes == math.MaxUint64, "two objects of sizes MaxUint64 and 1 recount as bytes=%d objects=%d without error", u.Bytes, u.Objects)
		return "unrepresentable total was rejected or saturated"
	})
	check("C24", "wire-code-table", func() string {
		ct, e := occ.Codes()
		must(e)
		seen := map[env.Code]bool{}
		for _, x := range occ.CodeEntries() {
			require(x.Code >= 128 && x.Code <= 143 && x.Code != 131 && !seen[x.Code], "invalid code %d", x.Code)
			seen[x.Code] = true
			c, m := ct.Encode(x.Err)
			require(c == x.Code && errors.Is(ct.Decode(c, m), x.Err), "roundtrip %d", c)
		}
		return "all assigned codes are unique, in range, skip 131 and preserve errors.Is"
	})
}
func main() {
	if len(os.Args) != 2 {
		os.Exit(2)
	}
	switch os.Args[1] {
	case "core":
		core()
		extras()
	case "sim":
		scenarios()
	default:
		os.Exit(2)
	}
	if failed {
		os.Exit(1)
	}
}
