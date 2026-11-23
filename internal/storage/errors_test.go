package storage_test

import (
	"errors"
	"testing"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/storage"
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

func TestCodeTableJoinedUnavailablePrecedesShardCauses(t *testing.T) {
	table, err := storage.Codes()
	mustNil(t, err)
	cases := []struct {
		name     string
		err      error
		wantCode env.Code
		wantErr  error
	}{
		{"unavailable with missing shard encodes as unavailable", errors.Join(storage.ErrUnavailable, storage.ErrNotFound), storage.CodeUnavailable, storage.ErrUnavailable},
		{"unavailable with corrupt shard encodes as unavailable", errors.Join(storage.ErrUnavailable, storage.ErrCorrupt), storage.CodeUnavailable, storage.ErrUnavailable},
		{"unavailable with env not found encodes as unavailable", errors.Join(storage.ErrUnavailable, env.ErrNotFound), storage.CodeUnavailable, storage.ErrUnavailable},
		{"unavailable with env timeout encodes as unavailable", errors.Join(storage.ErrUnavailable, env.ErrTimeout), storage.CodeUnavailable, storage.ErrUnavailable},
		{"unavailable with env unreachable encodes as unavailable", errors.Join(storage.ErrUnavailable, env.ErrUnreachable), storage.CodeUnavailable, storage.ErrUnavailable},
		{"unavailable with busy shard encodes as unavailable", errors.Join(storage.ErrUnavailable, storage.ErrBusy), storage.CodeUnavailable, storage.ErrUnavailable},
		{"unavailable with exists shard encodes as unavailable", errors.Join(storage.ErrUnavailable, storage.ErrExists), storage.CodeUnavailable, storage.ErrUnavailable},
		{"unavailable with invalid shard encodes as unavailable", errors.Join(storage.ErrUnavailable, storage.ErrInvalid), storage.CodeUnavailable, storage.ErrUnavailable},
		{"unavailable with all shard causes encodes as unavailable", errors.Join(storage.ErrUnavailable, storage.ErrNotFound, storage.ErrCorrupt, env.ErrNotFound, env.ErrTimeout, env.ErrUnreachable, storage.ErrBusy, storage.ErrExists, storage.ErrInvalid), storage.CodeUnavailable, storage.ErrUnavailable},
		{"standalone missing shard encodes as not found", storage.ErrNotFound, storage.CodeNotFound, storage.ErrNotFound},
		{"standalone corrupt shard encodes as corrupt", storage.ErrCorrupt, storage.CodeCorrupt, storage.ErrCorrupt},
		{"standalone exists shard encodes as exists", storage.ErrExists, storage.CodeExists, storage.ErrExists},
		{"standalone invalid shard encodes as invalid", storage.ErrInvalid, storage.CodeInvalid, storage.ErrInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, msg := table.Encode(tc.err)
			if code != tc.wantCode {
				t.Fatalf("Encode(%v) = code %d, want %d", tc.err, code, tc.wantCode)
			}
			back := table.Decode(code, msg)
			if !errors.Is(back, tc.wantErr) {
				t.Fatalf("Decode(%d) = %v, want %v", code, back, tc.wantErr)
			}
		})
	}
}
