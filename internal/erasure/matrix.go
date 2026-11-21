package erasure

import (
	"fmt"
	"math"
)

// MaxShards is the maximum total number of shards (k + m) supported in GF(2^8).
// The Cauchy construction requires k + m distinct field elements.
const MaxShards = 256

// Matrix is an immutable-by-API row-major matrix of GF(2^8) elements.
type Matrix struct {
	rows, cols int
	data       []byte
}

func newMatrix(rows, cols int) Matrix {
	return Matrix{rows: rows, cols: cols, data: make([]byte, rows*cols)}
}

// NewMatrix copies rows into a new Matrix. It returns ErrInvalidConfig if rows
// is empty, has zero columns, or is ragged.
func NewMatrix(rows [][]byte) (Matrix, error) {
	if len(rows) == 0 || len(rows[0]) == 0 {
		return Matrix{}, fmt.Errorf("erasure: new matrix: empty: %w", ErrInvalidConfig)
	}
	m := newMatrix(len(rows), len(rows[0]))
	for i, r := range rows {
		if len(r) != m.cols {
			return Matrix{}, fmt.Errorf("erasure: new matrix: row %d has %d columns, want %d: %w", i, len(r), m.cols, ErrInvalidConfig)
		}
		copy(m.row(i), r)
	}
	return m, nil
}

// Identity returns the n-by-n identity matrix over GF(2^8). For n <= 0 or when
// n*n would overflow int, it returns the empty 0x0 matrix.
func Identity(n int) Matrix {
	if n <= 0 || n > math.MaxInt/n {
		return Matrix{}
	}
	m := newMatrix(n, n)
	for i := 0; i < n; i++ {
		m.data[i*n+i] = 1
	}
	return m
}

func (m Matrix) row(i int) []byte { return m.data[i*m.cols : (i+1)*m.cols] }

// Dims returns the number of rows and columns in m.
func (m Matrix) Dims() (int, int) { return m.rows, m.cols }

// Rows returns a freshly allocated copy of m's rows.
func (m Matrix) Rows() [][]byte {
	out := make([][]byte, m.rows)
	for i := range out {
		out[i] = append([]byte(nil), m.row(i)...)
	}
	return out
}

// Equal reports whether m and o have identical dimensions and entries.
func (m Matrix) Equal(o Matrix) bool {
	if m.rows != o.rows || m.cols != o.cols {
		return false
	}
	for i := range m.data {
		if m.data[i] != o.data[i] {
			return false
		}
	}
	return true
}

// Mul returns the GF(2^8) matrix product m * o. It returns ErrInvalidConfig if
// m.cols != o.rows or if the product size would overflow int.
func (m Matrix) Mul(o Matrix) (Matrix, error) {
	if m.cols != o.rows {
		return Matrix{}, fmt.Errorf("erasure: multiply %dx%d by %dx%d: %w", m.rows, m.cols, o.rows, o.cols, ErrInvalidConfig)
	}
	if m.rows > 0 && o.cols > math.MaxInt/m.rows {
		return Matrix{}, fmt.Errorf("erasure: multiply %dx%d by %dx%d: product too large: %w", m.rows, m.cols, o.rows, o.cols, ErrInvalidConfig)
	}
	p := newMatrix(m.rows, o.cols)
	for i := 0; i < m.rows; i++ {
		dst := p.row(i)
		for j, c := range m.row(i) {
			MulAddSlice(c, o.row(j), dst)
		}
	}
	return p, nil
}

// SelectRows returns a new Matrix containing the specified row indices of m in
// order. It returns ErrInvalidConfig if rows is empty or any index is out of range.
func (m Matrix) SelectRows(rows []int) (Matrix, error) {
	if len(rows) == 0 {
		return Matrix{}, fmt.Errorf("erasure: select rows: none: %w", ErrInvalidConfig)
	}
	s := newMatrix(len(rows), m.cols)
	for i, r := range rows {
		if r < 0 || r >= m.rows {
			return Matrix{}, fmt.Errorf("erasure: select rows: row %d of %d: %w", r, m.rows, ErrInvalidConfig)
		}
		copy(s.row(i), m.row(r))
	}
	return s, nil
}

// Invert returns the GF(2^8) inverse of square matrix m via Gauss-Jordan
// elimination without modifying m. It returns ErrInvalidConfig if m is not
// square, or ErrSingular if any column lacks a non-zero pivot.
func (m Matrix) Invert() (Matrix, error) {
	if m.rows != m.cols {
		return Matrix{}, fmt.Errorf("erasure: invert %dx%d: not square: %w", m.rows, m.cols, ErrInvalidConfig)
	}
	n := m.rows
	work := newMatrix(n, n)
	copy(work.data, m.data)
	inv := Identity(n)
	for col := 0; col < n; col++ {
		pivot := col
		for pivot < n && work.data[pivot*n+col] == 0 {
			pivot++
		}
		if pivot == n {
			return Matrix{}, fmt.Errorf("erasure: invert %dx%d: column %d: %w", n, n, col, ErrSingular)
		}
		if pivot != col {
			swapRows(work, pivot, col)
			swapRows(inv, pivot, col)
		}
		// The pivot check above guarantees a non-zero element, so Inv succeeds.
		scale, _ := Inv(work.data[col*n+col])
		mulSlice(scale, work.row(col), work.row(col))
		mulSlice(scale, inv.row(col), inv.row(col))
		for r := 0; r < n; r++ {
			if r == col {
				continue
			}
			f := work.data[r*n+col]
			if f == 0 {
				continue
			}
			MulAddSlice(f, work.row(col), work.row(r))
			MulAddSlice(f, inv.row(col), inv.row(r))
		}
	}
	return inv, nil
}

func swapRows(m Matrix, a, b int) {
	ra, rb := m.row(a), m.row(b)
	for i := range ra {
		ra[i], rb[i] = rb[i], ra[i]
	}
}

func validConfig(k, m int) error {
	// Check bounds before adding to prevent signed overflow on hostile inputs.
	if k < 1 || m < 0 || k > MaxShards || m > MaxShards-k {
		return fmt.Errorf("erasure: k=%d m=%d: need k >= 1, m >= 0, k+m <= %d: %w", k, m, MaxShards, ErrInvalidConfig)
	}
	return nil
}

// CodingMatrix builds the (k+m)-by-k systematic Cauchy coding matrix [I; C],
// where the top k rows form the identity and parity entry C[i][j] = 1/(x_i + y_j)
// with x_i = k+i and y_j = j. Because all x_i and y_j are distinct in GF(2^8)
// for k+m <= MaxShards, every k-by-k submatrix is invertible (MDS). It returns
// ErrInvalidConfig for invalid (k, m).
func CodingMatrix(k, m int) (Matrix, error) {
	if err := validConfig(k, m); err != nil {
		return Matrix{}, err
	}
	c := newMatrix(k+m, k)
	for i := 0; i < k; i++ {
		c.data[i*k+i] = 1
	}
	for i := 0; i < m; i++ {
		x := byte(k + i)
		row := c.row(k + i)
		for j := 0; j < k; j++ {
			// x >= k > j, so x^byte(j) is always non-zero and Inv succeeds.
			row[j], _ = Inv(x ^ byte(j))
		}
	}
	return c, nil
}
