package node_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

// TestJoinedCodeTableHasNoCollision proves AllCodeEntries registers distinct
// codes across all subsystems and NewCodeTable rejects duplicates.
func TestJoinedCodeTableHasNoCollision(t *testing.T) {
	seen := map[env.Code]error{}
	for _, e := range node.AllCodeEntries() {
		if prev, ok := seen[e.Code]; ok {
			t.Fatalf("code %d registered for %v and %v", e.Code, prev, e.Err)
		}
		seen[e.Code] = e.Err
	}
	if _, err := node.CodeTable(); err != nil {
		t.Fatal(err)
	}
	if _, err := env.NewCodeTable(append(node.AllCodeEntries(), node.NodeCodeEntries()[0])...); !errors.Is(err, env.ErrInvalidArgument) {
		t.Fatalf("a duplicate code was accepted: %v", err)
	}
}

// TestEveryCodeRoundTripsToItsSentinel proves every registered code encodes
// and decodes back to its sentinel plus env.ErrRemote, and unknown codes map
// to env.ErrRemote.
func TestEveryCodeRoundTripsToItsSentinel(t *testing.T) {
	table, err := node.CodeTable()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range table.Entries() {
		t.Run(e.Err.Error(), func(t *testing.T) {
			wrapped := fmt.Errorf("op on object: %w", e.Err)
			code, msg := table.Encode(wrapped)
			if code != e.Code {
				t.Fatalf("encoded as %d, want %d", code, e.Code)
			}
			back := table.Decode(code, msg)
			if !errors.Is(back, e.Err) || !errors.Is(back, env.ErrRemote) {
				t.Fatalf("decoded %v does not match %v and ErrRemote", back, e.Err)
			}
		})
	}
	if err := table.Decode(250, "x"); !errors.Is(err, env.ErrRemote) {
		t.Fatalf("unknown code decoded as %v", err)
	}
}

// TestNodeAndAPICodesStayInTheirBlock proves object-API and node codes stay
// inside their reserved 160..191 wire-code block.
func TestNodeAndAPICodesStayInTheirBlock(t *testing.T) {
	for _, e := range append(node.NodeCodeEntries(), storage.APICodeEntries()...) {
		if e.Code < 160 || e.Code > 191 {
			t.Errorf("code %d for %v outside 160..191", e.Code, e.Err)
		}
	}
}

// TestJoinedUnavailablePrecedesEveryShardCause proves the merged node code
// table encodes storage.ErrUnavailable ahead of every per-shard cause in both
// errors.Join orders while leaving standalone causes unchanged.
func TestJoinedUnavailablePrecedesEveryShardCause(t *testing.T) {
	table, err := node.CodeTable()
	if err != nil {
		t.Fatal(err)
	}
	causes := []struct {
		name     string
		err      error
		wantCode env.Code
	}{
		{"storage.ErrNotFound", storage.ErrNotFound, storage.CodeNotFound},
		{"storage.ErrCorrupt", storage.ErrCorrupt, storage.CodeCorrupt},
		{"env.ErrNotFound", env.ErrNotFound, env.CodeNotFound},
		{"env.ErrTimeout", env.ErrTimeout, env.CodeTimeout},
		{"env.ErrUnreachable", env.ErrUnreachable, env.CodeUnreachable},
		{"storage.ErrBusy", storage.ErrBusy, storage.CodeBusy},
		{"storage.ErrExists", storage.ErrExists, storage.CodeExists},
		{"storage.ErrInvalid", storage.ErrInvalid, storage.CodeInvalid},
	}
	var all []error
	all = append(all, storage.ErrUnavailable)
	for _, c := range causes {
		all = append(all, c.err)
		t.Run("unavailable_before_"+c.name, func(t *testing.T) {
			code, msg := table.Encode(errors.Join(storage.ErrUnavailable, c.err))
			if code != storage.CodeUnavailable {
				t.Fatalf("Encode(Join(ErrUnavailable, %s)) = %d, want %d", c.name, code, storage.CodeUnavailable)
			}
			if back := table.Decode(code, msg); !errors.Is(back, storage.ErrUnavailable) {
				t.Fatalf("Decode(%d) = %v, want ErrUnavailable", code, back)
			}
		})
		t.Run(c.name+"_before_unavailable", func(t *testing.T) {
			code, msg := table.Encode(errors.Join(c.err, storage.ErrUnavailable))
			if code != storage.CodeUnavailable {
				t.Fatalf("Encode(Join(%s, ErrUnavailable)) = %d, want %d", c.name, code, storage.CodeUnavailable)
			}
			if back := table.Decode(code, msg); !errors.Is(back, storage.ErrUnavailable) {
				t.Fatalf("Decode(%d) = %v, want ErrUnavailable", code, back)
			}
		})
		t.Run("standalone_"+c.name, func(t *testing.T) {
			code, msg := table.Encode(c.err)
			if code != c.wantCode {
				t.Fatalf("Encode(%s) = %d, want %d", c.name, code, c.wantCode)
			}
			if back := table.Decode(code, msg); !errors.Is(back, c.err) {
				t.Fatalf("Decode(%d) = %v, want %v", code, back, c.err)
			}
		})
	}
	t.Run("all_causes_joined_with_unavailable", func(t *testing.T) {
		code, msg := table.Encode(errors.Join(all...))
		if code != storage.CodeUnavailable {
			t.Fatalf("Encode(all) = %d, want %d", code, storage.CodeUnavailable)
		}
		if back := table.Decode(code, msg); !errors.Is(back, storage.ErrUnavailable) {
			t.Fatalf("Decode(%d) = %v, want ErrUnavailable", code, back)
		}
	})
}
