package erasure_test

import (
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/erasure"
	"pgregory.net/rapid"
)

func bitwiseMul(a, b byte) byte {
	x, y := uint16(a), uint16(b)
	var p uint16
	for y != 0 {
		if y&1 != 0 {
			p ^= x
		}
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11D
		}
		y >>= 1
	}
	return byte(p)
}

func TestGFGoldenProducts(t *testing.T) {
	cases := []struct {
		name    string
		a, b, p byte
	}{
		{"zero times zero is zero", 0x00, 0x00, 0x00},
		{"one is the identity", 0x01, 0x01, 0x01},
		{"two times two is four", 0x02, 0x02, 0x04},
		{"x times x^7 reduces by the polynomial", 0x02, 0x80, 0x1d},
		{"x^7 squared", 0x80, 0x80, 0x13},
		{"three times seven", 0x03, 0x07, 0x09},
		{"0x53 times 0xca under 0x11d", 0x53, 0xca, 0x8f},
		{"0xff squared", 0xff, 0xff, 0xe2},
		{"0x1d times two", 0x1d, 0x02, 0x3a},
		{"0x8e times 0x47", 0x8e, 0x47, 0xad},
		{"0x12 times 0x34", 0x12, 0x34, 0x0f},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := erasure.Mul(c.a, c.b); got != c.p {
				t.Fatalf("Mul(%#x, %#x) = %#x, want %#x", c.a, c.b, got, c.p)
			}
			if got := erasure.Mul(c.b, c.a); got != c.p {
				t.Fatalf("Mul(%#x, %#x) = %#x, want %#x", c.b, c.a, got, c.p)
			}
		})
	}
}

func TestGFGoldenInverses(t *testing.T) {
	cases := []struct{ a, inv byte }{{0x01, 0x01}, {0x02, 0x8e}, {0x03, 0xf4}, {0x8e, 0x02}, {0xff, 0xfd}, {0x53, 0x8c}}
	for _, c := range cases {
		got, ok := erasure.Inv(c.a)
		if !ok || got != c.inv {
			t.Fatalf("Inv(%#x) = %#x, %v; want %#x, true", c.a, got, ok, c.inv)
		}
	}
	if _, ok := erasure.Inv(0); ok {
		t.Fatal("Inv(0) reported an inverse")
	}
}

func TestGFGoldenExp(t *testing.T) {
	want := []byte{0x01, 0x02, 0x04, 0x08, 0x10, 0x20, 0x40, 0x80, 0x1d, 0x3a}
	for i, w := range want {
		if got := erasure.Exp(i); got != w {
			t.Fatalf("Exp(%d) = %#x, want %#x", i, got, w)
		}
	}
	if erasure.Exp(255) != 1 || erasure.Exp(-1) != erasure.Exp(254) || erasure.Exp(510) != 1 {
		t.Fatal("Exp is not periodic with period 255")
	}
}

func TestGFLogExpAgree(t *testing.T) {
	seen := map[byte]bool{}
	for i := 0; i < 255; i++ {
		e := erasure.Exp(i)
		if seen[e] {
			t.Fatalf("Exp(%d) = %#x repeats: 2 is not a generator", i, e)
		}
		seen[e] = true
		l, ok := erasure.Log(e)
		if !ok || l != i {
			t.Fatalf("Log(Exp(%d)) = %d, %v", i, l, ok)
		}
	}
	if _, ok := erasure.Log(0); ok {
		t.Fatal("Log(0) reported a logarithm")
	}
}

func TestGFTablesMatchBitwiseExhaustive(t *testing.T) {
	for a := 0; a < 256; a++ {
		for b := 0; b < 256; b++ {
			if got, want := erasure.Mul(byte(a), byte(b)), bitwiseMul(byte(a), byte(b)); got != want {
				t.Fatalf("Mul(%#x, %#x) = %#x, bitwise %#x", a, b, got, want)
			}
		}
	}
}

func TestGFDiv(t *testing.T) {
	for a := 0; a < 256; a++ {
		for b := 1; b < 256; b++ {
			q, ok := erasure.Div(byte(a), byte(b))
			if !ok || erasure.Mul(q, byte(b)) != byte(a) {
				t.Fatalf("Div(%#x, %#x) = %#x, %v", a, b, q, ok)
			}
		}
		if _, ok := erasure.Div(byte(a), 0); ok {
			t.Fatalf("Div(%#x, 0) succeeded", a)
		}
	}
}

func TestGFMulAddSlice(t *testing.T) {
	src := []byte{0, 1, 2, 0x80, 0xff, 0x53}
	for _, c := range []byte{0, 1, 2, 0x8e, 0xff} {
		dst := []byte{9, 8, 7, 6, 5, 4}
		want := make([]byte, len(dst))
		for i := range dst {
			want[i] = dst[i] ^ bitwiseMul(c, src[i])
		}
		erasure.MulAddSlice(c, src, dst)
		for i := range dst {
			if dst[i] != want[i] {
				t.Fatalf("c=%#x: dst[%d] = %#x, want %#x", c, i, dst[i], want[i])
			}
		}
	}
	short := []byte{1, 1}
	erasure.MulAddSlice(2, src, short)
	if short[0] != 1 || short[1] != 3 {
		t.Fatalf("MulAddSlice over the shorter length: %v", short)
	}
}

func checkFieldLaws(a, b, c byte) string {
	mul, add := erasure.Mul, erasure.Add
	switch {
	case add(a, b) != add(b, a):
		return "addition is not commutative"
	case mul(a, b) != mul(b, a):
		return "multiplication is not commutative"
	case add(add(a, b), c) != add(a, add(b, c)):
		return "addition is not associative"
	case mul(mul(a, b), c) != mul(a, mul(b, c)):
		return "multiplication is not associative"
	case mul(a, add(b, c)) != add(mul(a, b), mul(a, c)):
		return "multiplication does not distribute over addition"
	case add(a, 0) != a:
		return "zero is not the additive identity"
	case mul(a, 1) != a:
		return "one is not the multiplicative identity"
	case add(a, a) != 0:
		return "a is not its own additive inverse"
	case mul(a, 0) != 0:
		return "zero does not annihilate"
	}
	if a != 0 {
		inv, ok := erasure.Inv(a)
		if !ok {
			return "a non-zero element has no inverse"
		}
		if mul(a, inv) != 1 {
			return "a times its inverse is not one"
		}
		if q, ok := erasure.Div(b, a); !ok || mul(q, a) != b {
			return "division does not invert multiplication"
		}
	}
	if mul(a, b) != bitwiseMul(a, b) {
		return "table product differs from bitwise product"
	}
	return ""
}

func propFieldAxioms(t *rapid.T) {
	a := rapid.Byte().Draw(t, "a")
	b := rapid.Byte().Draw(t, "b")
	c := rapid.Byte().Draw(t, "c")
	if msg := checkFieldLaws(a, b, c); msg != "" {
		t.Fatalf("a=%#x b=%#x c=%#x: %s", a, b, c, msg)
	}
}

func TestPropFieldAxioms(t *testing.T) {
	rapid.Check(t, propFieldAxioms)
}

func TestFieldAxiomsExhaustivePairs(t *testing.T) {
	for a := 0; a < 256; a++ {
		for b := 0; b < 256; b++ {
			if msg := checkFieldLaws(byte(a), byte(b), byte(a^b^0x5a)); msg != "" {
				t.Fatalf("a=%#x b=%#x: %s", a, b, msg)
			}
		}
	}
}

func FuzzGF256(f *testing.F) {
	for _, seed := range gf256FuzzSeeds() {
		f.Add(seed)
	}
	f.Fuzz(rapid.MakeFuzz(propFieldAxioms))
}
