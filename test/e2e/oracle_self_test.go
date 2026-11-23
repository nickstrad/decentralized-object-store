package e2e

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/storage"
)

// TestLedgerFlagsBytesNoClientWroteUnderThatVersion proves Ledger accepts exact
// acknowledged writes and unknown-ETag matching attempts while rejecting wrong
// bytes, wrong sizes, wrong hashes, and bytes mismatched to a known ETag.
func TestLedgerFlagsBytesNoClientWroteUnderThatVersion(t *testing.T) {
	l := newLedger()
	put := &Call{Op: "put", Bucket: "b", Key: "k"}
	l.attempt(put, []byte("hello"))
	put.Info = storage.ObjectInfo{ETag: "e1"}
	l.settle(put)
	other := &Call{Op: "put", Bucket: "b", Key: "k"}
	l.attempt(other, []byte("world"))
	rows := []struct {
		name string
		info storage.ObjectInfo
		data []byte
		bad  bool
	}{
		{"the bytes written under the version read clean", storage.ObjectInfo{ETag: "e1", Size: 5}, []byte("hello"), false},
		{"another attempt's bytes under an unknown version read clean", storage.ObjectInfo{ETag: "e9", Size: 5}, []byte("world"), false},
		{"another attempt's bytes under a known version are wrong", storage.ObjectInfo{ETag: "e1", Size: 5}, []byte("world"), true},
		{"one flipped byte is wrong", storage.ObjectInfo{ETag: "e1", Size: 5}, []byte("hellp"), true},
		{"bytes no client wrote are wrong", storage.ObjectInfo{ETag: "e9", Size: 5}, []byte("xxxxx"), true},
		{"a size that disagrees with the body is wrong", storage.ObjectInfo{ETag: "e1", Size: 4}, []byte("hello"), true},
		{"a content hash that disagrees with the body is wrong", storage.ObjectInfo{ETag: "e1", Size: 5, ContentSHA: "00"}, []byte("hello"), true},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			err := l.checkRead(&Call{Op: "get", Bucket: "b", Key: "k", Info: r.info, Data: r.data})
			if (err != nil) != r.bad {
				t.Fatalf("checkRead = %v, want error %v", err, r.bad)
			}
		})
	}
	l.settle(&Call{Op: "get", Bucket: "b", Key: "k", Info: storage.ObjectInfo{ETag: "e1", Size: 5}, Data: []byte("hellp")})
	if err := l.Wrong(); !errors.Is(err, errScenario) {
		t.Fatalf("Wrong() = %v after a wrong read", err)
	}
}

func bootSelfTest(t *testing.T, seed int64) *simCluster {
	t.Helper()
	c := newSimCluster(seed, defaultSimOptions())
	if err := c.boot(); err != nil {
		t.Fatal(err)
	}
	if err := createBucket(c, simBounds, storage.BucketSpec{Name: "o"}); err != nil {
		t.Fatal(err)
	}
	return c
}

func putOne(t *testing.T, c *simCluster, key string, size int) *Call {
	t.Helper()
	k := c.Put(c.nodes[0], "o", key, randomBytes(c.Rand(), size), storage.Condition{})
	if err := waitAll(c, k); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Await("replicated", simBounds.converge, c.Converged); err != nil {
		t.Fatal(err)
	}
	return k
}

func manifestFor(t *testing.T, c *simCluster, bucket, key string) *storage.Manifest {
	t.Helper()
	rd, err := c.upNodes()[0].OCC().Reader()
	if err != nil {
		t.Fatal(err)
	}
	man := manifestOf(occ.ManifestKey(bucket, key), mustVersion(t, rd, bucket, key))
	if man == nil {
		t.Fatalf("no manifest for %s/%s", bucket, key)
	}
	return man
}

func mustVersion(t *testing.T, rd occ.Reader, bucket, key string) occ.Version {
	t.Helper()
	v, ok := rd.Record(occ.ManifestKey(bucket, key)).Version()
	if !ok {
		t.Fatalf("no record for %s/%s", bucket, key)
	}
	return v
}

// TestLossOracleFlagsADeclarationWhileEveryStripeHasKShards plants a ~lost/
// record for a healthy object and proves checkLossDeclaration rejects it.
func TestLossOracleFlagsADeclarationWhileEveryStripeHasKShards(t *testing.T) {
	c := bootSelfTest(t, 11)
	putOne(t, c, "x", 3*simStripe)
	man := manifestFor(t, c, "o", "x")
	n := c.upNodes()[0]
	key := occ.TagKey("o", "x", "~lost/"+man.Version.String())
	v, err := n.OCC().Put(key, []byte("lost"), occ.Precondition{})
	if err != nil {
		t.Fatal(err)
	}
	c.o.checkLossDeclaration(n.ID(), key, v)
	if len(c.o.failed) == 0 || !strings.Contains(c.o.failed[0], "verifiable shards") {
		t.Fatalf("the loss oracle accepted a declaration over a healthy object: %v", c.o.failed)
	}
}

// TestDeletionOracleFlagsAShardOfTheCurrentWinner plants a shard deletion on a
// live winner and proves watchDisk catches it immediately.
func TestDeletionOracleFlagsAShardOfTheCurrentWinner(t *testing.T) {
	c := bootSelfTest(t, 12)
	putOne(t, c, "x", 100)
	man := manifestFor(t, c, "o", "x")
	holder := man.Stripes[0].Shards[0].Node
	d := watchDisk{Disk: c.disks[holder], id: holder, o: c.o}
	if err := d.Delete(man.ShardID(0, 0).BlobName()); err != nil {
		t.Fatal(err)
	}
	if len(c.o.failed) == 0 || !strings.Contains(c.o.failed[0], "can still make the winner") {
		t.Fatalf("the deletion oracle accepted deleting a live winner's shard: %v", c.o.failed)
	}
}

// TestOrphanCheckFlagsAShardNoManifestExpects plants a stray shard on a
// non-holder node and proves checkOrphans flags it.
func TestOrphanCheckFlagsAShardNoManifestExpects(t *testing.T) {
	c := bootSelfTest(t, 13)
	k := putOne(t, c, "x", 100)
	if err := c.o.checkOrphans(); err != nil {
		t.Fatalf("clean cluster: %v", err)
	}
	man := manifestFor(t, c, "o", "x")
	var stranger env.NodeID
	for _, id := range c.ids {
		holds := false
		for _, sh := range man.Stripes[0].Shards {
			holds = holds || sh.Node == id
		}
		if !holds {
			stranger = id
			break
		}
	}
	blob, ok := c.s.Disk(man.Stripes[0].Shards[0].Node).Peek(man.ShardID(0, 0).BlobName())
	if !ok {
		t.Fatalf("shard of %s missing", k.Info.Version)
	}
	if err := c.disks[stranger].Write(man.ShardID(0, 0).BlobName(), blob); err != nil {
		t.Fatal(err)
	}
	if err := c.o.checkOrphans(); !errors.Is(err, errScenario) {
		t.Fatalf("checkOrphans = %v with a stray shard on %s", err, stranger)
	}
}

// TestLiveReadableOrLostFlagsAnUnreadableLiveKeyWithNoLossRecord deletes m+1
// shards of a live object without a loss record and proves
// checkLiveReadableOrLost fails.
func TestLiveReadableOrLostFlagsAnUnreadableLiveKeyWithNoLossRecord(t *testing.T) {
	c := bootSelfTest(t, 14)
	putOne(t, c, "x", 100)
	man := manifestFor(t, c, "o", "x")
	c.o.off = true
	for i := 0; i <= man.Layout.M; i++ {
		ref := man.Stripes[0].Shards[i]
		if err := c.disks[ref.Node].Delete(man.ShardID(0, i).BlobName()); err != nil {
			t.Fatal(err)
		}
	}
	c.o.off = false
	if _, err := c.checkLiveReadableOrLost(); !errors.Is(err, errScenario) {
		t.Fatalf("checkLiveReadableOrLost = %v with %d shards of a stripe gone", err, man.Layout.M+1)
	}
}

// TestSpreadOracleFlagsMoreThanMShardsInOneRegion feeds checkSpread a manifest
// placing m+1 shards in r0 and proves the spread oracle rejects it.
func TestSpreadOracleFlagsMoreThanMShardsInOneRegion(t *testing.T) {
	c := bootSelfTest(t, 15)
	putOne(t, c, "x", 100)
	man := manifestFor(t, c, "o", "x")
	bad := *man
	bad.Stripes = []storage.StripeRef{{Shards: nil}}
	for _, id := range c.ids {
		if c.s.Region(id) == "r0" && len(bad.Stripes[0].Shards) < man.Layout.M+1 {
			bad.Stripes[0].Shards = append(bad.Stripes[0].Shards, storage.ShardRef{Node: id})
		}
	}
	c.o.spreadOK[string(c.ids[0])+"|o/x"] = true
	c.o.checkSpread(c.ids[0], &bad)
	if len(c.o.failed) == 0 || !strings.Contains(c.o.failed[0], "more than m") {
		t.Fatalf("the spread oracle accepted %d shards in one region: %v", man.Layout.M+1, c.o.failed)
	}
}

// TestAckOracleFlagsALostWriteMissingFromEveryDiscardLog proves checkAcks
// passes when a partition-time discard is recorded and fails once the lineage
// oracle's discard records are cleared.
func TestAckOracleFlagsALostWriteMissingFromEveryDiscardLog(t *testing.T) {
	c := bootSelfTest(t, 16)
	putOne(t, c, "x", 10)
	if err := c.PartitionRegion("r0"); err != nil {
		t.Fatal(err)
	}
	if err := c.Sleep(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	a := c.PutTag(inRegion(c, "r0")[1], "o", "x", "owner", "a", storage.Condition{})
	b := c.PutTag(inRegion(c, "r1")[1], "o", "x", "owner", "b", storage.Condition{})
	if err := waitAll(c, a, b); err != nil {
		t.Fatal(err)
	}
	if err := c.Heal(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Await("healed", simBounds.converge, c.Converged); err != nil {
		t.Fatal(err)
	}
	rd, err := c.upNodes()[0].OCC().Reader()
	if err != nil {
		t.Fatal(err)
	}
	tally, err := c.o.checkAcks(rd)
	if err != nil {
		t.Fatalf("with the discard logs: %v", err)
	}
	if tally["discarded"] == 0 {
		t.Fatalf("no write was discarded: %v", tally)
	}
	c.o.lineage.Forget()
	if _, err := c.o.checkAcks(rd); !errors.Is(err, errScenario) {
		t.Fatalf("checkAcks = %v with the discard logs emptied", err)
	}
}

// TestHealthReportsTheMetadataDigestAndRoot proves every converged node reports
// identical non-empty MetaDigest and MetaRoot values that advance on metadata
// writes.
func TestHealthReportsTheMetadataDigestAndRoot(t *testing.T) {
	c := bootSelfTest(t, 17)
	putOne(t, c, "x", 10)
	var first storage.Health
	for i, n := range c.Nodes() {
		h, err := c.Health(n)
		if err != nil {
			t.Fatal(err)
		}
		if h.MetaDigest == "" || h.MetaRoot == "" {
			t.Fatalf("%s reports no digest or root", n.ID)
		}
		if i == 0 {
			first = h
		} else if h.MetaDigest != first.MetaDigest || h.MetaRoot != first.MetaRoot {
			t.Fatalf("%s reports %s/%s, %s reports %s/%s after convergence", n.ID, short(h.MetaDigest), short(h.MetaRoot), c.nodes[0].ID, short(first.MetaDigest), short(first.MetaRoot))
		}
	}
	if err := waitAll(c, c.PutTag(c.nodes[3], "o", "x", "t", "v", storage.Condition{})); err != nil {
		t.Fatal(err)
	}
	h, err := c.Health(c.nodes[3])
	if err != nil {
		t.Fatal(err)
	}
	if h.MetaDigest == first.MetaDigest || h.MetaRoot == first.MetaRoot {
		t.Fatal("digest and root did not move after a metadata write")
	}
}

// TestNemesisSettleValidatesTerminalErrorsForEveryOp proves nemesis.settle
// enforces per-operation allowed terminal errors, rejects unknown ops, and
// voids availability reads only at or after fault or node-touch timestamps.
func TestNemesisSettleValidatesTerminalErrorsForEveryOp(t *testing.T) {
	c := newSimCluster(801001, defaultSimOptions())
	if err := c.boot(); err != nil {
		t.Fatal(err)
	}
	at := c.nodes[0]
	unknownErr := errors.New("unexpected synthetic error")
	cases := []struct {
		name    string
		call    *Call
		wantErr bool
	}{
		{"put with ErrCorrupt fails", &Call{Op: "put", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrCorrupt, done: true}, true},
		{"put with unknown error fails", &Call{Op: "put", At: at, Bucket: bucketW, Key: "k00", Err: unknownErr, done: true}, true},
		{"put with ErrInvalid fails", &Call{Op: "put", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrInvalid, done: true}, true},
		{"put with ErrPreconditionFailed fails", &Call{Op: "put", At: at, Bucket: bucketW, Key: "k00", Err: occ.ErrPreconditionFailed, done: true}, true},
		{"put with ErrNotFound fails", &Call{Op: "put", At: at, Bucket: bucketW, Key: "k00", Err: occ.ErrNotFound, done: true}, true},
		{"put to unmigrated bucketW with ErrBusy fails", &Call{Op: "put", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrBusy, done: true}, true},
		{"put to migrating bucketM with ErrBusy passes", &Call{Op: "put", At: at, Bucket: bucketM, Key: "m001", Err: storage.ErrBusy, done: true}, false},
		{"put with ErrUnavailable passes", &Call{Op: "put", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrUnavailable, done: true}, false},
		{"put with ErrTimeout passes", &Call{Op: "put", At: at, Bucket: bucketW, Key: "k00", Err: env.ErrTimeout, done: true}, false},
		{"put with ErrNoSuchBucket passes", &Call{Op: "put", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrNoSuchBucket, done: true}, false},

		{"get with ErrCorrupt fails", &Call{Op: "get", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrCorrupt, done: true}, true},
		{"get with unknown error fails", &Call{Op: "get", At: at, Bucket: bucketW, Key: "k00", Err: unknownErr, done: true}, true},
		{"get with ErrInvalid fails", &Call{Op: "get", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrInvalid, done: true}, true},
		{"get with ErrBusy fails", &Call{Op: "get", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrBusy, done: true}, true},
		{"get with storage.ErrNotFound fails", &Call{Op: "get", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrNotFound, done: true}, true},
		{"get with occ.ErrNotFound passes", &Call{Op: "get", At: at, Bucket: bucketW, Key: "k00", Err: occ.ErrNotFound, done: true}, false},
		{"get with ErrUnavailable passes", &Call{Op: "get", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrUnavailable, done: true}, false},
		{"get with ErrTimeout passes", &Call{Op: "get", At: at, Bucket: bucketW, Key: "k00", Err: env.ErrTimeout, done: true}, false},
		{"get with ErrNoSuchBucket passes", &Call{Op: "get", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrNoSuchBucket, done: true}, false},

		{"tag with ErrCorrupt fails", &Call{Op: "tag", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrCorrupt, done: true}, true},
		{"tag with unknown error fails", &Call{Op: "tag", At: at, Bucket: bucketW, Key: "k00", Err: unknownErr, done: true}, true},
		{"tag with ErrInvalid fails", &Call{Op: "tag", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrInvalid, done: true}, true},
		{"tag with ErrUnavailable fails", &Call{Op: "tag", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrUnavailable, done: true}, true},
		{"tag with ErrPreconditionFailed fails", &Call{Op: "tag", At: at, Bucket: bucketW, Key: "k00", Err: occ.ErrPreconditionFailed, done: true}, true},
		{"tag with occ.ErrNotFound passes", &Call{Op: "tag", At: at, Bucket: bucketW, Key: "k00", Err: occ.ErrNotFound, done: true}, false},
		{"tag with ErrNoSuchBucket passes", &Call{Op: "tag", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrNoSuchBucket, done: true}, false},
		{"tag with ErrUnreachable passes", &Call{Op: "tag", At: at, Bucket: bucketW, Key: "k00", Err: env.ErrUnreachable, done: true}, false},

		{"delete with ErrCorrupt fails", &Call{Op: "delete", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrCorrupt, done: true}, true},
		{"delete with unknown error fails", &Call{Op: "delete", At: at, Bucket: bucketW, Key: "k00", Err: unknownErr, done: true}, true},
		{"delete with ErrInvalid fails", &Call{Op: "delete", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrInvalid, done: true}, true},
		{"delete with ErrUnavailable fails", &Call{Op: "delete", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrUnavailable, done: true}, true},
		{"delete with storage.ErrNotFound fails", &Call{Op: "delete", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrNotFound, done: true}, true},
		{"delete with ErrPreconditionFailed fails", &Call{Op: "delete", At: at, Bucket: bucketW, Key: "k00", Err: occ.ErrPreconditionFailed, done: true}, true},
		{"delete with occ.ErrNotFound passes", &Call{Op: "delete", At: at, Bucket: bucketW, Key: "k00", Err: occ.ErrNotFound, done: true}, false},
		{"delete with ErrNoSuchBucket passes", &Call{Op: "delete", At: at, Bucket: bucketW, Key: "k00", Err: storage.ErrNoSuchBucket, done: true}, false},
		{"delete with ErrTimeout passes", &Call{Op: "delete", At: at, Bucket: bucketW, Key: "k00", Err: env.ErrTimeout, done: true}, false},
		{"delete with occ.ErrNotReady passes", &Call{Op: "delete", At: at, Bucket: bucketW, Key: "k00", Err: occ.ErrNotReady, done: true}, false},

		{"unknown op fails", &Call{Op: "bogus", At: at, Bucket: bucketW, Key: "k00", Err: nil, done: true}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := newNemesis(c)
			n.dirty = map[string]bool{}
			n.pending = []*Call{tc.call}
			err := n.settle()
			if tc.wantErr && err == nil {
				t.Fatalf("settle() = nil for %s (%v), want error", tc.call.Op, tc.call.Err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("settle() = %v for %s (%v), want nil", err, tc.call.Op, tc.call.Err)
			}
		})
	}
	t.Run("availability get voids only reads not yet complete when fault or node touch occurs", func(t *testing.T) {
		n := newNemesis(c)
		n.dirty = map[string]bool{}
		n.touched = map[env.NodeID]bool{}
		n.pending = []*Call{{Op: "get", At: at, Bucket: bucketW, Key: "k00", Size: -1, Ended: time.Second, Err: storage.ErrUnavailable, done: true}}
		if err := n.settle(); err == nil || !strings.Contains(err.Error(), "availability within the fault budget") {
			t.Fatalf("untouched availability get without netFault: settle() = %v, want availability failure", err)
		}
		n = newNemesis(c)
		n.dirty = map[string]bool{}
		n.touched = map[env.NodeID]bool{}
		n.netFault = true
		n.netFaultAt = 2 * time.Second
		n.pending = []*Call{{Op: "get", At: at, Bucket: bucketW, Key: "k00", Size: -1, Ended: 2 * time.Second, Err: storage.ErrUnavailable, done: true}}
		if err := n.settle(); err != nil {
			t.Fatalf("availability get ending at netFaultAt: settle() = %v, want nil", err)
		}
		if n.st.availabilityGets != 0 {
			t.Fatalf("voided availability get was counted: availabilityGets = %d, want 0", n.st.availabilityGets)
		}
		n = newNemesis(c)
		n.dirty = map[string]bool{}
		n.touched = map[env.NodeID]bool{}
		n.netFault = true
		n.netFaultAt = 2 * time.Second
		n.pending = []*Call{{Op: "get", At: at, Bucket: bucketW, Key: "k00", Size: -1, Ended: time.Second, Err: storage.ErrUnavailable, done: true}}
		if err := n.settle(); err == nil || !strings.Contains(err.Error(), "availability within the fault budget") {
			t.Fatalf("availability get finished before netFaultAt must still fail: settle() = %v", err)
		}
		n = newNemesis(c)
		n.dirty = map[string]bool{}
		n.touched = map[env.NodeID]bool{}
		n.netFault = true
		n.netFaultAt = 2 * time.Second
		n.pending = []*Call{{Op: "get", At: at, Bucket: bucketW, Key: "k00", Size: -1, Ended: time.Second, Err: nil, done: true}}
		if err := n.settle(); err != nil {
			t.Fatalf("clean availability get finished before netFaultAt: settle() = %v", err)
		}
		if n.st.availabilityGets != 1 {
			t.Fatalf("clean availability get before netFaultAt was voided: availabilityGets = %d, want 1", n.st.availabilityGets)
		}
		n = newNemesis(c)
		n.dirty = map[string]bool{}
		n.touched = map[env.NodeID]bool{env.NodeID(at.ID): true}
		n.touchedAt = map[env.NodeID]time.Duration{env.NodeID(at.ID): 2 * time.Second}
		n.pending = []*Call{{Op: "get", At: at, Bucket: bucketW, Key: "k00", Size: -1, Ended: 2500 * time.Millisecond, Err: env.ErrTimeout, done: true}}
		if err := n.settle(); err != nil {
			t.Fatalf("availability get still in flight when node touched: settle() = %v, want nil", err)
		}
		if n.st.availabilityGets != 0 {
			t.Fatalf("voided touched-node get was counted: availabilityGets = %d, want 0", n.st.availabilityGets)
		}
		n = newNemesis(c)
		n.dirty = map[string]bool{}
		n.touched = map[env.NodeID]bool{env.NodeID(at.ID): true}
		n.touchedAt = map[env.NodeID]time.Duration{env.NodeID(at.ID): 2 * time.Second}
		n.pending = []*Call{{Op: "get", At: at, Bucket: bucketW, Key: "k00", Size: -1, Ended: time.Second, Err: storage.ErrUnavailable, done: true}}
		if err := n.settle(); err == nil || !strings.Contains(err.Error(), "availability within the fault budget") {
			t.Fatalf("availability get finished before node was touched must still fail: settle() = %v", err)
		}
		n = newNemesis(c)
		n.dirty = map[string]bool{"k00": true}
		n.dirtyAt = map[string]time.Duration{"k00": 2 * time.Second}
		n.touched = map[env.NodeID]bool{}
		n.pending = []*Call{{Op: "get", At: at, Bucket: bucketW, Key: "k00", Size: -1, Ended: time.Second, Err: storage.ErrUnavailable, done: true}}
		if err := n.settle(); err == nil || !strings.Contains(err.Error(), "availability within the fault budget") {
			t.Fatalf("availability get finished before key became dirty must still fail: settle() = %v", err)
		}
	})
}

// TestOracleSeesMigratedManifestSwap proves Retire reports swapped destination
// manifests to CommitSink so simOracle tracks them and rejects spurious loss
// declarations on migrated versions.
func TestOracleSeesMigratedManifestSwap(t *testing.T) {
	c := newSimCluster(18, fuzzOptions())
	if err := c.boot(); err != nil {
		t.Fatal(err)
	}
	if err := createBucket(c, simBounds, storage.BucketSpec{Name: "m", Region: "r0"}); err != nil {
		t.Fatal(err)
	}
	k := c.Put(c.nodes[0], "m", "k0", randomBytes(c.Rand(), 2048), storage.Condition{})
	if err := waitAll(c, k); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Await("replicated", simBounds.converge, c.Converged); err != nil {
		t.Fatal(err)
	}
	start := c.StartMigration(c.nodes[0], "m", "r1")
	if err := waitAll(c, start); err != nil {
		t.Fatal(err)
	}
	mi, _, _, err := awaitMigration(c, simBounds, c.nodes[0], start.Mig.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mi.State != "completed" {
		t.Fatalf("migration ended %q, want completed", mi.State)
	}
	if _, err := c.Await("converged", simBounds.converge, c.Converged); err != nil {
		t.Fatal(err)
	}
	man := manifestFor(t, c, "m", "k0")
	if c.o.mans[man.Version] == nil {
		t.Fatalf("oracle mans missing migrated version %s swapped in by Retire", man.Version)
	}
	n := c.upNodes()[0]
	lk := occ.TagKey("m", "k0", "~lost/"+man.Version.String())
	v, err := n.OCC().Put(lk, []byte("unrecoverable"), occ.Precondition{})
	if err != nil {
		t.Fatal(err)
	}
	c.o.checkLossDeclaration(n.ID(), lk, v)
	if len(c.o.failed) == 0 || !strings.Contains(c.o.failed[0], "verifiable shards") {
		t.Fatalf("loss oracle did not judge migrated version: %v", c.o.failed)
	}
}
