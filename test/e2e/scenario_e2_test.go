package e2e

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

func etagOf(k *Call) string {
	switch k.Op {
	case "put", "delete":
		return k.Info.ETag
	case "tag":
		return k.Tag.ETag
	case "acl":
		return k.ACL.ETag
	}
	return ""
}

type contest struct {
	what   string
	writes []*Call
	read   func(at Node) *Call
	cur    func(k *Call) (string, bool)
}

type contestResult struct {
	acked, current, discarded, unacked int
}

type discardSummary struct {
	losers map[string]bool
	most   uint64
	total  uint64
}

func discardLog(c Cluster) (discardSummary, error) {
	ds := discardSummary{losers: map[string]bool{}}
	ups := upNodes(c)
	if len(ups) == 0 {
		return ds, failf("no up nodes to read discard logs")
	}
	for _, n := range ups {
		h, err := c.Health(n)
		if err != nil {
			return ds, err
		}
		wantTail := min(h.Discards, uint64(occ.DiscardTailLen))
		if uint64(len(h.RecentDiscards)) != wantTail {
			return ds, failf("node %s reports Health.Discards=%d with %d RecentDiscards (want %d)", n.ID, h.Discards, len(h.RecentDiscards), wantTail)
		}
		for _, d := range h.RecentDiscards {
			ds.losers[d.Loser] = true
		}
		ds.most = max(ds.most, h.Discards)
		ds.total += h.Discards
	}
	return ds, nil
}

func lostObjects(c Cluster) (uint64, error) {
	var most uint64
	for _, n := range upNodes(c) {
		h, err := c.Health(n)
		if err != nil {
			return 0, err
		}
		most = max(most, counter(h, "lost_objects"))
	}
	return most, nil
}

func liveState(s string) bool { return s == "alive" || s == "suspect" }

func splitViews(c Cluster, sides ...[]Node) func() (bool, error) {
	return func() (bool, error) {
		for _, side := range sides {
			in := map[string]bool{}
			for _, n := range side {
				in[n.ID] = true
			}
			for _, n := range side {
				if !c.Up(n) {
					continue
				}
				ms, err := c.Members(n)
				if err != nil {
					return false, err
				}
				for _, m := range ms {
					if liveState(m.State) && !in[m.ID] {
						return false, nil
					}
				}
			}
		}
		return true, nil
	}
}

func checkContests(c Cluster, cs []contest) (contestResult, error) {
	var res contestResult
	if len(cs) == 0 {
		return res, failf("no contested writes to check")
	}
	ds, err := discardLog(c)
	if err != nil {
		return res, err
	}
	reads := make([]*Call, len(cs))
	for i, ct := range cs {
		reads[i] = ct.read(pick(c, c.Nodes()))
	}
	if err := c.Wait(reads...); err != nil {
		return res, err
	}
	for i, ct := range cs {
		cur, ok := ct.cur(reads[i])
		for _, w := range ct.writes {
			if w.Err != nil {
				res.unacked++
				continue
			}
			res.acked++
			switch tag := etagOf(w); {
			case ok && tag == cur:
				res.current++
			case ds.losers[tag]:
				res.discarded++
			case ds.most > uint64(occ.DiscardTailLen):
				return res, failf("%s: acknowledged write %s via %s is not current and the discard tails (%d discards on one node) overflowed, so the check cannot finish", ct.what, short(tag), w.At.ID, ds.most)
			default:
				return res, failf("%s: acknowledged write %s via %s is not current (current %s) and no node logged it as a discarded loser", ct.what, short(tag), w.At.ID, short(cur))
			}
		}
	}
	if res.acked == 0 || res.current == 0 {
		return res, failf("contest check observed %d acked and %d current writes, want both > 0", res.acked, res.current)
	}
	return res, nil
}

func tagContest(c Cluster, bucket, key, name string, writes ...*Call) contest {
	return contest{
		what:   fmt.Sprintf("tag %s on %s/%s", name, bucket, key),
		writes: writes,
		read:   func(at Node) *Call { return c.Tags(at, bucket, key) },
		cur: func(k *Call) (string, bool) {
			for _, t := range k.Tags {
				if t.Name == name {
					return t.ETag, k.Err == nil
				}
			}
			return "", false
		},
	}
}

func aclContest(c Cluster, bucket, principal string, writes ...*Call) contest {
	return contest{
		what:   fmt.Sprintf("acl %s on %s", principal, bucket),
		writes: writes,
		read:   func(at Node) *Call { return c.ACL(at, bucket, principal) },
		cur:    func(k *Call) (string, bool) { return k.ACL.ETag, k.Err == nil },
	}
}

func objectContest(c Cluster, bucket, key string, writes ...*Call) contest {
	return contest{
		what:   fmt.Sprintf("object %s/%s", bucket, key),
		writes: writes,
		read:   func(at Node) *Call { return c.Get(at, bucket, key) },
		cur:    func(k *Call) (string, bool) { return k.Info.ETag, k.Err == nil },
	}
}

func allowedDuringPartition(k *Call) bool {
	return k.Err == nil || errors.Is(k.Err, storage.ErrUnavailable)
}

func allowedReadDuringPartition(k *Call) bool {
	return allowedDuringPartition(k) || errors.Is(k.Err, occ.ErrNotFound) || errors.Is(k.Err, storage.ErrNotFound)
}

func sideCanPut(side []Node, l storage.Layout) bool {
	return len(side) >= l.K+l.M
}

func sideHasKShards(all map[string]map[uint32]map[uint16][]Node, version string, side []Node, k int) bool {
	stripes, ok := all[version]
	if !ok || len(stripes) == 0 {
		return false
	}
	in := map[string]bool{}
	for _, n := range side {
		in[n.ID] = true
	}
	for _, idx := range stripes {
		have := 0
		for _, holders := range idx {
			for _, h := range holders {
				if in[h.ID] {
					have++
					break
				}
			}
		}
		if have < k {
			return false
		}
	}
	return true
}

type e2Params struct {
	objects, size int
	detect        time.Duration
	keepCut       time.Duration
}

type e2Result struct {
	puts, putsAcked, putsUnavailable                      int
	postPutsRequired, earlyGetsRequired, postGetsRequired int
	gets, getsOK                                          int
	meta                                                  int
	contested                                             contestResult
	discards                                              uint64
	lost                                                  uint64
	converge, detect                                      time.Duration
}

func scenarioE2(c Cluster, b bounds, p e2Params) (e2Result, error) {
	var res e2Result
	var err error
	const bucket = "e2"
	if p.objects < 2 {
		return res, failf("e2 requires at least 2 objects, got %d", p.objects)
	}
	l := c.Layout()
	if err := createBucket(c, b, storage.BucketSpec{Name: bucket}); err != nil {
		return res, err
	}
	var preObjs []*Call
	for i := range p.objects {
		preObjs = append(preObjs, c.Put(pick(c, c.Nodes()), bucket, fmt.Sprintf("k%02d", i), randomBytes(c.Rand(), p.size), storage.Condition{}))
	}
	if err := waitAll(c, preObjs...); err != nil {
		return res, err
	}
	if _, err := c.Await("objects replicated", b.converge, c.Converged); err != nil {
		return res, err
	}
	var pre []*Call
	for i := range p.objects {
		pre = append(pre, c.PutTag(pick(c, c.Nodes()), bucket, fmt.Sprintf("k%02d", i), "owner", "pre", storage.Condition{}))
	}
	pre = append(pre, c.PutACL(pick(c, c.Nodes()), bucket, "alice", "r", storage.Condition{}))
	if err := waitAll(c, pre...); err != nil {
		return res, err
	}
	if _, err := c.Await("writes before the cut replicated", b.converge, c.Converged); err != nil {
		return res, err
	}
	preShards, err := collectShards(c, bucket)
	if err != nil {
		return res, err
	}
	cut := c.Regions()[0]
	sideA, sideB := inRegion(c, cut), outside(c, cut)
	if err := c.PartitionRegion(cut); err != nil {
		return res, err
	}
	type earlyGetCheck struct {
		call   *Call
		mustOK bool
	}
	var contests []contest
	var earlyPuts, earlyGets, meta []*Call
	var earlyGetChecks []earlyGetCheck
	for i := range p.objects / 2 {
		key := fmt.Sprintf("k%02d", i)
		ta := c.PutTag(pick(c, sideA), bucket, key, "early", "a", storage.Condition{})
		tb := c.PutTag(pick(c, sideB), bucket, key, "early", "b", storage.Condition{})
		meta = append(meta, ta, tb)
		contests = append(contests, tagContest(c, bucket, key, "early", ta, tb))
		ea := c.Put(pick(c, sideA), bucket, fmt.Sprintf("ea%02d", i), randomBytes(c.Rand(), p.size), storage.Condition{})
		eb := c.Put(pick(c, sideB), bucket, fmt.Sprintf("eb%02d", i), randomBytes(c.Rand(), p.size), storage.Condition{})
		earlyPuts = append(earlyPuts, ea, eb)
		contests = append(contests, objectContest(c, bucket, ea.Key, ea), objectContest(c, bucket, eb.Key, eb))
		ga := c.Get(pick(c, sideA), bucket, key)
		gb := c.Get(pick(c, sideB), bucket, key)
		earlyGets = append(earlyGets, ga, gb)
		earlyGetChecks = append(earlyGetChecks,
			earlyGetCheck{call: ga, mustOK: sideHasKShards(preShards, preObjs[i].Info.Version, sideA, l.K)},
			earlyGetCheck{call: gb, mustOK: sideHasKShards(preShards, preObjs[i].Info.Version, sideB, l.K)},
		)
	}
	if res.detect, err = c.Await("each side of the cut sees only itself", b.converge, splitViews(c, sideA, sideB)); err != nil {
		return res, err
	}
	if err := c.Sleep(p.detect); err != nil {
		return res, err
	}
	type preGetCheck struct {
		call   *Call
		want   *Call
		mustOK bool
	}
	var postPreChecks []preGetCheck
	var postPreCalls []*Call
	for i, po := range preObjs {
		key := fmt.Sprintf("k%02d", i)
		for _, side := range [][]Node{sideA, sideB} {
			canRead := sideHasKShards(preShards, po.Info.Version, side, l.K)
			g := c.Get(pick(c, side), bucket, key)
			postPreChecks = append(postPreChecks, preGetCheck{call: g, want: po, mustOK: canRead})
			postPreCalls = append(postPreCalls, g)
		}
	}
	if err := c.Wait(postPreCalls...); err != nil {
		return res, err
	}
	for _, cg := range postPreChecks {
		res.gets++
		if cg.mustOK {
			res.postGetsRequired++
			if cg.call.Err != nil {
				return res, failf("post-split get %s/%s via %s must succeed (side holds >= k=%d shards): %v", cg.call.Bucket, cg.call.Key, cg.call.At.ID, l.K, cg.call.Err)
			}
			if sha256.Sum256(cg.call.Data) != cg.want.Sum || cg.call.Info.ETag != cg.want.Info.ETag {
				return res, failf("post-split get %s/%s via %s returned wrong bytes or etag %s (want %s)", cg.call.Bucket, cg.call.Key, cg.call.At.ID, short(cg.call.Info.ETag), short(cg.want.Info.ETag))
			}
			res.getsOK++
		} else if !errors.Is(cg.call.Err, storage.ErrUnavailable) {
			return res, failf("post-split get %s/%s via %s with < k=%d shards returned %v, want unavailable", cg.call.Bucket, cg.call.Key, cg.call.At.ID, l.K, cg.call.Err)
		}
	}
	type putCheck struct {
		call   *Call
		side   []Node
		mustOK bool
	}
	type getCheck struct {
		call            *Call
		mustOK          bool
		mustUnavailable bool
	}
	canPutA, canPutB := sideCanPut(sideA, l), sideCanPut(sideB, l)
	var postPuts []putCheck
	var postGets []getCheck
	var postPutCalls, postGetCalls []*Call
	for i := range p.objects {
		key := fmt.Sprintf("k%02d", i)
		ta := c.PutTag(pick(c, sideA), bucket, key, "owner", "a", storage.Condition{})
		tb := c.PutTag(pick(c, sideB), bucket, key, "owner", "b", storage.Condition{})
		meta = append(meta, ta, tb)
		contests = append(contests, tagContest(c, bucket, key, "owner", ta, tb))
		if i%2 == 0 {
			pa := c.Put(pick(c, sideA), bucket, key, randomBytes(c.Rand(), p.size), storage.Condition{})
			pb := c.Put(pick(c, sideB), bucket, key, randomBytes(c.Rand(), p.size+1), storage.Condition{})
			postPuts = append(postPuts, putCheck{call: pa, side: sideA, mustOK: canPutA}, putCheck{call: pb, side: sideB, mustOK: canPutB})
			postPutCalls = append(postPutCalls, pa, pb)
			contests = append(contests, objectContest(c, bucket, key, pa, pb))
		} else {
			side, mustOK := sideA, canPutA
			if i%4 == 1 {
				side, mustOK = sideB, canPutB
			}
			pn := c.Put(pick(c, side), bucket, fmt.Sprintf("n%02d", i), randomBytes(c.Rand(), p.size), storage.Condition{})
			postPuts = append(postPuts, putCheck{call: pn, side: side, mustOK: mustOK})
			postPutCalls = append(postPutCalls, pn)
			contests = append(contests, objectContest(c, bucket, pn.Key, pn))
		}
		ga := c.Get(pick(c, sideA), bucket, key)
		gb := c.Get(pick(c, sideB), bucket, key)
		canReadA := sideHasKShards(preShards, preObjs[i].Info.Version, sideA, l.K)
		canReadB := sideHasKShards(preShards, preObjs[i].Info.Version, sideB, l.K)
		overA := i%2 == 0 && canPutA
		overB := i%2 == 0 && canPutB
		postGets = append(postGets,
			getCheck{call: ga, mustOK: canReadA, mustUnavailable: !canReadA && !overA},
			getCheck{call: gb, mustOK: canReadB, mustUnavailable: !canReadB && !overB},
		)
		postGetCalls = append(postGetCalls, ga, gb)
	}
	aa := c.PutACL(pick(c, sideA), bucket, "alice", "rw", storage.Condition{})
	ab := c.PutACL(pick(c, sideB), bucket, "alice", "ra", storage.Condition{})
	meta = append(meta, aa, ab)
	contests = append(contests, aclContest(c, bucket, "alice", aa, ab))
	if err := c.Wait(slices.Concat(earlyPuts, postPutCalls, earlyGets, postGetCalls, meta)...); err != nil {
		return res, err
	}
	for _, k := range meta {
		if k.Err != nil {
			return res, failf("metadata write %s %s/%s via %s refused during the partition: %v", k.Op, k.Bucket, k.Key, k.At.ID, k.Err)
		}
	}
	res.meta = len(meta)
	for _, k := range earlyPuts {
		res.puts++
		switch {
		case k.Err == nil:
			res.putsAcked++
		case allowedDuringPartition(k):
			res.putsUnavailable++
		default:
			return res, failf("put %s/%s via %s during the partition: %v (want success or unavailable)", k.Bucket, k.Key, k.At.ID, k.Err)
		}
	}
	for _, cp := range postPuts {
		res.puts++
		if cp.mustOK {
			res.postPutsRequired++
			if cp.call.Err != nil {
				return res, failf("post-split put %s/%s via %s must succeed (side size %d >= k+m=%d): %v", cp.call.Bucket, cp.call.Key, cp.call.At.ID, len(cp.side), l.K+l.M, cp.call.Err)
			}
			res.putsAcked++
		} else {
			if !errors.Is(cp.call.Err, storage.ErrUnavailable) {
				return res, failf("post-split put %s/%s via %s (side size %d < k+m=%d) returned %v, want unavailable", cp.call.Bucket, cp.call.Key, cp.call.At.ID, len(cp.side), l.K+l.M, cp.call.Err)
			}
			res.putsUnavailable++
		}
	}
	for _, eg := range earlyGetChecks {
		res.gets++
		if eg.mustOK {
			res.earlyGetsRequired++
			if eg.call.Err != nil {
				return res, failf("early partition get %s/%s via %s must succeed (side holds >= k=%d shards): %v", eg.call.Bucket, eg.call.Key, eg.call.At.ID, l.K, eg.call.Err)
			}
			res.getsOK++
			continue
		}
		if !allowedReadDuringPartition(eg.call) {
			return res, failf("get %s/%s via %s during the partition: %v (want the bytes, unavailable or not found)", eg.call.Bucket, eg.call.Key, eg.call.At.ID, eg.call.Err)
		}
		if eg.call.Err == nil {
			res.getsOK++
		}
	}
	for _, cg := range postGets {
		res.gets++
		switch {
		case cg.mustOK:
			res.postGetsRequired++
			if cg.call.Err != nil {
				return res, failf("post-split get %s/%s via %s must succeed (surviving shards allow read): %v", cg.call.Bucket, cg.call.Key, cg.call.At.ID, cg.call.Err)
			}
			res.getsOK++
		case cg.mustUnavailable:
			if !errors.Is(cg.call.Err, storage.ErrUnavailable) {
				return res, failf("post-split get %s/%s via %s with < k=%d shards returned %v, want unavailable", cg.call.Bucket, cg.call.Key, cg.call.At.ID, l.K, cg.call.Err)
			}
		default:
			if !allowedReadDuringPartition(cg.call) {
				return res, failf("get %s/%s via %s during the partition: %v (want the bytes, unavailable or not found)", cg.call.Bucket, cg.call.Key, cg.call.At.ID, cg.call.Err)
			}
			if cg.call.Err == nil {
				res.getsOK++
			}
		}
	}
	if res.meta == 0 {
		return res, failf("partition phase ran 0 metadata writes")
	}
	if res.postGetsRequired == 0 || res.getsOK == 0 {
		return res, failf("partition phase saw no required object read succeed (required=%d, ok=%d)", res.postGetsRequired, res.getsOK)
	}
	if res.postPutsRequired == 0 || res.putsAcked == 0 {
		return res, failf("partition phase saw no required object write succeed (required=%d, acked=%d)", res.postPutsRequired, res.putsAcked)
	}
	if err := c.Sleep(p.keepCut); err != nil {
		return res, err
	}
	if err := c.Heal(); err != nil {
		return res, err
	}
	res.converge, err = c.Await("identical membership, metadata and Merkle roots after the heal", b.converge, c.Converged)
	if err != nil {
		return res, err
	}
	res.contested, err = checkContests(c, contests)
	if err != nil {
		return res, err
	}
	ds, err := discardLog(c)
	if err != nil {
		return res, err
	}
	res.discards = ds.most
	res.lost, err = lostObjects(c)
	if err != nil {
		return res, err
	}
	if res.lost != 0 {
		return res, failf("%d objects declared lost after a partition shorter than the orphan grace period", res.lost)
	}
	if res.contested.discarded == 0 {
		return res, failf("no acknowledged write was discarded; the partition produced no conflict to check")
	}
	if res.discards == 0 || ds.total < uint64(res.contested.discarded) {
		return res, failf("discard counter inconsistent with contested discards: max_node=%d sum=%d contested_discarded=%d", res.discards, ds.total, res.contested.discarded)
	}
	return res, c.Ledger().Wrong()
}

type e8Params struct {
	objects int
	detect  time.Duration
}

type e8Result struct {
	conditional, refused int
	contested            contestResult
	discards             uint64
	converge             time.Duration
	staleRefused         int
}

func scenarioE8(c Cluster, b bounds, p e8Params) (e8Result, error) {
	var res e8Result
	const bucket = "e8"
	if p.objects <= 0 {
		return res, failf("e8 requires at least 1 object, got %d", p.objects)
	}
	if err := createBucket(c, b, storage.BucketSpec{Name: bucket, QuotaBytes: 1 << 30, QuotaObjects: 1000}); err != nil {
		return res, err
	}
	var objs []*Call
	for i := range p.objects {
		objs = append(objs, c.Put(pick(c, c.Nodes()), bucket, fmt.Sprintf("d%02d", i), randomBytes(c.Rand(), 64+i), storage.Condition{}))
	}
	if err := waitAll(c, objs...); err != nil {
		return res, err
	}
	if _, err := c.Await("objects replicated", b.converge, c.Converged); err != nil {
		return res, err
	}
	pre := objs
	for i := range p.objects {
		key := fmt.Sprintf("d%02d", i)
		pre = append(pre, c.PutTag(pick(c, c.Nodes()), bucket, key, "state", "draft", storage.Condition{}))
		pre = append(pre, c.PutTag(pick(c, c.Nodes()), bucket, key, "editor", "none", storage.Condition{}))
	}
	pre = append(pre, c.PutACL(pick(c, c.Nodes()), bucket, "bob", "r", storage.Condition{}), c.PutACL(pick(c, c.Nodes()), bucket, "carol", "r", storage.Condition{}))
	if err := waitAll(c, pre...); err != nil {
		return res, err
	}
	if _, err := c.Await("edits before the cut replicated", b.converge, c.Converged); err != nil {
		return res, err
	}
	etag := map[string]string{}
	for _, k := range pre {
		switch k.Op {
		case "tag":
			etag["tag|"+k.Key+"|"+k.Tag.Name] = k.Tag.ETag
		case "acl":
			etag["acl|"+k.Key] = k.ACL.ETag
		case "put":
			etag["obj|"+k.Key] = k.Info.ETag
		}
	}
	regions := c.Regions()
	sideA := inRegion(c, regions[0])
	sideB := outside(c, regions[0])
	if err := c.Partition(sideA, sideB); err != nil {
		return res, err
	}
	if _, err := c.Await("each side of the cut sees only itself", b.converge, splitViews(c, sideA, sideB)); err != nil {
		return res, err
	}
	if err := c.Sleep(p.detect); err != nil {
		return res, err
	}
	ifMatch := func(e string) storage.Condition { return storage.Condition{IfMatchList: true, IfMatchAny: []string{e}} }
	var contests []contest
	var all []*Call
	for i := range p.objects {
		key := fmt.Sprintf("d%02d", i)
		sa := c.PutTag(pick(c, sideA), bucket, key, "state", "review-a", ifMatch(etag["tag|"+key+"|state"]))
		sb := c.PutTag(pick(c, sideB), bucket, key, "state", "review-b", ifMatch(etag["tag|"+key+"|state"]))
		contests = append(contests, tagContest(c, bucket, key, "state", sa, sb))
		all = append(all, sa, sb)
		if i%2 == 0 {
			ea := c.PutTag(pick(c, sideA), bucket, key, "editor", "ann", ifMatch(etag["tag|"+key+"|editor"]))
			contests = append(contests, tagContest(c, bucket, key, "editor", ea))
			all = append(all, ea)
		} else {
			eb := c.PutTag(pick(c, sideB), bucket, key, "editor", "ben", ifMatch(etag["tag|"+key+"|editor"]))
			contests = append(contests, tagContest(c, bucket, key, "editor", eb))
			all = append(all, eb)
		}
	}
	ba := c.PutACL(pick(c, sideA), bucket, "bob", "rw", ifMatch(etag["acl|bob"]))
	bb := c.PutACL(pick(c, sideB), bucket, "bob", "ra", ifMatch(etag["acl|bob"]))
	ca := c.PutACL(pick(c, sideA), bucket, "carol", "rwa", ifMatch(etag["acl|carol"]))
	contests = append(contests, aclContest(c, bucket, "bob", ba, bb), aclContest(c, bucket, "carol", ca))
	all = append(all, ba, bb, ca)
	oa := c.Put(pick(c, sideA), bucket, "d00", randomBytes(c.Rand(), 70), ifMatch(etag["obj|d00"]))
	ob := c.Put(pick(c, sideB), bucket, "d00", randomBytes(c.Rand(), 71), ifMatch(etag["obj|d00"]))
	contests = append(contests, objectContest(c, bucket, "d00", oa, ob))
	qa := c.SetQuota(pick(c, sideA), bucket, 1<<29, 500)
	qb := c.SetQuota(pick(c, sideB), bucket, 1<<28, 400)
	all = append(all, oa, ob, qa, qb)
	if err := c.Wait(all...); err != nil {
		return res, err
	}
	for _, k := range all {
		res.conditional++
		if k.Op == "put" && allowedDuringPartition(k) {
			continue
		}
		if k.Err != nil {
			return res, failf("%s %s/%s via %s refused while disconnected: %v", k.Op, k.Bucket, k.Key, k.At.ID, k.Err)
		}
	}
	if res.conditional == 0 {
		return res, failf("disconnected phase ran 0 conditional writes")
	}
	if err := c.Heal(); err != nil {
		return res, err
	}
	d, err := c.Await("one version of every key on every replica after the heal", b.converge, c.Converged)
	res.converge = d
	if err != nil {
		return res, err
	}
	res.contested, err = checkContests(c, contests)
	if err != nil {
		return res, err
	}
	if res.contested.discarded == 0 {
		return res, failf("no edit was discarded; the disconnected edits produced no conflict")
	}
	ds, err := discardLog(c)
	if err != nil {
		return res, err
	}
	res.discards = ds.most
	if res.discards == 0 || ds.total < uint64(res.contested.discarded) {
		return res, failf("discard counter inconsistent with contested discards: max_node=%d sum=%d contested_discarded=%d", res.discards, ds.total, res.contested.discarded)
	}
	var qs []*Call
	for _, n := range upNodes(c) {
		qs = append(qs, c.Quota(n, bucket))
	}
	if len(qs) == 0 {
		return res, failf("no up nodes to check quota convergence")
	}
	if err := waitAll(c, qs...); err != nil {
		return res, err
	}
	for _, q := range qs {
		if q.Quota.LimitBytes != qs[0].Quota.LimitBytes || q.Quota.LimitObjects != qs[0].Quota.LimitObjects {
			return res, failf("quota limits differ after the heal: %s %d/%d, %s %d/%d", qs[0].At.ID, qs[0].Quota.LimitBytes, qs[0].Quota.LimitObjects, q.At.ID, q.Quota.LimitBytes, q.Quota.LimitObjects)
		}
	}
	if l := qs[0].Quota; !(l.LimitBytes == 1<<29 && l.LimitObjects == 500) && !(l.LimitBytes == 1<<28 && l.LimitObjects == 400) {
		return res, failf("quota limit %d/%d is neither side's write", l.LimitBytes, l.LimitObjects)
	}
	var stale []*Call
	for i, ct := range contests {
		if len(ct.writes) < 2 || ct.writes[0].Op != "tag" {
			continue
		}
		read := c.Tags(pick(c, c.Nodes()), bucket, ct.writes[0].Key)
		if err := waitAll(c, read); err != nil {
			return res, err
		}
		cur, _ := ct.cur(read)
		for _, w := range ct.writes {
			if tag := etagOf(w); tag != cur {
				stale = append(stale, c.PutTag(pick(c, c.Nodes()), bucket, w.Key, w.Tag.Name, fmt.Sprintf("late-%d", i), ifMatch(tag)))
			}
		}
	}
	if err := c.Wait(stale...); err != nil {
		return res, err
	}
	for _, k := range stale {
		if !errors.Is(k.Err, occ.ErrPreconditionFailed) {
			return res, failf("a conditional write naming the discarded version of %s/%s returned %v, want precondition failed", k.Bucket, k.Key, k.Err)
		}
		res.staleRefused++
	}
	if res.staleRefused < p.objects {
		return res, failf("only %d stale conditional writes refused, want at least %d", res.staleRefused, p.objects)
	}
	return res, c.Ledger().Wrong()
}
