package main

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"reflect"
	"strings"

	e "github.com/nickstrad/decentralized-object-store/internal/erasure"
)

var broken int
var selected string

func attack(claim, name string, f func() string) {
	if selected != "" && selected != name {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			broken++
			fmt.Printf("BROKEN %s %s: seed=220211 %s\n", claim, name, strings.ReplaceAll(fmt.Sprint(p), "\n", " "))
		}
	}()
	detail := f()
	fmt.Printf("HELD %s %s: %s\n", claim, name, detail)
}
func check(ok bool, format string, args ...any) {
	if !ok {
		panic(fmt.Sprintf(format, args...))
	}
}
func must(err error)   { check(err == nil, "unexpected error: %v", err) }
func same(a, b []byte) { check(bytes.Equal(a, b), "bytes differ (lengths %d/%d)", len(a), len(b)) }
func clone(s [][]byte) [][]byte {
	out := make([][]byte, len(s))
	for i, b := range s {
		if b != nil {
			out[i] = append([]byte{}, b...)
		}
	}
	return out
}
func payload(n int) []byte    { b := make([]byte, n); rand.New(rand.NewSource(220211)).Read(b); return b }
func codec(k, m int) *e.Codec { c, err := e.New(k, m); must(err); return c }

// Independent polynomial arithmetic: no production GF, matrix or codec calls in the oracle.
func mul(a, b byte) byte {
	var z uint16
	x := uint16(a)
	for b > 0 {
		if b&1 != 0 {
			z ^= x
		}
		b >>= 1
		x <<= 1
		if x&256 != 0 {
			x ^= 0x11d
		}
	}
	return byte(z)
}
func pow(a byte, n int) byte {
	r := byte(1)
	for ; n > 0; n-- {
		r = mul(r, a)
	}
	return r
}
func generator(k, n int) [][]byte {
	g := make([][]byte, n)
	for i := range g {
		g[i] = make([]byte, k)
		for j := range g[i] {
			if i < k {
				if i == j {
					g[i][j] = 1
				}
			} else {
				g[i][j] = pow(byte(i^j), 254)
			}
		}
	}
	return g
}
func refEncode(k, m int, data []byte) [][]byte {
	size := (len(data) + k - 1) / k
	out := make([][]byte, k+m)
	g := generator(k, k+m)
	for i := range out {
		out[i] = make([]byte, size)
		for j := 0; j < k; j++ {
			for p := 0; p < size; p++ {
				if j*size+p < len(data) {
					out[i][p] ^= mul(g[i][j], data[j*size+p])
				}
			}
		}
	}
	return out
}

// Independent augmented elimination solves all payload columns at once.
func refDecode(k int, sh [][]byte) [][]byte {
	g := generator(k, len(sh))
	a := [][]byte{}
	size := 0
	for i, b := range sh {
		if b != nil && len(a) < k {
			size = len(b)
			a = append(a, append(append([]byte{}, g[i]...), b...))
		}
	}
	check(len(a) == k, "reference insufficient shards")
	for col := 0; col < k; col++ {
		p := col
		for p < k && a[p][col] == 0 {
			p++
		}
		check(p < k, "reference singular")
		a[p], a[col] = a[col], a[p]
		inv := pow(a[col][col], 254)
		for j := col; j < k+size; j++ {
			a[col][j] = mul(a[col][j], inv)
		}
		for i := 0; i < k; i++ {
			if i == col {
				continue
			}
			f := a[i][col]
			for j := col; j < k+size; j++ {
				a[i][j] ^= mul(f, a[col][j])
			}
		}
	}
	out := make([][]byte, k)
	for i := range out {
		out[i] = a[i][k:]
	}
	return out
}
func algebra() {
	attack("C1,C2,C3", "field-oracle", func() string {
		for a := 0; a < 256; a++ {
			for b := 0; b < 256; b++ {
				check(e.Mul(byte(a), byte(b)) == mul(byte(a), byte(b)), "product %d,%d", a, b)
				q, ok := e.Div(byte(a), byte(b))
				check(ok == (b != 0), "division flag")
				if ok {
					check(mul(q, byte(b)) == byte(a), "division %d,%d", a, b)
				}
			}
			iv, ok := e.Inv(byte(a))
			check(ok == (a != 0), "inverse flag")
			if ok {
				check(mul(byte(a), iv) == 1, "inverse %d", a)
			}
		}
		for i := -1024; i < 1024; i++ {
			n := (i%255 + 255) % 255
			check(e.Exp(i) == pow(2, n), "power %d", i)
			l, ok := e.Log(e.Exp(i))
			check(ok && l == n, "log %d", i)
		}
		return "65536 products/divisions, 256 inverses and 2048 signed exponents match polynomial oracle"
	})
	attack("C3", "field-laws-slices", func() string {
		r := rand.New(rand.NewSource(220211))
		for i := 0; i < 20000; i++ {
			a, b, c := byte(r.Intn(256)), byte(r.Intn(256)), byte(r.Intn(256))
			check(e.Mul(a, e.Add(b, c)) == e.Add(e.Mul(a, b), e.Mul(a, c)), "distributivity")
			check(e.Mul(e.Mul(a, b), c) == e.Mul(a, e.Mul(b, c)), "associativity")
			src := payload(i % 31)
			dst := payload(i % 37)
			want := append([]byte{}, dst...)
			for j := 0; j < min(len(src), len(dst)); j++ {
				want[j] ^= mul(a, src[j])
			}
			e.MulAddSlice(a, src, dst)
			same(dst, want)
		}
		return "20000 triples plus unequal-length multiply-add buffers agree"
	})
	attack("C4", "matrix-hostile-copy", func() string {
		for _, rows := range [][][]byte{nil, {{}}, {{1}, {1, 2}}} {
			_, err := e.NewMatrix(rows)
			check(errors.Is(err, e.ErrInvalidConfig), "accepted ragged/empty")
		}
		for _, rows := range [][][]byte{{{0, 1}, {1, 0}}, {{1, 2, 3}, {7, 9, 11}, {13, 15, 17}}} {
			a, err := e.NewMatrix(rows)
			must(err)
			before := a.Rows()
			iv, err := a.Invert()
			must(err)
			l, err := a.Mul(iv)
			must(err)
			r, err := iv.Mul(a)
			must(err)
			check(l.Equal(e.Identity(len(rows))) && r.Equal(l), "inverse")
			check(reflect.DeepEqual(before, a.Rows()), "inverse mutated")
			rows[0][0] ^= 123
			check(reflect.DeepEqual(before, a.Rows()), "input aliases")
			rr := a.Rows()
			rr[0][0] ^= 12
			check(reflect.DeepEqual(before, a.Rows()), "output aliases")
			for _, idx := range [][]int{{-1}, {len(rows)}, nil} {
				_, err = a.SelectRows(idx)
				check(err != nil, "bad rows accepted")
			}
		}
		a, _ := e.NewMatrix([][]byte{{1, 1}, {1, 1}})
		_, err := a.Invert()
		check(errors.Is(err, e.ErrSingular), "singular")
		a, _ = e.NewMatrix([][]byte{{1, 2}})
		_, err = a.Invert()
		check(errors.Is(err, e.ErrInvalidConfig), "nonsquare")
		return "row swaps, two-sided inverses, ownership and malformed matrices checked"
	})
	attack("C5,C7", "differential-encode", func() string {
		for k := 1; k <= 20; k++ {
			for m := 0; m <= 10; m++ {
				g, err := e.CodingMatrix(k, m)
				must(err)
				check(reflect.DeepEqual(g.Rows(), generator(k, k+m)), "matrix k=%d m=%d", k, m)
				for _, n := range []int{0, 1, k - 1, k, k + 1, 97} {
					data := payload(n)
					c := codec(k, m)
					s := c.Split(data)
					must(c.Encode(s))
					check(reflect.DeepEqual(s, refEncode(k, m, data)), "parity k=%d m=%d n=%d", k, m, n)
					must(c.Verify(s))
				}
			}
		}
		return "1320 encodings and 220 matrices exactly match independent Cauchy implementation"
	})
	attack("C6,C9,C10", "exhaustive-loss-sets", func() string {
		cases := 0
		for k := 1; k <= 5; k++ {
			for m := 0; m <= 4; m++ {
				c := codec(k, m)
				data := payload(31)
				base := c.Split(data)
				must(c.Encode(base))
				for mask := 0; mask < 1<<c.N(); mask++ {
					s := clone(base)
					loss := 0
					for j := range s {
						if mask>>j&1 != 0 {
							s[j] = nil
							loss++
						}
					}
					before := clone(s)
					err := c.Reconstruct(s)
					if loss > m {
						check(errors.Is(err, e.ErrTooFewShards), "k=%d m=%d mask=%x err=%v", k, m, mask, err)
						check(reflect.DeepEqual(s, before), "failure mutated shards")
					} else {
						must(err)
						check(reflect.DeepEqual(s, base), "wrong shards k=%d m=%d mask=%x", k, m, mask)
						ref := refDecode(k, before)
						for j := 0; j < k; j++ {
							same(s[j], ref[j])
						}
						out, err := c.Join(s, len(data))
						must(err)
						same(out, data)
					}
					cases++
				}
			}
		}
		return fmt.Sprintf("%d masks including all over-budget masks checked against independent decoder", cases)
	})
	attack("C6,C8,C9", "field-limit", func() string {
		for _, km := range [][2]int{{1, 255}, {2, 254}, {127, 129}, {128, 128}, {255, 1}, {256, 0}} {
			k, m := km[0], km[1]
			c := codec(k, m)
			for trial := 0; trial < 8; trial++ {
				data := payload(k + 17)
				s := c.Split(data)
				must(c.Encode(s))
				ref := refEncode(k, m, data)
				check(reflect.DeepEqual(s, ref), "limit parity k=%d", k)
				r := rand.New(rand.NewSource(int64(220211 + trial)))
				for _, i := range r.Perm(256)[:m] {
					s[i] = nil
				}
				oracle := refDecode(k, s)
				must(c.Reconstruct(s))
				for j := 0; j < k; j++ {
					same(s[j], oracle[j])
				}
				out, err := c.Join(s, len(data))
				must(err)
				same(out, data)
			}
		}
		for _, km := range [][2]int{{0, 1}, {-1, 1}, {1, -1}, {128, 129}, {257, 0}} {
			_, err := e.New(km[0], km[1])
			check(errors.Is(err, e.ErrInvalidConfig), "config %v", km)
		}
		return "48 random exact-m-loss patterns across six n=256 layouts; ordinary invalid boundaries rejected"
	})
	attack("C9,C11", "sizes-and-hostile-shards", func() string {
		c := codec(4, 2)
		for _, n := range []int{0, 1, 3, 4, 5, 7, 8, 9, 4095, 4096, 4097, 65535, 65536, 65537, 3<<20 | 17} {
			data := payload(n)
			s := c.Split(data)
			must(c.Encode(s))
			s[0], s[5] = nil, nil
			must(c.Reconstruct(s))
			out, err := c.Join(s, n)
			must(err)
			same(out, data)
		}
		for _, s := range [][][]byte{nil, make([][]byte, 5), {{1}, {1, 2}, {1}, {1}, {1}, {1}}, make([][]byte, 6)} {
			for _, f := range []func([][]byte) error{c.Encode, c.Verify, c.Reconstruct, c.ReconstructData} {
				check(f(clone(s)) != nil, "malformed shards accepted")
			}
		}
		return "boundary and 3-MiB payloads recover; four APIs reject hostile shapes without panic"
	})
}
func main() {
	if len(os.Args) < 2 {
		os.Exit(2)
	}
	if len(os.Args) > 2 {
		selected = os.Args[2]
	}
	switch os.Args[1] {
	case "algebra":
		algebra()
		extraAlgebra()
	case "storage":
		storageAttacks()
	case "placement":
		placement()
		extraPlacement()
	case "node":
		nodes()
	default:
		os.Exit(2)
	}
	if broken > 0 {
		os.Exit(1)
	}
}
