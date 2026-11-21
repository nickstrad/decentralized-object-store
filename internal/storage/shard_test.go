package storage_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

func sampleHeader() storage.ShardHeader {
	return storage.ShardHeader{K: 4, M: 2, Index: 5, Stripe: 7, Version: 0x0123456789abcdef}
}

func mustEncodeShard(t testing.TB, h storage.ShardHeader, payload []byte) []byte {
	t.Helper()
	b, err := storage.EncodeShard(h, payload)
	if err != nil {
		t.Fatalf("EncodeShard: %v", err)
	}
	return b
}

func TestShardRoundTrip(t *testing.T) {
	for _, payload := range [][]byte{{}, {1}, bytes.Repeat([]byte{0xab}, 4099)} {
		h := sampleHeader()
		blob := mustEncodeShard(t, h, payload)
		if len(blob) != storage.HeaderSize+len(payload) {
			t.Fatalf("blob length %d", len(blob))
		}
		got, p, err := storage.DecodeShard(blob)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(p, payload) {
			t.Fatal("payload differs")
		}
		h.PayloadLen = len(payload)
		h.Checksum = got.Checksum
		if got != h {
			t.Fatalf("header %+v, want %+v", got, h)
		}
		if got.Checksum != storage.BlobChecksum(blob) {
			t.Fatal("BlobChecksum differs from the header checksum")
		}
		sum := sha256.Sum256(append(append([]byte{}, blob[:32]...), payload...))
		if got.Checksum != sum {
			t.Fatal("checksum is not SHA-256 of the header fields and the payload")
		}
	}
}

func TestShardHeaderGolden(t *testing.T) {
	blob := mustEncodeShard(t, sampleHeader(), []byte{1, 2, 3})
	want := []byte{'O', 'S', 'S', 'H', 1, 0, 0, 4, 0, 2, 0, 5, 0, 0, 0, 7, 0, 0, 0, 3,
		0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef, 0, 0, 0, 0}
	if !bytes.Equal(blob[:32], want) {
		t.Fatalf("header fields\n got %x\nwant %x", blob[:32], want)
	}
}

func TestEncodeShardRejectsBadHeaders(t *testing.T) {
	cases := []struct {
		name string
		edit func(*storage.ShardHeader)
	}{
		{"k=0", func(h *storage.ShardHeader) { h.K = 0 }},
		{"negative m", func(h *storage.ShardHeader) { h.M = -1 }},
		{"k+m=257", func(h *storage.ShardHeader) { h.K, h.M = 200, 57 }},
		{"index past k+m", func(h *storage.ShardHeader) { h.Index = 6 }},
		{"negative index", func(h *storage.ShardHeader) { h.Index = -1 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := sampleHeader()
			c.edit(&h)
			if _, err := storage.EncodeShard(h, []byte{1}); !errors.Is(err, storage.ErrInvalid) {
				t.Fatalf("error %v, want ErrInvalid", err)
			}
		})
	}
	h := sampleHeader()
	h.K, h.M, h.Index = 128, 128, 255
	if _, err := storage.EncodeShard(h, nil); err != nil {
		t.Fatalf("k+m=256 with the last index: %v", err)
	}
}

func resum(b []byte) []byte {
	sum := sha256.Sum256(append(append([]byte{}, b[:32]...), b[storage.HeaderSize:]...))
	copy(b[32:64], sum[:])
	return b
}

func TestDecodeShardMalformedTable(t *testing.T) {
	good := mustEncodeShard(t, sampleHeader(), []byte("payload bytes"))
	edit := func(f func(b []byte) []byte) []byte { return f(append([]byte{}, good...)) }
	cases := []struct {
		name string
		blob []byte
	}{
		{"empty", nil},
		{"shorter than the header", good[:63]},
		{"header only with a payload length", good[:64]},
		{"truncated payload", good[:len(good)-1]},
		{"oversized payload", append(append([]byte{}, good...), 0)},
		{"bad magic", edit(func(b []byte) []byte { b[0] = 'X'; return resum(b) })},
		{"unknown format version", edit(func(b []byte) []byte { b[4] = 2; return resum(b) })},
		{"non-zero reserved byte", edit(func(b []byte) []byte { b[5] = 1; return resum(b) })},
		{"k=0", edit(func(b []byte) []byte { binary.BigEndian.PutUint16(b[6:], 0); return resum(b) })},
		{"k+m over the field limit", edit(func(b []byte) []byte { binary.BigEndian.PutUint16(b[8:], 300); return resum(b) })},
		{"index past k+m", edit(func(b []byte) []byte { binary.BigEndian.PutUint16(b[10:], 6); return resum(b) })},
		{"non-zero reserved tail", edit(func(b []byte) []byte { b[31] = 1; return resum(b) })},
		{"flipped payload bit", edit(func(b []byte) []byte { b[70] ^= 1; return b })},
		{"flipped header bit, checksum kept", edit(func(b []byte) []byte { b[15] ^= 1; return b })},
		{"flipped checksum bit", edit(func(b []byte) []byte { b[40] ^= 0x80; return b })},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, _, err := storage.DecodeShard(c.blob); !errors.Is(err, storage.ErrCorrupt) {
				t.Fatalf("error %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestBlobNames(t *testing.T) {
	id := storage.ShardID{Bucket: "photos", Version: 0xfeed, Stripe: 12, Index: 3}
	name := id.BlobName()
	if want := "shards/photos/" + name[14:16] + "/000000000000feed/12.3"; name != want {
		t.Fatalf("BlobName = %q, want %q", name, want)
	}
	got, err := storage.ParseBlobName(name)
	if err != nil || got != id {
		t.Fatalf("ParseBlobName = %+v, %v", got, err)
	}
	if g := storage.StripeGroup("n001", name); g != "photos/000000000000feed/12" {
		t.Fatalf("StripeGroup = %q", g)
	}
	if storage.StripeGroup("n001", "other/blob") != "" {
		t.Fatal("a non-shard blob belongs to a group")
	}
	bad := []string{"", "shards/", "shards/photos/00/000000000000feed/12", "shards/photos/zz/000000000000feed/12.3",
		"shards/photos/" + name[14:16] + "/000000000000feed/12.x", "shards/photos/" + name[14:16] + "/feed/12.3",
		"shards/Bad!/00/000000000000feed/12.3", "blobs/photos/00/000000000000feed/12.3",
		"shards/photos/" + name[14:16] + "/000000000000feed/012.3", "shards/photos/" + name[14:16] + "/000000000000feed/12.3.4",
		"shards/photos/" + name[14:16] + "/000000000000feed/4294967296.3", "shards/photos/" + name[14:16] + "/000000000000feed/12.65536",
		"shards/photos/" + name[14:16] + "/000000000000FEED/12.3", "shards/photos/" + name[14:16] + "/zzzzzzzzzzzzzzzz/12.3"}
	for _, b := range bad {
		if _, err := storage.ParseBlobName(b); !errors.Is(err, storage.ErrInvalid) {
			t.Fatalf("ParseBlobName(%q) error %v", b, err)
		}
	}
}

func TestBucketNames(t *testing.T) {
	ok := []string{"a", "photos", "my-bucket.2026", "0123456789012345678901234567890123456789012345678901234567890ab"}
	bad := []string{"", "Photos", "a/b", "a b", "../x", string(make([]byte, 64))}
	for _, b := range ok {
		if err := storage.ValidBucket(b); err != nil {
			t.Fatalf("ValidBucket(%q): %v", b, err)
		}
	}
	for _, b := range bad {
		if err := storage.ValidBucket(b); !errors.Is(err, storage.ErrInvalid) {
			t.Fatalf("ValidBucket(%q) error %v", b, err)
		}
	}
}

func FuzzShardDecode(f *testing.F) {
	good, err := storage.EncodeShard(sampleHeader(), []byte("payload bytes"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add([]byte{})
	f.Add([]byte{0})
	f.Add(good)
	f.Add(good[:storage.HeaderSize])
	f.Add(good[:len(good)-1])
	f.Add(append(append([]byte{}, good...), 1))
	empty, err := storage.EncodeShard(storage.ShardHeader{K: 1, M: 0}, nil)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(empty)
	f.Fuzz(func(t *testing.T, blob []byte) {
		h, payload, err := storage.DecodeShard(blob)
		if err != nil {
			if !errors.Is(err, storage.ErrCorrupt) {
				t.Fatalf("decode error %v is not ErrCorrupt", err)
			}
			return
		}
		if h.PayloadLen != len(payload) || len(blob) != storage.HeaderSize+len(payload) {
			t.Fatalf("accepted a blob whose length disagrees with its header")
		}
		sum := sha256.Sum256(append(append([]byte{}, blob[:32]...), payload...))
		if sum != h.Checksum {
			t.Fatal("accepted a blob whose checksum does not match: checksum bypass")
		}
		again, err := storage.EncodeShard(storage.ShardHeader{K: h.K, M: h.M, Index: h.Index, Stripe: h.Stripe, Version: h.Version}, payload)
		if err != nil || !bytes.Equal(again, blob) {
			t.Fatalf("an accepted blob does not re-encode to itself: %v", err)
		}
	})
}
