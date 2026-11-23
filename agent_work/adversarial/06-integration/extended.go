package main

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"github.com/nickstrad/object-store/internal/node"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/saga"
	"github.com/nickstrad/object-store/internal/sim"
	"github.com/nickstrad/object-store/internal/storage"
	"os"
	"strconv"
	"time"
)

type refuseVerify struct{ saga.Step }

func (s refuseVerify) Do(c saga.Call, cb func([]byte, error)) {
	s.Step.Do(c, func(_ []byte, _ error) { cb(nil, fmt.Errorf("check rollback: %w", saga.ErrPermanent)) })
}

type acknowledgement struct {
	key string
	v   occ.Version
}

func covered(v, w occ.Version) bool {
	return v.ID() == w.ID() || v.Clock().Get(w.Dot().Actor) >= w.Dot().Counter
}
func recount(c *cluster, bucket string) {
	for i := range c.ids {
		objs, e := c.n(i).ListObjects(bucket, "", 10000)
		must(e)
		var size uint64
		for _, o := range objs {
			size += uint64(o.Size)
		}
		q, e := c.n(i).Quota(bucket)
		must(e)
		require(q.UsedBytes == size && q.UsedObjects == uint64(len(objs)), "node=%d bucket=%s quota=%d/%d recount=%d/%d", i, bucket, q.UsedBytes, q.UsedObjects, size, len(objs))
	}
}
func trafficScenario(seed int64, rollback bool) string {
	c := configuredCluster(seed, nil, func(cfg *node.Config) {
		if rollback {
			cfg.WrapMigration = func(d saga.Definition) saga.Definition {
				d.Steps = append([]saga.Step(nil), d.Steps...)
				d.Steps[2] = refuseVerify{d.Steps[2]}
				return d
			}
		}
	})
	c.bucket("r0", 0)
	contents := map[string]map[string]bool{}
	var acks []acknowledgement
	record := func(i int, k string) {
		v, ok := c.n(i).OCC().Record(occ.ManifestKey("b", k)).Version()
		require(ok, "ack has no version")
		acks = append(acks, acknowledgement{k, v})
	}
	for j := 0; j < 4; j++ {
		k := fmt.Sprintf("k%d", j)
		_, e := c.put(1, k, []byte("base"), storage.Condition{})
		must(e)
		record(1, k)
		contents[k] = map[string]bool{"base": true}
	}
	c.settle()
	start := c.s.Now()
	pending, attempts, accepted, deletes := 0, 0, 0, 0
	migrationID := ""
	during := 0
	for j := 0; j < 600; j++ {
		j := j
		c.s.At(start.Add(time.Duration(j)*100*time.Millisecond), "non-coordinator traffic", func() {
			if j >= 100 && migrationID != "" {
				st, e := c.n(0).Migration(migrationID)
				must(e)
				if st.State == "completed" || st.State == "compensated" {
					return
				}
			}
			if migrationID != "" {
				st, e := c.n(0).Migration(migrationID)
				must(e)
				if st.State != "completed" && st.State != "compensated" {
					during++
				}
			}
			i := 1 + (j+int(seed))%8
			k := fmt.Sprintf("k%d", j%4)
			if j%3 == 0 {
				k = fmt.Sprintf("new%d", j)
			}
			attempts++
			if j%3 == 2 {
				_, e := c.n(i).DeleteObject("b", k, storage.Condition{})
				if e == nil {
					record(i, k)
					accepted++
					deletes++
				}
				return
			}
			b := []byte(fmt.Sprintf("seed%d-op%d-%s", seed, j, bytes.Repeat([]byte{'x'}, j%127)))
			pending++
			c.n(i).PutObject(storage.PutRequest{Bucket: "b", Key: k, Data: b}, func(_ storage.ObjectInfo, e error) {
				pending--
				if e == nil {
					record(i, k)
					accepted++
					if contents[k] == nil {
						contents[k] = map[string]bool{}
					}
					contents[k][string(b)] = true
				}
			})
		})
	}
	c.run(500 * time.Millisecond)
	mi, e := c.n(0).StartMigration("b", "r1")
	must(e)
	migrationID = mi.ID
	c.run(12 * time.Second)
	c.wait(func() bool { return pending == 0 })
	c.wait(func() bool {
		st, e := c.n(0).Migration(mi.ID)
		return e == nil && (st.State == "completed" || st.State == "compensated")
	})
	c.run(time.Second)
	c.wait(func() bool { return pending == 0 })
	st, e := c.n(0).Migration(mi.ID)
	must(e)
	want := "completed"
	if rollback {
		want = "compensated"
	}
	require(st.State == want, "state=%s want=%s", st.State, want)
	c.s.HealAll()
	c.run(15 * time.Second)
	c.settle()
	require(attempts >= 100 && attempts < 600 && during > 0 && accepted > 0 && deletes > 0, "traffic coverage attempts=%d accepted=%d deletes=%d", attempts, accepted, deletes)
	for _, a := range acks {
		key := occ.ManifestKey("b", a.key)
		v, ok := c.n(0).OCC().Record(key).Version()
		explained := ok && covered(v, a.v)
		for _, l := range c.losses {
			if l.key == key && covered(l.loser, a.v) {
				explained = true
			}
		}
		require(explained, "unexplained acknowledged key=%s occ=%s", a.key, a.v.ID())
	}
	for i := range c.ids {
		objs, e := c.n(i).ListObjects("b", "", 10000)
		must(e)
		for _, o := range objs {
			_, b, e := c.get(i, o.Key)
			must(e)
			require(fmt.Sprintf("%x", sha256.Sum256(b)) == o.ContentSHA, "node=%d key=%s GET differs from winning metadata checksum", i, o.Key)
			require(contents[o.Key][string(b)], "node=%d key=%s bytes not an acknowledged value", i, o.Key)
		}
	}
	recount(c, "b")
	return fmt.Sprintf("%s; traffic attempts=%d during migration=%d on peers 1..8, accepted=%d deletes=%d; %d key-scoped acknowledgements explained, every live key read on nine nodes, quota recounted", st.State, attempts, during, accepted, deletes, len(acks))
}
func quotaManifestScenario(seed int64) string {
	c := configuredCluster(seed, nil, func(cfg *node.Config) { cfg.RecountSettle = 5 * time.Second })
	c.bucket("", 0)
	o, e := c.put(0, "k", bytes.Repeat([]byte{'a'}, 100), storage.Condition{})
	must(e)
	c.run(10 * time.Second)
	c.settle()
	c.losses = nil
	must(c.s.PartitionRegion("r0"))
	c.run(7 * time.Second)
	left, right := 0, 0
	for i, id := range c.ids {
		if c.s.Region(id) == "r0" {
			left = i
		} else {
			right = i
		}
	}
	_, e = c.put(left, "k", bytes.Repeat([]byte{'b'}, 500), storage.Condition{IfMatch: o.ETag})
	must(e)
	c.run(time.Second)
	beforeQuota := c.n(right).OCC().Record(occ.QuotaKey("b")).ETag()
	_, e = c.put(right, "k", bytes.Repeat([]byte{'c'}, 100), storage.Condition{IfMatch: o.ETag})
	must(e)
	require(beforeQuota == c.n(right).OCC().Record(occ.QuotaKey("b")).ETag(), "same-size overwrite changed quota")
	c.s.HealAll()
	c.run(25 * time.Second)
	c.settle()
	recount(c, "b")
	manifestLoss, quotaLoss, beforeManifest := 0, 0, 0
	for _, l := range c.losses {
		kind, _, e := occ.ParseKey(l.key)
		must(e)
		if kind == occ.KindManifest {
			manifestLoss++
		}
		if kind == occ.KindQuota {
			quotaLoss++
			if manifestLoss == 0 {
				beforeManifest++
			}
		}
	}
	require(manifestLoss > 0, "no object discard exercised")
	require(beforeManifest == 0, "quota discard preceded the object discard (%d)", beforeManifest)
	return fmt.Sprintf("manifest discards=%d quota discards before first object discard=0 later quota discards=%d; same-size overwrite did not update quota; nine independent recounts", manifestLoss, quotaLoss)
}
func duelScenario(seed int64, same bool) string {
	c := newCluster(seed)
	c.bucket("r0", 0)
	_, e := c.put(1, "k", []byte("duel"), storage.Condition{})
	must(e)
	bucket2 := "b"
	if !same {
		bucket2 = "other"
		must(c.n(0).CreateBucket(storage.BucketSpec{Name: bucket2, Region: "r0"}))
	}
	c.settle()
	// No simulated event advances between these two begins.
	a, e := c.n(0).StartMigration("b", "r1")
	must(e)
	b, e := c.n(4).StartMigration(bucket2, "r2")
	must(e)
	c.wait(func() bool {
		sa, ea := c.n(0).Migration(a.ID)
		sb, eb := c.n(4).Migration(b.ID)
		return ea == nil && eb == nil && (sa.State == "completed" || sa.State == "compensated") && (sb.State == "completed" || sb.State == "compensated")
	})
	c.run(15 * time.Second)
	c.settle()
	recount(c, "b")
	if !same {
		recount(c, bucket2)
	}
	for i := range c.ids {
		_, got, e := c.get(i, "k")
		must(e)
		require(string(got) == "duel", "wrong bytes node=%d", i)
	}
	sa, e := c.n(0).Migration(a.ID)
	must(e)
	sb, e := c.n(4).Migration(b.ID)
	must(e)
	require(sa.State == "completed" || sb.State == "completed", "neither migration completed")
	if !same {
		require(sa.State == "completed" && sb.State == "completed", "different buckets did not both complete")
	}
	return fmt.Sprintf("same_bucket=%v states=%s/%s; identical metadata, nine exact reads and quota recounts", same, sa.State, sb.State)
}
func extendedChecks() {
	for seed := int64(7201); seed <= 7250; seed++ {
		if !selectedSeed(seed) {
			continue
		}
		for _, rollback := range []bool{false, true} {
			check("C29,C4", "migration-traffic-"+fmt.Sprint(rollback), seed, func() string { return trafficScenario(seed, rollback) })
		}
	}
	for seed := int64(7301); seed <= 7320; seed++ {
		if !selectedSeed(seed) {
			continue
		}
		check("C33,C4", "isolated-writer-return", seed, func() string { return isolatedWriter(seed) })
		check("C4", "object-only-discard-quota", seed, func() string { return quotaManifestScenario(seed) })
		for _, same := range []bool{true, false} {
			check("C30", "simultaneous-migrations-"+fmt.Sprint(same), seed, func() string { return duelScenario(seed, same) })
		}
	}
}

func selectedSeed(seed int64) bool {
	v := os.Getenv("CHECK_SEED")
	if v == "" {
		return true
	}
	n, e := strconv.ParseInt(v, 10, 64)
	must(e)
	return seed == n
}
func isolatedWriter(seed int64) string {
	c := newCluster(seed)
	c.bucket("r0", 0)
	writer := 1
	key := "hidden"
	done := false
	var putErr error
	c.n(writer).PutObject(storage.PutRequest{Bucket: "b", Key: key, Data: []byte("held only by isolated writer")}, func(_ storage.ObjectInfo, e error) { putErr = e; done = true })
	// Shard requests have already left; prevent subsequent metadata datagrams.
	for _, id := range c.ids {
		if id != c.ids[writer] {
			must(c.s.SetLinkLoss(c.ids[writer], id, sim.PPM))
		}
	}
	c.wait(func() bool { return done })
	must(putErr)
	v, ok := c.n(writer).OCC().Record(occ.ManifestKey("b", key)).Version()
	require(ok, "no writer version")
	must(c.s.Isolate(c.ids[writer]))
	paused := seed%2 == 0
	if paused {
		must(c.s.Pause(c.ids[writer]))
	}
	c.run(7 * time.Second)
	for i := range c.ids {
		if i != writer {
			require(!c.n(i).OCC().Record(occ.ManifestKey("b", key)).Live(), "fixture leaked hidden write node=%d", i)
		}
	}
	mi, e := c.n(0).StartMigration("b", "r1")
	must(e)
	c.wait(func() bool { st, e := c.n(0).Migration(mi.ID); return e == nil && st.State == "completed" })
	if paused {
		must(c.s.Resume(c.ids[writer]))
	}
	c.s.HealAll()
	c.run(15 * time.Second)
	c.settle()
	for i := range c.ids {
		_, b, e := c.get(i, key)
		must(e)
		require(string(b) == "held only by isolated writer", "node=%d wrong hidden bytes", i)
		cur, ok := c.n(i).OCC().Record(occ.ManifestKey("b", key)).Version()
		require(ok && covered(cur, v), "hidden acknowledged version unexplained")
	}
	recount(c, "b")
	return fmt.Sprintf("paused=%v; write invisible to eight peers during completed migration; all nine read exact bytes after return; quota recounted", paused)
}
