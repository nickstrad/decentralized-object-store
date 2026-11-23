package e2e

import (
	"fmt"
	"time"

	"github.com/nickstrad/object-store/internal/storage"
)

type bounds struct {
	converge   time.Duration
	repair     time.Duration
	migrate    time.Duration
	catchUp    time.Duration
	refill     time.Duration
	readBatch  int
	writeBatch int
}

func waitAll(c Cluster, calls ...*Call) error {
	if err := c.Wait(calls...); err != nil {
		return err
	}
	for _, k := range calls {
		if k.Err != nil {
			return failf("%s %s/%s via %s: %v", k.Op, k.Bucket, k.Key, k.At.ID, k.Err)
		}
	}
	return nil
}

func createBucket(c Cluster, b bounds, spec storage.BucketSpec) error {
	if err := waitAll(c, c.CreateBucket(c.Nodes()[0], spec)); err != nil {
		return err
	}
	_, err := c.Await("bucket "+spec.Name+" known everywhere", b.converge, c.Converged)
	return err
}

func collectShards(c Cluster, bucket string) (map[string]map[uint32]map[uint16][]Node, error) {
	out := map[string]map[uint32]map[uint16][]Node{}
	for _, n := range c.Nodes() {
		files, err := c.Shards(n)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			sid, err := storage.ParseBlobName(f.Name)
			if err != nil || sid.Bucket != bucket {
				continue
			}
			v := sid.Version.String()
			if out[v] == nil {
				out[v] = map[uint32]map[uint16][]Node{}
			}
			if out[v][sid.Stripe] == nil {
				out[v][sid.Stripe] = map[uint16][]Node{}
			}
			out[v][sid.Stripe][sid.Index] = append(out[v][sid.Stripe][sid.Index], n)
		}
	}
	return out, nil
}

func checkSpread(c Cluster, bucket string, versions []string) (int, error) {
	all, err := collectShards(c, bucket)
	if err != nil {
		return 0, err
	}
	m := c.Layout().M
	checked := 0
	for _, v := range versions {
		stripes, ok := all[v]
		if !ok {
			return checked, failf("no shard of %s version %s on any disk", bucket, v)
		}
		for s, idx := range stripes {
			per := map[string]int{}
			for _, holders := range idx {
				for _, h := range holders {
					per[h.Region]++
				}
			}
			for r, cnt := range per {
				if cnt > m {
					return checked, failf("%s version %s stripe %d holds %d shards in region %s, more than m=%d", bucket, v, s, cnt, r, m)
				}
			}
			checked++
		}
	}
	return checked, nil
}

type e1Params struct {
	sizes []int
}

type e1Result struct {
	objects, reads, stripes int
	putTime, getTime        time.Duration
	getLatencies            []time.Duration
}

func scenarioE1(c Cluster, b bounds, p e1Params) (e1Result, error) {
	var res e1Result
	const bucket = "e1"
	if err := createBucket(c, b, storage.BucketSpec{Name: bucket}); err != nil {
		return res, err
	}
	var puts, batch []*Call
	for _, r := range c.Regions() {
		for i, size := range p.sizes {
			via := pick(c, inRegion(c, r))
			batch = append(batch, c.Put(via, bucket, fmt.Sprintf("%s-%02d-%d", r, i, size), randomBytes(c.Rand(), size), storage.Condition{}))
			if b.writeBatch > 0 && len(batch) >= b.writeBatch {
				if err := waitAll(c, batch...); err != nil {
					return res, err
				}
				puts, batch = append(puts, batch...), nil
			}
		}
	}
	if err := waitAll(c, batch...); err != nil {
		return res, err
	}
	puts = append(puts, batch...)
	for _, k := range puts {
		res.putTime = max(res.putTime, k.Ended-k.Began)
	}
	res.objects = len(puts)
	if _, err := c.Await("objects replicated", b.converge, c.Converged); err != nil {
		return res, err
	}
	var versions []string
	for _, k := range puts {
		if k.Size > 0 {
			versions = append(versions, k.Info.Version)
		}
		var readers []Node
		for _, n := range c.Nodes() {
			if n != k.At {
				readers = append(readers, n)
			}
		}
		for len(readers) > 0 {
			batch := readers[:min(len(readers), max(b.readBatch, 1))]
			readers = readers[len(batch):]
			var gets []*Call
			for _, n := range batch {
				gets = append(gets, c.Get(n, bucket, k.Key))
			}
			if err := waitAll(c, gets...); err != nil {
				return res, err
			}
			for _, g := range gets {
				if g.Info.ETag != k.Info.ETag || len(g.Data) != k.Size {
					return res, failf("get %s via %s: version %s, %d bytes; wrote version %s, %d bytes", k.Key, g.At.ID, short(g.Info.ETag), len(g.Data), short(k.Info.ETag), k.Size)
				}
				res.getTime = max(res.getTime, g.Ended-g.Began)
				res.getLatencies = append(res.getLatencies, g.Ended-g.Began)
				res.reads++
			}
		}
	}
	if err := c.Ledger().Wrong(); err != nil {
		return res, err
	}
	n, err := checkSpread(c, bucket, versions)
	res.stripes = n
	return res, err
}
