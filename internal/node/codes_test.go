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
