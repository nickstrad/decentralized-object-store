package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/gossip"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

var failed int

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func require(ok bool, f string, a ...any) {
	if !ok {
		panic(fmt.Sprintf(f, a...))
	}
}
func check(claim, name string, seed int64, fn func() string) {
	defer func() {
		if e := recover(); e != nil {
			failed++
			fmt.Printf("BROKEN %s %s: seed=%d %s\n", claim, name, seed, strings.ReplaceAll(fmt.Sprint(e), "\n", " "))
		}
	}()
	detail := fn()
	fmt.Printf("HELD %s %s: seed=%d %s\n", claim, name, seed, detail)
}

type loss struct {
	key   string
	loser occ.Version
}
type cluster struct {
	s      *sim.Sim
	ids    []env.NodeID
	losses []loss
	logs   []string
}

func config() node.Config {
	return node.Config{Layout: storage.Layout{K: 2, M: 1, StripeSize: 1024}, RequestTimeout: 400 * time.Millisecond, RPCTimeout: 400 * time.Millisecond, GCGrace: 4 * time.Second, OrphanGrace: 10 * time.Minute, ViewSettle: time.Second, MaintainPeriod: 200 * time.Millisecond, RecountSettle: time.Second, Gossip: gossip.Config{ProbeInterval: 200 * time.Millisecond, ProbeTimeout: 80 * time.Millisecond, SyncInterval: time.Second}}
}
func newCluster(seed int64) *cluster { return makeCluster(seed, nil) }
func makeCluster(seed int64, wrap func(env.Env) env.Env) *cluster {
	c := &cluster{ids: sim.NodeIDs(9)}
	codes, e := node.CodeTable()
	must(e)
	net := sim.DefaultNetConfig()
	net.DropPPM = 10000
	net.DuplicatePPM = 20000
	c.s = sim.New(sim.Config{Seed: seed, Codes: codes, Net: net})
	cfg := config()
	cfg.Seeds = c.ids[:3]
	cfg.DiscardSink = func(k string, w, l occ.Version) { c.losses = append(c.losses, loss{k, l}) }
	cfg.Log = func(s string) { c.logs = append(c.logs, s) }
	factory := node.NewFactory(cfg)
	must(c.s.AddCluster(c.ids, 3, func(e env.Env) env.Process {
		if wrap != nil {
			e = wrap(e)
		}
		return factory(e)
	}))
	c.run(5 * time.Second)
	return c
}
func (c *cluster) n(i int) *node.Node  { return c.s.Process(c.ids[i]).(*node.Node) }
func (c *cluster) run(d time.Duration) { must(c.s.RunFor(d)) }
func (c *cluster) wait(f func() bool)  { must(c.s.RunUntilConverged(c.s.Now().Add(50*time.Second), f)) }
func (c *cluster) settle() {
	c.wait(func() bool {
		d := c.n(0).OCC().Digest()
		for i := range c.ids {
			if !c.n(i).Ready() || c.n(i).OCC().Digest() != d || len(c.n(i).Members()) != len(c.ids) {
				return false
			}
		}
		return true
	})
}
func (c *cluster) put(i int, k string, b []byte, cond storage.Condition) (storage.ObjectInfo, error) {
	var o storage.ObjectInfo
	var e error
	done := false
	c.n(i).PutObject(storage.PutRequest{Bucket: "b", Key: k, Data: b, Cond: cond}, func(v storage.ObjectInfo, err error) { o, e, done = v, err, true })
	c.wait(func() bool { return done })
	return o, e
}
func (c *cluster) get(i int, k string) (storage.ObjectInfo, []byte, error) {
	var o storage.ObjectInfo
	var b []byte
	var e error
	done := false
	c.n(i).GetObject("b", k, func(v storage.ObjectInfo, p []byte, err error) { o, b, e, done = v, p, err, true })
	c.wait(func() bool { return done })
	return o, b, e
}
func (c *cluster) bucket(region string, quota uint64) {
	must(c.n(0).CreateBucket(storage.BucketSpec{Name: "b", Region: region, QuotaBytes: quota}))
	c.settle()
}
func manifest(c *cluster, key string) *storage.Manifest {
	r, e := c.n(0).OCC().Reader()
	must(e)
	v, _, e := r.Get(occ.ManifestKey("b", key))
	must(e)
	require(len(v) > 1 && v[0] == 1, "record version")
	n, k := binary.Uvarint(v[1:])
	require(k > 0 && int(n)+1+k <= len(v), "record content-type")
	m, e := storage.DecodeManifest(v[1+k+int(n):])
	must(e)
	return m
}
func partition(seed int64) {
	c := newCluster(seed)
	c.bucket("", 2200)
	cond := storage.Condition{IfNoneMatch: true}
	if seed%2 == 0 {
		o, e := c.put(0, "same", []byte("base"), storage.Condition{})
		must(e)
		c.settle()
		cond = storage.Condition{IfMatch: o.ETag}
	}
	var left, right []env.NodeID
	li, ri := 0, 0
	for i, id := range c.ids {
		if c.s.Region(id) == "r0" {
			left = append(left, id)
			li = i
		} else {
			right = append(right, id)
			ri = i
		}
	}
	must(c.s.Partition(left, right))
	c.run(7 * time.Second)
	bodies := map[string][]byte{}
	versions := map[string]occ.Version{}
	var ack []storage.ObjectInfo
	for j := range 2 {
		i := li
		if j == 1 {
			i = ri
		}
		b := bytes.Repeat([]byte{byte(70 + j)}, 701)
		o, e := c.put(i, "same", b, cond)
		must(e)
		ack = append(ack, o)
		bodies[o.Version] = b
		v, _ := c.n(i).OCC().Record(occ.ManifestKey("b", "same")).Version()
		versions[o.Version] = v
		for q := range 2 {
			o, e = c.put(i, fmt.Sprintf("side%d-%d", j, q), bytes.Repeat([]byte{byte(q)}, 600), storage.Condition{})
			must(e)
		}
	}
	c.s.HealAll()
	c.run(15 * time.Second)
	c.settle()
	check("C2,C3", "partition-single-winner", seed, func() string {
		first := ""
		for i := range c.ids {
			o, b, e := c.get(i, "same")
			must(e)
			require(bytes.Equal(b, bodies[o.Version]), "node=%d wrong bytes version=%s", i, o.Version)
			if first == "" {
				first = o.Version
			}
			require(first == o.Version, "divergent winner")
		}
		return "nine replicas return identical acknowledged bytes after two isolated writers"
	})
	check("C3", "discard-record-and-log", seed, func() string {
		o, e := c.n(0).HeadObject("b", "same")
		must(e)
		for _, a := range ack {
			if a.Version == o.Version {
				continue
			}
			found := false
			for _, l := range c.losses {
				if l.loser.ID() == versions[a.Version].ID() {
					found = true
				}
			}
			require(found, "missing loser=%s", a.Version)
			logged := false
			for _, line := range c.logs {
				if strings.Contains(line, "loser="+versions[a.Version].ID().String()+" ") {
					logged = true
				}
			}
			require(logged, "loser not logged=%s", a.Version)
		}
		var count uint64
		for i := range c.ids {
			count += c.n(i).Health().Discards
		}
		require(count > 0 && count == uint64(len(c.losses)) && len(c.logs) == len(c.losses), "counter/callback/log mismatch")
		return fmt.Sprintf("%d discard callbacks, health total=%d, log lines=%d", len(c.losses), count, len(c.logs))
	})
	check("C4,C25", "quota-overshoot-recount", seed, func() string {
		objs, e := c.n(0).ListObjects("b", "", 100)
		must(e)
		var sum uint64
		for _, o := range objs {
			sum += uint64(o.Size)
		}
		for i := range c.ids {
			q, e := c.n(i).Quota("b")
			must(e)
			require(q.UsedBytes == sum && q.UsedObjects == uint64(len(objs)), "node=%d usage=%+v truth=%d/%d", i, q, sum, len(objs))
		}
		return fmt.Sprintf("settled bytes=%d limit=2200 overshoot=%d objects=%d", sum, int64(sum)-2200, len(objs))
	})
	check("C5", "loser-only-collection", seed, func() string {
		o, e := c.n(0).HeadObject("b", "same")
		must(e)
		var loser string
		for _, a := range ack {
			if a.Version != o.Version {
				loser = a.Version
			}
		}
		require(loser != "", "no loser")
		collected := 0
		for i, id := range c.ids {
			for _, name := range c.s.Disk(id).Names() {
				sid, e := storage.ParseBlobName(name)
				if e == nil {
					require(sid.Version.String() != loser, "loser shard remains %s", name)
				}
			}
			for _, r := range c.n(i).Collected() {
				require(r.Version.String() != o.Version, "winner collected")
				if r.Version.String() == loser {
					collected++
				}
			}
		}
		require(collected > 0, "no loser collection observed")
		return fmt.Sprintf("%d loser shard collections; winning version remains readable", collected)
	})
}
func faults(seed int64) {
	c := newCluster(seed)
	c.bucket("", 0)
	size := []int{0, 1, 1023, 1024, 1025, 3077}[seed%6]
	want := bytes.Repeat([]byte{byte(seed)}, size)
	_, e := c.put(0, "k", want, storage.Condition{})
	must(e)
	c.settle()
	check("C8,C11", "get-during-repair", seed, func() string {
		m := manifest(c, "k")
		type damage struct {
			id   env.NodeID
			name string
			sum  [32]byte
		}
		var ds []damage
		var damagedBytes uint64
		for st := range m.Stripes {
			ix := int(seed) % len(m.Stripes[st].Shards)
			sh := m.Stripes[st].Shards[ix]
			name := m.ShardID(st, ix).BlobName()
			b, ok := c.s.Disk(sh.Node).PeekDurable(name)
			require(ok, "shard absent before rot")
			damagedBytes += uint64(len(b))
			must(c.s.BitRot(sh.Node, name))
			ds = append(ds, damage{sh.Node, name, sh.Checksum})
		}
		for i := range c.ids {
			_, b, e := c.get(i, "k")
			must(e)
			require(bytes.Equal(b, want), "wrong bytes node=%d", i)
		}
		c.run(3 * time.Second)
		for _, d := range ds {
			b, ok := c.s.Disk(d.id).PeekDurable(d.name)
			require(ok && storage.BlobChecksum(b) == d.sum, "not repaired")
		}
		var rebuilt uint64
		for i := range c.ids {
			rebuilt += c.n(i).Stats().RebuiltBytes
		}
		require(rebuilt == damagedBytes, "rebuilt=%d damaged=%d", rebuilt, damagedBytes)
		return fmt.Sprintf("size=%d repaired=%d shards bytes=%d; all nine reads exact", size, len(ds), rebuilt)
	})
	check("C2,C6,C7", "wipe-rejoin-delete", seed, func() string {
		id := c.ids[8]
		must(c.s.Crash(id))
		must(c.s.WipeDisk(id))
		must(c.s.Restart(id))
		c.run(12 * time.Second)
		c.settle()
		_, b, e := c.get(8, "k")
		must(e)
		require(bytes.Equal(b, want), "wipe rejoin changed bytes")
		m := manifest(c, "k")
		for st := range m.Stripes {
			for ix, sh := range m.Stripes[st].Shards {
				if sh.Node == id {
					blob, ok := c.s.Disk(id).PeekDurable(m.ShardID(st, ix).BlobName())
					require(ok && storage.BlobChecksum(blob) == sh.Checksum, "wiped shard not rebuilt")
				}
			}
		}
		must(c.s.Crash(id))
		_, e = c.n(0).DeleteObject("b", "k", storage.Condition{})
		must(e)
		c.run(time.Second)
		must(c.s.Restart(id))
		c.run(12 * time.Second)
		c.settle()
		for i := range c.ids {
			_, e := c.n(i).HeadObject("b", "k")
			require(errors.Is(e, occ.ErrNotFound), "deleted object reappears node=%d err=%v", i, e)
		}
		return "wiped holder refilled, then stale restarted replica learned deletion"
	})
}
func migration(seed int64) {
	c := newCluster(seed)
	c.bucket("r0", 0)
	want := bytes.Repeat([]byte{byte(seed)}, 2081)
	_, e := c.put(0, "k", want, storage.Condition{})
	must(e)
	c.settle()
	m := manifest(c, "k")
	sh := m.Stripes[0].Shards[int(seed)%3]
	must(c.s.BitRot(sh.Node, m.ShardID(0, int(seed)%3).BlobName()))
	must(c.s.PartitionRegion("r2"))
	mi, e := c.n(0).StartMigration("b", "r1")
	must(e)
	good, unavailable := 0, 0
	for j := range 15 {
		_, b, e := c.get(0, "k")
		if e == nil {
			require(bytes.Equal(b, want), "migration returned wrong bytes")
			good++
		} else {
			require(errors.Is(e, storage.ErrUnavailable) || errors.Is(e, env.ErrTimeout), "unexpected migration read: %v", e)
			unavailable++
		}
		c.run(100 * time.Millisecond)
		if j == 4 {
			must(c.s.Crash(c.ids[0]))
			must(c.s.Restart(c.ids[0]))
			c.run(time.Second)
		}
	}
	c.s.HealAll()
	c.run(25 * time.Second)
	c.settle()
	st, e := c.n(0).Migration(mi.ID)
	must(e)
	require(st.State == "Completed" || st.State == "completed", "migration status %+v", st)
	for i := range c.ids {
		_, b, e := c.get(i, "k")
		must(e)
		require(bytes.Equal(b, want), "final migration read changed")
		rd, e := c.n(i).OCC().Reader()
		must(e)
		route, e := rd.Route("b")
		must(e)
		require(string(route) == "r1", "route=%s", route)
	}
	fmtDetail := fmt.Sprintf("partition+rot+coordinator restart; reads exact=%d temporarily unavailable=%d; completed with nine agreeing routes", good, unavailable)
	check("C9,C26", "migration-partition-rot", seed, func() string { return fmtDetail })
	check("C10", "migration-source-collected", seed, func() string {
		for _, id := range c.ids {
			if c.s.Region(id) != "r0" {
				continue
			}
			for _, name := range c.s.Disk(id).Names() {
				sid, e := storage.ParseBlobName(name)
				require(e != nil || sid.Bucket != "b", "source shard remains %s", name)
			}
		}
		return "no source-region bucket shards remain after migration"
	})
}
func main() {
	mode := "sim"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	if mode == "real" {
		realChecks()
	} else if mode == "cli" {
		cliChecks()
	} else {
		for seed := int64(6101); seed <= 6120; seed++ {
			check("C12", "duplicate-lost-replies", seed, func() string { return duplicateScenario(seed) })
			check("C2,C3,C4,C5", "partition-scenario", seed, func() string { partition(seed); return "scenario completed" })
			check("C6,C7,C8,C11", "fault-scenario", seed, func() string { faults(seed); return "scenario completed" })
			check("C9,C26", "migration-scenario", seed, func() string { migration(seed); return "scenario completed" })
		}
	}
	if failed > 0 {
		os.Exit(1)
	}
}
