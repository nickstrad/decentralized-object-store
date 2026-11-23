package e2e

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

func hasLiveManifest(c *simCluster, id env.NodeID, bucket, key string) bool {
	nd := c.node(id)
	if nd == nil {
		return false
	}
	rd, err := nd.OCC().Reader()
	if err != nil {
		return false
	}
	return rd.Record(occ.ManifestKey(bucket, key)).Live()
}

func scenarioAbsenceShortOfLoss(c *simCluster) (int, error) {
	const bucket = "e3abs"
	if err := createBucket(c, simBounds, storage.BucketSpec{Name: bucket}); err != nil {
		return 0, err
	}

	writerID := c.ids[2]
	delayedID := c.ids[len(c.ids)-2]
	writerNode := c.nodeOf(writerID)
	delayedNode := c.nodeOf(delayedID)

	if err := c.apply(sim.Fault{Kind: sim.FaultClog, Node: writerID, Peer: delayedID}); err != nil {
		return 0, err
	}
	healed := false
	heal := func() error {
		if healed {
			return nil
		}
		healed = true
		return c.Heal()
	}
	defer func() { _ = heal() }()

	var chosen *Call
	var man *storage.Manifest
	var absID, rebID env.NodeID
	var absIdx, rebIdx, delIdx int

	for attempt := range 100 {
		rem := time.Second - (c.Now() % time.Second)
		if rem > 120*time.Millisecond {
			if err := c.Sleep(rem - 120*time.Millisecond); err != nil {
				return 0, err
			}
		} else if rem < 70*time.Millisecond {
			if err := c.Sleep(rem + 880*time.Millisecond); err != nil {
				return 0, err
			}
		}
		key := fmt.Sprintf("abs%02d", attempt)
		k := c.Put(writerNode, bucket, key, randomBytes(c.Rand(), 4096), storage.Condition{})
		if err := waitAll(c, k); err != nil {
			return 0, err
		}
		w := c.node(writerID)
		if w == nil {
			return 0, failf("writer %s is not up", writerID)
		}
		rd, err := w.OCC().Reader()
		if err != nil {
			return 0, err
		}
		v, ok := rd.Record(occ.ManifestKey(bucket, key)).Version()
		if !ok {
			continue
		}
		m := manifestOf(occ.ManifestKey(bucket, key), v)
		if m == nil || len(m.Stripes) != 1 {
			continue
		}
		foundDel := -1
		for i, sh := range m.Stripes[0].Shards {
			if sh.Node == delayedID {
				foundDel = i
				break
			}
		}
		if foundDel < 0 {
			continue
		}
		if err := c.Sleep(30 * time.Millisecond); err != nil {
			return 0, err
		}
		if rem = time.Second - (c.Now() % time.Second); rem > 20*time.Millisecond && rem < 200*time.Millisecond {
			if err := c.Sleep(rem - 15*time.Millisecond); err != nil {
				return 0, err
			}
		}
		if hasLiveManifest(c, delayedID, bucket, key) {
			continue
		}
		aID, aIdx := env.NodeID(""), -1
		rID, rIdx := env.NodeID(""), -1
		for i, sh := range m.Stripes[0].Shards {
			if sh.Node == delayedID || sh.Node == writerID {
				continue
			}
			if aID == "" && hasLiveManifest(c, sh.Node, bucket, key) {
				aID, aIdx = sh.Node, i
			}
		}
		if aID == "" {
			continue
		}
		for i, sh := range m.Stripes[0].Shards {
			if sh.Node == delayedID || sh.Node == aID || sh.Node == c.ids[0] {
				continue
			}
			if !hasLiveManifest(c, sh.Node, bucket, key) {
				rID, rIdx = sh.Node, i
				break
			}
		}
		if rID == "" {
			continue
		}
		chosen, man = k, m
		absID, absIdx = aID, aIdx
		rebID, rebIdx = rID, rIdx
		delIdx = foundDel
		break
	}
	if chosen == nil || man == nil {
		return 0, failf("could not set up absence-short-of-loss stripe on %s", delayedID)
	}

	var shield []env.NodeID
	for _, id := range c.ids {
		if id != absID && id != rebID && id != delayedID {
			shield = append(shield, id)
		}
	}
	if err := c.apply(sim.Fault{Kind: sim.FaultPartitionOneWay, Side: shield, Other: []env.NodeID{rebID, delayedID}}); err != nil {
		return 0, err
	}

	absBlob := man.ShardID(0, absIdx).BlobName()
	rebBlob := man.ShardID(0, rebIdx).BlobName()
	delBlob := man.ShardID(0, delIdx).BlobName()
	absBefore := c.node(absID).Stats().AbsentRecords

	if err := c.Rot(c.nodeOf(absID), absBlob); err != nil {
		return 0, err
	}
	if err := c.Rot(c.nodeOf(rebID), rebBlob); err != nil {
		return 0, err
	}

	if _, err := c.Await("holder writes absence record short of loss", 5*time.Second, func() (bool, error) {
		a := c.node(absID)
		return a != nil && a.Stats().AbsentRecords > absBefore, nil
	}); err != nil {
		return 0, err
	}
	if err := heal(); err != nil {
		return 0, err
	}

	if _, err := c.Await("converged after absence short of loss", simBounds.converge, c.Converged); err != nil {
		return 0, err
	}
	rebNode := c.nodeOf(rebID)
	if _, err := c.Await("non-absent rotted shard rebuilt", simBounds.repair, func() (bool, error) {
		okReb, err := verifiableOn(c, rebNode, []string{rebBlob})
		if err != nil || !okReb[rebBlob] {
			return false, err
		}
		okDel, err := verifiableOn(c, delayedNode, []string{delBlob})
		return okDel[delBlob], err
	}); err != nil {
		return 0, err
	}
	if err := c.Sleep(3 * time.Second); err != nil {
		return 0, err
	}

	rd0, err := c.node(c.ids[0]).OCC().Reader()
	if err != nil {
		return 0, err
	}
	absPrefix := "~absent/" + man.Version.String() + "/"
	absCount := 0
	for _, k := range rd0.Keys() {
		if len(k) == 0 || occ.Kind(k[0]) != occ.KindTag || !rd0.Record(k).Live() {
			continue
		}
		_, parts, err := occ.ParseKey(k)
		if err == nil && len(parts) == 3 && parts[0] == bucket && strings.HasPrefix(parts[2], absPrefix) {
			absCount++
		}
	}
	if absCount == 0 {
		return 0, failf("expected at least 1 replicated absence record for %s/%s version %s, got 0", bucket, chosen.Key, man.Version)
	}
	if absCount > man.Layout.M {
		return 0, failf("expected absence records <= m=%d for recoverable object %s/%s, got %d", man.Layout.M, bucket, chosen.Key, absCount)
	}
	if len(c.o.declared) > 0 {
		return absCount, failf("recoverable object %s/%s with %d absence records (<= m=%d) was declared lost: %v", bucket, chosen.Key, absCount, man.Layout.M, c.o.declared)
	}
	for _, n := range upNodes(c) {
		h, err := c.Health(n)
		if err != nil {
			return absCount, err
		}
		if counter(h, "lost_objects") > 0 {
			return absCount, failf("node %s reports lost_objects=%d after absence short of loss", n.ID, counter(h, "lost_objects"))
		}
	}
	g := c.Get(c.nodes[0], bucket, chosen.Key)
	if err := waitAll(c, g); err != nil {
		return absCount, err
	}
	if sha256.Sum256(g.Data) != chosen.Sum {
		return absCount, failf("get %s/%s after absence short of loss returned wrong bytes", bucket, chosen.Key)
	}
	return absCount, c.Ledger().Wrong()
}
