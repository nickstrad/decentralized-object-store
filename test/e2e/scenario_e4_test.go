package e2e

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

func allowedDuringMigration(k *Call) bool {
	return k.Err == nil || errors.Is(k.Err, storage.ErrBusy) || errors.Is(k.Err, storage.ErrUnavailable)
}

func terminal(state string) bool { return state == "completed" || state == "compensated" }

func awaitMigration(c Cluster, b bounds, coord Node, id string, during func() []*Call) (storage.MigrationInfo, []*Call, time.Duration, error) {
	start := c.Now()
	var traffic []*Call
	for {
		if during != nil {
			traffic = append(traffic, during()...)
		}
		st := c.Migration(coord, id)
		if err := c.Wait(st); err != nil {
			return st.Mig, traffic, c.Now() - start, err
		}
		if st.Err == nil && terminal(st.Mig.State) {
			return st.Mig, traffic, c.Now() - start, nil
		}
		if c.Now()-start > b.migrate {
			return st.Mig, traffic, c.Now() - start, failf("migration %s not terminal within %v: state %q, error %v", id, b.migrate, st.Mig.State, st.Err)
		}
		if err := c.Sleep(250 * time.Millisecond); err != nil {
			return st.Mig, traffic, c.Now() - start, err
		}
	}
}

func shardsOutside(c Cluster, bucket, region string) ([]string, error) {
	all, err := collectShards(c, bucket)
	if err != nil {
		return nil, err
	}
	var out []string
	for v, stripes := range all {
		for s, idx := range stripes {
			for i, hs := range idx {
				for _, h := range hs {
					if h.Region != region {
						out = append(out, fmt.Sprintf("%s/%d.%d on %s", v, s, i, h.ID))
					}
				}
			}
		}
	}
	slices.Sort(out)
	return out, nil
}

func routedTo(c Cluster, b bounds, bucket, region string) error {
	var probes []*Call
	for _, n := range upNodes(c) {
		probes = append(probes, c.Put(n, bucket, "probe-"+n.ID, randomBytes(c.Rand(), 100), storage.Condition{}))
	}
	if len(probes) == 0 {
		return failf("no up nodes to probe routing for %s", bucket)
	}
	if err := waitAll(c, probes...); err != nil {
		return err
	}
	all, err := collectShards(c, bucket)
	if err != nil {
		return err
	}
	for _, k := range probes {
		stripes, ok := all[k.Info.Version]
		if !ok || len(stripes) == 0 {
			return failf("probe through %s left no shard", k.At.ID)
		}
		for _, idx := range stripes {
			for _, hs := range idx {
				for _, h := range hs {
					if h.Region != region {
						return failf("a write of %s through %s placed a shard on %s in %s: that node does not route the bucket to %s", bucket, k.At.ID, h.ID, h.Region, region)
					}
				}
			}
		}
	}
	return nil
}

type e4Params struct {
	objects, size   int
	multiStripeSize int
	trafficSize     int
}

type e4Result struct {
	migrate             time.Duration
	traffic, trafficOK  int
	trafficBusy, pruned int
	objects             int
	purge               time.Duration
}

func scenarioE4(c Cluster, b bounds, p e4Params) (e4Result, error) {
	var res e4Result
	const bucket = "e4"
	if p.objects <= 0 {
		return res, failf("e4 requires at least one initial object, got %d", p.objects)
	}
	from, to := c.Regions()[0], c.Regions()[1]
	if err := createBucket(c, b, storage.BucketSpec{Name: bucket, Region: from}); err != nil {
		return res, err
	}
	if err := createBucket(c, b, storage.BucketSpec{Name: "e4bg"}); err != nil {
		return res, err
	}
	var pre []*Call
	for i := range p.objects {
		sz := p.size + i
		if i == 0 && p.multiStripeSize > 0 {
			sz = p.multiStripeSize
		}
		pre = append(pre, c.Put(pick(c, c.Nodes()), bucket, fmt.Sprintf("m%02d", i), randomBytes(c.Rand(), sz), storage.Condition{}))
	}
	if err := waitAll(c, pre...); err != nil {
		return res, err
	}
	if _, err := c.Await("objects replicated", b.converge, c.Converged); err != nil {
		return res, err
	}
	coord := c.Nodes()[2]
	start := c.StartMigration(coord, bucket, to)
	if err := waitAll(c, start); err != nil {
		return res, err
	}
	seq := 0
	trafficBase := p.trafficSize
	if trafficBase <= 0 {
		trafficBase = p.size / 2
	}
	during := func() []*Call {
		seq++
		out := []*Call{
			c.Put(pick(c, c.Nodes()), bucket, fmt.Sprintf("t%03d", seq), randomBytes(c.Rand(), trafficBase+seq), storage.Condition{}),
			c.Get(pick(c, c.Nodes()), bucket, pre[c.Rand().IntN(len(pre))].Key),
		}
		if seq%2 == 0 {
			out = append(out, c.Put(pick(c, c.Nodes()), "e4bg", fmt.Sprintf("b%03d", seq), randomBytes(c.Rand(), 200), storage.Condition{}))
		}
		return out
	}
	mi, traffic, d, err := awaitMigration(c, b, coord, start.Mig.ID, during)
	res.migrate = d
	if err != nil {
		return res, err
	}
	if mi.State != "completed" {
		return res, failf("migration ended %s (%s), want completed", mi.State, mi.Error)
	}
	if err := c.Wait(traffic...); err != nil {
		return res, err
	}
	acked := slices.Clone(pre)
	for _, k := range traffic {
		res.traffic++
		if !allowedDuringMigration(k) {
			return res, failf("%s %s/%s via %s during the migration: %v", k.Op, k.Bucket, k.Key, k.At.ID, k.Err)
		}
		if k.Err == nil {
			res.trafficOK++
			if k.Op == "put" && k.Bucket == bucket {
				acked = append(acked, k)
			}
		} else {
			res.trafficBusy++
		}
	}
	if res.traffic == 0 || res.trafficOK == 0 {
		return res, failf("migration saw no successful live traffic (total=%d, ok=%d, busy=%d)", res.traffic, res.trafficOK, res.trafficBusy)
	}
	if _, err := c.Await("converged after the migration", b.converge, c.Converged); err != nil {
		return res, err
	}
	var left []string
	res.purge, err = c.Await("no shard of the bucket left in "+from, b.migrate, func() (bool, error) {
		var err error
		left, err = shardsOutside(c, bucket, to)
		return len(left) == 0, err
	})
	if err != nil {
		if len(left) > 0 {
			return res, failf("%d shards of %s outside %s after the migration; first %s", len(left), bucket, to, left[0])
		}
		return res, err
	}
	var gets []*Call
	for _, k := range acked {
		gets = append(gets, c.Get(pick(c, c.Nodes()), bucket, k.Key))
	}
	if err := waitAll(c, gets...); err != nil {
		return res, err
	}
	for i, g := range gets {
		if sha256.Sum256(g.Data) != acked[i].Sum {
			return res, failf("get %s after the migration: %d bytes that are not the %d acknowledged", g.Key, len(g.Data), acked[i].Size)
		}
	}
	res.objects = len(acked)
	if res.objects == 0 {
		return res, failf("e4 read back 0 objects after migration")
	}
	if err := routedTo(c, b, bucket, to); err != nil {
		return res, err
	}
	return res, c.Ledger().Wrong()
}
