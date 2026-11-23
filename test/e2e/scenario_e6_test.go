package e2e

import (
	"errors"
	"fmt"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

func incarnationOf(c Cluster, observer, target Node) (uint32, string, error) {
	ms, err := c.Members(observer)
	if err != nil {
		return 0, "", err
	}
	for _, m := range ms {
		if m.ID == target.ID {
			return m.Incarnation, m.State, nil
		}
	}
	return 0, "", nil
}

func shardNames(c Cluster, n Node) ([]string, error) {
	files, err := c.Shards(n)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		if _, _, err := storage.DecodeShard(f.Blob); err == nil {
			out = append(out, f.Name)
		}
	}
	return out, nil
}

func holdsAll(c Cluster, n Node, names []string) (bool, error) {
	ok, err := verifiableOn(c, n, names)
	if err != nil {
		return false, err
	}
	return len(ok) == len(names), nil
}

type e6Params struct {
	perWave, size int
	down          time.Duration
	settle        time.Duration
}

type e6Result struct {
	acked, failed       int
	incBefore, incAfter uint32
	rejoin, catchUp     time.Duration
	refill              time.Duration
	reads               int
	victimShards        int
	wipedShards         int
}

func scenarioE6(c Cluster, b bounds, p e6Params) (e6Result, error) {
	var res e6Result
	const bucket = "e6"
	if p.perWave <= 0 {
		return res, failf("e6 requires perWave > 0, got %d", p.perWave)
	}
	if err := createBucket(c, b, storage.BucketSpec{Name: bucket}); err != nil {
		return res, err
	}
	nodes := c.Nodes()
	victim, wiped, observer := nodes[4], nodes[8], nodes[0]
	var others []Node
	for _, n := range nodes {
		if n != victim && n != wiped {
			others = append(others, n)
		}
	}
	seq := 0
	wave := func(via []Node) []*Call {
		var out []*Call
		for range p.perWave {
			seq++
			out = append(out, c.Put(pick(c, via), bucket, fmt.Sprintf("w%03d", seq), randomBytes(c.Rand(), p.size+seq), storage.Condition{}))
		}
		return out
	}
	var puts []*Call
	first := wave(others)
	if err := waitAll(c, first...); err != nil {
		return res, err
	}
	puts = append(puts, first...)
	if _, err := c.Await("first wave replicated", b.converge, c.Converged); err != nil {
		return res, err
	}
	for range 16 {
		have, err := shardNames(c, victim)
		if err != nil {
			return res, err
		}
		if len(have) > 0 {
			break
		}
		seq++
		extra := c.Put(pick(c, others), bucket, fmt.Sprintf("w%03d", seq), randomBytes(c.Rand(), p.size+seq), storage.Condition{})
		if err := waitAll(c, extra); err != nil {
			return res, err
		}
		puts = append(puts, extra)
		if _, err := c.Await("extra victim-shard write replicated", b.converge, c.Converged); err != nil {
			return res, err
		}
	}
	var err error
	res.incBefore, _, err = incarnationOf(c, observer, victim)
	if err != nil {
		return res, err
	}
	inFlight := wave(others)
	if err := c.Sleep(p.settle); err != nil {
		return res, err
	}
	if err := c.Crash(victim); err != nil {
		return res, err
	}
	if err := c.Wait(inFlight...); err != nil {
		return res, err
	}
	whileDown := wave(others)
	if err := c.Wait(whileDown...); err != nil {
		return res, err
	}
	for _, k := range append(inFlight, whileDown...) {
		switch {
		case k.Err == nil:
			puts = append(puts, k)
		case errors.Is(k.Err, storage.ErrUnavailable):
			res.failed++
		default:
			return res, failf("put %s via %s while %s was killed: %v", k.Key, k.At.ID, victim.ID, k.Err)
		}
	}
	if err := c.Sleep(p.down); err != nil {
		return res, err
	}
	if err := c.Restart(victim); err != nil {
		return res, err
	}
	rejoined := func() (bool, error) {
		observers := 0
		for _, n := range upNodes(c) {
			if n == victim {
				continue
			}
			observers++
			inc, state, err := incarnationOf(c, n, victim)
			if err != nil {
				return false, err
			}
			if state != "alive" || inc <= res.incBefore {
				return false, nil
			}
		}
		if observers == 0 {
			return false, nil
		}
		return c.Converged()
	}
	if res.rejoin, err = c.Await(fmt.Sprintf("%s rejoined with a higher incarnation and caught up its metadata", victim.ID), b.catchUp, rejoined); err != nil {
		return res, err
	}
	res.incAfter, _, err = incarnationOf(c, observer, victim)
	if err != nil {
		return res, err
	}
	if res.incAfter <= res.incBefore {
		return res, failf("%s incarnation did not increase after crash/restart (%d -> %d)", victim.ID, res.incBefore, res.incAfter)
	}
	if es, ok := c.(expectedShards); ok {
		want, err := es.expectedShards(victim)
		if err != nil {
			return res, err
		}
		res.victimShards = len(want)
		if res.victimShards == 0 {
			return res, failf("restarted %s expected 0 shards, so shard catch-up was vacuous", victim.ID)
		}
		d, err := c.Await(fmt.Sprintf("%s holds every shard its manifests place on it", victim.ID), b.catchUp, func() (bool, error) { return holdsAll(c, victim, want) })
		res.catchUp = res.rejoin + d
		if err != nil {
			return res, err
		}
	} else {
		res.catchUp = res.rejoin
	}
	var reads []*Call
	for _, k := range puts {
		reads = append(reads, c.Get(victim, bucket, k.Key))
	}
	if err := waitAll(c, reads...); err != nil {
		return res, err
	}
	for i, g := range reads {
		if g.Info.ETag != puts[i].Info.ETag {
			return res, failf("get %s through the restarted %s returned version %s, acknowledged %s", g.Key, victim.ID, short(g.Info.ETag), short(puts[i].Info.ETag))
		}
	}
	res.reads += len(reads)
	before, err := shardNames(c, wiped)
	if err != nil {
		return res, err
	}
	if len(before) == 0 {
		for _, cand := range nodes {
			if cand == victim || cand == observer {
				continue
			}
			bs, err := shardNames(c, cand)
			if err != nil {
				return res, err
			}
			if len(bs) > 0 {
				wiped, before = cand, bs
				break
			}
		}
	}
	res.wipedShards = len(before)
	if res.wipedShards == 0 {
		return res, failf("no candidate node for wipe held any shards")
	}
	if err := c.Wipe(wiped); err != nil {
		return res, err
	}
	want := before
	if es, ok := c.(expectedShards); ok {
		if want, err = es.expectedShards(wiped); err != nil {
			return res, err
		}
	}
	if len(want) == 0 {
		return res, failf("wiped %s expected 0 shards to refill", wiped.ID)
	}
	if res.refill, err = c.Await(fmt.Sprintf("wiped %s refilled", wiped.ID), b.refill, func() (bool, error) {
		ok, err := c.Converged()
		if !ok || err != nil {
			return false, err
		}
		return holdsAll(c, wiped, want)
	}); err != nil {
		return res, err
	}
	reads = nil
	for _, k := range puts {
		reads = append(reads, c.Get(pick(c, c.Nodes()), bucket, k.Key))
	}
	if err := waitAll(c, reads...); err != nil {
		return res, err
	}
	res.reads += len(reads)
	res.acked = len(puts)
	if res.acked == 0 || res.reads == 0 {
		return res, failf("e6 observed %d acked puts and %d reads, want both > 0", res.acked, res.reads)
	}
	return res, c.Ledger().Wrong()
}
