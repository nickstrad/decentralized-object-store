package node_test

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

func TestConfirmRepeatedReadErrorsOnAllHoldersAndRottedPeer(t *testing.T) {
	h, _ := lossHarness(t, 3, storage.Layout{K: 2, M: 1, StripeSize: 1024}, 3)
	_, data, man := putEverywhere(t, h, 700)
	h0 := man.Stripes[0].Shards[0].Node
	h1 := man.Stripes[0].Shards[1].Node
	h2 := man.Stripes[0].Shards[2].Node
	n0, _ := holderOf(man, 0, h0)
	n1, _ := holderOf(man, 0, h1)
	n2, _ := holderOf(man, 0, h2)

	if err := h.s.BitRot(h2, n2); err != nil {
		t.Fatal(err)
	}
	if err := h.s.FailReads(h0, n0, 5); err != nil {
		t.Fatal(err)
	}
	if err := h.s.FailReads(h1, n1, 5); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(5 * time.Second); err != nil {
		t.Fatalf("sim failed during first wave: %v", err)
	}
	if err := h.s.FailReads(h0, n0, 3); err != nil {
		t.Fatal(err)
	}
	if err := h.s.FailReads(h1, n1, 3); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(20 * time.Second); err != nil {
		t.Fatalf("sim failed after second wave: %v", err)
	}
	if len(h.declared) != 0 {
		t.Fatalf("recoverable object declared lost: %v", h.declared)
	}
	for _, id := range []env.NodeID{h0, h1, h2} {
		if a := h.node(id).Stats().AbsentRecords; a != 0 {
			t.Fatalf("%s wrote %d absence records while object was recoverable", id, a)
		}
	}
	for _, n := range h.up() {
		_, got, err := h.syncGet(t, n.ID(), "g", "k")
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%s: GET failed after recovery: len=%d err=%v", n.ID(), len(got), err)
		}
	}
}

func TestConfirmReadErrorOnlyDuringStateAbsent(t *testing.T) {
	cfg := smallConfig()
	cfg.Layout = storage.Layout{K: 2, M: 0, StripeSize: 1024}
	cfg.MaintainPeriod = 500 * time.Millisecond
	cfg.RequestTimeout = 5 * time.Second
	h, err := newHarness(1, 3, cfg, fixedNet())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	_, data, man := putEverywhere(t, h, 700)
	h0 := man.Stripes[0].Shards[0].Node
	h1 := man.Stripes[0].Shards[1].Node
	n0, _ := holderOf(man, 0, h0)

	if err := h.s.Clog(h1, h0); err != nil {
		t.Fatal(err)
	}
	if err := h.s.FailReads(h0, n0, 1); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(cfg.MaintainPeriod + 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := h.s.FailReads(h0, n0, 1); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Unclog(h1, h0); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(15 * time.Second); err != nil {
		t.Fatalf("sim failed: %v", err)
	}
	if len(h.declared) != 0 {
		t.Fatalf("healthy k=2,m=0 object declared lost: %v", h.declared)
	}
	if h.node(h0).Stats().AbsentRecords != 0 {
		t.Fatalf("%s wrote %d absence records when stateAbsent hit ErrIO", h0, h.node(h0).Stats().AbsentRecords)
	}
	for _, n := range h.up() {
		_, got, err := h.syncGet(t, n.ID(), "g", "k")
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("%s: GET failed: len=%d err=%v", n.ID(), len(got), err)
		}
	}
}

func TestConfirmPeerReadErrorBlocksUnrecoverableDeclarationUntilCleared(t *testing.T) {
	h, _ := lossHarness(t, 3, storage.Layout{K: 2, M: 1, StripeSize: 1024}, 3)
	oi, _, man := putEverywhere(t, h, 700)
	h0 := man.Stripes[0].Shards[0].Node
	h1 := man.Stripes[0].Shards[1].Node
	h2 := man.Stripes[0].Shards[2].Node
	n0, _ := holderOf(man, 0, h0)
	n1, _ := holderOf(man, 0, h1)
	n2, _ := holderOf(man, 0, h2)

	if err := h.s.BitRot(h1, n1); err != nil {
		t.Fatal(err)
	}
	if err := h.s.BitRot(h2, n2); err != nil {
		t.Fatal(err)
	}
	if err := h.s.FailReads(h0, n0, 50); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(5 * time.Second); err != nil {
		t.Fatalf("sim failed while h0 disk unreadable: %v", err)
	}
	if len(h.declared) != 0 {
		t.Fatalf("declared lost while h0 disk was returning ErrIO: %v", h.declared)
	}
	for _, id := range []env.NodeID{h0, h1, h2} {
		if a := h.node(id).Stats().AbsentRecords; a != 0 {
			t.Fatalf("%s wrote %d absence records while h0 returned ErrIO", id, a)
		}
	}
	if err := h.s.RunFor(40 * time.Second); err != nil {
		t.Fatalf("sim failed after h0 read errors drained: %v", err)
	}
	if !slices.Contains(h.declared, oi.Version) {
		t.Fatalf("genuinely unrecoverable version %s was not declared lost after h0 disk recovered", oi.Version)
	}
	if h.node(h0).Stats().AbsentRecords != 0 {
		t.Fatalf("healthy holder %s wrote %d absence records", h0, h.node(h0).Stats().AbsentRecords)
	}
}

func TestConfirmK1M0SingleHolderRepeatedReadErrors(t *testing.T) {
	h, _ := lossHarness(t, 3, storage.Layout{K: 1, M: 0, StripeSize: 1024}, 0)
	_, data, man := putEverywhere(t, h, 300)
	h0 := man.Stripes[0].Shards[0].Node
	n0, _ := holderOf(man, 0, h0)
	if err := h.s.FailReads(h0, n0, 5); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(15 * time.Second); err != nil {
		t.Fatalf("sim failed: %v", err)
	}
	if len(h.declared) != 0 || h.node(h0).Stats().AbsentRecords != 0 {
		t.Fatalf("k=1,m=0 false absence/declaration: declared=%v absent=%d", h.declared, h.node(h0).Stats().AbsentRecords)
	}
	_, got, err := h.syncGet(t, h0, "g", "k")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("k=1,m=0 GET failed: len=%d err=%v", len(got), err)
	}
}

func TestConfirmAReadErrorOnAnIndexedShardStartsNoRebuild(t *testing.T) {
	t.Run("persistent_ErrIO_with_k_healthy_peers", func(t *testing.T) {
		h, _ := lossHarness(t, 3, storage.Layout{K: 2, M: 1, StripeSize: 1024}, 3)
		_, data, man := putEverywhere(t, h, 700)
		h0 := man.Stripes[0].Shards[0].Node
		n0, _ := holderOf(man, 0, h0)
		origBlob, ok := h.s.Disk(h0).Peek(n0)
		if !ok {
			t.Fatal("missing shard")
		}
		origCopy := slices.Clone(origBlob)
		if err := h.s.RunFor(10 * time.Second); err != nil {
			t.Fatal(err)
		}
		before := h.node(h0).Stats().RebuildFailures
		if err := h.s.FailReads(h0, n0, 100); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunFor(10 * time.Second); err != nil {
			t.Fatal(err)
		}
		st := h.node(h0).Stats()
		t.Logf("persistent_ErrIO (10s, MaintainPeriod=%v): Rebuilds=%d RebuildFailures=%d AbsentRecords=%d",
			h.cfg.MaintainPeriod, st.Rebuilds, st.RebuildFailures, st.AbsentRecords)
		if st.RebuildFailures != before {
			t.Fatalf("a read error on an indexed shard started %d rebuilds that could not store", st.RebuildFailures-before)
		}
		if st.AbsentRecords != 0 {
			t.Fatalf("unexpected absence record: %d", st.AbsentRecords)
		}
		curBlob, ok := h.s.Disk(h0).Peek(n0)
		if !ok || !bytes.Equal(curBlob, origCopy) {
			t.Fatal("good shard bytes were modified or lost")
		}
		_, got, err := h.syncGet(t, h0, "g", "k")
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("GET failed despite k healthy peers: %v", err)
		}
	})

	t.Run("single_transient_ErrIO_with_many_objects_m0", func(t *testing.T) {
		cfg := smallConfig()
		cfg.Layout = storage.Layout{K: 2, M: 0, StripeSize: 1024}
		cfg.MaintainPeriod = time.Second
		cfg.InventoryScrub = 1
		h, err := newHarness(1, 3, cfg, fixedNet())
		if err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunFor(3 * time.Second); err != nil {
			t.Fatal(err)
		}
		if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: "g"}); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
			t.Fatal(err)
		}
		var targetShard string
		var targetHolder env.NodeID
		for i := 0; i < 20; i++ {
			k := fmt.Sprintf("k%02d", i)
			oi, err := h.syncPut(t, h.ids[0], storage.PutRequest{Bucket: "g", Key: k, Data: bytes.Repeat([]byte{byte(i)}, 100)})
			if err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				man := h.mans[oi.Version]
				targetHolder = man.Stripes[0].Shards[0].Node
				targetShard, _ = holderOf(man, 0, targetHolder)
			}
		}
		if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
			t.Fatal(err)
		}
		before := h.node(targetHolder).Stats().RebuildFailures
		if err := h.s.FailReads(targetHolder, targetShard, 1); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunFor(25 * time.Second); err != nil {
			t.Fatal(err)
		}
		st := h.node(targetHolder).Stats()
		t.Logf("single_transient_ErrIO (20 objects, InventoryScrub=1, 25s, holder=%s): Rebuilds=%d RebuildFailures=%d AbsentRecords=%d",
			targetHolder, st.Rebuilds, st.RebuildFailures, st.AbsentRecords)
		if st.RebuildFailures > before+1 {
			t.Fatalf("one read error on an intact shard started %d rebuilds that could not succeed", st.RebuildFailures-before)
		}
		if st.AbsentRecords != 0 || len(h.declared) != 0 {
			t.Fatalf("false absence/declaration: absent=%d declared=%v", st.AbsentRecords, h.declared)
		}
	})
}

func setupWipedHolderWithAbsence(t *testing.T) (*harness, env.NodeID, string, occ.Doc, occ.Doc, []env.NodeID) {
	t.Helper()
	cfg := smallConfig()
	cfg.Layout = storage.Layout{K: 2, M: 2, StripeSize: 1024}
	cfg.RequestTimeout = 5 * time.Second
	cfg.MaintainPeriod = 500 * time.Millisecond
	h, err := newHarness(1, 4, cfg, fixedNet())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: "g"}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	_, _, man := putEverywhere(t, h, 700)
	h0 := man.Stripes[0].Shards[0].Node
	name0, _ := holderOf(man, 0, h0)
	sid0, _ := storage.ParseBlobName(name0)
	absKey := node.AbsentKeyForTest(sid0, h0)

	if _, err := h.node(h0).OCC().SetScalar(absKey, []byte{1}); err != nil {
		t.Fatal(err)
	}
	h.node(h0).RecordMeta(absKey)
	if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
		t.Fatal(err)
	}
	var peers []env.NodeID
	for _, id := range h.ids {
		if id != h0 {
			peers = append(peers, id)
		}
	}
	rdPeer, _ := h.node(peers[0]).OCC().Reader()
	manDoc := occ.NewDoc()
	mv, _ := rdPeer.Record(occ.ManifestKey("g", "k")).Version()
	manDoc.MergeVersion(occ.ManifestKey("g", "k"), mv)
	absDoc := occ.NewDoc()
	av, _ := rdPeer.Record(absKey).Version()
	absDoc.MergeVersion(absKey, av)
	return h, h0, name0, *manDoc, *absDoc, peers
}

func TestConfirmAbsenceRecordArrivesBeforeFirstReply(t *testing.T) {
	h, h0, name0, manDoc, absDoc, peers := setupWipedHolderWithAbsence(t)
	if err := h.s.WipeRejoin(h0); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(5 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	for _, p := range peers {
		if err := h.s.Clog(p, h0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.node(h0).OCC().Merge(&manDoc); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(h.cfg.MaintainPeriod + 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, err := h.node(h0).OCC().Merge(&absDoc); err != nil {
		t.Fatal(err)
	}
	for _, p := range peers {
		if err := h.s.Unclog(p, h0); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunFor(50 * time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.s.RunFor(500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, onDisk := h.s.Disk(h0).Peek(name0); onDisk {
		t.Fatalf("shard %s stored on %s despite absence record arriving before first reply", name0, h0)
	}
	if h.node(h0).Stats().Rebuilds != 0 || h.node(h0).Stats().RebuildFailures == 0 {
		t.Fatalf("unexpected rebuild stats: Rebuilds=%d RebuildFailures=%d", h.node(h0).Stats().Rebuilds, h.node(h0).Stats().RebuildFailures)
	}
}

func TestConfirmAbsenceRecordArrivesBetweenReplies(t *testing.T) {
	h, h0, name0, manDoc, absDoc, peers := setupWipedHolderWithAbsence(t)
	if err := h.s.WipeRejoin(h0); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(5 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Clog(peers[1], h0); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Clog(peers[2], h0); err != nil {
		t.Fatal(err)
	}
	servedBefore := h.node(peers[0]).Storage().Stats().Served
	if _, err := h.node(h0).OCC().Merge(&manDoc); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(h.cfg.MaintainPeriod + 50*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if h.node(peers[0]).Storage().Stats().Served == servedBefore {
		t.Fatal("peers[0] did not serve first reply before absence record arrived")
	}
	if _, onDisk := h.s.Disk(h0).Peek(name0); onDisk {
		t.Fatal("rebuild finished prematurely before clogged peers replied")
	}
	if _, err := h.node(h0).OCC().Merge(&absDoc); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Unclog(peers[1], h0); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Unclog(peers[2], h0); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(500 * time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if _, onDisk := h.s.Disk(h0).Peek(name0); onDisk {
		t.Fatalf("shard %s stored on %s despite absence record arriving between replies", name0, h0)
	}
	if h.node(h0).Stats().Rebuilds != 0 || h.node(h0).Stats().RebuildFailures == 0 {
		t.Fatalf("unexpected rebuild stats: Rebuilds=%d RebuildFailures=%d", h.node(h0).Stats().Rebuilds, h.node(h0).Stats().RebuildFailures)
	}
}

func TestConfirmCrashAndRestartInMiddleOfRebuild(t *testing.T) {
	t.Run("crash_after_absence_merged_mid_rebuild", func(t *testing.T) {
		h, h0, name0, manDoc, absDoc, peers := setupWipedHolderWithAbsence(t)
		sid0, _ := storage.ParseBlobName(name0)
		if err := h.s.WipeRejoin(h0); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunFor(5 * time.Millisecond); err != nil {
			t.Fatal(err)
		}
		for _, p := range peers {
			if err := h.s.Clog(p, h0); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := h.node(h0).OCC().Merge(&manDoc); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunFor(h.cfg.MaintainPeriod + 50*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if _, err := h.node(h0).OCC().Merge(&absDoc); err != nil {
			t.Fatal(err)
		}
		if err := h.s.Crash(h0); err != nil {
			t.Fatal(err)
		}
		for _, p := range peers {
			if err := h.s.Unclog(p, h0); err != nil {
				t.Fatal(err)
			}
		}
		if err := h.s.RunFor(100 * time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if err := h.s.Restart(h0); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunFor(3 * time.Second); err != nil {
			t.Fatal(err)
		}
		if _, onDisk := h.s.Disk(h0).Peek(name0); onDisk {
			t.Fatalf("shard %s stored on %s after crash+restart mid-rebuild", name0, h0)
		}
		rd0, err := h.node(h0).OCC().Reader()
		if err != nil || !rd0.Record(node.AbsentKeyForTest(sid0, h0)).Live() {
			t.Fatalf("absence record lost across crash+restart: err=%v", err)
		}
	})

	t.Run("crash_before_absence_merged_then_restart_and_race_again", func(t *testing.T) {
		h, h0, name0, manDoc, absDoc, peers := setupWipedHolderWithAbsence(t)
		if err := h.s.WipeRejoin(h0); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunFor(5 * time.Millisecond); err != nil {
			t.Fatal(err)
		}
		for _, p := range peers {
			if err := h.s.Clog(p, h0); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := h.node(h0).OCC().Merge(&manDoc); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunFor(h.cfg.MaintainPeriod + 50*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if err := h.s.Crash(h0); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunFor(100 * time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if err := h.s.Restart(h0); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunFor(h.cfg.MaintainPeriod + 50*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		if _, err := h.node(h0).OCC().Merge(&absDoc); err != nil {
			t.Fatal(err)
		}
		for _, p := range peers {
			if err := h.s.Unclog(p, h0); err != nil {
				t.Fatal(err)
			}
		}
		if err := h.s.RunFor(2 * time.Second); err != nil {
			t.Fatal(err)
		}
		if _, onDisk := h.s.Disk(h0).Peek(name0); onDisk {
			t.Fatalf("shard %s stored on %s after crash+restart+absence race", name0, h0)
		}
	})
}

func TestConfirmOracleSoundness(t *testing.T) {
	t.Run("fails_unknown_version_no_manifest", func(t *testing.T) {
		h, _ := lossHarness(t, 3, storage.Layout{K: 2, M: 1, StripeSize: 1024}, 0)
		lk := occ.TagKey("g", "missing", "~lost/deadbeef00000001")
		lv, err := h.node(h.ids[0]).OCC().SetScalar(lk, []byte("unrecoverable"))
		if err != nil {
			t.Fatal(err)
		}
		h.onCommitLoss(h.ids[0], lk, lv)
		if err := h.s.Err(); err == nil || !strings.Contains(err.Error(), "a version no node ever committed") {
			t.Fatalf("oracle should fail unknown version, got: %v", err)
		}
	})

	t.Run("fails_different_version_than_live_manifest", func(t *testing.T) {
		h, _ := lossHarness(t, 3, storage.Layout{K: 2, M: 1, StripeSize: 1024}, 0)
		_, _, man := putEverywhere(t, h, 700)
		wrongVer := fmt.Sprintf("%016x", uint64(man.Version)^0x1)
		lk := occ.TagKey("g", "k", "~lost/"+wrongVer)
		lv, err := h.node(h.ids[0]).OCC().SetScalar(lk, []byte("unrecoverable"))
		if err != nil {
			t.Fatal(err)
		}
		h.onCommitLoss(h.ids[0], lk, lv)
		if err := h.s.Err(); err == nil || !strings.Contains(err.Error(), "a version no node ever committed") {
			t.Fatalf("oracle should fail mismatched version %s (live=%s), got: %v", wrongVer, man.Version, err)
		}
	})

	t.Run("fails_false_declaration_on_healthy_migrated_version", func(t *testing.T) {
		cfg := smallConfig()
		cfg.Layout = storage.Layout{K: 2, M: 1, StripeSize: 1024}
		cfg.ViewSettle = 2 * time.Second
		h, err := newHarness(1, 9, cfg, fixedNet())
		if err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunFor(5 * time.Second); err != nil {
			t.Fatal(err)
		}
		if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: "m", Region: "r0"}); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
			t.Fatal(err)
		}
		if _, err := h.syncPut(t, h.ids[0], storage.PutRequest{Bucket: "m", Key: "k", Data: []byte("healthy-migrated-object")}); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
			t.Fatal(err)
		}
		mi, err := h.node(h.ids[0]).StartMigration("m", "r1")
		if err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunUntilConverged(h.s.Now().Add(60*time.Second), func() bool {
			st, err := h.node(h.ids[0]).Migration(mi.ID)
			return err == nil && st.State == saga.StateCompleted.String()
		}); err != nil {
			t.Fatal(err)
		}
		if err := h.s.RunUntilConverged(h.s.Now().Add(20*time.Second), h.converged); err != nil {
			t.Fatal(err)
		}
		rd, err := h.node(h.ids[0]).OCC().Reader()
		if err != nil {
			t.Fatal(err)
		}
		val, _, err := rd.Get(occ.ManifestKey("m", "k"))
		if err != nil {
			t.Fatal(err)
		}
		man, err := manifestOf(val)
		if err != nil {
			t.Fatal(err)
		}
		if h.mans[man.Version.String()] != nil {
			t.Fatalf("expected migrated version %s not to be in h.mans", man.Version)
		}
		for i := 0; i < 2; i++ {
			ref := man.Stripes[0].Shards[i]
			ak := node.AbsentKeyForTest(man.ShardID(0, i), ref.Node)
			if _, err := h.node(h.ids[0]).OCC().SetScalar(ak, []byte{1}); err != nil {
				t.Fatal(err)
			}
			h.node(h.ids[0]).RecordMeta(ak)
		}
		runErr := h.s.RunFor(10 * time.Second)
		if runErr == nil {
			t.Fatal("oracle failed to catch false loss declaration on a healthy migrated version!")
		}
		if !strings.Contains(runErr.Error(), "while every stripe still had at least k=2 verifiable shards") {
			t.Fatalf("expected converse oracle failure on healthy migrated version, got: %v", runErr)
		}
	})
}

var _ = errors.Is
