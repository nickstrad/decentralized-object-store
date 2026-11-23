package noderuntime_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/node"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/sim"
	"github.com/nickstrad/object-store/internal/storage"
)

type driver interface {
	size() int
	eventually(what string, cond func() bool) error
	members(i int) ([]storage.MemberInfo, error)
	create(i int, spec storage.BucketSpec) error
	put(i int, req storage.PutRequest) (storage.ObjectInfo, error)
	get(i int, bucket, key string) ([]byte, error)
	tag(i int, bucket, key, name, value string, c storage.Condition) (storage.TagInfo, error)
	tags(i int, bucket, key string) ([]storage.TagInfo, error)
}

type scenario struct {
	name string
	run  func(d driver) error
}

func allJoined(d driver) func() bool {
	return func() bool {
		for i := range d.size() {
			ms, err := d.members(i)
			if err != nil {
				return false
			}
			alive := 0
			for _, m := range ms {
				if m.State == "alive" {
					alive++
				}
			}
			if alive != d.size() {
				return false
			}
		}
		return true
	}
}

func withBucket(d driver, name string) error {
	if err := d.eventually("every node sees every other", allJoined(d)); err != nil {
		return err
	}
	if err := d.create(0, storage.BucketSpec{Name: name}); err != nil {
		return err
	}
	return d.eventually("the bucket reaches every node", func() bool {
		for i := range d.size() {
			if _, err := d.tags(i, name, "absent"); errors.Is(err, storage.ErrNoSuchBucket) {
				return false
			}
		}
		return true
	})
}

var scenarios = []scenario{
	{"join", func(d driver) error { return d.eventually("every node sees every other", allJoined(d)) }},
	{"put-get-across-nodes", func(d driver) error {
		if err := withBucket(d, "cf"); err != nil {
			return err
		}
		data := bytes.Repeat([]byte("conformance"), 3000)
		if _, err := d.put(0, storage.PutRequest{Bucket: "cf", Key: "k", Data: data}); err != nil {
			return err
		}
		return d.eventually("the last node reads the object", func() bool {
			got, err := d.get(d.size()-1, "cf", "k")
			return err == nil && bytes.Equal(got, data)
		})
	}},
	{"conditional-write-race", func(d driver) error {
		if err := withBucket(d, "cw"); err != nil {
			return err
		}
		first, err := d.put(1, storage.PutRequest{Bucket: "cw", Key: "k", Data: []byte("v1"), Cond: storage.Condition{IfNoneMatch: true}})
		if err != nil {
			return err
		}
		if _, err := d.put(1, storage.PutRequest{Bucket: "cw", Key: "k", Data: []byte("v2"), Cond: storage.Condition{IfNoneMatch: true}}); !errors.Is(err, occ.ErrPreconditionFailed) {
			return fmt.Errorf("second create-only put: %v", err)
		}
		if _, err := d.put(1, storage.PutRequest{Bucket: "cw", Key: "k", Data: []byte("v3"), Cond: storage.Condition{IfMatch: first.ETag}}); err != nil {
			return fmt.Errorf("put matching the current etag: %w", err)
		}
		if _, err := d.put(1, storage.PutRequest{Bucket: "cw", Key: "k", Data: []byte("v4"), Cond: storage.Condition{IfMatch: first.ETag}}); !errors.Is(err, occ.ErrPreconditionFailed) {
			return fmt.Errorf("put matching a replaced etag: %v", err)
		}
		return d.eventually("every node reads the winner", func() bool {
			for i := range d.size() {
				if got, err := d.get(i, "cw", "k"); err != nil || string(got) != "v3" {
					return false
				}
			}
			return true
		})
	}},
	{"metadata-convergence", func(d driver) error {
		if err := withBucket(d, "mc"); err != nil {
			return err
		}
		if _, err := d.put(0, storage.PutRequest{Bucket: "mc", Key: "k", Data: []byte("x")}); err != nil {
			return err
		}
		if err := d.eventually("the object reaches every node", func() bool {
			for i := range d.size() {
				if _, err := d.tags(i, "mc", "k"); err != nil {
					return false
				}
			}
			return true
		}); err != nil {
			return err
		}
		for i := range d.size() {
			if _, err := d.tag(i, "mc", "k", "owner", fmt.Sprint(i), storage.Condition{}); err != nil {
				return err
			}
		}
		return d.eventually("every node holds the same single tag version", func() bool {
			var first string
			for i := range d.size() {
				ts, err := d.tags(i, "mc", "k")
				if err != nil || len(ts) != 1 {
					return false
				}
				if i == 0 {
					first = ts[0].ETag
				} else if ts[0].ETag != first {
					return false
				}
			}
			return true
		})
	}},
}

type simCluster struct {
	s   *sim.Sim
	ids []env.NodeID
}

func newSimCluster(seed int64, n int, cfg node.Config) (*simCluster, error) {
	table, err := node.CodeTable()
	if err != nil {
		return nil, err
	}
	c := &simCluster{s: sim.New(sim.Config{Seed: seed, Codes: table, Net: sim.DefaultNetConfig()}), ids: sim.NodeIDs(n)}
	cfg.Seeds = c.ids
	if err := c.s.AddCluster(c.ids, 3, node.NewFactory(cfg)); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *simCluster) node(id env.NodeID) *node.Node {
	n, _ := c.s.Process(id).(*node.Node)
	return n
}

type simDriver struct{ h *simCluster }

func (d simDriver) size() int { return len(d.h.ids) }

func (d simDriver) eventually(what string, cond func() bool) error {
	if err := d.h.s.RunUntilConverged(d.h.s.Now().Add(60*time.Second), cond); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

func (d simDriver) members(i int) ([]storage.MemberInfo, error) {
	n := d.h.node(d.h.ids[i])
	if n == nil || !n.Ready() {
		return nil, node.ErrNotReady
	}
	return n.Members(), nil
}

func (d simDriver) create(i int, spec storage.BucketSpec) error {
	return d.h.node(d.h.ids[i]).CreateBucket(spec)
}

func (d simDriver) wait(done *bool) error {
	return d.h.s.RunUntilConverged(d.h.s.Now().Add(30*time.Second), func() bool { return *done })
}

func (d simDriver) put(i int, req storage.PutRequest) (storage.ObjectInfo, error) {
	var (
		oi   storage.ObjectInfo
		err  error
		done bool
	)
	d.h.node(d.h.ids[i]).PutObject(req, func(o storage.ObjectInfo, e error) { oi, err, done = o, e, true })
	if werr := d.wait(&done); werr != nil {
		return oi, werr
	}
	return oi, err
}

func (d simDriver) get(i int, bucket, key string) ([]byte, error) {
	var (
		data []byte
		err  error
		done bool
	)
	d.h.node(d.h.ids[i]).GetObject(bucket, key, func(_ storage.ObjectInfo, b []byte, e error) { data, err, done = b, e, true })
	if werr := d.wait(&done); werr != nil {
		return nil, werr
	}
	return data, err
}

func (d simDriver) tag(i int, bucket, key, name, value string, c storage.Condition) (storage.TagInfo, error) {
	return d.h.node(d.h.ids[i]).PutTag(bucket, key, name, value, c)
}

func (d simDriver) tags(i int, bucket, key string) ([]storage.TagInfo, error) {
	return d.h.node(d.h.ids[i]).Tags(bucket, key)
}

type realDriver struct {
	t *testing.T
	c *realCluster
}

func (d realDriver) size() int { return len(d.c.rts) }

func (d realDriver) eventually(what string, cond func() bool) error {
	deadline := time.Now().Add(30 * time.Second)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for !cond() {
		if time.Now().After(deadline) {
			return fmt.Errorf("%s: not within 30s", what)
		}
		<-tick.C
	}
	return nil
}

func (d realDriver) ctx() context.Context { return context.Background() }

func (d realDriver) members(i int) ([]storage.MemberInfo, error) { return d.c.rts[i].Members(d.ctx()) }
func (d realDriver) create(i int, spec storage.BucketSpec) error {
	return d.c.rts[i].CreateBucket(d.ctx(), spec)
}
func (d realDriver) put(i int, req storage.PutRequest) (storage.ObjectInfo, error) {
	return d.c.rts[i].PutObject(d.ctx(), req)
}
func (d realDriver) get(i int, bucket, key string) ([]byte, error) {
	_, b, err := d.c.rts[i].GetObject(d.ctx(), bucket, key)
	return b, err
}
func (d realDriver) tag(i int, bucket, key, name, value string, c storage.Condition) (storage.TagInfo, error) {
	return d.c.rts[i].PutTag(d.ctx(), bucket, key, name, value, c)
}
func (d realDriver) tags(i int, bucket, key string) ([]storage.TagInfo, error) {
	return d.c.rts[i].Tags(d.ctx(), bucket, key)
}

// TestConformanceOnTheSimulator proves join, cross-node PUT/GET, conditional
// writes, and concurrent tag convergence pass against Node on the simulator.
func TestConformanceOnTheSimulator(t *testing.T) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			cfg := realConfig()
			h, err := newSimCluster(1, 3, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := sc.run(simDriver{h: h}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestConformanceOnLoopback proves the same four conformance scenarios pass
// against the exact same Node code running on real loopback sockets.
func TestConformanceOnLoopback(t *testing.T) {
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			c := startReal(t, 3, realConfig())
			if err := sc.run(realDriver{t: t, c: c}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

var _ = env.ErrClosed
