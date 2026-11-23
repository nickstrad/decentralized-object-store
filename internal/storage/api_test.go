package storage_test

import (
	"testing"

	"github.com/nickstrad/object-store/internal/storage"
)

func TestAPICodesAreInTheirBlockAndDistinct(t *testing.T) {
	seen := map[int]bool{}
	for _, e := range storage.APICodeEntries() {
		if e.Code < 160 || e.Code > 175 || seen[int(e.Code)] || e.Err == nil {
			t.Fatalf("api code %d for %v", e.Code, e.Err)
		}
		seen[int(e.Code)] = true
	}
}
