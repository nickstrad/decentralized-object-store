package node

import (
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

// TestAbsenceRecordKeyRoundTripsForAnyHolderName proves absentKey and
// noteAbsent round-trip holder IDs containing slashes, colons, or a bare "/".
func TestAbsenceRecordKeyRoundTripsForAnyHolderName(t *testing.T) {
	id := storage.ShardID{Bucket: "g", Version: storage.VersionID(0xc7bd04bd6c737491), Stripe: 12, Index: 5}
	for _, holder := range []env.NodeID{"n003", "dc1/rack2/n7", "a/b/c/d", "host.example:7000", "/"} {
		n := &Node{absent: map[storage.VersionID]map[[2]int]map[env.NodeID]bool{}}
		_, parts, err := occ.ParseKey(absentKey(id.Bucket, id, holder))
		if err != nil {
			t.Fatalf("holder %q: %v", holder, err)
		}
		n.noteAbsent(parts[2])
		if !n.absent[id.Version][[2]int{12, 5}][holder] {
			t.Fatalf("holder %q: the absence record was not read back: %v", holder, n.absent)
		}
		if len(n.absent) != 1 || len(n.absent[id.Version]) != 1 || len(n.absent[id.Version][[2]int{12, 5}]) != 1 {
			t.Fatalf("holder %q: read back more than the one record: %v", holder, n.absent)
		}
	}
}

// TestMalformedAbsenceRecordNamesAreIgnored proves noteAbsent ignores invalid
// hex versions, missing dots, non-numeric indices, and empty holder names.
func TestMalformedAbsenceRecordNamesAreIgnored(t *testing.T) {
	for _, name := range []string{
		tagAbsent,
		tagAbsent + "zz/0.0/n1",
		tagAbsent + "00ab/0/n1",
		tagAbsent + "00ab/x.0/n1",
		tagAbsent + "00ab/0.y/n1",
		tagAbsent + "00ab/0.0",
		tagAbsent + "00ab/0.0/",
		tagAbsent + "00ab/-1.0/n1",
	} {
		n := &Node{absent: map[storage.VersionID]map[[2]int]map[env.NodeID]bool{}}
		n.noteAbsent(name)
		if len(n.absent) != 0 {
			t.Fatalf("%q was read as an absence record: %v", name, n.absent)
		}
	}
}
