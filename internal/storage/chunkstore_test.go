package storage_test

import (
	"bytes"
	"errors"
	"maps"
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

// TestChunkStoreTable proves ChunkStore Get, GetBlob, Put, and Delete behavior
// across missing, valid, idempotent, conflicting, bit-rotted, repaired, torn,
// misnamed, malformed, deleted, and bad-bucket cases.
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

// TestChunkStoreWipedDisk proves that after a disk wipe, Get returns
// ErrNotFound and List returns empty.
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

// TestChunkStoreWritesAreDurable proves Put and Delete both sync to the
// durable disk layer before returning.
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

// TestChunkStoreIOErrors proves injected disk read errors pass through as
// env.ErrIO rather than being misclassified as ErrCorrupt or ErrNotFound.
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

// TestChunkStoreList proves List and ListVersion return only canonical shard
// IDs for the requested bucket or version, skip stray entries, and reject bad buckets.
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

type chunkModel struct {
	written, durable map[storage.ShardID][]byte
	rotW, rotD       map[storage.ShardID]bool
	dirty            map[storage.ShardID]bool
}

func (m *chunkModel) has(id storage.ShardID) bool {
	_, ok := m.written[id]
	return ok
}

func (m *chunkModel) setBoth(id storage.ShardID, p []byte) {
	m.written[id], m.durable[id] = p, p
	delete(m.rotW, id)
	delete(m.rotD, id)
	delete(m.dirty, id)
}

func (m *chunkModel) dropBoth(id storage.ShardID) {
	delete(m.written, id)
	delete(m.durable, id)
	delete(m.rotW, id)
	delete(m.rotD, id)
	delete(m.dirty, id)
}

func (m *chunkModel) clean(id storage.ShardID) bool {
	w, okW := m.written[id]
	d, okD := m.durable[id]
	return okW && okD && bytes.Equal(w, d) && !m.rotW[id] && !m.rotD[id] && !m.dirty[id]
}

func (m *chunkModel) crash() {
	m.written = maps.Clone(m.durable)
	m.rotW = maps.Clone(m.rotD)
	m.dirty = map[storage.ShardID]bool{}
}

// TestPropChunkStoreModel checks random sequences of Put, Delete, bit-rot,
// unsynced disk writes/deletes, and crashes against a two-layer reference model,
// verifying durability after every Put/Delete and every key after every step.
func TestPropChunkStoreModel(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		rd := newRestartableDisk(rt)
		st := storage.NewChunkStore(rd.disk)
		m := &chunkModel{written: map[storage.ShardID][]byte{}, durable: map[storage.ShardID][]byte{}, rotW: map[storage.ShardID]bool{}, rotD: map[storage.ShardID]bool{}, dirty: map[storage.ShardID]bool{}}
		ids := make([]storage.ShardID, 0, 8)
		for v := 1; v <= 2; v++ {
			for idx := 0; idx < 4; idx++ {
				ids = append(ids, storage.ShardID{Bucket: "m", Version: storage.VersionID(v), Stripe: uint32(idx % 2), Index: uint16(idx)})
			}
		}
		genID := rapid.SampledFrom(ids)
		genPayload := rapid.SampledFrom([][]byte{{}, {1}, []byte("two"), []byte("payload four")})
		durableIs := func(rt *rapid.T, id storage.ShardID, want []byte) {
			got, ok := rd.s.Disk(rd.id).PeekDurable(id.BlobName())
			if want == nil {
				if ok {
					rt.Fatalf("%v is durable after a call that reported it absent", id)
				}
				return
			}
			if !ok || !bytes.Equal(got, shardFor(rt, id, want)) {
				rt.Fatalf("%v is not durable with the acknowledged bytes (present %v)", id, ok)
			}
		}
		rt.Repeat(map[string]func(*rapid.T){
			"put": func(rt *rapid.T) {
				id := genID.Draw(rt, "id")
				p := genPayload.Draw(rt, "payload")
				err := st.Put(id, shardFor(rt, id, p))
				prev, ok := m.written[id]
				switch {
				case !ok || m.rotW[id]:
					if err != nil {
						rt.Fatalf("put %v: %v", id, err)
					}
					m.setBoth(id, p)
					durableIs(rt, id, p)
				case bytes.Equal(prev, p):
					if err != nil {
						rt.Fatalf("idempotent put %v: %v", id, err)
					}
					m.setBoth(id, p)
					durableIs(rt, id, p)
				default:
					if !errors.Is(err, storage.ErrExists) {
						rt.Fatalf("conflicting put %v: %v", id, err)
					}
				}
			},
			"delete": func(rt *rapid.T) {
				id := genID.Draw(rt, "id")
				err := st.Delete(id)
				if !m.has(id) {
					if !errors.Is(err, storage.ErrNotFound) {
						rt.Fatalf("delete of missing %v: %v", id, err)
					}
				} else if err != nil {
					rt.Fatalf("delete %v: %v", id, err)
				}
				m.dropBoth(id)
				durableIs(rt, id, nil)
			},
			"rot": func(rt *rapid.T) {
				id := genID.Draw(rt, "id")
				if !m.clean(id) {
					rt.Skip("nothing clean to rot")
				}
				if err := rd.s.BitRot(rd.id, id.BlobName()); err != nil {
					rt.Fatal(err)
				}
				m.rotW[id], m.rotD[id] = true, true
			},
			"unsynced write": func(rt *rapid.T) {
				id := genID.Draw(rt, "id")
				p := genPayload.Draw(rt, "payload")
				if err := rd.disk.Write(id.BlobName(), shardFor(rt, id, p)); err != nil {
					rt.Fatal(err)
				}
				m.written[id] = p
				m.dirty[id] = true
				delete(m.rotW, id)
			},
			"unsynced delete": func(rt *rapid.T) {
				id := genID.Draw(rt, "id")
				if !m.has(id) {
					rt.Skip("nothing written to delete")
				}
				if err := rd.disk.Delete(id.BlobName()); err != nil {
					rt.Fatal(err)
				}
				delete(m.written, id)
				m.dirty[id] = true
				delete(m.rotW, id)
			},
			"crash": func(rt *rapid.T) {
				rd.crash(rt)
				st = storage.NewChunkStore(rd.disk)
				m.crash()
			},
			"": func(rt *rapid.T) {
				for _, id := range ids {
					_, p, err := st.Get(id)
					want, ok := m.written[id]
					switch {
					case !ok:
						if !errors.Is(err, storage.ErrNotFound) {
							rt.Fatalf("get missing %v: %v", id, err)
						}
					case m.rotW[id]:
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
				if len(listed) != len(m.written) {
					rt.Fatalf("List has %d shards, model %d", len(listed), len(m.written))
				}
				for _, id := range listed {
					if !m.has(id) {
						rt.Fatalf("List has %v, not in the model", id)
					}
				}
			},
		})
	})
}

// TestChunkStorePutOfUnsyncedIdenticalBytesIsDurable proves Put syncs even
// when matching bytes already exist in the unsynced written layer.
func TestChunkStorePutOfUnsyncedIdenticalBytesIsDurable(t *testing.T) {
	rd := newRestartableDisk(t)
	st := storage.NewChunkStore(rd.disk)
	id := storage.ShardID{Bucket: "b", Version: 1, Stripe: 0, Index: 0}
	blob := shardFor(t, id, []byte("unsynced"))
	mustNil(t, rd.disk.Write(id.BlobName(), blob))
	mustNil(t, st.Put(id, blob))
	rd.crash(t)
	st = storage.NewChunkStore(rd.disk)
	_, p, err := st.Get(id)
	if err != nil || !bytes.Equal(p, []byte("unsynced")) {
		t.Fatalf("an acknowledged Put was lost in a crash: %q, %v", p, err)
	}
}

// TestChunkStoreSecondPutAfterAFailedSyncIsDurable proves repeated Puts over
// an initially unsynced write leave the shard durable across a crash.
func TestChunkStoreSecondPutAfterAFailedSyncIsDurable(t *testing.T) {
	rd := newRestartableDisk(t)
	st := storage.NewChunkStore(rd.disk)
	id := storage.ShardID{Bucket: "b", Version: 1, Stripe: 2, Index: 3}
	blob := shardFor(t, id, []byte("retried"))
	mustNil(t, rd.disk.Write(id.BlobName(), blob))
	mustNil(t, st.Put(id, blob))
	mustNil(t, st.Put(id, blob))
	rd.crash(t)
	if got, ok := rd.s.Disk(rd.id).PeekDurable(id.BlobName()); !ok || !bytes.Equal(got, blob) {
		t.Fatalf("after two acknowledged Puts and a crash the shard is not durable (present %v)", ok)
	}
}

// TestChunkStoreDeleteOfAnUnsyncedRemovalIsDurable proves Delete syncs a prior
// unsynced deletion before returning ErrNotFound so the shard does not return
// after a crash.
func TestChunkStoreDeleteOfAnUnsyncedRemovalIsDurable(t *testing.T) {
	rd := newRestartableDisk(t)
	st := storage.NewChunkStore(rd.disk)
	id := storage.ShardID{Bucket: "b", Version: 1, Stripe: 0, Index: 1}
	mustNil(t, st.Put(id, shardFor(t, id, []byte("gone"))))
	mustNil(t, rd.disk.Delete(id.BlobName()))
	if err := st.Delete(id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("Delete after an unsynced removal: %v, want ErrNotFound", err)
	}
	rd.crash(t)
	st = storage.NewChunkStore(rd.disk)
	if _, _, err := st.Get(id); !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("a shard Delete reported absent came back after a crash: %v", err)
	}
}

// TestChunkStoreClosedDiskAndBadBuckets proves invalid buckets return
// ErrInvalid and operations on a closed disk return env.ErrClosed.
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
