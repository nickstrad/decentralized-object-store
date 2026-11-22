package node_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

func TestBucketMetadataAPIOnOneNode(t *testing.T) {
	h := smallCluster(t, 2)
	id := h.ids[0]
	n := h.node(id)
	if err := n.CreateBucket(storage.BucketSpec{Name: "m", QuotaObjects: 3}); err != nil {
		t.Fatal(err)
	}
	if err := n.CreateBucket(storage.BucketSpec{Name: "m"}); !errors.Is(err, storage.ErrBucketExists) {
		t.Fatalf("second create: %v", err)
	}
	for _, bad := range []storage.BucketSpec{{Name: "UP"}, {Name: "ok", Region: strings.Repeat("r", 300)}} {
		if err := n.CreateBucket(bad); !errors.Is(err, storage.ErrInvalid) {
			t.Errorf("create %+v: %v", bad, err)
		}
	}
	ai, err := n.PutACL("m", "alice", "rw", storage.Condition{IfNoneMatch: true})
	if err != nil || ai.Perm != "rw" {
		t.Fatalf("acl %+v %v", ai, err)
	}
	if got, err := n.ACL("m", "alice"); err != nil || got.Perm != "rw" || got.ETag != ai.ETag {
		t.Fatalf("read acl %+v %v", got, err)
	}
	if _, err := n.PutACL("m", "alice", "rwa", storage.Condition{IfMatch: ai.ETag}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		principal, perm string
		cond            storage.Condition
		want            error
	}{
		{"alice", "rx", storage.Condition{}, storage.ErrInvalid},
		{"", "r", storage.Condition{}, storage.ErrInvalid},
		{"alice", "r", storage.Condition{IfMatch: ai.ETag}, occ.ErrPreconditionFailed},
		{"alice", "r", storage.Condition{IfMatch: "zz"}, storage.ErrInvalid},
		{"alice", "r", storage.Condition{IfMatch: "x", IfNoneMatch: true}, storage.ErrInvalid},
	} {
		if _, err := n.PutACL("m", c.principal, c.perm, c.cond); !errors.Is(err, c.want) {
			t.Errorf("acl %q %q %+v: %v, want %v", c.principal, c.perm, c.cond, err, c.want)
		}
	}
	if _, err := n.ACL("m", "bob"); !errors.Is(err, occ.ErrNotFound) {
		t.Fatalf("missing acl: %v", err)
	}
	if _, err := n.ACL("nob", "bob"); !errors.Is(err, storage.ErrNoSuchBucket) {
		t.Fatalf("acl of a missing bucket: %v", err)
	}
	if _, err := n.PutACL("nob", "bob", "r", storage.Condition{}); !errors.Is(err, storage.ErrNoSuchBucket) {
		t.Fatalf("acl on a missing bucket: %v", err)
	}
	q, err := n.SetQuota("m", 500, 2)
	if err != nil || q.LimitBytes != 500 || q.LimitObjects != 2 {
		t.Fatalf("set quota %+v %v", q, err)
	}
	if _, err := n.SetQuota("nob", 1, 1); !errors.Is(err, storage.ErrNoSuchBucket) {
		t.Fatalf("quota of a missing bucket: %v", err)
	}
	for i := range 3 {
		_, err := h.syncPut(t, id, storage.PutRequest{Bucket: "m", Key: fmt.Sprintf("p/%d", i), Data: []byte("x")})
		if i < 2 && err != nil {
			t.Fatal(err)
		}
		if i == 2 && !errors.Is(err, occ.ErrQuotaExceeded) {
			t.Fatalf("third object over an object quota: %v", err)
		}
	}
	list, err := n.ListObjects("m", "p/", 1)
	if err != nil || len(list) != 1 || list[0].Key != "p/0" {
		t.Fatalf("list %+v %v", list, err)
	}
	if list, err := n.ListObjects("m", "zz", 0); err != nil || len(list) != 0 {
		t.Fatalf("list with no match %+v %v", list, err)
	}
	if _, err := n.ListObjects("nob", "", 0); !errors.Is(err, storage.ErrNoSuchBucket) {
		t.Fatalf("list of a missing bucket: %v", err)
	}
	if _, err := n.PutTag("m", "p/0", "~system", "v", storage.Condition{}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("a system tag name: %v", err)
	}
	if _, err := n.PutTag("m", "missing", "t", "v", storage.Condition{}); !errors.Is(err, occ.ErrNotFound) {
		t.Fatalf("a tag on a missing object: %v", err)
	}
	if _, err := n.Tags("m", "missing"); !errors.Is(err, occ.ErrNotFound) {
		t.Fatalf("tags of a missing object: %v", err)
	}
	ti, err := n.PutTag("m", "p/0", "t", "v", storage.Condition{IfNoneMatch: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.PutTag("m", "p/0", "t", "w", storage.Condition{IfNoneMatch: true}); !errors.Is(err, occ.ErrPreconditionFailed) {
		t.Fatalf("create-only tag twice: %v", err)
	}
	if _, err := n.PutTag("m", "p/0", "t", "w", storage.Condition{IfMatch: "*"}); err != nil {
		t.Fatalf("tag if-match *: %v", err)
	}
	if _, err := n.PutTag("m", "p/0", "u", "w", storage.Condition{IfMatch: "*"}); !errors.Is(err, occ.ErrPreconditionFailed) {
		t.Fatalf("tag if-match * on a missing tag: %v", err)
	}
	if _, err := n.PutTag("m", "p/0", "t", "x", storage.Condition{IfMatch: ti.ETag}); !errors.Is(err, occ.ErrPreconditionFailed) {
		t.Fatalf("tag with a replaced etag: %v", err)
	}
	if _, err := n.DeleteObject("m", "p/0", storage.Condition{IfMatch: "zz"}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("delete with a malformed etag: %v", err)
	}
	if _, err := n.DeleteObject("m", "", storage.Condition{}); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("delete with an empty key: %v", err)
	}
	if _, err := n.DeleteObject("nob", "k", storage.Condition{}); !errors.Is(err, storage.ErrNoSuchBucket) {
		t.Fatalf("delete in a missing bucket: %v", err)
	}
	if _, err := n.HeadObject("m", strings.Repeat("k", storage.MaxKeyLen+1)); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("head with an overlong key: %v", err)
	}
	if _, err := n.StartMigration("m", "r1"); !errors.Is(err, node.ErrNoMigration) {
		t.Fatalf("migration of an unrouted bucket: %v", err)
	}
	if err := n.CreateBucket(storage.BucketSpec{Name: "routed", Region: "r0"}); err != nil {
		t.Fatal(err)
	}
	if _, err := n.StartMigration("routed", "r0"); !errors.Is(err, node.ErrNoMigration) {
		t.Fatalf("migration to the current region: %v", err)
	}
	if _, err := n.StartMigration("nob", "r1"); !errors.Is(err, storage.ErrNoSuchBucket) {
		t.Fatalf("migration of a missing bucket: %v", err)
	}
	if _, err := n.Migration("nope"); !errors.Is(err, saga.ErrSagaNotFound) {
		t.Fatalf("unknown migration: %v", err)
	}
	if _, err := h.syncPut(t, id, storage.PutRequest{Bucket: "m", Key: "big", Data: make([]byte, node.DefaultMaxObjectSize+1)}); !errors.Is(err, env.ErrTooLarge) {
		t.Fatalf("an object over the node limit: %v", err)
	}
	if _, _, err := h.syncGet(t, id, "m", ""); !errors.Is(err, storage.ErrInvalid) {
		t.Fatalf("get with an empty key: %v", err)
	}
	if hl := n.Health(); !hl.Ready || len(hl.Counters) == 0 || hl.Node != string(id) {
		t.Fatalf("health %+v", hl)
	}
	if n.ID() != id || n.Region() == "" || n.Storage() == nil || n.Meta() == nil || n.Engine() == nil || n.Stopped() {
		t.Fatal("accessors")
	}
}

func TestConfigValidationRejectsEachBadField(t *testing.T) {
	ok := smallConfig()
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		name string
		mut  func(*node.Config)
	}{
		{"an invalid layout", func(c *node.Config) { c.Layout = storage.Layout{K: 0, M: 1, StripeSize: 10} }},
		{"a negative duration", func(c *node.Config) { c.GCGrace = -time.Second }},
		{"a merkle depth out of range", func(c *node.Config) { c.MetaDepth = 99 }},
		{"a negative scrub batch", func(c *node.Config) { c.ScrubBatch = -1 }},
		{"an object size over the limit", func(c *node.Config) { c.MaxObjectSize = node.MaxObjectSizeLimit + 1 }},
		{"an ack quorum outside [k, k+m]", func(c *node.Config) { c.AckQuorum = 9 }},
		{"an invalid saga policy", func(c *node.Config) {
			c.SagaPolicy = saga.Policy{MaxAttempts: -1, BaseDelay: 1, MaxDelay: 1, StepTimeout: 1}
		}},
		{"an invalid gossip setting", func(c *node.Config) { c.Gossip.ProbeTimeout = time.Hour * 2 }},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			c := smallConfig()
			r.mut(&c)
			if err := c.Validate(); !errors.Is(err, node.ErrConfig) {
				t.Fatalf("got %v", err)
			}
		})
	}
	if _, err := node.New(env.Env{ID: "x"}, ok); !errors.Is(err, node.ErrConfig) {
		t.Fatalf("an incomplete environment: %v", err)
	}
	bad := ok
	bad.ScrubBatch = -1
	p := node.NewFactory(bad)(env.Env{ID: "x"})
	if f, isFailed := p.(interface{ Err() error }); !isFailed || !errors.Is(f.Err(), node.ErrConfig) {
		t.Fatal("a factory with an invalid config did not yield a failed process")
	}
	p.Start()
	p.OnDatagram("y", nil)
	var got error
	p.OnRequest("y", nil, func(_ []byte, err error) { got = err })
	if !errors.Is(got, node.ErrConfig) || len(p.EncodeState()) == 0 {
		t.Fatalf("failed process reply %v", got)
	}
}

func TestRuntimeConfigValidation(t *testing.T) {
	base := node.RuntimeConfig{ID: "h:1", Region: "r", DataDir: "/d", Node: smallConfig()}
	if err := base.Validate(); err != nil {
		t.Fatal(err)
	}
	rows := []func(*node.RuntimeConfig){
		func(c *node.RuntimeConfig) { c.ID = "" },
		func(c *node.RuntimeConfig) { c.Region = env.Region(strings.Repeat("r", 256)) },
		func(c *node.RuntimeConfig) { c.DataDir = "" },
		func(c *node.RuntimeConfig) { c.ControlQueue = -1 },
		func(c *node.RuntimeConfig) { c.ID = "nohostport" },
		func(c *node.RuntimeConfig) { c.Listen = "bad" },
		func(c *node.RuntimeConfig) { c.Node.ScrubBatch = -1 },
	}
	for i, mut := range rows {
		c := base
		mut(&c)
		if err := c.Validate(); !errors.Is(err, node.ErrConfig) {
			t.Errorf("row %d: %v", i, err)
		}
		if _, err := node.NewRuntime(c); err == nil {
			t.Errorf("row %d: runtime built", i)
		}
	}
}

func TestRecordDecodersRejectMalformedBytes(t *testing.T) {
	for _, b := range [][]byte{nil, {9}, {1}, {1, 0x80}} {
		for i, err := range node.DecodeRecordsForTest(b) {
			if !errors.Is(err, node.ErrMalformed) && !errors.Is(err, storage.ErrCorrupt) {
				t.Errorf("decoder %d on %x: %v", i, b, err)
			}
		}
	}
	if errs := node.DecodeRecordsForTest(append(node.EncodeSetForTest([]string{"a"}), 1)); !errors.Is(errs[3], node.ErrMalformed) {
		t.Errorf("a set with a trailing byte: %v", errs[3])
	}
	if errs := node.DecodeRecordsForTest(node.EncodeStagedForTest("s", []byte{1, 2})[:5]); !errors.Is(errs[2], node.ErrMalformed) {
		t.Errorf("a truncated staged record: %v", errs[2])
	}
	if errs := node.DecodeRecordsForTest([]byte{1, 0, 9}); !errors.Is(errs[0], storage.ErrCorrupt) {
		t.Errorf("an object record with a broken manifest: %v", errs[0])
	}
}

func TestNodeRefusesMalformedAndPostStopTraffic(t *testing.T) {
	h := smallCluster(t, 3)
	n := h.node(h.ids[0])
	reply := func(req []byte) error {
		var got error
		n.OnRequest("x", req, func(_ []byte, err error) { got = err })
		return got
	}
	for _, req := range [][]byte{nil, {99}, {5, 99}, {5, node.OpMetaFetch}, {5, node.OpPointer}, {5, node.OpPurge, 1, 'b'}, append(node.NodeRequestForTest(node.OpPurge, "b", "r0"), 0x80)} {
		if err := reply(req); !errors.Is(err, node.ErrMalformed) {
			t.Errorf("request %x: %v", req, err)
		}
	}
	if err := reply(node.NodeRequestForTest(node.OpMetaFetch, "\x05\x01b\x01k")); !errors.Is(err, env.ErrNotFound) {
		t.Fatalf("fetch of a missing key: %v", err)
	}
	before := n.Stats().Malformed
	n.OnDatagram("x", nil)
	n.OnDatagram("x", []byte{99})
	n.OnDatagram("x", []byte{2, 1})
	if n.Stats().Malformed < before+2 {
		t.Fatal("malformed datagrams were not counted")
	}
	if n.RunningWorkers() == 0 {
		t.Fatal("a running node reports no worker")
	}
	n.Stop()
	n.Stop()
	if n.RunningWorkers() != 0 || n.Inventories() != 0 || !n.Stopped() {
		t.Fatalf("a stopped node still runs %d workers", n.RunningWorkers())
	}
	if err := reply([]byte{3, 1}); !errors.Is(err, node.ErrStopped) {
		t.Fatalf("request after stop: %v", err)
	}
	n.OnDatagram("x", []byte{1})
	if _, err := n.HeadObject("b", "k"); !errors.Is(err, node.ErrStopped) {
		t.Fatalf("head after stop: %v", err)
	}
	if _, err := n.Migration("x"); !errors.Is(err, node.ErrStopped) {
		t.Fatalf("migration after stop: %v", err)
	}
}

func TestBucketMigratingAwayStopsItsWorkers(t *testing.T) {
	cfg := partitionConfig()
	cfg.ViewSettle = 2 * time.Second
	h, err := newHarness(4, dstNodes, cfg, sim.DefaultNetConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	n0 := h.node(h.ids[0])
	if err := n0.CreateBucket(storage.BucketSpec{Name: "mv", Region: "r0"}); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		if _, err := h.syncPut(t, h.ids[0], storage.PutRequest{Bucket: "mv", Key: fmt.Sprint(i), Data: make([]byte, 5000)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.s.RunFor(3 * time.Second); err != nil {
		t.Fatal(err)
	}
	holders := 0
	for _, id := range h.region("r0") {
		holders += h.node(id).Inventories()
	}
	if holders == 0 {
		t.Fatal("no source node runs an inventory for the bucket")
	}
	mi, err := n0.StartMigration("mv", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunUntilConverged(h.s.Now().Add(60*time.Second), func() bool {
		st, err := n0.Migration(mi.ID)
		return err == nil && st.State == saga.StateCompleted.String()
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.RunFor(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	for _, id := range h.region("r0") {
		if c := h.node(id).Inventories(); c != 0 {
			t.Errorf("source node %s still runs %d inventories after the bucket moved away", id, c)
		}
	}
	for _, n := range h.up() {
		n.Stop()
		if n.RunningWorkers() != 0 {
			t.Errorf("node %s runs %d workers after Stop", n.ID(), n.RunningWorkers())
		}
	}
}
