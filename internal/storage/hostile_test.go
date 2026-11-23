package storage_test

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/storage"
)

var hostileCodes = []struct{ k, m int }{
	{math.MaxInt, 1},
	{1, math.MaxInt},
	{1 << 62, 1 << 62},
	{math.MaxInt, math.MaxInt},
	{math.MinInt, 1},
	{257, -1},
}

// TestHostileCodeShapesAreErrorsNotPanics proves overflowing and out-of-range
// (K, M) pairs return ErrInvalid across Layout, Config, Manifest, and EncodeShard.
func TestHostileCodeShapesAreErrorsNotPanics(t *testing.T) {
	for _, c := range hostileCodes {
		l := storage.Layout{K: c.k, M: c.m, StripeSize: 1024}
		if err := l.Validate(); !errors.Is(err, storage.ErrInvalid) {
			t.Fatalf("Layout{K: %d, M: %d}.Validate() = %v, want ErrInvalid", c.k, c.m, err)
		}
		if _, err := l.Codec(); !errors.Is(err, storage.ErrInvalid) {
			t.Fatalf("Layout{K: %d, M: %d}.Codec() = %v, want ErrInvalid", c.k, c.m, err)
		}
		cfg := storage.Config{Layout: l, Members: storage.StaticMembership{{ID: "n000", Region: "r0"}}}
		if err := cfg.Validate(); !errors.Is(err, storage.ErrInvalid) {
			t.Fatalf("Config with k=%d m=%d: %v", c.k, c.m, err)
		}
		man := sampleManifest(17)
		man.Layout = l
		if err := man.Validate(); !errors.Is(err, storage.ErrInvalid) {
			t.Fatalf("Manifest with k=%d m=%d: %v", c.k, c.m, err)
		}
		_, err := storage.EncodeShard(storage.ShardHeader{K: c.k, M: c.m}, []byte{1})
		if !errors.Is(err, storage.ErrInvalid) {
			t.Fatalf("EncodeShard with k=%d m=%d: %v", c.k, c.m, err)
		}
		if strings.Contains(err.Error(), "index 0 of") {
			t.Fatalf("EncodeShard with k=%d m=%d passed the code check on a wrapped k+m: %v", c.k, c.m, err)
		}
	}
}

// TestGetOfAManifestWithAHostileLayoutCallsBackWithErrInvalid proves Node.Get
// rejects manifests with overflowing (K, M) via its callback without panicking.
func TestGetOfAManifestWithAHostileLayoutCallsBackWithErrInvalid(t *testing.T) {
	s, ids, _ := newCluster(t, 31, 6, 3, smallLayout())
	for _, c := range hostileCodes {
		man := sampleManifest(17)
		man.Layout = storage.Layout{K: c.k, M: c.m, StripeSize: 1024}
		g := get(t, s, nodeOf(t, s, ids[0]), man)
		if !errors.Is(g.err, storage.ErrInvalid) {
			t.Fatalf("get with k=%d m=%d: %v", c.k, c.m, g.err)
		}
	}
}

// TestDecodeShardRejectsPayloadOverMaxStripeSize proves DecodeShard rejects a
// checksum-valid blob larger than MaxStripeSize as ErrCorrupt while accepting
// one of exactly MaxStripeSize.
func TestDecodeShardRejectsPayloadOverMaxStripeSize(t *testing.T) {
	payloadLen := storage.MaxStripeSize + 1
	blob := make([]byte, storage.HeaderSize+payloadLen)
	copy(blob, "OSSH")
	blob[4] = storage.ShardFormatVersion
	binary.BigEndian.PutUint16(blob[6:], 4)
	binary.BigEndian.PutUint16(blob[8:], 2)
	binary.BigEndian.PutUint32(blob[16:], uint32(payloadLen))
	d := sha256.New()
	_, _ = d.Write(blob[:32])
	_, _ = d.Write(blob[storage.HeaderSize:])
	copy(blob[32:storage.HeaderSize], d.Sum(nil))
	if _, _, err := storage.DecodeShard(blob); !errors.Is(err, storage.ErrCorrupt) {
		t.Fatalf("DecodeShard of a %d-byte payload: %v, want ErrCorrupt", payloadLen, err)
	}
	if _, err := storage.EncodeShard(storage.ShardHeader{K: 4, M: 2}, blob[storage.HeaderSize:]); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("EncodeShard of a %d-byte payload: %v", payloadLen, err)
	}
	at := blob[:storage.HeaderSize+storage.MaxStripeSize]
	binary.BigEndian.PutUint32(at[16:], uint32(storage.MaxStripeSize))
	d.Reset()
	_, _ = d.Write(at[:32])
	_, _ = d.Write(at[storage.HeaderSize:])
	copy(at[32:storage.HeaderSize], d.Sum(nil))
	if _, _, err := storage.DecodeShard(at); err != nil {
		t.Fatalf("DecodeShard of a payload of exactly MaxStripeSize: %v", err)
	}
}

type cachedMembership struct {
	members []storage.Member
	calls   int
}

func (c *cachedMembership) Members() []storage.Member {
	c.calls++
	return c.members
}

// TestPlacementWithNilMembershipIsErrInvalid proves Place and Replace return
// ErrInvalid instead of panicking on a nil Membership.
func TestPlacementWithNilMembershipIsErrInvalid(t *testing.T) {
	if _, err := storage.Place(nil, "b/0/0", 6, 2); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("Place(nil): %v", err)
	}
	if _, err := storage.Replace(nil, "b/0/0", []env.NodeID{"a"}, 2); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("Replace(nil): %v", err)
	}
}

// TestPlacementDoesNotReorderTheMembershipSlice proves Place and Replace clone
// the slice returned by Membership.Members before sorting.
func TestPlacementDoesNotReorderTheMembershipSlice(t *testing.T) {
	ids := []env.NodeID{"n9", "n7", "n5", "n3", "n1", "n0", "n2", "n4"}
	ms := &cachedMembership{}
	for i, id := range ids {
		ms.members = append(ms.members, storage.Member{ID: id, Region: env.Region([]string{"r0", "r1", "r2"}[i%3])})
	}
	before := slices.Clone(ms.members)
	got, err := storage.Place(ms, "b/0/0", 6, 2)
	mustNil(t, err)
	if !slices.Equal(ms.members, before) {
		t.Fatalf("Place reordered the membership's slice: %v", ms.members)
	}
	if _, err := storage.Replace(ms, "b/0/0", got, 2); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ms.members, before) {
		t.Fatalf("Replace reordered the membership's slice: %v", ms.members)
	}
}
