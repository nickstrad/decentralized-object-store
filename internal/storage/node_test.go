package storage_test

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/sim"
	"github.com/nickstrad/object-store/internal/storage"
)

func clusterMembers(ids []env.NodeID, regions int) storage.StaticMembership {
	ms := make(storage.StaticMembership, len(ids))
	for i, id := range ids {
		ms[i] = storage.Member{ID: id, Region: env.Region(fmt.Sprintf("r%d", i%regions))}
	}
	return ms
}

func nodeFactory(cfg storage.Config) sim.Factory {
	return func(e env.Env) env.Process {
		n, err := storage.NewNode(e, cfg)
		if err != nil {
			panic(err)
		}
		return n
	}
}

func newCluster(t testing.TB, seed int64, n, regions int, l storage.Layout) (*sim.Sim, []env.NodeID, storage.Config) {
	t.Helper()
	codes, err := storage.Codes()
	mustNil(t, err)
	s := sim.New(sim.Config{Seed: seed, Codes: codes})
	ids := sim.NodeIDs(n)
	cfg := storage.Config{Layout: l, Members: clusterMembers(ids, regions)}
	mustNil(t, s.AddCluster(ids, regions, nodeFactory(cfg)))
	mustNil(t, s.RunUntilQuiet())
	return s, ids, cfg
}

func nodeOf(t testing.TB, s *sim.Sim, id env.NodeID) *storage.Node {
	t.Helper()
	n, ok := s.Process(id).(*storage.Node)
	if !ok {
		t.Fatalf("node %s is not a storage node", id)
	}
	return n
}

type putResult struct {
	man  *storage.Manifest
	err  error
	done bool
}

// put runs Node.Put to completion and asserts its callback fires exactly once
// and never synchronously inside Put.
func put(t testing.TB, s *sim.Sim, n *storage.Node, bucket, key string, data []byte) putResult {
	t.Helper()
	var r putResult
	returned := false
	n.Put(bucket, key, data, func(man *storage.Manifest, err error) {
		if r.done {
			t.Fatal("put callback called twice")
		}
		if !returned {
			t.Fatalf("put callback ran inside the Put call (err %v)", err)
		}
		r = putResult{man, err, true}
	})
	returned = true
	mustNil(t, s.RunUntilQuiet())
	if !r.done {
		t.Fatal("put never called back")
	}
	return r
}

type getResult struct {
	res  storage.GetResult
	err  error
	done bool
}

// get runs Node.Get to completion and asserts its callback fires exactly once
// and never synchronously inside Get.
func get(t testing.TB, s *sim.Sim, n *storage.Node, man *storage.Manifest) getResult {
	t.Helper()
	var r getResult
	returned := false
	n.Get(man, func(res storage.GetResult, err error) {
		if r.done {
			t.Fatal("get callback called twice")
		}
		if !returned {
			t.Fatalf("get callback ran inside the Get call (err %v)", err)
		}
		r = getResult{res, err, true}
	})
	returned = true
	mustNil(t, s.RunUntilQuiet())
	if !r.done {
		t.Fatal("get never called back")
	}
	return r
}

func smallLayout() storage.Layout { return storage.Layout{K: 4, M: 2, StripeSize: 64} }

// TestNodePutGet proves objects from 0 to 4099 bytes write durably with at most
// m shards per region and read back byte-identically from every node using k
// requests per stripe.
func TestNodePutGet(t *testing.T) {
	for _, size := range []int{0, 1, 3, 4, 5, 63, 64, 65, 641, 4099} {
		t.Run(fmt.Sprintf("size=%d", size), func(t *testing.T) {
			s, ids, cfg := newCluster(t, 1, 9, 3, smallLayout())
			data := make([]byte, size)
			for i := range data {
				data[i] = byte(i*31 + size)
			}
			p := put(t, s, nodeOf(t, s, ids[0]), "b", "k", data)
			mustNil(t, p.err)
			man := p.man
			if man.Size != int64(size) || man.Layout != cfg.Layout || len(man.Stripes) != cfg.Layout.Stripes(int64(size)) {
				t.Fatalf("manifest %+v", man)
			}
			for st, ref := range man.Stripes {
				counts := map[env.Region]int{}
				for idx, sh := range ref.Shards {
					blob, ok := s.Disk(sh.Node).PeekDurable(man.ShardID(st, idx).BlobName())
					if !ok {
						t.Fatalf("stripe %d shard %d not durable on %s", st, idx, sh.Node)
					}
					if storage.BlobChecksum(blob) != sh.Checksum {
						t.Fatalf("stripe %d shard %d checksum differs from the manifest", st, idx)
					}
					counts[s.Region(sh.Node)]++
				}
				for r, c := range counts {
					if c > cfg.Layout.M {
						t.Fatalf("region %s holds %d shards of stripe %d", r, c, st)
					}
				}
			}
			for _, id := range ids {
				g := get(t, s, nodeOf(t, s, id), man)
				mustNil(t, g.err)
				if !bytes.Equal(g.res.Data, data) {
					t.Fatalf("get from %s returned different bytes", id)
				}
				if g.res.Requests != len(man.Stripes)*cfg.Layout.K || g.res.Rejected != 0 {
					t.Fatalf("a clean get made %d requests (%d rejected), want k per stripe", g.res.Requests, g.res.Rejected)
				}
			}
		})
	}
}

// TestNodeGetFallsBackToParity proves GET recovers exact bytes via parity when
// m shards per stripe are bit-rotted and returns an *ObjectError wrapping
// ErrUnavailable and remote ErrCorrupt causes when m+1 shards are rotted.
func TestNodeGetFallsBackToParity(t *testing.T) {
	s, ids, cfg := newCluster(t, 2, 9, 3, smallLayout())
	data := bytes.Repeat([]byte("0123456789"), 30)
	p := put(t, s, nodeOf(t, s, ids[0]), "b", "k", data)
	mustNil(t, p.err)
	man := p.man
	for st := range man.Stripes {
		for _, idx := range []int{0, 3} {
			sh := man.Stripes[st].Shards[idx]
			mustNil(t, s.BitRot(sh.Node, man.ShardID(st, idx).BlobName()))
		}
	}
	g := get(t, s, nodeOf(t, s, ids[1]), man)
	mustNil(t, g.err)
	if !bytes.Equal(g.res.Data, data) {
		t.Fatal("wrong bytes after two rotted shards per stripe")
	}
	if want := len(man.Stripes) * (cfg.Layout.K + 2); g.res.Requests != want || g.res.Rejected != 2*len(man.Stripes) {
		t.Fatalf("requests %d rejected %d, want %d and %d", g.res.Requests, g.res.Rejected, want, 2*len(man.Stripes))
	}
	sh := man.Stripes[0].Shards[5]
	mustNil(t, s.BitRot(sh.Node, man.ShardID(0, 5).BlobName()))
	g = get(t, s, nodeOf(t, s, ids[1]), man)
	if !errors.Is(g.err, storage.ErrUnavailable) || g.res.Data != nil {
		t.Fatalf("three rotted shards in stripe 0: %v, %d bytes", g.err, len(g.res.Data))
	}
	var oe *storage.ObjectError
	if !errors.As(g.err, &oe) || oe.Op != "get" || oe.Bucket != "b" || oe.Key != "k" {
		t.Fatalf("want an ObjectError for get b/k, got %v", g.err)
	}
	if !errors.Is(g.err, storage.ErrCorrupt) || !errors.Is(g.err, env.ErrRemote) {
		t.Fatalf("the per-shard causes did not cross the wire: %v", g.err)
	}
}

// TestNodePutUnavailable proves PUT fails with an *ObjectError wrapping
// ErrUnavailable and env.ErrUnreachable when fewer than k+1 holders are up, and
// succeeds once k+1 holders are reachable.
func TestNodePutUnavailable(t *testing.T) {
	s, ids, cfg := newCluster(t, 3, 6, 3, smallLayout())
	mustNil(t, s.Crash(ids[4]))
	mustNil(t, s.Crash(ids[5]))
	p := put(t, s, nodeOf(t, s, ids[0]), "b", "k", []byte("needs k+1 durable shards"))
	if !errors.Is(p.err, storage.ErrUnavailable) || p.man != nil {
		t.Fatalf("put with 4 of 6 nodes up: %v", p.err)
	}
	var oe *storage.ObjectError
	if !errors.As(p.err, &oe) || oe.Op != "put" {
		t.Fatalf("want an ObjectError for put, got %v", p.err)
	}
	if !errors.Is(p.err, env.ErrUnreachable) {
		t.Fatalf("per-shard causes are not reachable: %v", p.err)
	}
	mustNil(t, s.Restart(ids[5]))
	mustNil(t, s.RunUntilQuiet())
	p = put(t, s, nodeOf(t, s, ids[0]), "b", "k2", []byte("k+1 durable shards suffice"))
	mustNil(t, p.err)
	_ = cfg
}

// TestNodePutAcksAtQuorum proves PUT acknowledges as soon as k+1 holders reply
// without waiting for a paused node to time out.
func TestNodePutAcksAtQuorum(t *testing.T) {
	s, ids, _ := newCluster(t, 4, 6, 3, smallLayout())
	mustNil(t, s.Pause(ids[5]))
	var done bool
	var perr error
	nodeOf(t, s, ids[0]).Put("b", "k", []byte("quorum"), func(_ *storage.Manifest, err error) { done, perr = true, err })
	mustNil(t, s.RunFor(time.Second/2))
	if !done || perr != nil {
		t.Fatalf("put with one paused node did not ack at k+1 before the timeout: done=%v err=%v", done, perr)
	}
	mustNil(t, s.Resume(ids[5]))
	mustNil(t, s.RunUntilQuiet())
}

// TestNodeRejectsBadInput proves Put and Get reject bad buckets, empty keys,
// and nil or invalid manifests with ErrInvalid, and return ErrUnavailable
// wrapping ErrNotFound for an unwritten manifest.
func TestNodeRejectsBadInput(t *testing.T) {
	s, ids, _ := newCluster(t, 5, 6, 3, smallLayout())
	n := nodeOf(t, s, ids[0])
	p := put(t, s, n, "Bad Bucket", "k", []byte("x"))
	if !errors.Is(p.err, storage.ErrInvalid) {
		t.Fatalf("put to a bad bucket: %v", p.err)
	}
	p = put(t, s, n, "b", "", []byte("x"))
	if !errors.Is(p.err, storage.ErrInvalid) {
		t.Fatalf("put with an empty key: %v", p.err)
	}
	g := get(t, s, n, nil)
	if !errors.Is(g.err, storage.ErrInvalid) {
		t.Fatalf("get of a nil manifest: %v", g.err)
	}
	bad := sampleManifest(17)
	bad.Stripes = nil
	g = get(t, s, n, bad)
	if !errors.Is(g.err, storage.ErrInvalid) {
		t.Fatalf("get of an invalid manifest: %v", g.err)
	}
	g = get(t, s, n, sampleManifest(17))
	if !errors.Is(g.err, storage.ErrUnavailable) || !errors.Is(g.err, storage.ErrNotFound) {
		t.Fatalf("get of a manifest whose shards were never written: %v", g.err)
	}
}

// TestNodePutWithTooFewMembers proves Put returns ErrUnavailable (outside the
// Put call) when membership has fewer than k+m nodes.
func TestNodePutWithTooFewMembers(t *testing.T) {
	codes, err := storage.Codes()
	mustNil(t, err)
	s := sim.New(sim.Config{Seed: 1, Codes: codes})
	ids := sim.NodeIDs(3)
	cfg := storage.Config{Layout: smallLayout(), Members: clusterMembers(ids, 3)}
	mustNil(t, s.AddCluster(ids, 3, nodeFactory(cfg)))
	mustNil(t, s.RunUntilQuiet())
	p := put(t, s, nodeOf(t, s, ids[0]), "b", "k", []byte("six shards, three members"))
	if !errors.Is(p.err, storage.ErrUnavailable) {
		t.Fatalf("put with three members for six shards: %v", p.err)
	}
}

// TestNodePutUnlaunchableStripeLeavesAwaitingPeakZero proves a Put whose first
// stripe fails placement leaves PutAwaitingPeak and PutOutstanding at zero.
func TestNodePutUnlaunchableStripeLeavesAwaitingPeakZero(t *testing.T) {
	codes, err := storage.Codes()
	mustNil(t, err)
	s := sim.New(sim.Config{Seed: 1, Codes: codes})
	ids := sim.NodeIDs(3)
	cfg := storage.Config{Layout: smallLayout(), Members: clusterMembers(ids, 3)}
	mustNil(t, s.AddCluster(ids, 3, nodeFactory(cfg)))
	mustNil(t, s.RunUntilQuiet())
	n := nodeOf(t, s, ids[0])
	p := put(t, s, n, "b", "k", []byte("six shards, three members"))
	if !errors.Is(p.err, storage.ErrUnavailable) {
		t.Fatalf("put with three members for six shards: %v", p.err)
	}
	if st := n.Stats(); st.PutAwaitingPeak != 0 || st.PutOutstanding != 0 {
		t.Fatalf("a put whose first stripe never launched left awaiting peak %d, outstanding %d", st.PutAwaitingPeak, st.PutOutstanding)
	}
}

// TestNodeDetectsContentMismatch proves Node.Get verifies the reassembled
// object's SHA-256 against Manifest.ContentSHA and returns ErrCorrupt on mismatch.
func TestNodeDetectsContentMismatch(t *testing.T) {
	s, ids, _ := newCluster(t, 6, 6, 3, smallLayout())
	p := put(t, s, nodeOf(t, s, ids[0]), "b", "k", []byte("content"))
	mustNil(t, p.err)
	p.man.ContentSHA[0] ^= 1
	g := get(t, s, nodeOf(t, s, ids[1]), p.man)
	if !errors.Is(g.err, storage.ErrCorrupt) || g.res.Data != nil {
		t.Fatalf("a content hash mismatch gave %v", g.err)
	}
}

// TestNodeShardProtocol proves OnRequest serves Put, Get, and Delete shard RPCs,
// surfaces ErrNotFound/ErrExists/ErrCorrupt/ErrInvalid, and handles EncodeState and OnDatagram.
func TestNodeShardProtocol(t *testing.T) {
	s, ids, _ := newCluster(t, 7, 3, 1, storage.Layout{K: 1, M: 0, StripeSize: 8})
	id := storage.ShardID{Bucket: "b", Version: 1, Stripe: 0, Index: 0}
	blob, err := storage.EncodeShard(storage.ShardHeader{K: 1, M: 0, Version: 1}, []byte("x"))
	mustNil(t, err)
	call := func(req []byte) ([]byte, error) {
		var out []byte
		var rerr error
		done := false
		nodeOf(t, s, ids[1]).OnRequest(ids[0], req, func(b []byte, err error) { out, rerr, done = b, err, true })
		if !done {
			t.Fatal("request never answered")
		}
		return out, rerr
	}
	_, err = call(storage.EncodeShardRequest(storage.OpGetShard, id, nil))
	if !errors.Is(err, storage.ErrNotFound) || !errors.Is(err, env.ErrNotFound) {
		t.Fatalf("get of a missing shard: %v", err)
	}
	_, err = call(storage.EncodeShardRequest(storage.OpPutShard, id, blob))
	mustNil(t, err)
	got, err := call(storage.EncodeShardRequest(storage.OpGetShard, id, nil))
	mustNil(t, err)
	if !bytes.Equal(got, blob) {
		t.Fatal("get returned different bytes")
	}
	other, err := storage.EncodeShard(storage.ShardHeader{K: 1, M: 0, Version: 1}, []byte("y"))
	mustNil(t, err)
	_, err = call(storage.EncodeShardRequest(storage.OpPutShard, id, other))
	if !errors.Is(err, storage.ErrExists) {
		t.Fatalf("conflicting put: %v", err)
	}
	mustNil(t, s.BitRot(ids[1], id.BlobName()))
	_, err = call(storage.EncodeShardRequest(storage.OpGetShard, id, nil))
	if !errors.Is(err, storage.ErrCorrupt) {
		t.Fatalf("get of a rotted shard: %v", err)
	}
	_, err = call(storage.EncodeShardRequest(storage.OpDeleteShard, id, nil))
	mustNil(t, err)
	_, err = call(storage.EncodeShardRequest(storage.OpDeleteShard, id, nil))
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	for _, req := range [][]byte{nil, {9}, {byte(storage.OpGetShard)}, {byte(storage.OpGetShard), 200}, append([]byte{byte(storage.OpGetShard), 3}, "abc"...)} {
		if _, err := call(req); !errors.Is(err, storage.ErrInvalid) {
			t.Fatalf("malformed request %x: %v", req, err)
		}
	}
	if len(nodeOf(t, s, ids[1]).EncodeState()) == 0 {
		t.Fatal("empty state encoding")
	}
	nodeOf(t, s, ids[1]).OnDatagram(ids[0], []byte("ignored"))
}

// TestDecodeShardRequest proves EncodeShardRequest/DecodeShardRequest round-trip
// a put request and reject non-empty bodies on get requests.
func TestDecodeShardRequest(t *testing.T) {
	id := storage.ShardID{Bucket: "b", Version: 3, Stripe: 4, Index: 5}
	req := storage.EncodeShardRequest(storage.OpPutShard, id, []byte("blob"))
	op, gotID, body, err := storage.DecodeShardRequest(req)
	mustNil(t, err)
	if op != storage.OpPutShard || gotID != id || string(body) != "blob" {
		t.Fatalf("decoded %v %+v %q", op, gotID, body)
	}
	if _, _, _, err := storage.DecodeShardRequest(storage.EncodeShardRequest(storage.OpGetShard, id, []byte("x"))); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("get with a body: %v", err)
	}
}

// TestConfigValidate proves Config.Validate and NewNode enforce quorum bounds,
// non-nil membership, valid layout, and non-negative timeout and window values.
func TestConfigValidate(t *testing.T) {
	ms := clusterMembers(sim.NodeIDs(6), 3)
	cases := []struct {
		name string
		cfg  storage.Config
		ok   bool
	}{
		{"defaults fill in", storage.Config{Layout: storage.DefaultLayout(), Members: ms}, true},
		{"quorum k is allowed", storage.Config{Layout: storage.DefaultLayout(), Members: ms, AckQuorum: 4}, true},
		{"quorum k+m is allowed", storage.Config{Layout: storage.DefaultLayout(), Members: ms, AckQuorum: 6}, true},
		{"quorum below k", storage.Config{Layout: storage.DefaultLayout(), Members: ms, AckQuorum: 3}, false},
		{"quorum above k+m", storage.Config{Layout: storage.DefaultLayout(), Members: ms, AckQuorum: 7}, false},
		{"no membership", storage.Config{Layout: storage.DefaultLayout()}, false},
		{"bad layout", storage.Config{Layout: storage.Layout{K: 0}, Members: ms}, false},
		{"negative timeout", storage.Config{Layout: storage.DefaultLayout(), Members: ms, RequestTimeout: -1}, false},
		{"negative window", storage.Config{Layout: storage.DefaultLayout(), Members: ms, Window: -1}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.cfg.Validate()
			if c.ok && err != nil {
				t.Fatal(err)
			}
			if !c.ok && !errors.Is(err, storage.ErrInvalid) {
				t.Fatalf("error %v, want ErrInvalid", err)
			}
			_, nerr := storage.NewNode(env.Env{}, c.cfg)
			if (nerr == nil) != c.ok {
				t.Fatalf("NewNode error %v", nerr)
			}
		})
	}
}

// TestNodeGetRetriesTransientErrors proves GET retries transient env.ErrIO
// failures after backoff to recover exact bytes and fails with ErrUnavailable
// wrapping env.ErrIO within MaxShardAttempts when read errors persist.
func TestNodeGetRetriesTransientErrors(t *testing.T) {
	s, ids, cfg := newCluster(t, 8, 9, 3, smallLayout())
	data := []byte("read errors pass; a retry after a backoff finds the shard")
	p := put(t, s, nodeOf(t, s, ids[0]), "b", "k", data)
	mustNil(t, p.err)
	man := p.man
	for _, idx := range []int{0, 1, 2} {
		sh := man.Stripes[0].Shards[idx]
		mustNil(t, s.FailReads(sh.Node, man.ShardID(0, idx).BlobName(), 1))
	}
	g := get(t, s, nodeOf(t, s, ids[1]), man)
	mustNil(t, g.err)
	if !bytes.Equal(g.res.Data, data) {
		t.Fatal("wrong bytes after transient read errors on three shards")
	}
	if g.res.Rejected != 3 {
		t.Fatalf("rejected %d, want 3", g.res.Rejected)
	}
	for _, idx := range []int{0, 1, 2} {
		sh := man.Stripes[0].Shards[idx]
		mustNil(t, s.FailReads(sh.Node, man.ShardID(0, idx).BlobName(), storage.MaxShardAttempts))
	}
	g = get(t, s, nodeOf(t, s, ids[1]), man)
	if !errors.Is(g.err, storage.ErrUnavailable) || !errors.Is(g.err, env.ErrIO) {
		t.Fatalf("persistent read errors on three shards: %v", g.err)
	}
	if max := len(man.Stripes) * (cfg.Layout.K + cfg.Layout.M) * storage.MaxShardAttempts; g.res.Requests > max {
		t.Fatalf("%d requests, bound %d", g.res.Requests, max)
	}
	if want := 3*storage.MaxShardAttempts + 3; g.res.Requests != want {
		t.Fatalf("%d requests, want %d (three shards at every attempt, three clean)", g.res.Requests, want)
	}
}

// TestNodeStatsAndStore proves Node.Stats tracks completed puts, gets, and
// served/issued requests and Node.Store exposes persisted shards.
func TestNodeStatsAndStore(t *testing.T) {
	s, ids, _ := newCluster(t, 9, 6, 3, smallLayout())
	n := nodeOf(t, s, ids[0])
	n.Start()
	p := put(t, s, n, "b", "k", []byte("stats"))
	mustNil(t, p.err)
	_ = get(t, s, n, p.man)
	st := n.Stats()
	if st.PutsDone != 1 || st.GetsDone != 1 || st.Requests != 6+4 {
		t.Fatalf("stats %+v", st)
	}
	holder := p.man.Stripes[0].Shards[0].Node
	if _, _, err := nodeOf(t, s, holder).Store().Get(p.man.ShardID(0, 0)); err != nil {
		t.Fatalf("Store().Get on the holder: %v", err)
	}
	served := uint64(0)
	for _, id := range ids {
		served += nodeOf(t, s, id).Stats().Served
	}
	if served != 6+4 {
		t.Fatalf("served %d requests, want 10", served)
	}
}
