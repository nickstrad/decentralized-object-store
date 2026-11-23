package e2e

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

type onWriteDisk struct {
	env.Disk
	id      env.NodeID
	onWrite func(id env.NodeID, name string)
}

func (d onWriteDisk) Write(name string, data []byte) error {
	err := d.Disk.Write(name, data)
	if err == nil && d.onWrite != nil {
		d.onWrite(d.id, name)
	}
	return err
}

type migStallResult struct {
	state   string
	elapsed time.Duration
}

func runMigrationLongStall(opts simOptions, pauseDur time.Duration, res *migStallResult) func(int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		const (
			bucket = "ms"
			from   = "r0"
			to     = "r1"
		)
		var c *simCluster
		var coord Node
		paused := false
		destWrites := 0
		needWrites := opts.cfg.Layout.K + opts.cfg.Layout.M

		prevWrap := opts.wrap
		opts.wrap = func(id env.NodeID, e env.Env) env.Env {
			if prevWrap != nil {
				e = prevWrap(id, e)
			}
			return e.WithDisk(onWriteDisk{Disk: e.Disk, id: id, onWrite: func(id env.NodeID, name string) {
				if c == nil || coord.ID == "" || paused {
					return
				}
				if string(c.s.Region(id)) != to || !strings.HasPrefix(name, "shards/"+bucket+"/") {
					return
				}
				destWrites++
				if destWrites >= needWrites {
					paused = true
					cid := env.NodeID(coord.ID)
					if err := c.s.Pause(cid); err != nil {
						c.fail(err)
						return
					}
					c.s.InjectAt(c.s.Now().Add(pauseDur), sim.Fault{Kind: sim.FaultResume, Node: cid})
				}
			}})
		}

		c = newSimCluster(seed, opts)
		if c.s.Err() != nil {
			return c.s
		}
		if err := c.boot(); err != nil {
			c.fail(err)
			return c.s
		}
		if err := createBucket(c, simBounds, storage.BucketSpec{Name: bucket, Region: from}); err != nil {
			c.fail(err)
			return c.s
		}
		var pre []*Call
		for i := range 2 {
			pre = append(pre, c.Put(c.nodes[i%len(c.nodes)], bucket, fmt.Sprintf("warm%02d", i), randomBytes(c.Rand(), 4096), storage.Condition{}))
		}
		if err := waitAll(c, pre...); err != nil {
			c.fail(err)
			return c.s
		}
		if _, err := c.Await("objects replicated", simBounds.converge, c.Converged); err != nil {
			c.fail(err)
			return c.s
		}

		coord = c.nodes[int(uint64(seed)%uint64(len(c.nodes)))]
		if coord.Region == to {
			for _, n := range c.nodes {
				if n.Region != to {
					coord = n
					break
				}
			}
		}

		start := c.StartMigration(coord, bucket, to)
		if err := waitAll(c, start); err != nil {
			c.fail(err)
			return c.s
		}
		b := simBounds
		b.migrate = pauseDur + 60*time.Second
		mi, _, elapsed, err := awaitMigration(c, b, coord, start.Mig.ID, nil)
		if err != nil {
			c.fail(err)
			return c.s
		}
		if !paused {
			c.fail(failf("coordinator pause trigger never fired"))
			return c.s
		}
		if !terminal(mi.State) {
			c.fail(failf("migration %s ended in unexpected state %q", start.Mig.ID, mi.State))
			return c.s
		}
		if res != nil {
			res.state = mi.State
			res.elapsed = elapsed
		}
		if _, err := c.Await("converged after stalled migration", simBounds.converge, c.Converged); err != nil {
			c.fail(err)
			return c.s
		}
		finalRegion := from
		if mi.State == "completed" {
			finalRegion = to
		}
		if len(c.o.declared) > 0 {
			c.fail(failf("stalled migration declared loss of %s", c.o.declared[0]))
			return c.s
		}
		for _, nd := range c.upNodes() {
			rd, err := nd.OCC().Reader()
			if err != nil {
				c.fail(err)
				return c.s
			}
			r, err := rd.Route(bucket)
			if err != nil || string(r) != finalRegion {
				c.fail(failf("node %s routes %s to %q (%v), want %q", nd.ID(), bucket, r, err, finalRegion))
				return c.s
			}
		}
		if _, err := c.Await("no shard of "+bucket+" outside "+finalRegion, c.cfg.OrphanGrace+c.cfg.GCGrace+15*time.Second, func() (bool, error) {
			left, err := shardsOutside(c, bucket, finalRegion)
			return len(left) == 0, err
		}); err != nil {
			c.fail(err)
			return c.s
		}
		var gets []*Call
		for _, k := range pre {
			gets = append(gets, c.Get(pick(c, c.nodes), bucket, k.Key))
		}
		if err := waitAll(c, gets...); err != nil {
			c.fail(err)
			return c.s
		}
		for i, g := range gets {
			if sha256.Sum256(g.Data) != pre[i].Sum {
				c.fail(failf("get %s/%s after stalled migration returned wrong bytes", bucket, g.Key))
				return c.s
			}
		}
		if _, _, err := c.finalChecks(false); err != nil {
			c.fail(err)
			return c.s
		}
		if len(c.o.declared) > 0 {
			c.fail(failf("stalled migration declared loss of %s", c.o.declared[0]))
		}
		return c.s
	}
}

func TestE4MigrationCoordinatorStalledPastOrphanGraceS(t *testing.T) {
	t.Parallel()
	var m measure
	t.Cleanup(func() { m.report(t) })
	sweep(t, seedsFor(t, 1, 10), func(t *testing.T, seed int64) {
		opts := fuzzOptions()
		opts.cfg.OrphanGrace = 20 * time.Second
		opts.cfg.GCGrace = 5 * time.Second
		var res migStallResult
		simtest.RequireDeterministic(t, seed, runMigrationLongStall(opts, opts.cfg.OrphanGrace+10*time.Second, &res))
		m.ms("e4stall/begin_to_terminal", res.elapsed)
		if res.state == "completed" {
			m.add("e4stall/completed", 1)
		} else {
			m.add("e4stall/compensated", 1)
		}
	})
}
