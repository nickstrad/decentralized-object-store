package erasure

import "fmt"

const MaxShards = 256

type Matrix struct {
	rows, cols int
	data       []byte
}

func newMatrix(rows, cols int) Matrix {
	return Matrix{rows: rows, cols: cols, data: make([]byte, rows*cols)}
}

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

func Identity(n int) Matrix {
	m := newMatrix(n, n)
	for i := 0; i < n; i++ {
		m.data[i*n+i] = 1
	}
	return m
}

func (m Matrix) row(i int) []byte { return m.data[i*m.cols : (i+1)*m.cols] }

func (m Matrix) Dims() (int, int) { return m.rows, m.cols }

func (m Matrix) Rows() [][]byte {
	out := make([][]byte, m.rows)
	for i := range out {
		out[i] = append([]byte(nil), m.row(i)...)
	}
	return out
}

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

func (m Matrix) Mul(o Matrix) (Matrix, error) {
	if m.cols != o.rows {
		return Matrix{}, fmt.Errorf("erasure: multiply %dx%d by %dx%d: %w", m.rows, m.cols, o.rows, o.cols, ErrInvalidConfig)
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
	if k < 1 || m < 0 || k+m > MaxShards {
		return fmt.Errorf("erasure: k=%d m=%d: need k >= 1, m >= 0, k+m <= %d: %w", k, m, MaxShards, ErrInvalidConfig)
	}
	return nil
}

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
			row[j], _ = Inv(x ^ byte(j))
		}
	}
	return c, nil
}
