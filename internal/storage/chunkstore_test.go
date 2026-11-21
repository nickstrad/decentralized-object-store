package storage_test

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
	"pgregory.net/rapid"
)

func shardFor(t rapid.TB, id storage.ShardID, payload []byte) []byte {
	t.Helper()
	b, err := storage.EncodeShard(storage.ShardHeader{K: 4, M: 2, Index: int(id.Index), Stripe: id.Stripe, Version: id.Version}, payload)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestChunkStoreTable(t *testing.T) {
	id := storage.ShardID{Bucket: "b", Version: 9, Stripe: 1, Index: 2}
	other := storage.ShardID{Bucket: "b", Version: 9, Stripe: 1, Index: 3}
	cases := []struct {
		name    string
		prepare func(t *testing.T, st *storage.ChunkStore, d env.Disk, fault func(string))
		get     storage.ShardID
		wantErr error
		wantEnv error
		want    []byte
	}{
		{
			name:    "a missing shard is ErrNotFound",
			prepare: func(*testing.T, *storage.ChunkStore, env.Disk, func(string)) {},
			get:     id, wantErr: storage.ErrNotFound, wantEnv: env.ErrNotFound,
		},
		{
			name: "a stored shard reads back verified",
			prepare: func(t *testing.T, st *storage.ChunkStore, _ env.Disk, _ func(string)) {
				mustNil(t, st.Put(id, shardFor(t, id, []byte("abc"))))
			},
			get: id, want: []byte("abc"),
		},
		{
			name: "a second put of the same bytes is idempotent",
			prepare: func(t *testing.T, st *storage.ChunkStore, _ env.Disk, _ func(string)) {
				mustNil(t, st.Put(id, shardFor(t, id, []byte("abc"))))
				mustNil(t, st.Put(id, shardFor(t, id, []byte("abc"))))
			},
			get: id, want: []byte("abc"),
		},
		{
			name: "a put of different bytes over a valid shard is ErrExists and keeps the first",
			prepare: func(t *testing.T, st *storage.ChunkStore, _ env.Disk, _ func(string)) {
				mustNil(t, st.Put(id, shardFor(t, id, []byte("abc"))))
				if err := st.Put(id, shardFor(t, id, []byte("xyz"))); !errors.Is(err, storage.ErrExists) {
					t.Fatalf("second put error %v, want ErrExists", err)
				}
			},
			get: id, want: []byte("abc"),
		},
		{
			name: "a bit-rotted shard is ErrCorrupt, never wrong bytes",
			prepare: func(t *testing.T, st *storage.ChunkStore, _ env.Disk, rot func(string)) {
				mustNil(t, st.Put(id, shardFor(t, id, []byte("abcdef"))))
				rot(id.BlobName())
			},
			get: id, wantErr: storage.ErrCorrupt,
		},
		{
			name: "a put over a corrupt shard repairs it",
			prepare: func(t *testing.T, st *storage.ChunkStore, _ env.Disk, rot func(string)) {
				mustNil(t, st.Put(id, shardFor(t, id, []byte("abcdef"))))
				rot(id.BlobName())
				mustNil(t, st.Put(id, shardFor(t, id, []byte("abcdef"))))
			},
			get: id, want: []byte("abcdef"),
		},
		{
			name: "a torn blob on disk is ErrCorrupt",
			prepare: func(t *testing.T, _ *storage.ChunkStore, d env.Disk, _ func(string)) {
				b := shardFor(t, id, []byte("abcdef"))
				mustNil(t, d.Write(id.BlobName(), b[:len(b)-2]))
			},
			get: id, wantErr: storage.ErrCorrupt,
		},
		{
			name: "a shard stored under another shard's name is ErrCorrupt",
			prepare: func(t *testing.T, _ *storage.ChunkStore, d env.Disk, _ func(string)) {
				mustNil(t, d.Write(id.BlobName(), shardFor(t, other, []byte("abc"))))
			},
			get: id, wantErr: storage.ErrCorrupt,
		},
		{
			name: "a put whose blob names another shard is rejected as ErrCorrupt",
			prepare: func(t *testing.T, st *storage.ChunkStore, _ env.Disk, _ func(string)) {
				if err := st.Put(id, shardFor(t, other, []byte("abc"))); !errors.Is(err, storage.ErrCorrupt) {
					t.Fatalf("put error %v, want ErrCorrupt", err)
				}
			},
			get: id, wantErr: storage.ErrNotFound,
		},
		{
			name: "a put of malformed bytes is rejected as ErrCorrupt",
			prepare: func(t *testing.T, st *storage.ChunkStore, _ env.Disk, _ func(string)) {
				if err := st.Put(id, []byte("not a shard")); !errors.Is(err, storage.ErrCorrupt) {
					t.Fatalf("put error %v, want ErrCorrupt", err)
				}
			},
			get: id, wantErr: storage.ErrNotFound,
		},
		{
			name: "a deleted shard is ErrNotFound",
			prepare: func(t *testing.T, st *storage.ChunkStore, _ env.Disk, _ func(string)) {
				mustNil(t, st.Put(id, shardFor(t, id, []byte("abc"))))
				mustNil(t, st.Delete(id))
			},
			get: id, wantErr: storage.ErrNotFound,
		},
		{
			name:    "an invalid bucket is ErrInvalid",
			prepare: func(*testing.T, *storage.ChunkStore, env.Disk, func(string)) {},
			get:     storage.ShardID{Bucket: "Bad/Name"}, wantErr: storage.ErrInvalid,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, node, d := simDisk(t)
			st := storage.NewChunkStore(d)
			c.prepare(t, st, d, func(name string) { mustNil(t, s.BitRot(node, name)) })
			h, p, err := st.Get(c.get)
			if c.wantErr != nil {
				if !errors.Is(err, c.wantErr) {
					t.Fatalf("Get error %v, want %v", err, c.wantErr)
				}
				if c.wantEnv != nil && !errors.Is(err, c.wantEnv) {
					t.Fatalf("Get error %v does not wrap %v", err, c.wantEnv)
				}
				if p != nil {
					t.Fatal("Get returned bytes with an error")
				}
				return
			}
			mustNil(t, err)
			if !bytes.Equal(p, c.want) || h.PayloadLen != len(c.want) || h.Index != int(c.get.Index) {
				t.Fatalf("Get = %+v %q, want %q", h, p, c.want)
			}
			blob, err := st.GetBlob(c.get)
			mustNil(t, err)
			if !bytes.Equal(blob[storage.HeaderSize:], c.want) {
				t.Fatal("GetBlob payload differs")
			}
		})
	}
}

func TestChunkStoreWipedDisk(t *testing.T) {
	s, node, d := simDisk(t)
	st := storage.NewChunkStore(d)
	ids := []storage.ShardID{{Bucket: "b", Version: 1, Stripe: 0, Index: 0}, {Bucket: "b", Version: 1, Stripe: 0, Index: 1}}
	for _, id := range ids {
		mustNil(t, st.Put(id, shardFor(t, id, []byte{byte(id.Index)})))
	}
	mustNil(t, s.WipeDisk(node))
	for _, id := range ids {
		if _, _, err := st.Get(id); !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("after wipe Get error %v, want ErrNotFound", err)
		}
	}
	got, err := st.List("b")
	mustNil(t, err)
	if len(got) != 0 {
		t.Fatalf("after wipe List = %v", got)
	}
}

func TestChunkStoreWritesAreDurable(t *testing.T) {
	s, node, d := simDisk(t)
	st := storage.NewChunkStore(d)
	id := storage.ShardID{Bucket: "b", Version: 1, Stripe: 0, Index: 0}
	mustNil(t, st.Put(id, shardFor(t, id, []byte("durable"))))
	if _, ok := s.Disk(node).PeekDurable(id.BlobName()); !ok {
		t.Fatal("Put returned before the shard was durable")
	}
	mustNil(t, st.Delete(id))
	if _, ok := s.Disk(node).PeekDurable(id.BlobName()); ok {
		t.Fatal("Delete returned before the removal was durable")
	}
	if err := st.Delete(id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("second Delete error %v, want ErrNotFound", err)
	}
}

func TestChunkStoreIOErrors(t *testing.T) {
	s, node, d := simDisk(t)
	st := storage.NewChunkStore(d)
	id := storage.ShardID{Bucket: "b", Version: 1, Stripe: 0, Index: 0}
	mustNil(t, st.Put(id, shardFor(t, id, []byte("x"))))
	mustNil(t, s.FailReads(node, id.BlobName(), 1))
	_, _, err := st.Get(id)
	if !errors.Is(err, env.ErrIO) || errors.Is(err, storage.ErrCorrupt) || errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Get under a read error: %v", err)
	}
	mustNil(t, s.FailReads(node, id.BlobName(), 1))
	if err := st.Put(id, shardFor(t, id, []byte("x"))); !errors.Is(err, env.ErrIO) {
		t.Fatalf("Put under a read error: %v", err)
	}
}

func TestChunkStoreList(t *testing.T) {
	_, _, d := simDisk(t)
	st := storage.NewChunkStore(d)
	var want []storage.ShardID
	for _, v := range []storage.VersionID{3, 1, 2} {
		for stripe := uint32(0); stripe < 2; stripe++ {
			for idx := uint16(0); idx < 2; idx++ {
				id := storage.ShardID{Bucket: "b", Version: v, Stripe: stripe, Index: idx}
				mustNil(t, st.Put(id, shardFor(t, id, nil)))
				want = append(want, id)
			}
		}
	}
	other := storage.ShardID{Bucket: "c", Version: 1}
	mustNil(t, st.Put(other, shardFor(t, other, nil)))
	mustNil(t, d.Write("shards/b/zz/junk", []byte("x")))
	got, err := st.List("b")
	mustNil(t, err)
	if !sameIDs(got, want) {
		t.Fatalf("List(b) = %v", got)
	}
	gotV, err := st.ListVersion("b", 2)
	mustNil(t, err)
	if len(gotV) != 4 {
		t.Fatalf("ListVersion(b, 2) = %v", gotV)
	}
	for _, id := range gotV {
		if id.Version != 2 || id.Bucket != "b" {
			t.Fatalf("ListVersion returned %+v", id)
		}
	}
	if _, err := st.List("Bad"); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("List(Bad) error %v", err)
	}
	if _, err := st.ListVersion("Bad", 1); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("ListVersion(Bad) error %v", err)
	}
}

func sameIDs(a, b []storage.ShardID) bool {
	key := func(s []storage.ShardID) []string {
		out := make([]string, len(s))
		for i, id := range s {
			out[i] = id.BlobName()
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(key(a), key(b))
}

func mustNil(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestPropChunkStoreModel(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		s, node, d := simDisk(t)
		st := storage.NewChunkStore(d)
		model := map[storage.ShardID][]byte{}
		rotted := map[storage.ShardID]bool{}
		ids := make([]storage.ShardID, 0, 8)
		for v := 1; v <= 2; v++ {
			for idx := 0; idx < 4; idx++ {
				ids = append(ids, storage.ShardID{Bucket: "m", Version: storage.VersionID(v), Stripe: uint32(idx % 2), Index: uint16(idx)})
			}
		}
		genID := rapid.SampledFrom(ids)
		genPayload := rapid.SampledFrom([][]byte{{}, {1}, []byte("two"), []byte("payload four")})
		rt.Repeat(map[string]func(*rapid.T){
			"put": func(rt *rapid.T) {
				id := genID.Draw(rt, "id")
				p := genPayload.Draw(rt, "payload")
				err := st.Put(id, shardFor(rt, id, p))
				prev, ok := model[id]
				switch {
				case !ok || rotted[id]:
					if err != nil {
						rt.Fatalf("put %v: %v", id, err)
					}
					model[id] = p
					delete(rotted, id)
				case bytes.Equal(prev, p):
					if err != nil {
						rt.Fatalf("idempotent put %v: %v", id, err)
					}
				default:
					if !errors.Is(err, storage.ErrExists) {
						rt.Fatalf("conflicting put %v: %v", id, err)
					}
				}
			},
			"delete": func(rt *rapid.T) {
				id := genID.Draw(rt, "id")
				err := st.Delete(id)
				if _, ok := model[id]; !ok {
					if !errors.Is(err, storage.ErrNotFound) {
						rt.Fatalf("delete of missing %v: %v", id, err)
					}
					return
				}
				if err != nil {
					rt.Fatalf("delete %v: %v", id, err)
				}
				delete(model, id)
				delete(rotted, id)
			},
			"rot": func(rt *rapid.T) {
				id := genID.Draw(rt, "id")
				if _, ok := model[id]; !ok || rotted[id] {
					rt.Skip("nothing to rot")
				}
				if err := s.BitRot(node, id.BlobName()); err != nil {
					rt.Fatal(err)
				}
				rotted[id] = true
			},
			"": func(rt *rapid.T) {
				for _, id := range ids {
					_, p, err := st.Get(id)
					want, ok := model[id]
					switch {
					case !ok:
						if !errors.Is(err, storage.ErrNotFound) {
							rt.Fatalf("get missing %v: %v", id, err)
						}
					case rotted[id]:
						if !errors.Is(err, storage.ErrCorrupt) {
							rt.Fatalf("get rotted %v: %v %q", id, err, p)
						}
					default:
						if err != nil || !bytes.Equal(p, want) {
							rt.Fatalf("get %v = %q, %v; want %q", id, p, err, want)
						}
					}
				}
				listed, err := st.List("m")
				if err != nil {
					rt.Fatal(err)
				}
				if len(listed) != len(model) {
					rt.Fatalf("List has %d shards, model %d", len(listed), len(model))
				}
				for _, id := range listed {
					if _, ok := model[id]; !ok {
						rt.Fatalf("List has %v, not in the model", id)
					}
				}
			},
		})
	})
}

func TestChunkStoreClosedDiskAndBadBuckets(t *testing.T) {
	s, node, d := simDisk(t)
	st := storage.NewChunkStore(d)
	id := storage.ShardID{Bucket: "b", Version: 1}
	mustNil(t, st.Put(id, shardFor(t, id, []byte("x"))))
	bad := storage.ShardID{Bucket: "B"}
	if err := st.Put(bad, nil); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("Put to a bad bucket: %v", err)
	}
	if err := st.Delete(bad); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("Delete in a bad bucket: %v", err)
	}
	if _, err := st.GetBlob(bad); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("GetBlob in a bad bucket: %v", err)
	}
	mustNil(t, s.Crash(node))
	for name, err := range map[string]error{
		"put":    st.Put(id, shardFor(t, id, []byte("x"))),
		"delete": st.Delete(id),
	} {
		if !errors.Is(err, env.ErrClosed) || errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("%s on a closed disk: %v", name, err)
		}
	}
	if _, err := st.List("b"); !errors.Is(err, env.ErrClosed) {
		t.Fatalf("List on a closed disk: %v", err)
	}
	if _, err := st.GetBlob(id); !errors.Is(err, env.ErrClosed) {
		t.Fatalf("GetBlob on a closed disk: %v", err)
	}
}
