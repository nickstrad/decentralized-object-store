package occ

import (
	"bytes"
	"slices"
)

type Scalar struct {
	Value []byte
	Stamp Stamp
}

func (s Scalar) Compare(t Scalar) int {
	if c := s.Stamp.Compare(t.Stamp); c != 0 {
		return c
	}
	return bytes.Compare(s.Value, t.Value)
}

func (s Scalar) Merge(t Scalar) Scalar {
	if t.Compare(s) > 0 {
		return Scalar{Value: slices.Clone(t.Value), Stamp: t.Stamp}
	}
	return Scalar{Value: slices.Clone(s.Value), Stamp: s.Stamp}
}
