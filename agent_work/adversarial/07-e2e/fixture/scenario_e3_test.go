package e2e

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

type movedCounter interface {
	storageBytesMoved() uint64
}

type expectedShards interface {
	expectedShards(n Node) ([]string, error)
}

func sumCounter(c Cluster, name string) (uint64, error) {
	var sum uint64
	for _, n := range upNodes(c) {
		h, err := c.Health(n)
		if err != nil {
			return 0, err
		}
		sum += counter(h, name)
	}
	return sum, nil
}

type shardAt struct {
	node Node
	name string
	size int
}

func verifiableOn(c Cluster, n Node, names []string) (map[string]bool, error) {
	files, err := c.Shards(n)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, nm := range names {
		want[nm] = true
	}
	ok := map[string]bool{}
	for _, f := range files {
		if !want[f.Name] {
			continue
		}
		if _, _, err := storage.DecodeShard(f.Blob); err == nil {
			ok[f.Name] = true
		}
	}
	return ok, nil
}

type e3Params struct {
	objects, damaged int
	size             int
}

type e3Result struct {
	shardsRotted, bucketBytes, damagedBytes int
	rebuiltBytes, movedBytes                uint64
	repairUnderReads, repair                time.Duration
	reads                                   int
}

func pickRot(c Cluster, bucket string, all map[string]map[uint32]map[uint16][]Node, puts []*Call) ([]shardAt, error) {
	m := c.Layout().M
	var rotted []shardAt
	for _, k := range puts {
		stripes := all[k.Info.Version]
		var ss []uint32
		for s := range stripes {
			ss = append(ss, s)
		}
		slices.Sort(ss)
		for _, s := range ss {
			idx := stripes[s]
			var is []uint16
			for i := range idx {
				is = append(is, i)
			}
			slices.Sort(is)
			want := 1 + c.Rand().IntN(m)
			for _, j := range c.Rand().Perm(len(is))[:min(want, len(is))] {
				h := idx[is[j]][0]
				name := storage.ShardID{Bucket: bucket, Version: mustVersionID(k.Info.Version), Stripe: s, Index: is[j]}.BlobName()
				files, err := c.Shards(h)
				if err != nil {
					return nil, err
				}
				for _, f := range files {
					if f.Name == name {
						rotted = append(rotted, shardAt{node: h, name: f.Name, size: len(f.Blob)})
					}
				}
			}
		}
	}
	return rotted, nil
}

func mustVersionID(s string) storage.VersionID {
	v, _ := strconv.ParseUint(s, 16, 64)
	return storage.VersionID(v)
}

func rotAndAwait(c Cluster, b bounds, rotted []shardAt) (time.Duration, int, error) {
	damaged := 0
	for _, r := range rotted {
		if err := c.Rot(r.node, r.name); err != nil {
			return 0, 0, err
		}
		damaged += r.size
	}
	byNode := map[Node][]string{}
	for _, r := range rotted {
		byNode[r.node] = append(byNode[r.node], r.name)
	}
	start := c.Now()
	_, err := c.Await("every rotted shard rebuilt", b.repair, func() (bool, error) {
		for n, names := range byNode {
			ok, err := verifiableOn(c, n, names)
			if err != nil {
				return false, err
			}
			if len(ok) < len(names) {
				return false, nil
			}
		}
		return true, nil
	})
	return c.Now() - start, damaged, err
}

func scenarioE3(c Cluster, b bounds, p e3Params) (e3Result, error) {
	var res e3Result
	const bucket = "e3"
	if err := createBucket(c, b, storage.BucketSpec{Name: bucket}); err != nil {
		return res, err
	}
	var puts []*Call
	for i := range p.objects {
		puts = append(puts, c.Put(pick(c, c.Nodes()), bucket, fmt.Sprintf("r%02d", i), randomBytes(c.Rand(), p.size+i), storage.Condition{}))
	}
	if err := waitAll(c, puts...); err != nil {
		return res, err
	}
	if _, err := c.Await("objects replicated", b.converge, c.Converged); err != nil {
		return res, err
	}
	all, err := collectShards(c, bucket)
	if err != nil {
		return res, err
	}
	for _, n := range c.Nodes() {
		files, err := c.Shards(n)
		if err != nil {
			return res, err
		}
		for _, f := range files {
			if sid, err := storage.ParseBlobName(f.Name); err == nil && sid.Bucket == bucket {
				res.bucketBytes += len(f.Blob)
			}
		}
	}
	first, err := pickRot(c, bucket, all, puts[:p.damaged])
	if err != nil {
		return res, err
	}
	for _, r := range first {
		if err := c.Rot(r.node, r.name); err != nil {
			return res, err
		}
	}
	var gets []*Call
	for _, k := range puts {
		gets = append(gets, c.Get(pick(c, c.Nodes()), bucket, k.Key))
	}
	if err := waitAll(c, gets...); err != nil {
		return res, err
	}
	for i, g := range gets {
		if g.Info.ETag != puts[i].Info.ETag {
			return res, failf("get %s after bit-rot returned version %s, wrote %s", g.Key, short(g.Info.ETag), short(puts[i].Info.ETag))
		}
	}
	res.reads = len(gets)
	byNode := map[Node][]string{}
	for _, r := range first {
		byNode[r.node] = append(byNode[r.node], r.name)
	}
	if res.repairUnderReads, err = c.Await("the shards rotted under reads rebuilt", b.repair, func() (bool, error) {
		for n, names := range byNode {
			ok, err := verifiableOn(c, n, names)
			if err != nil || len(ok) < len(names) {
				return false, err
			}
		}
		return true, nil
	}); err != nil {
		return res, err
	}
	second, err := pickRot(c, bucket, all, puts[p.damaged:2*p.damaged])
	if err != nil {
		return res, err
	}
	rebuilt0, err := sumCounter(c, "rebuilt_bytes")
	if err != nil {
		return res, err
	}
	mc, hasMoved := c.(movedCounter)
	var moved0 uint64
	if hasMoved {
		moved0 = mc.storageBytesMoved()
	}
	res.repair, res.damagedBytes, err = rotAndAwait(c, b, second)
	res.shardsRotted = len(first) + len(second)
	if err != nil {
		return res, err
	}
	rebuilt1, err := sumCounter(c, "rebuilt_bytes")
	if err != nil {
		return res, err
	}
	res.rebuiltBytes = rebuilt1 - rebuilt0
	if hasMoved {
		res.movedBytes = mc.storageBytesMoved() - moved0
	}
	if res.rebuiltBytes < uint64(res.damagedBytes)*9/10 || res.rebuiltBytes > uint64(2*res.damagedBytes) {
		return res, failf("repair rebuilt %d bytes for %d damaged bytes", res.rebuiltBytes, res.damagedBytes)
	}
	layout := c.Layout()
	limit := uint64((layout.K+layout.M)*res.damagedBytes) + 64<<10
	if limit*2 > uint64(res.bucketBytes) {
		return res, failf("the bucket (%d bytes) is too small to tell repair traffic for %d damaged bytes from a bucket copy", res.bucketBytes, res.damagedBytes)
	}
	if hasMoved && res.movedBytes > limit {
		return res, failf("repair moved %d shard bytes for %d damaged bytes (limit %d) in a %d-byte bucket", res.movedBytes, res.damagedBytes, limit, res.bucketBytes)
	}
	return res, c.Ledger().Wrong()
}
