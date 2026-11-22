package e2e

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/gossip"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

const (
	simRegions     = 3
	simStripe      = 64 << 10
	simCallTimeout = 20 * time.Second
	simAwaitStep   = 20 * time.Millisecond
	simBoot        = 6 * time.Second
)

type simOptions struct {
	nodes  int
	cfg    node.Config
	net    sim.NetConfig
	skew   time.Duration
	driftP int64
	wrap   func(id env.NodeID, e env.Env) env.Env
	tweak  func(cfg *node.Config)
}

func simConfig() node.Config {
	return node.Config{
		Layout:         storage.Layout{K: 4, M: 2, StripeSize: simStripe},
		RequestTimeout: 500 * time.Millisecond,
		GCGrace:        10 * time.Second,
		OrphanGrace:    60 * time.Second,
		ScrubBatch:     16,
		MaintainPeriod: time.Second,
		RecountSettle:  3 * time.Second,
		RPCTimeout:     500 * time.Millisecond,
		ViewSettle:     2 * time.Second,
		Gossip:         gossip.Config{ProbeInterval: 250 * time.Millisecond, ProbeTimeout: 100 * time.Millisecond, SyncInterval: 2 * time.Second},
	}
}

func defaultSimOptions() simOptions {
	return simOptions{nodes: 21, cfg: simConfig(), net: sim.DefaultNetConfig()}
}

type simCluster struct {
	s       *sim.Sim
	ids     []env.NodeID
	nodes   []Node
	cli     map[string]env.Env
	cfg     node.Config
	o       *simOracle
	ledger  *Ledger
	moved   map[byte]uint64
	opts    simOptions
	calls   int
	regions []string
}

func newSimCluster(seed int64, opts simOptions) *simCluster {
	table, err := node.CodeTable()
	c := &simCluster{cli: map[string]env.Env{}, ledger: newLedger(), moved: map[byte]uint64{}, opts: opts}
	if err != nil {
		c.s = sim.New(sim.Config{Seed: seed})
		c.s.Fail(err)
		return c
	}
	c.s = sim.New(sim.Config{Seed: seed, Codes: table, Net: opts.net})
	c.ids = sim.NodeIDs(opts.nodes)
	c.o = newSimOracle(c)
	cfg := opts.cfg
	cfg.Seeds = []env.NodeID{c.ids[0], c.ids[1], c.ids[2]}
	if opts.tweak != nil {
		opts.tweak(&cfg)
	}
	c.cfg = cfg
	skewRand := sim.NewStream(seed, "e2e/clock")
	for r := range simRegions {
		c.regions = append(c.regions, fmt.Sprintf("r%d", r))
	}
	for i, id := range c.ids {
		spec := sim.NodeSpec{ID: id, Region: env.Region(c.regions[i%simRegions])}
		if opts.skew > 0 {
			spec.Skew = time.Duration(skewRand.Int64N(int64(2*opts.skew))) - opts.skew
		}
		if opts.driftP > 0 {
			spec.DriftPPM = skewRand.Int64N(2*opts.driftP) - opts.driftP
		}
		if err := c.s.AddNode(spec, c.factory); err != nil {
			c.s.Fail(err)
			return c
		}
		c.nodes = append(c.nodes, Node{ID: string(id), Region: string(spec.Region)})
	}
	for _, r := range c.regions {
		cid := env.NodeID("client-" + r)
		if err := c.s.AddNode(sim.NodeSpec{ID: cid, Region: env.Region(r)}, func(e env.Env) env.Process {
			c.cli[r] = e
			return &clientProc{e: e}
		}); err != nil {
			c.s.Fail(err)
			return c
		}
	}
	c.s.AddInvariant("e2e/no-get-returns-wrong-bytes", 1, c.ledger.Wrong)
	c.s.AddInvariant("e2e/oracle", 1, c.o.err)
	return c
}

func (c *simCluster) factory(e env.Env) env.Process {
	cfg := c.cfg
	id := e.ID
	cfg.CommitSink = func(key string, held occ.Record, v occ.Version) { c.o.commit(id, key, held, v) }
	cfg.DiscardSink = func(key string, w, l occ.Version) { c.o.discard(id, key, w, l) }
	e = e.WithDisk(watchDisk{Disk: e.Disk, id: id, o: c.o})
	e = e.WithNet(countNet{Transport: e.Net, moved: c.moved})
	if c.opts.wrap != nil {
		e = c.opts.wrap(id, e)
	}
	return &apiNode{p: node.NewFactory(cfg)(e), onPut: c.o.notePut}
}

type countNet struct {
	env.Transport
	moved map[byte]uint64
}

func (n countNet) Request(to env.NodeID, req []byte, timeout time.Duration, cb func([]byte, error)) {
	ch := byte(0)
	if len(req) > 0 {
		ch = req[0]
	}
	n.moved[ch] += uint64(len(req))
	n.Transport.Request(to, req, timeout, func(resp []byte, err error) {
		n.moved[ch] += uint64(len(resp))
		cb(resp, err)
	})
}

func (c *simCluster) node(id env.NodeID) *node.Node {
	if c.s.Err() != nil && c.s.Process(id) == nil {
		return nil
	}
	a, _ := c.s.Process(id).(*apiNode)
	if a == nil {
		return nil
	}
	st := c.s.Status(id)
	if !st.Up || st.Paused {
		return nil
	}
	n, _ := a.p.(*node.Node)
	return n
}

func (c *simCluster) anyNode(id env.NodeID) *node.Node {
	a, _ := c.s.Process(id).(*apiNode)
	if a == nil {
		return nil
	}
	n, _ := a.p.(*node.Node)
	return n
}

func (c *simCluster) upNodes() []*node.Node {
	var out []*node.Node
	for _, id := range c.ids {
		if n := c.node(id); n != nil {
			out = append(out, n)
		}
	}
	return out
}

func (c *simCluster) Tier() string             { return "S" }
func (c *simCluster) Nodes() []Node            { return slices.Clone(c.nodes) }
func (c *simCluster) Regions() []string        { return slices.Clone(c.regions) }
func (c *simCluster) Layout() storage.Layout   { return c.cfg.Layout }
func (c *simCluster) Rand() *rand.Rand         { return c.s.WorkloadRand() }
func (c *simCluster) Now() time.Duration       { return time.Duration(c.s.Now()) }
func (c *simCluster) Ledger() *Ledger          { return c.ledger }
func (c *simCluster) Up(n Node) bool           { return c.node(env.NodeID(n.ID)) != nil }
func (c *simCluster) Err() error               { return c.s.Err() }
func (c *simCluster) fail(err error)           { c.s.Fail(err) }
func (c *simCluster) sim() *sim.Sim            { return c.s }
func (c *simCluster) ready() bool              { return c.allReady() }
func (c *simCluster) idOf(n Node) env.NodeID   { return env.NodeID(n.ID) }
func (c *simCluster) nodeOf(id env.NodeID) Node { return c.nodes[slices.Index(c.ids, id)] }

func (c *simCluster) allReady() bool {
	for _, id := range c.ids {
		n := c.node(id)
		if n == nil || !n.Ready() {
			return false
		}
	}
	return true
}

func (c *simCluster) call(k *Call, op byte, a apiArgs, data []byte) *Call {
	req, err := encodeCall(op, a, data)
	if err != nil {
		k.Err, k.done = err, true
		return k
	}
	e, ok := c.cli[k.At.Region]
	if !ok {
		k.Err, k.done = fmt.Errorf("e2e: no client in region %q", k.At.Region), true
		return k
	}
	c.calls++
	c.s.At(c.s.Now(), "client "+k.Op+" "+k.Bucket+"/"+k.Key+" via "+k.At.ID, func() {
		k.Began = time.Duration(c.s.Now())
		e.Net.Request(env.NodeID(k.At.ID), req, simCallTimeout, func(resp []byte, err error) {
			k.Ended = time.Duration(c.s.Now())
			k.done = true
			if err == nil {
				var r apiResult
				r, k.Data, err = decodeResult(resp)
				k.Info, k.Infos, k.Tag, k.ACL, k.Quota, k.Mig = r.Info, r.Infos, r.Tag, r.ACL, r.Quota, r.Mig
			}
			k.Err = err
			c.ledger.settle(k)
			c.o.settled(k)
		})
	})
	return k
}

func (c *simCluster) CreateBucket(at Node, spec storage.BucketSpec) *Call {
	return c.call(&Call{Op: "create", At: at, Bucket: spec.Name}, opCreate, apiArgs{Spec: spec}, nil)
}

func (c *simCluster) Put(at Node, bucket, key string, data []byte, cond storage.Condition) *Call {
	k := &Call{Op: "put", At: at, Bucket: bucket, Key: key}
	c.ledger.attempt(k, data)
	return c.call(k, opPut, apiArgs{Bucket: bucket, Key: key, Cond: cond}, data)
}

func (c *simCluster) Get(at Node, bucket, key string) *Call {
	return c.call(&Call{Op: "get", At: at, Bucket: bucket, Key: key}, opGet, apiArgs{Bucket: bucket, Key: key}, nil)
}

func (c *simCluster) Delete(at Node, bucket, key string, cond storage.Condition) *Call {
	return c.call(&Call{Op: "delete", At: at, Bucket: bucket, Key: key}, opDelete, apiArgs{Bucket: bucket, Key: key, Cond: cond}, nil)
}

func (c *simCluster) List(at Node, bucket string) *Call {
	return c.call(&Call{Op: "list", At: at, Bucket: bucket}, opList, apiArgs{Bucket: bucket}, nil)
}

func (c *simCluster) PutTag(at Node, bucket, key, name, value string, cond storage.Condition) *Call {
	return c.call(&Call{Op: "tag", At: at, Bucket: bucket, Key: key}, opPutTag, apiArgs{Bucket: bucket, Key: key, Name: name, Value: value, Cond: cond}, nil)
}

func (c *simCluster) PutACL(at Node, bucket, principal, perm string, cond storage.Condition) *Call {
	return c.call(&Call{Op: "acl", At: at, Bucket: bucket, Key: principal}, opPutACL, apiArgs{Bucket: bucket, Principal: principal, Perm: perm, Cond: cond}, nil)
}

func (c *simCluster) SetQuota(at Node, bucket string, bytes, objects uint64) *Call {
	return c.call(&Call{Op: "quota-set", At: at, Bucket: bucket}, opSetQuota, apiArgs{Bucket: bucket, Bytes: bytes, Objects: objects}, nil)
}

func (c *simCluster) Quota(at Node, bucket string) *Call {
	return c.call(&Call{Op: "quota", At: at, Bucket: bucket}, opQuota, apiArgs{Bucket: bucket}, nil)
}

func (c *simCluster) StartMigration(at Node, bucket, to string) *Call {
	return c.call(&Call{Op: "migrate", At: at, Bucket: bucket}, opStartMigration, apiArgs{Bucket: bucket, To: to}, nil)
}

func (c *simCluster) Migration(at Node, id string) *Call {
	return c.call(&Call{Op: "migration", At: at, Key: id}, opMigration, apiArgs{ID: id}, nil)
}

func (c *simCluster) Wait(calls ...*Call) error {
	done := func() bool {
		for _, k := range calls {
			if !k.done {
				return false
			}
		}
		return true
	}
	if err := c.s.RunUntilConverged(c.s.Now().Add(simCallTimeout+5*time.Second), done); err != nil {
		return err
	}
	return c.s.Err()
}

func (c *simCluster) Sleep(d time.Duration) error {
	if err := c.s.RunFor(d); err != nil {
		return err
	}
	return c.s.Err()
}

func (c *simCluster) Await(what string, bound time.Duration, cond func() (bool, error)) (time.Duration, error) {
	start := c.s.Now()
	last := env.Time(-1)
	var cerr error
	err := c.s.RunUntilConverged(start.Add(bound), func() bool {
		if last >= 0 && c.s.Now().Sub(last) < simAwaitStep {
			return false
		}
		last = c.s.Now()
		ok, e := cond()
		cerr = e
		return ok || e != nil
	})
	if err == nil {
		err = cerr
	}
	if err != nil {
		err = fmt.Errorf("e2e: %s within %v: %w", what, bound, err)
		c.s.Fail(err)
		return c.s.Now().Sub(start), err
	}
	return c.s.Now().Sub(start), nil
}

func (c *simCluster) apply(f sim.Fault) error {
	if err := c.s.Apply(f); err != nil {
		err = fmt.Errorf("e2e: %v: %w", f, err)
		c.s.Fail(err)
		return err
	}
	return nil
}

func (c *simCluster) PartitionRegion(r string) error {
	return c.apply(sim.Fault{Kind: sim.FaultPartitionRegion, Region: env.Region(r)})
}

func (c *simCluster) Partition(side, other []Node) error {
	ids := func(ns []Node) []env.NodeID {
		var out []env.NodeID
		for _, n := range ns {
			out = append(out, env.NodeID(n.ID))
		}
		return out
	}
	return c.apply(sim.Fault{Kind: sim.FaultPartition, Side: ids(side), Other: ids(other)})
}

func (c *simCluster) Heal() error { return c.apply(sim.Fault{Kind: sim.FaultHealNet}) }

func (c *simCluster) Crash(n Node) error {
	return c.apply(sim.Fault{Kind: sim.FaultCrash, Node: env.NodeID(n.ID)})
}

func (c *simCluster) Restart(n Node) error {
	return c.apply(sim.Fault{Kind: sim.FaultRestart, Node: env.NodeID(n.ID)})
}

func (c *simCluster) Wipe(n Node) error {
	return c.apply(sim.Fault{Kind: sim.FaultWipeRejoin, Node: env.NodeID(n.ID)})
}

func (c *simCluster) Shards(n Node) ([]ShardFile, error) {
	d := c.s.Disk(env.NodeID(n.ID))
	if d == nil {
		return nil, fmt.Errorf("e2e: no disk for %s", n.ID)
	}
	var out []ShardFile
	for _, name := range d.Names() {
		if !strings.HasPrefix(name, "shards/") {
			continue
		}
		if b, ok := d.Peek(name); ok {
			out = append(out, ShardFile{Name: name, Blob: b})
		}
	}
	return out, nil
}

func (c *simCluster) Rot(n Node, name string) error {
	return c.apply(sim.Fault{Kind: sim.FaultBitRot, Node: env.NodeID(n.ID), Blob: name})
}

func (c *simCluster) Health(n Node) (storage.Health, error) {
	nd := c.node(env.NodeID(n.ID))
	if nd == nil {
		return storage.Health{}, fmt.Errorf("e2e: health of %s: %w", n.ID, storage.ErrUnavailable)
	}
	return nd.Health(), nil
}

func (c *simCluster) Members(n Node) ([]storage.MemberInfo, error) {
	nd := c.node(env.NodeID(n.ID))
	if nd == nil {
		return nil, fmt.Errorf("e2e: members of %s: %w", n.ID, storage.ErrUnavailable)
	}
	return nd.Members(), nil
}

func (c *simCluster) Converged() (bool, error) {
	ns := c.upNodes()
	if len(ns) == 0 {
		return false, nil
	}
	var want []env.NodeID
	for _, n := range ns {
		want = append(want, n.ID())
	}
	var dg [32]byte
	var root [32]byte
	for i, n := range ns {
		if !n.Ready() {
			return false, nil
		}
		var got []env.NodeID
		for _, m := range n.Gossip().Members() {
			got = append(got, m.ID)
		}
		if !slices.Equal(got, want) {
			return false, nil
		}
		d, r := n.OCC().Digest(), n.Meta().Tree().Root()
		if i == 0 {
			dg, root = d, r
		} else if d != dg || r != root {
			return false, nil
		}
	}
	return true, nil
}

func (c *simCluster) metaConverged() bool {
	ns := c.upNodes()
	if len(ns) == 0 {
		return false
	}
	d := ns[0].OCC().Digest()
	for _, n := range ns {
		if !n.Ready() || n.OCC().Digest() != d {
			return false
		}
	}
	return true
}

func (c *simCluster) boot() error {
	if err := c.s.RunFor(simBoot); err != nil {
		return err
	}
	_, err := c.Await("cluster formed", 30*time.Second, c.Converged)
	return err
}
