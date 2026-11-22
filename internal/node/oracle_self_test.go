package node_test

import (
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

// TestAckOracleKeepsIdenticalPayloadsOnTwoKeysApart proves ackOracle scopes
// content matching by key so overwriting k2 cannot mask a silent loss on k1
// when both keys held the same bytes.
func TestAckOracleKeepsIdenticalPayloadsOnTwoKeysApart(t *testing.T) {
	h := smallCluster(t, 1)
	id := h.ids[0]
	n := h.node(id)
	if err := n.CreateBucket(storage.BucketSpec{Name: "o"}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ key, data string }{{"k1", "x"}, {"k2", "x"}, {"k2", "y"}} {
		if _, err := h.syncPut(t, id, storage.PutRequest{Bucket: "o", Key: p.key, Data: []byte(p.data)}); err != nil {
			t.Fatal(err)
		}
	}
	k1 := occ.ManifestKey("o", "k1")
	only := func(k string) bool { return k == k1 }
	rd, err := n.OCC().Reader()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.acks.check(rd, only); err != nil {
		t.Fatalf("the oracle failed a current write: %v", err)
	}
	if _, err := n.OCC().Delete(k1, occ.Unconditional()); err != nil {
		t.Fatal(err)
	}
	rd, err = n.OCC().Reader()
	if err != nil {
		t.Fatal(err)
	}
	if tally, err := h.acks.check(rd, only); err == nil {
		t.Fatalf("the oracle explained a silently lost write on k1 as %v because k2 once held the same payload", tally)
	}
}
