package e2e

import (
	"errors"
	"fmt"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

func TestIndependentTerminalErrors(t *testing.T) {
	for _, op := range []string{"put", "get", "tag", "delete"} {
		t.Run(fmt.Sprintf("C7/unexpected-%s-error/seed=801001", op), func(t *testing.T) {
			c := newSimCluster(801001, defaultSimOptions())
			n := newNemesis(c)
			n.pending = []*Call{{Op: op, Bucket: "w", Key: "k00", Err: storage.ErrCorrupt, done: true}}
			if err := n.settle(); err == nil {
				t.Fatalf("seed=801001 single completed %s with ErrCorrupt accepted by nemesis.settle; expected rejection of an unexpected terminal error", op)
			}
		})
	}
}

func TestIndependentWire(t *testing.T) {
	for _, seed := range []int64{801011, 801013} {
		t.Run(fmt.Sprintf("C11/mux-rejections/seed=%d", seed), func(t *testing.T) {
			runSim(t, seed, defaultSimOptions(), func(c *simCluster) error {
				requests := [][]byte{nil, {0}, {6}, {255}, {5}, {5, 255}, {5, 1, 255}, {5, 2, 255}, {5, 3, 255}}
				for _, nd := range c.nodes {
					for _, req := range requests {
						done := false
						var got error
						c.cli["r0"].Net.Request(env.NodeID(nd.ID), req, time.Second, func(_ []byte, e error) { done = true; got = e })
						if _, e := c.Await("malformed request finishes", 2*time.Second, func() (bool, error) { return done, nil }); e != nil {
							return e
						}
						if got == nil {
							return fmt.Errorf("node %s accepted malformed frame %x", nd.ID, req)
						}
					}
				}
				if err := createBucket(c, simBounds, storage.BucketSpec{Name: "afterwire"}); err != nil {
					return err
				}
				if err := waitAll(c, c.Put(c.nodes[0], "afterwire", "ok", []byte("still works"), storage.Condition{})); err != nil {
					return err
				}
				return c.ledger.Wrong()
			})
		})
	}
}

func TestIndependentTombstone(t *testing.T) {
	for _, seed := range []int64{811001, 811003} {
		t.Run(fmt.Sprintf("C6/stale-restart-after-gc/seed=%d", seed), func(t *testing.T) {
			runSim(t, seed, defaultSimOptions(), func(c *simCluster) error {
				if e := createBucket(c, simBounds, storage.BucketSpec{Name: "stale"}); e != nil {
					return e
				}
				p := c.Put(c.nodes[0], "stale", "deleted", []byte("must not reappear"), storage.Condition{})
				if e := waitAll(c, p); e != nil {
					return e
				}
				if _, e := c.Await("replicated before crash", simBounds.converge, c.Converged); e != nil {
					return e
				}
				victim := c.nodes[8]
				if e := c.Crash(victim); e != nil {
					return e
				}
				if e := waitAll(c, c.Delete(c.nodes[0], "stale", "deleted", storage.Condition{IfMatch: p.Info.ETag})); e != nil {
					return e
				}
				if e := c.Sleep(90 * time.Second); e != nil {
					return e
				}
				if e := c.Restart(victim); e != nil {
					return e
				}
				if _, e := c.Await("stale node caught up", simBounds.converge, func() (bool, error) {
					if !c.allReady() {
						return false, nil
					}
					return c.Converged()
				}); e != nil {
					return e
				}
				for _, nd := range c.nodes {
					g := c.Get(nd, "stale", "deleted")
					if e := c.Wait(g); e != nil {
						return e
					}
					if !errors.Is(g.Err, occ.ErrNotFound) {
						return fmt.Errorf("deleted key on %s returned %v instead of not found", nd.ID, g.Err)
					}
				}
				q := c.Put(victim, "stale", "deleted", []byte("new generation"), storage.Condition{IfNoneMatch: true})
				if e := waitAll(c, q); e != nil {
					return e
				}
				if _, e := c.Await("new generation replicated", simBounds.converge, c.Converged); e != nil {
					return e
				}
				for _, nd := range c.nodes {
					g := c.Get(nd, "stale", "deleted")
					if e := waitAll(c, g); e != nil {
						return e
					}
					if string(g.Data) != "new generation" {
						return fmt.Errorf("old bytes resurrected on %s", nd.ID)
					}
				}
				_, _, e := c.finalChecks(true)
				return e
			})
		})
	}
}
