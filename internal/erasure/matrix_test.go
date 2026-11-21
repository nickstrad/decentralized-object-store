package erasure_test

import (
	"errors"
	"math"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/erasure"
	"pgregory.net/rapid"
)

func mustMatrix(t testing.TB, rows [][]byte) erasure.Matrix {
	t.Helper()
	m, err := erasure.NewMatrix(rows)
	if err != nil {
		t.Fatalf("NewMatrix: %v", err)
	}
	return m
}

func TestMatrixInvert(t *testing.T) {
	cases := []struct {
		name string
		rows [][]byte
		want [][]byte
		err  error
	}{
		{"the 1x1 identity is its own inverse", [][]byte{{1}}, [][]byte{{1}}, nil},
		{"a 1x1 scalar inverts to its field inverse", [][]byte{{2}}, [][]byte{{0x8e}}, nil},
		{"a 2x2 Cauchy block inverts", [][]byte{{0x8e, 0xf4}, {0xf4, 0x8e}}, nil, nil},
		{"a matrix that needs a row swap inverts", [][]byte{{0, 1}, {1, 0}}, [][]byte{{0, 1}, {1, 0}}, nil},
		{"a zero matrix is singular", [][]byte{{0, 0}, {0, 0}}, nil, erasure.ErrSingular},
		{"two equal rows are singular", [][]byte{{3, 7, 1}, {3, 7, 1}, {0, 1, 0}}, nil, erasure.ErrSingular},
		{"a dependent row is singular", [][]byte{{1, 2}, {2, 4}}, nil, erasure.ErrSingular},
		{"a non-square matrix cannot be inverted", [][]byte{{1, 2, 3}, {4, 5, 6}}, nil, erasure.ErrInvalidConfig},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := mustMatrix(t, c.rows)
			inv, err := m.Invert()
			if c.err != nil {
				if !errors.Is(err, c.err) {
					t.Fatalf("Invert error %v, want %v", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Invert: %v", err)
			}
			if c.want != nil && !inv.Equal(mustMatrix(t, c.want)) {
				t.Fatalf("Invert = %v, want %v", inv.Rows(), c.want)
			}
			p, err := m.Mul(inv)
			if err != nil {
				t.Fatal(err)
			}
			if !p.Equal(erasure.Identity(len(c.rows))) {
				t.Fatalf("m * inv = %v, not the identity", p.Rows())
			}
			q, err := inv.Mul(m)
			if err != nil {
				t.Fatal(err)
			}
			if !q.Equal(erasure.Identity(len(c.rows))) {
				t.Fatalf("inv * m = %v, not the identity", q.Rows())
			}
		})
	}
}

func TestMatrixInvertLeavesInputUnchanged(t *testing.T) {
	rows := [][]byte{{0, 1}, {1, 1}}
	m := mustMatrix(t, rows)
	if _, err := m.Invert(); err != nil {
		t.Fatal(err)
	}
	if !m.Equal(mustMatrix(t, [][]byte{{0, 1}, {1, 1}})) {
		t.Fatalf("Invert changed its receiver: %v", m.Rows())
	}
	rows[0][0] = 9
	if m.Rows()[0][0] != 0 {
		t.Fatal("NewMatrix did not copy its rows")
	}
	got := m.Rows()
	got[0][0] = 7
	if m.Rows()[0][0] != 0 {
		t.Fatal("Rows did not return a copy")
	}
}

func TestNewMatrixRejectsBadShapes(t *testing.T) {
	cases := []struct {
		name string
		rows [][]byte
	}{
		{"no rows", nil},
		{"an empty row", [][]byte{{}}},
		{"ragged rows", [][]byte{{1, 2}, {3}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := erasure.NewMatrix(c.rows); !errors.Is(err, erasure.ErrInvalidConfig) {
				t.Fatalf("NewMatrix error %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestMatrixMulShapes(t *testing.T) {
	a := mustMatrix(t, [][]byte{{1, 2, 3}})
	b := mustMatrix(t, [][]byte{{1}, {1}, {1}})
	p, err := a.Mul(b)
	if err != nil {
		t.Fatal(err)
	}
	if want := mustMatrix(t, [][]byte{{1 ^ 2 ^ 3}}); !p.Equal(want) {
		t.Fatalf("product %v", p.Rows())
	}
	if _, err := a.Mul(a); !errors.Is(err, erasure.ErrInvalidConfig) {
		t.Fatalf("mismatched Mul error %v", err)
	}
	if a.Equal(b) || a.Equal(mustMatrix(t, [][]byte{{1, 2, 4}})) {
		t.Fatal("Equal reports different matrices equal")
	}
	if r, c := a.Dims(); r != 1 || c != 3 {
		t.Fatalf("Dims = %d, %d", r, c)
	}
}

func TestMatrixSelectRows(t *testing.T) {
	m := mustMatrix(t, [][]byte{{1, 2}, {3, 4}, {5, 6}})
	s, err := m.SelectRows([]int{2, 0})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Equal(mustMatrix(t, [][]byte{{5, 6}, {1, 2}})) {
		t.Fatalf("SelectRows = %v", s.Rows())
	}
	for _, bad := range [][]int{nil, {3}, {-1}} {
		if _, err := m.SelectRows(bad); !errors.Is(err, erasure.ErrInvalidConfig) {
			t.Fatalf("SelectRows(%v) error %v", bad, err)
		}
	}
}

func TestCodingMatrixIsSystematicCauchy(t *testing.T) {
	m, err := erasure.CodingMatrix(4, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{
		{1, 0, 0, 0}, {0, 1, 0, 0}, {0, 0, 1, 0}, {0, 0, 0, 1},
		{0x47, 0xa7, 0x7a, 0xba}, {0xa7, 0x47, 0xba, 0x7a},
	}
	if !m.Equal(mustMatrix(t, want)) {
		t.Fatalf("CodingMatrix(4, 2) = %v", m.Rows())
	}
	for _, bad := range [][2]int{{0, 1}, {1, -1}, {200, 57}, {-3, 3}} {
		if _, err := erasure.CodingMatrix(bad[0], bad[1]); !errors.Is(err, erasure.ErrInvalidConfig) {
			t.Fatalf("CodingMatrix(%d, %d) error %v", bad[0], bad[1], err)
		}
	}
}

func combinations(n, k int, fn func([]int) bool) {
	idx := make([]int, k)
	for i := range idx {
		idx[i] = i
	}
	for {
		if !fn(idx) {
			return
		}
		i := k - 1
		for i >= 0 && idx[i] == n-k+i {
			i--
		}
		if i < 0 {
			return
		}
		idx[i]++
		for j := i + 1; j < k; j++ {
			idx[j] = idx[j-1] + 1
		}
	}
}

func checkSubmatrixInvertible(t rapid.TB, m erasure.Matrix, rows []int) {
	t.Helper()
	sub, err := m.SelectRows(rows)
	if err != nil {
		t.Fatal(err)
	}
	inv, err := sub.Invert()
	if err != nil {
		t.Fatalf("rows %v: %v", rows, err)
	}
	p, err := sub.Mul(inv)
	if err != nil || !p.Equal(erasure.Identity(len(rows))) {
		t.Fatalf("rows %v: inverse is wrong (%v)", rows, err)
	}
}

func TestMDSExhaustiveSmall(t *testing.T) {
	for k := 1; k <= 6; k++ {
		for m := 0; m <= 6; m++ {
			cm, err := erasure.CodingMatrix(k, m)
			if err != nil {
				t.Fatal(err)
			}
			combinations(k+m, k, func(rows []int) bool {
				checkSubmatrixInvertible(t, cm, rows)
				return !t.Failed()
			})
		}
	}
}

func TestPropMDS(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		k := rapid.IntRange(1, 64).Draw(t, "k")
		m := rapid.IntRange(0, min(64, erasure.MaxShards-k)).Draw(t, "m")
		cm, err := erasure.CodingMatrix(k, m)
		if err != nil {
			t.Fatal(err)
		}
		rows := rapid.SliceOfNDistinct(rapid.IntRange(0, k+m-1), k, k, rapid.ID[int]).Draw(t, "rows")
		checkSubmatrixInvertible(t, cm, rows)
	})
}

func TestMDSAtTheFieldLimit(t *testing.T) {
	cm, err := erasure.CodingMatrix(128, 128)
	if err != nil {
		t.Fatal(err)
	}
	parity := make([]int, 128)
	for i := range parity {
		parity[i] = 128 + i
	}
	checkSubmatrixInvertible(t, cm, parity)
	mixed := make([]int, 0, 128)
	for i := 0; i < 256 && len(mixed) < 128; i += 2 {
		mixed = append(mixed, i)
	}
	checkSubmatrixInvertible(t, cm, mixed)
}

func TestIdentityOfNonPositiveOrHugeSizeIsEmpty(t *testing.T) {
	for _, n := range []int{0, -1, -2, math.MinInt, math.MaxInt, 1 << 32} {
		id := erasure.Identity(n)
		if r, c := id.Dims(); r != 0 || c != 0 {
			t.Fatalf("Identity(%d) has dims %dx%d, want 0x0", n, r, c)
		}
		if rows := id.Rows(); len(rows) != 0 {
			t.Fatalf("Identity(%d).Rows() has %d rows", n, len(rows))
		}
		inv, err := id.Invert()
		if err != nil {
			t.Fatalf("Identity(%d).Invert(): %v", n, err)
		}
		if !inv.Equal(id) {
			t.Fatalf("Identity(%d).Invert() is not empty", n)
		}
	}
	if r, c := erasure.Identity(3).Dims(); r != 3 || c != 3 {
		t.Fatalf("Identity(3) has dims %dx%d", r, c)
	}
}
