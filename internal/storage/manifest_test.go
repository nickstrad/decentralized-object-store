package storage_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/storage"
)

func sampleManifest(size int64) *storage.Manifest {
	l := storage.Layout{K: 2, M: 1, StripeSize: 8}
	man := &storage.Manifest{
		Bucket: "b", Key: "dir/key.txt", Version: 42, Size: size,
		ContentSHA: sha256.Sum256([]byte("x")), Layout: l,
	}
	for i := 0; i < l.Stripes(size); i++ {
		var st storage.StripeRef
		for j := 0; j < l.K+l.M; j++ {
			st.Shards = append(st.Shards, storage.ShardRef{Node: env.NodeID("n00" + string(rune('0'+j))), Checksum: [32]byte{byte(i), byte(j)}})
		}
		man.Stripes = append(man.Stripes, st)
	}
	return man
}

// TestManifestRoundTrip proves Encode and DecodeManifest preserve all fields
// and re-encode identically across empty, single-stripe, and multi-stripe sizes.
func TestManifestRoundTrip(t *testing.T) {
	for _, size := range []int64{0, 1, 8, 9, 17} {
		man := sampleManifest(size)
		b, err := man.Encode()
		mustNil(t, err)
		got, err := storage.DecodeManifest(b)
		mustNil(t, err)
		again, err := got.Encode()
		mustNil(t, err)
		if !bytes.Equal(again, b) {
			t.Fatalf("size %d: decode then encode is not the identity", size)
		}
		if got.Key != man.Key || got.Size != size || len(got.Stripes) != len(man.Stripes) || got.Layout != man.Layout {
			t.Fatalf("size %d: decoded %+v", size, got)
		}
		id := got.ShardID(1, 2)
		if id != (storage.ShardID{Bucket: "b", Version: 42, Stripe: 1, Index: 2}) {
			t.Fatalf("ShardID = %+v", id)
		}
	}
}

// TestManifestValidate proves Validate and Encode return ErrInvalid for every
// out-of-range bucket, key, size, layout, stripe count, shard count, or node ID.
func TestManifestValidate(t *testing.T) {
	cases := []struct {
		name string
		edit func(*storage.Manifest)
	}{
		{"bad bucket", func(m *storage.Manifest) { m.Bucket = "B" }},
		{"empty key", func(m *storage.Manifest) { m.Key = "" }},
		{"key over the limit", func(m *storage.Manifest) { m.Key = string(make([]byte, storage.MaxKeyLen+1)) }},
		{"negative size", func(m *storage.Manifest) { m.Size = -1 }},
		{"bad layout", func(m *storage.Manifest) { m.Layout.K = 0 }},
		{"too few stripes for the size", func(m *storage.Manifest) { m.Stripes = m.Stripes[:1] }},
		{"a stripe with too few shards", func(m *storage.Manifest) { m.Stripes[0].Shards = m.Stripes[0].Shards[:2] }},
		{"a shard with no node", func(m *storage.Manifest) { m.Stripes[1].Shards[0].Node = "" }},
		{"a node id over the limit", func(m *storage.Manifest) { m.Stripes[1].Shards[0].Node = env.NodeID(make([]byte, 256)) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			man := sampleManifest(17)
			c.edit(man)
			if err := man.Validate(); !errors.Is(err, storage.ErrInvalid) {
				t.Fatalf("Validate error %v, want ErrInvalid", err)
			}
			if _, err := man.Encode(); !errors.Is(err, storage.ErrInvalid) {
				t.Fatalf("Encode error %v, want ErrInvalid", err)
			}
		})
	}
}

// TestDecodeManifestMalformedTable proves DecodeManifest returns ErrCorrupt on
// bad magic/version, truncated or trailing bytes, invalid fields, and non-minimal varints.
func TestDecodeManifestMalformedTable(t *testing.T) {
	good, err := sampleManifest(17).Encode()
	mustNil(t, err)
	if good[60] != 2 {
		t.Fatalf("layout k is not at offset 60: %x", good[55:65])
	}
	edit := func(f func(b []byte) []byte) []byte { return f(append([]byte{}, good...)) }
	cases := []struct {
		name string
		b    []byte
	}{
		{"empty", nil},
		{"bad magic", edit(func(b []byte) []byte { b[0] = 'X'; return b })},
		{"unknown version", edit(func(b []byte) []byte { b[4] = 9; return b })},
		{"truncated", good[:len(good)-1]},
		{"trailing bytes", append(append([]byte{}, good...), 0)},
		{"truncated in the header", good[:6]},
		{"huge length prefix", append(append([]byte{}, good[:5]...), 0xff, 0xff, 0xff, 0xff, 0x0f)},
		{"a field that fails validation", edit(func(b []byte) []byte { b[6] = 'B'; return b })},
		{"a non-minimal number", append(append(append([]byte{}, good[:60]...), 0x82, 0x00), good[61:]...)},
	}
	for i := 5; i < len(good); i += 3 {
		cut := good[:i]
		cases = append(cases, struct {
			name string
			b    []byte
		}{"cut", cut})
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := storage.DecodeManifest(c.b); !errors.Is(err, storage.ErrCorrupt) {
				t.Fatalf("DecodeManifest error %v, want ErrCorrupt", err)
			}
		})
	}
}
