package erasure

import "fmt"

type Codec struct {
	k, m   int
	matrix Matrix
}

func New(k, m int) (*Codec, error) {
	cm, err := CodingMatrix(k, m)
	if err != nil {
		return nil, err
	}
	return &Codec{k: k, m: m, matrix: cm}, nil
}

func (c *Codec) K() int { return c.k }

func (c *Codec) M() int { return c.m }

func (c *Codec) N() int { return c.k + c.m }

func (c *Codec) ShardSize(dataLen int) int {
	if dataLen <= 0 {
		return 0
	}
	return (dataLen + c.k - 1) / c.k
}

func (c *Codec) Split(data []byte) [][]byte {
	size := c.ShardSize(len(data))
	buf := make([]byte, size*c.N())
	copy(buf, data)
	shards := make([][]byte, c.N())
	for i := range shards {
		shards[i] = buf[i*size : (i+1)*size : (i+1)*size]
	}
	return shards
}

func (c *Codec) checkCount(op string, shards [][]byte) error {
	if len(shards) != c.N() {
		return fmt.Errorf("erasure: %s: %d shards, want %d: %w", op, len(shards), c.N(), ErrShardCount)
	}
	return nil
}

func (c *Codec) dataSize(op string, shards [][]byte) (int, error) {
	size := len(shards[0])
	for i := 0; i < c.k; i++ {
		if shards[i] == nil {
			return 0, shardErr(op, i, ErrTooFewShards)
		}
		if len(shards[i]) != size {
			return 0, shardErr(op, i, ErrShardSize)
		}
	}
	return size, nil
}

func (c *Codec) Encode(shards [][]byte) error {
	if err := c.checkCount("encode", shards); err != nil {
		return err
	}
	size, err := c.dataSize("encode", shards)
	if err != nil {
		return err
	}
	for i := c.k; i < c.N(); i++ {
		if len(shards[i]) != size || shards[i] == nil {
			shards[i] = make([]byte, size)
		}
		c.encodeRow(i, shards[:c.k], shards[i])
	}
	return nil
}

func (c *Codec) encodeRow(row int, data [][]byte, dst []byte) {
	clear(dst)
	for j, coef := range c.matrix.row(row) {
		MulAddSlice(coef, data[j], dst)
	}
}

func (c *Codec) Verify(shards [][]byte) error {
	if err := c.checkCount("verify", shards); err != nil {
		return err
	}
	size, err := c.dataSize("verify", shards)
	if err != nil {
		return err
	}
	buf := make([]byte, size)
	for i := c.k; i < c.N(); i++ {
		if shards[i] == nil {
			return shardErr("verify", i, ErrTooFewShards)
		}
		if len(shards[i]) != size {
			return shardErr("verify", i, ErrShardSize)
		}
		c.encodeRow(i, shards[:c.k], buf)
		for j := range buf {
			if buf[j] != shards[i][j] {
				return shardErr("verify", i, ErrParityMismatch)
			}
		}
	}
	return nil
}

func (c *Codec) Reconstruct(shards [][]byte) error {
	return c.reconstruct("reconstruct", shards, true)
}

func (c *Codec) ReconstructData(shards [][]byte) error {
	return c.reconstruct("reconstruct data", shards, false)
}

func (c *Codec) reconstruct(op string, shards [][]byte, parity bool) error {
	if err := c.checkCount(op, shards); err != nil {
		return err
	}
	size := -1
	present := make([]int, 0, c.k)
	for i, s := range shards {
		if s == nil {
			continue
		}
		if size < 0 {
			size = len(s)
		} else if len(s) != size {
			return shardErr(op, i, ErrShardSize)
		}
		if len(present) < c.k {
			present = append(present, i)
		}
	}
	if len(present) < c.k {
		return fmt.Errorf("erasure: %s: %d of %d shards present, need %d: %w", op, countPresent(shards), c.N(), c.k, ErrTooFewShards)
	}
	missingData := false
	for i := 0; i < c.k; i++ {
		if shards[i] == nil {
			missingData = true
			break
		}
	}
	if missingData {
		if err := c.rebuildData(op, shards, present, size); err != nil {
			return err
		}
	}
	if !parity {
		return nil
	}
	for i := c.k; i < c.N(); i++ {
		if shards[i] == nil {
			shards[i] = make([]byte, size)
			c.encodeRow(i, shards[:c.k], shards[i])
		}
	}
	return nil
}

func (c *Codec) rebuildData(op string, shards [][]byte, present []int, size int) error {
	sub, err := c.matrix.SelectRows(present)
	if err != nil {
		return fmt.Errorf("erasure: %s: %w", op, err)
	}
	dec, err := sub.Invert()
	if err != nil {
		return fmt.Errorf("erasure: %s: rows %v: %w", op, present, err)
	}
	inputs := make([][]byte, c.k)
	for i, r := range present {
		inputs[i] = shards[r]
	}
	for i := 0; i < c.k; i++ {
		if shards[i] != nil {
			continue
		}
		out := make([]byte, size)
		for j, coef := range dec.row(i) {
			MulAddSlice(coef, inputs[j], out)
		}
		shards[i] = out
	}
	return nil
}

func countPresent(shards [][]byte) int {
	n := 0
	for _, s := range shards {
		if s != nil {
			n++
		}
	}
	return n
}

func (c *Codec) Join(shards [][]byte, size int) ([]byte, error) {
	if err := c.checkCount("join", shards); err != nil {
		return nil, err
	}
	shardSize, err := c.dataSize("join", shards)
	if err != nil {
		return nil, err
	}
	if size < 0 || size > shardSize*c.k {
		return nil, fmt.Errorf("erasure: join: size %d outside [0, %d]: %w", size, shardSize*c.k, ErrShardSize)
	}
	out := make([]byte, 0, size)
	for i := 0; i < c.k && len(out) < size; i++ {
		out = append(out, shards[i][:min(shardSize, size-len(out))]...)
	}
	return out, nil
}

func shardErr(op string, index int, err error) error {
	var e error = &ShardError{Index: index, Err: err}
	return fmt.Errorf("erasure: %s: %w", op, e)
}
