package buildcheck_test

import (
	"testing"

	"pgregory.net/rapid"
)

func TestPropRapidIsWired(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := rapid.Int32().Draw(t, "a")
		b := rapid.Int32().Draw(t, "b")
		if int64(a)+int64(b) != int64(b)+int64(a) {
			t.Fatalf("addition not commutative for %d, %d", a, b)
		}
	})
}
