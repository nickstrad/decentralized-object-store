package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand"
	"reflect"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/sim"
	s "github.com/nickstrad/object-store/internal/storage"
)

type idle struct{}

func (idle) Start()                                  {}
func (idle) OnDatagram(env.NodeID, []byte)           {}
func (idle) OnRequest(env.NodeID, []byte, env.Reply) {}
func (idle) EncodeState() []byte                     { return nil }
func disk() (*sim.Sim, env.Disk, *s.ChunkStore) {
	world := sim.New(sim.Config{Seed: 220211})
	var d env.Disk
	must(world.AddNode(sim.NodeSpec{ID: "disk", Region: "r"}, func(e env.Env) env.Process { d = e.Disk; return idle{} }))
	must(world.RunUntilQuiet())
	return world, d, s.NewChunkStore(d)
}
func blob(id s.ShardID, p []byte) []byte {
	b, err := s.EncodeShard(s.ShardHeader{K: 4, M: 2, Version: id.Version, Stripe: id.Stripe, Index: int(id.Index)}, p)
	must(err)
	return b
}
func storageAttacks() {
	for _, mode := range []string{"flip", "truncate", "extend", "index", "stripe", "version", "forge", "code", "length"} {
		mode := mode
		attack("C12,C13,C14", "shard-"+mode, func() string {
			c := codec(4, 2)
			data := payload(101)
			base, err := s.EncodeStripe(c, 17, 3, data)
			must(err)
			want := make([][32]byte, 6)
			for i := range base {
				want[i] = s.BlobChecksum(base[i])
			}
			for damage := 1; damage <= 3; damage++ {
				b := clone(base)
				for i := 0; i < damage; i++ {
					h, p, err := s.DecodeShard(b[i])
					must(err)
					switch mode {
					case "flip":
						b[i][len(b[i])-1] ^= 1
					case "truncate":
						b[i] = b[i][:len(b[i])-1]
					case "extend":
						b[i] = append(b[i], 0)
					case "index":
						h.Index = (i + 1) % 6
						b[i], err = s.EncodeShard(h, p)
					case "stripe":
						h.Stripe++
						b[i], err = s.EncodeShard(h, p)
					case "version":
						h.Version++
						b[i], err = s.EncodeShard(h, p)
					case "forge":
						p = append([]byte{}, p...)
						p[0] ^= 1
						b[i], err = s.EncodeShard(h, p)
					case "code":
						h.K = 3
						h.M = 3
						b[i], err = s.EncodeShard(h, p)
					case "length":
						b[i], err = s.EncodeShard(h, append(p, 0))
					}
					must(err)
				}
				out, bad, err := s.DecodeStripe(c, 17, 3, len(data), b, want)
				check(len(bad) == damage, "mode=%s damaged=%d reported=%v", mode, damage, bad)
				if damage <= 2 {
					must(err)
					same(out, data)
				} else {
					check(errors.Is(err, s.ErrUnavailable) && out == nil, "over budget returned data/error %v", err)
				}
			}
			return "one/two damaged shards recovered and reported; three rejected, including self-consistent headers"
		})
	}
	attack("C14", "shard-parser-mutations", func() string {
		id := s.ShardID{Bucket: "b", Version: 1}
		b := blob(id, payload(31))
		for i := 0; i < len(b); i++ {
			bad := append([]byte{}, b...)
			bad[i] ^= 1
			_, _, err := s.DecodeShard(bad)
			check(errors.Is(err, s.ErrCorrupt), "bit flip offset=%d accepted", i)
		}
		for i := 0; i < len(b); i++ {
			_, _, err := s.DecodeShard(b[:i])
			check(errors.Is(err, s.ErrCorrupt), "prefix %d accepted", i)
		}
		r := rand.New(rand.NewSource(220211))
		for i := 0; i < 10000; i++ {
			x := make([]byte, r.Intn(300))
			r.Read(x)
			h, p, err := s.DecodeShard(x)
			if err == nil {
				round, err := s.EncodeShard(h, p)
				must(err)
				same(round, x)
			}
		}
		return "every bit-position byte flip and truncation rejected; 10000 random inputs are total and canonical"
	})
	attack("C15,C16", "store-model", func() string {
		world, d, st := disk()
		model := map[s.ShardID][]byte{}
		r := rand.New(rand.NewSource(220211))
		for step := 0; step < 1000; step++ {
			id := s.ShardID{Bucket: "b", Version: s.VersionID(r.Intn(20)), Index: uint16(r.Intn(6))}
			b := blob(id, []byte{byte(step), 17})
			old, exists := model[id]
			switch r.Intn(5) {
			case 0, 1:
				err := st.Put(id, b)
				if exists && !bytes.Equal(old, b) {
					check(errors.Is(err, s.ErrExists), "step=%d overwrite", step)
				} else {
					must(err)
					model[id] = b
				}
			case 2:
				err := st.Delete(id)
				if exists {
					must(err)
					delete(model, id)
				} else {
					check(errors.Is(err, s.ErrNotFound), "missing delete %v", err)
				}
			case 3:
				if exists {
					must(world.BitRot("disk", id.BlobName()))
					_, err := st.GetBlob(id)
					check(errors.Is(err, s.ErrCorrupt), "rot not detected")
					must(st.Put(id, b))
					model[id] = b
				}
			case 4:
				if exists {
					must(world.FailReads("disk", id.BlobName(), 1))
					_, err := st.GetBlob(id)
					check(errors.Is(err, env.ErrIO) && !errors.Is(err, s.ErrCorrupt), "IO mislabeled")
					must(st.Put(id, old))
				}
			}
			ids, err := st.List("b")
			must(err)
			check(len(ids) == len(model), "step=%d list count", step)
			for id, want := range model {
				got, err := st.GetBlob(id)
				must(err)
				same(got, want)
				durable, ok := world.Disk("disk").PeekDurable(id.BlobName())
				check(ok, "not durable")
				same(durable, want)
			}
		}
		must(d.Write("shards/b/junk", []byte{1}))
		ids, err := st.List("b")
		must(err)
		check(len(ids) == len(model), "noncanonical listed")
		must(world.Crash("disk"))
		for id, want := range model {
			got, ok := world.Disk("disk").PeekDurable(id.BlobName())
			check(ok, "lost on crash")
			same(got, want)
		}
		return "1000 modeled operations, all keys and durable bytes checked after each step, then crash"
	})
	attack("C15", "store-identity-and-isolation", func() string {
		world, _, st := disk()
		for _, bucket := range []string{"a", "a-b", "a.b"} {
			for v := 0; v < 20; v++ {
				id := s.ShardID{Bucket: bucket, Version: s.VersionID(v)}
				must(st.Put(id, blob(id, []byte{byte(v)})))
				other := id
				other.Stripe = 1
				check(errors.Is(st.Put(other, blob(id, []byte{1})), s.ErrCorrupt), "misnamed accepted")
				ids, err := st.ListVersion(bucket, s.VersionID(v))
				must(err)
				check(len(ids) == 1 && ids[0] == id, "version list leaked")
			}
		}
		must(world.WipeDisk("disk"))
		ids, err := st.List("a")
		must(err)
		check(len(ids) == 0, "wipe list")
		_, err = st.GetBlob(s.ShardID{Bucket: "a"})
		check(errors.Is(err, s.ErrNotFound) && errors.Is(err, env.ErrNotFound), "wipe error %v", err)
		return "60 version/bucket namespaces isolated, misnamed writes rejected, wipe exposes joined not-found"
	})
	attack("C20", "manifest-canonical", func() string {
		for _, size := range []int64{0, 1, 7, 16, 17, 1001} {
			m := &s.Manifest{Bucket: "bucket", Key: "nul\x00key", Version: 123, Size: size, Layout: s.Layout{K: 4, M: 2, StripeSize: 16}}
			m.Stripes = make([]s.StripeRef, m.Layout.Stripes(size))
			for i := range m.Stripes {
				m.Stripes[i].Shards = make([]s.ShardRef, 6)
				for j := range m.Stripes[i].Shards {
					m.Stripes[i].Shards[j].Node = env.NodeID(fmt.Sprintf("n%d", j))
				}
			}
			b, err := m.Encode()
			must(err)
			dec, err := s.DecodeManifest(b)
			must(err)
			again, err := dec.Encode()
			must(err)
			same(again, b)
			for i := 0; i < len(b); i++ {
				_, err := s.DecodeManifest(b[:i])
				check(errors.Is(err, s.ErrCorrupt), "manifest prefix %d accepted", i)
			}
			for _, bad := range [][]byte{append(append([]byte{}, b...), 0), append(append([]byte{}, b[:5]...), append([]byte{0x86, 0}, b[6:]...)...)} {
				_, err := s.DecodeManifest(bad)
				check(errors.Is(err, s.ErrCorrupt), "noncanonical manifest accepted")
			}
			m.Size = -1
			check(errors.Is(m.Validate(), s.ErrInvalid), "negative size accepted")
		}
		return "six shapes round-trip; every truncation, trailing byte and nonminimal bucket varint rejected"
	})
	attack("C20", "manifest-hostile-numbers", func() string {
		r := rand.New(rand.NewSource(220211))
		for i := 0; i < 20000; i++ {
			b := make([]byte, 5+r.Intn(256))
			r.Read(b)
			copy(b, []byte{'O', 'M', 'A', 'N', 1})
			if i%2 == 0 {
				b = append(b, binary.AppendUvarint(nil, ^uint64(0))...)
			}
			m, err := s.DecodeManifest(b)
			if err == nil {
				round, err := m.Encode()
				must(err)
				same(round, b)
			}
		}
		return "20000 magic-prefixed random manifests with oversized varints processed without panic"
	})
	attack("C26", "wire-and-object-errors", func() string {
		table, err := s.Codes()
		must(err)
		for _, entry := range s.CodeEntries() {
			for _, cause := range []error{entry.Err, fmt.Errorf("outer: %w", entry.Err), errors.Join(entry.Err, errors.New("detail"))} {
				code, msg := table.Encode(cause)
				out := table.Decode(code, msg)
				check(errors.Is(out, entry.Err) && errors.Is(out, env.ErrRemote), "sentinel lost %v", cause)
				oe := &s.ObjectError{Op: "get", Bucket: "b", Key: "k", Err: out}
				check(errors.Is(oe, entry.Err) && oe.Error() != "", "ObjectError lost cause")
			}
		}
		for _, op := range []s.Op{s.OpPutShard, s.OpGetShard, s.OpDeleteShard} {
			id := s.ShardID{Bucket: "b", Version: 123, Stripe: 9, Index: 5}
			body := []byte(nil)
			if op == s.OpPutShard {
				body = blob(id, []byte{1})
			}
			req := s.EncodeShardRequest(op, id, body)
			got, rid, p, err := s.DecodeShardRequest(req)
			must(err)
			check(got == op && rid == id && reflect.DeepEqual(p, body) || got == op && rid == id && bytes.Equal(p, body), "wire mismatch")
			for i := 0; i < 3; i++ {
				_, _, _, err = s.DecodeShardRequest(req[:i])
				check(err != nil, "short request")
			}
		}
		return "all five sentinels survive direct/wrapped/joined code mapping and ObjectError; all request ops round-trip"
	})
}
