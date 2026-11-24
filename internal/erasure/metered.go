package erasure

import "github.com/nickstrad/object-store/internal/metrics"

// MeteredCodec is an owner-confined facade over an immutable Codec.
type MeteredCodec struct {
	*Codec
	state *codecMetrics
}
type codecMetrics struct {
	operations    [2][2]uint64
	input, output [2]uint64
	restored      uint64
}

func satAdd(dst *uint64, n uint64) {
	if ^uint64(0)-*dst < n {
		*dst = ^uint64(0)
	} else {
		*dst += n
	}
}
func NewMetered(k, m int, enabled bool) (*MeteredCodec, error) {
	c, err := New(k, m)
	if err != nil {
		return nil, err
	}
	out := &MeteredCodec{Codec: c}
	if enabled {
		out.state = &codecMetrics{}
	}
	return out, nil
}
func (c *MeteredCodec) Encode(shards [][]byte) error {
	return c.measure(0, shards, false, func() error { return c.Codec.Encode(shards) })
}
func (c *MeteredCodec) Reconstruct(shards [][]byte) error {
	return c.measure(1, shards, false, func() error { return c.Codec.Reconstruct(shards) })
}
func (c *MeteredCodec) ReconstructData(shards [][]byte) error {
	return c.measure(1, shards, true, func() error { return c.Codec.ReconstructData(shards) })
}
func (c *MeteredCodec) measure(op int, shards [][]byte, dataOnly bool, fn func() error) error {
	if c.state == nil {
		return fn()
	}
	var input, missing uint64
	for i, b := range shards {
		if op == 0 {
			if i < c.K() {
				satAdd(&input, uint64(len(b)))
			}
		} else {
			satAdd(&input, uint64(len(b)))
			if b == nil && (!dataOnly || i < c.K()) {
				missing++
			}
		}
	}
	err := fn()
	outcome := 0
	if err != nil {
		outcome = 1
	}
	satAdd(&c.state.operations[op][outcome], 1)
	if err == nil {
		satAdd(&c.state.input[op], input)
		var output uint64
		if op == 0 {
			for _, b := range shards {
				satAdd(&output, uint64(len(b)))
			}
		} else if missing > 0 {
			// All successfully reconstructed shards have equal length.
			for _, b := range shards {
				if b != nil {
					output = uint64(len(b)) * missing
					break
				}
			}
			satAdd(&c.state.restored, missing)
		}
		satAdd(&c.state.output[op], output)
	}
	return err
}
func (c *MeteredCodec) Snapshot() metrics.Snapshot {
	s := metrics.Snapshot{Component: "erasure"}
	if c.state == nil {
		return s
	}
	for i, op := range []string{"encode", "reconstruct"} {
		for j, outcome := range []string{"ok", "error"} {
			s.Points = append(s.Points, metrics.Point{Name: "objstore_erasure_operations_total", Labels: []metrics.Label{{Name: "operation", Value: op}, {Name: "outcome", Value: outcome}}, Counter: c.state.operations[i][j]})
		}
		for j, name := range []string{"input", "output"} {
			v := c.state.input[i]
			if j == 1 {
				v = c.state.output[i]
			}
			s.Points = append(s.Points, metrics.Point{Name: "objstore_erasure_" + name + "_bytes_total", Labels: []metrics.Label{{Name: "operation", Value: op}}, Counter: v})
		}
	}
	s.Points = append(s.Points, metrics.Point{Name: "objstore_erasure_reconstructed_shards_total", Counter: c.state.restored}, metrics.Point{Name: "objstore_erasure_operations_in_flight"})
	return s
}
