package storage_test

import (
	"errors"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

// TestCodeTableRoundTrip proves every storage sentinel encodes to an
// application wire code and decodes back matching both itself and env.ErrRemote.
func TestCodeTableRoundTrip(t *testing.T) {
	table, err := storage.Codes()
	mustNil(t, err)
	sentinels := []error{storage.ErrNotFound, storage.ErrCorrupt, storage.ErrExists, storage.ErrUnavailable, storage.ErrInvalid}
	entries := storage.CodeEntries()
	if len(entries) != len(sentinels) {
		t.Fatalf("%d code entries for %d sentinels", len(entries), len(sentinels))
	}
	for _, s := range sentinels {
		wrapped := &storage.ObjectError{Op: "get", Bucket: "b", Key: "k", Err: s}
		code, msg := table.Encode(wrapped)
		if code < env.CodeAppMin {
			t.Fatalf("%v encodes to %d, below CodeAppMin", s, code)
		}
		back := table.Decode(code, msg)
		if !errors.Is(back, s) || !errors.Is(back, env.ErrRemote) {
			t.Fatalf("%v did not survive the wire: %v", s, back)
		}
	}
	code, _ := table.Encode(errors.Join(storage.ErrNotFound, env.ErrNotFound))
	if code != storage.CodeNotFound {
		t.Fatalf("a storage not-found that also wraps env.ErrNotFound encodes to %d", code)
	}
}

// TestObjectErrorMessage proves ObjectError formats its op, bucket, and key
// and unwraps to its underlying sentinel.
func TestObjectErrorMessage(t *testing.T) {
	err := &storage.ObjectError{Op: "put", Bucket: "b", Key: "dir/k", Err: storage.ErrUnavailable}
	if got, want := err.Error(), "storage: put b/dir/k: "+storage.ErrUnavailable.Error(); got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, storage.ErrUnavailable) {
		t.Fatal("ObjectError does not unwrap")
	}
}
