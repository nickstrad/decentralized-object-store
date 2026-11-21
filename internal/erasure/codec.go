// Package erasure implements systematic Reed-Solomon erasure coding over
// GF(2^8) using a Cauchy coding matrix.
//
// The package is pure computation with no I/O or external dependencies. It
// corrects up to m erasures at known positions; callers must detect corrupted
// shards externally (for example by checksum) and pass them as nil slots. A nil
// slot denotes a missing shard, whereas an empty non-nil slice []byte{} is a
// present zero-length shard. Valid shapes satisfy k >= 1, m >= 0, and
// k+m <= MaxShards (256).
package erasure

import "fmt"

// Codec encodes data into k data and m parity shards and reconstructs missing
// shards from any k present ones. A Codec is immutable after construction.
type Codec struct {
	k, m   int
	matrix Matrix
}

// New constructs a Reed-Solomon Codec for k data shards and m parity shards.
// It returns ErrInvalidConfig unless k >= 1, m >= 0, and k+m <= MaxShards.
func New(k, m int) (*Codec, error) {
	cm, err := CodingMatrix(k, m)
	if err != nil {
		return nil, err
	}
	return &Codec{k: k, m: m, matrix: cm}, nil
}

// K returns the number of data shards.
func (c *Codec) K() int { return c.k }

// M returns the number of parity shards.
func (c *Codec) M() int { return c.m }

// N returns the total number of shards (k + m).
func (c *Codec) N() int { return c.k + c.m }

// ShardSize returns the per-shard byte length required to hold dataLen bytes,
// or 0 when dataLen <= 0.
func (c *Codec) ShardSize(dataLen int) int {
	if dataLen <= 0 {
		return 0
	}
	// Subtract before dividing to avoid signed overflow near math.MaxInt.
	return (dataLen-1)/c.k + 1
}

// Split divides data into k equal-length data shards (zero-padded in the tail)
// followed by m zeroed parity shard buffers, all backed by one allocation.
// For empty data every slot is an empty non-nil slice.
func (c *Codec) Split(data []byte) [][]byte {
	size := c.ShardSize(len(data))
	buf := make([]byte, size*c.N())
	copy(buf, data)
	shards := make([][]byte, c.N())
	for i := range shards {
		// Full slice expressions prevent appends on one shard from overwriting the next.
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

// dataSize validates that all k data shards are present and equal in length.
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

// Encode computes the m parity shards in shards[k:k+m] from the k data shards
// in shards[:k]. Existing parity buffers of the exact shard length are reused
// in place; nil or wrong-sized parity slots are reallocated. It returns
// ErrShardCount, or a *ShardError wrapping ErrTooFewShards or ErrShardSize.
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

// Verify checks that all k+m shards are present, equal in length, and satisfy
// the coding equations. It returns ErrShardCount, or a *ShardError wrapping
// ErrTooFewShards, ErrShardSize, or ErrParityMismatch for the first bad shard.
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

// Reconstruct rebuilds all missing (nil) data and parity shards in place from
// any k present shards of equal length. On error shards is left unmodified. It
// returns ErrShardCount, ErrTooFewShards, or a *ShardError wrapping ErrShardSize.
func (c *Codec) Reconstruct(shards [][]byte) error {
	return c.reconstruct("reconstruct", shards, true)
}

// ReconstructData rebuilds all missing (nil) data shards in place from any k
// present shards of equal length, leaving missing parity shards nil. On error
// shards is left unmodified.
func (c *Codec) ReconstructData(shards [][]byte) error {
	return c.reconstruct("reconstruct data", shards, false)
}

func (c *Codec) reconstruct(op string, shards [][]byte, parity bool) error {
	if err := c.checkCount(op, shards); err != nil {
		return err
	}
	size := -1
	// Keep the first k present shard indices for decoding while validating all
	// present shard lengths so mismatches beyond the first k are still caught.
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

// rebuildData inverts the k-by-k submatrix for the present shard indices and
// multiplies it by those shards to restore every missing data shard.
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

// Join concatenates the k data shards and truncates the result to size bytes,
// stripping Split's tail padding. It returns ErrShardCount, ErrShardSize (if
// size is outside [0, shardSize*k] or data shards differ in length), or a
// *ShardError wrapping ErrTooFewShards if any data shard is nil.
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
