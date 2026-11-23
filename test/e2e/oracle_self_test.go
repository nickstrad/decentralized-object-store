package e2e

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

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
}
