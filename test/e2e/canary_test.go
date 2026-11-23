package e2e

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/saga/sagasim"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

const (
	chOCCByte  byte = 3
	chMetaByte byte = 4
)

type deafNet struct {
	env.Transport
	clock env.Clock
	deaf  func() bool
}

func metaChannel(msg []byte) bool {
	return len(msg) > 0 && (msg[0] == chOCCByte || msg[0] == chMetaByte)
}

func (d deafNet) Send(to env.NodeID, msg []byte) error {
	if d.deaf() && metaChannel(msg) {
		return nil
	}
	return d.Transport.Send(to, msg)
}

func (d deafNet) Request(to env.NodeID, req []byte, timeout time.Duration, cb func([]byte, error)) {
	if d.deaf() && metaChannel(req) {
		d.clock.AfterFunc(0, func() { cb(nil, fmt.Errorf("e2e canary: deaf to %s: %w", to, env.ErrUnreachable)) })
		return
	}
	d.Transport.Request(to, req, timeout, cb)
}

type deafProc struct {
	env.Process
	deaf func() bool
}

func (p deafProc) OnDatagram(from env.NodeID, msg []byte) {
	if p.deaf() && metaChannel(msg) {
		return
	}
	p.Process.OnDatagram(from, msg)
}

func (p deafProc) OnRequest(from env.NodeID, req []byte, reply env.Reply) {
	if p.deaf() && metaChannel(req) {
		reply(nil, env.ErrUnreachable)
		return
	}
	p.Process.OnRequest(from, req, reply)
}

type ignoreShardDeletes struct{ env.Disk }

func (d ignoreShardDeletes) Delete(name string) error {
	if strings.HasPrefix(name, "shards/") {
		return nil
	}
	return d.Disk.Delete(name)
}

type skipStep struct {
	saga.Step
	do   bool
	comp bool
}

func (s skipStep) Do(c saga.Call, done func([]byte, error)) {
	if s.do {
		done([]byte(saga.OutcomeMoved), nil)
		return
	}
	s.Step.Do(c, done)
}

func (s skipStep) Compensate(c saga.Call, done func(error)) {
	if s.comp {
		done(nil)
		return
	}
	s.Step.Compensate(c, done)
}

func plant(d saga.Definition, name string, do, comp bool) saga.Definition {
	steps := make([]saga.Step, len(d.Steps))
	copy(steps, d.Steps)
	for i, st := range steps {
		if st.Name() == name {
			steps[i] = skipStep{Step: st, do: do, comp: comp}
		}
	}
	d.Steps = steps
	return d
}

func checkCanaryFailure(err error, want string) error {
	if strings.TrimSpace(want) == "" {
		return fmt.Errorf("canary helper called with empty error match string")
	}
	if err == nil {
		return fmt.Errorf("the scenario passed with the planted fault on")
	}
	if !strings.Contains(err.Error(), want) {
		return fmt.Errorf("the scenario failed for another reason than the planted fault (want %q): %v", want, err)
	}
	return nil
}

func expectCanaryFailure(t *testing.T, s *sim.Sim, want string) {
	t.Helper()
	err := s.Err()
	if cerr := checkCanaryFailure(err, want); cerr != nil {
		t.Fatal(cerr)
	}
	t.Logf("caught: %v", err)
}

func TestExpectCanaryFailureRejectsEmptyMatch(t *testing.T) {
	if err := checkCanaryFailure(fmt.Errorf("anything"), ""); err == nil {
		t.Fatal("checkCanaryFailure accepted an empty match string")
	}
	if err := checkCanaryFailure(fmt.Errorf("anything"), "   "); err == nil {
		t.Fatal("checkCanaryFailure accepted a whitespace-only match string")
	}
	if err := checkCanaryFailure(nil, "expected reason"); err == nil {
		t.Fatal("checkCanaryFailure accepted a nil error")
	}
	if err := checkCanaryFailure(fmt.Errorf("unrelated failure"), "expected reason"); err == nil {
		t.Fatal("checkCanaryFailure accepted an unrelated error")
	}
	if err := checkCanaryFailure(fmt.Errorf("hit expected reason here"), "expected reason"); err != nil {
		t.Fatalf("checkCanaryFailure rejected matching error: %v", err)
	}
}

func runCanary(seed int64, opts simOptions, body func(c *simCluster) error) *sim.Sim {
	c := newSimCluster(seed, opts)
	if c.s.Err() != nil {
		return c.s
	}
	if err := c.boot(); err != nil {
		c.fail(err)
		return c.s
	}
	if err := body(c); err != nil {
		c.fail(err)
	}
	return c.s
}

func TestCanaryE1NodesThatMisreportTheirRegionFailTheSpreadCheck(t *testing.T) {
	opts := defaultSimOptions()
	opts.wrap = func(_ env.NodeID, e env.Env) env.Env {
		e.Region = "r0"
		return e
	}
	s := runCanary(1, opts, func(c *simCluster) error {
		_, err := scenarioE1(c, simBounds, e1Params{sizes: []int{7, 4093, simStripe + 1}})
		return err
	})
	expectCanaryFailure(t, s, "more than m=")
}

func TestCanaryE2ANodeDeafToAntiEntropyNeverConverges(t *testing.T) {
	opts := defaultSimOptions()
	var deafID env.NodeID = "n004"
	healed := false
	deaf := func() bool { return healed }
	opts.wrap = func(id env.NodeID, e env.Env) env.Env {
		if id != deafID {
			return e
		}
		return e.WithNet(deafNet{Transport: e.Net, clock: e.Clock, deaf: deaf})
	}
	c := newSimCluster(1, opts)
	c.onHeal = func() { healed = true }
	c.procWrap = func(id env.NodeID, p env.Process) env.Process {
		if id != deafID {
			return p
		}
		return deafProc{Process: p, deaf: deaf}
	}
	if err := c.boot(); err != nil {
		t.Fatal(err)
	}
	if _, err := scenarioE2(c, simBounds, e2Params{objects: 4, size: 3000, detect: 500 * time.Millisecond, keepCut: time.Second}); err != nil {
		c.fail(err)
	}
	expectCanaryFailure(t, c.s, "after the heal: not within")
}

func TestCanaryE3RepairThatNeverRunsMissesTheBound(t *testing.T) {
	opts := defaultSimOptions()
	opts.tweak = func(cfg *node.Config) { cfg.MaintainPeriod = time.Hour }
	s := runCanary(1, opts, func(c *simCluster) error {
		_, err := scenarioE3(c, simBounds, e3Params{objects: 16, damaged: 2, size: 3*simStripe + 100})
		return err
	})
	expectCanaryFailure(t, s, "rebuilt: not within")
}

func TestCanaryE4AMigrationThatSkipsDeletingTheSourceFails(t *testing.T) {
	opts := defaultSimOptions()
	opts.tweak = func(cfg *node.Config) {
		cfg.WrapMigration = func(d saga.Definition) saga.Definition { return plant(d, saga.StepDeleteSource, true, false) }
	}
	s := runCanary(1, opts, func(c *simCluster) error {
		_, err := scenarioE4(c, simBounds, e4Params{objects: 4, size: 2*simStripe + 5})
		return err
	})
	expectCanaryFailure(t, s, "no shard of the bucket left in r0")
}

func TestCanaryE5ACopyThatIsNeverCompensatedLeavesOrphans(t *testing.T) {
	var cell sagasim.Cell
	found := false
	for _, c := range sagasim.Cells() {
		if c.Step == 2 && c.Want() == saga.StateCompensated && !c.Torn && !c.Double && !strings.Contains(c.Kind.String(), "crash") {
			cell, found = c, true
			break
		}
	}
	if !found {
		t.Fatal("no compensated verify cell in the matrix")
	}
	h := plantedE5{}
	s := sagasim.Run(h, cell, sagasim.RunOptions{Settle: 8 * time.Second})(1)
	expectCanaryFailure(t, s, "still holds a staged entry")
}

type plantedE5 struct{}

func (plantedE5) Build(seed int64, o sagasim.BuildOptions) (*sim.Sim, sagasim.Cluster, error) {
	wrap := o.Wrap
	o.Wrap = func(d saga.Definition) saga.Definition { return wrap(plant(d, saga.StepCopy, false, true)) }
	return e5Harness{}.Build(seed, o)
}

func TestCanaryE6ARestartedNodeDeafToAntiEntropyNeverCatchesUp(t *testing.T) {
	opts := defaultSimOptions()
	starts := map[env.NodeID]int{}
	opts.wrap = func(id env.NodeID, e env.Env) env.Env {
		starts[id]++
		if id != "n004" || starts[id] < 2 {
			return e
		}
		return e.WithNet(deafNet{Transport: e.Net, clock: e.Clock, deaf: func() bool { return true }})
	}
	c := newSimCluster(1, opts)
	c.procWrap = func(id env.NodeID, p env.Process) env.Process {
		if id != "n004" || starts[id] < 2 {
			return p
		}
		return deafProc{Process: p, deaf: func() bool { return true }}
	}
	if err := c.boot(); err != nil {
		t.Fatal(err)
	}
	if _, err := scenarioE6(c, simBounds, e6Params{perWave: 4, size: 20000, down: 2 * time.Second, settle: 30 * time.Millisecond}); err != nil {
		c.fail(err)
	}
	expectCanaryFailure(t, c.s, "rejoined with a higher incarnation and caught up its metadata: not within")
}

func TestCanaryE7ShardsThatAreNeverCollectedFailTheOrphanCheck(t *testing.T) {
	opts := e7Options()
	opts.wrap = func(_ env.NodeID, e env.Env) env.Env { return e.WithDisk(ignoreShardDeletes{Disk: e.Disk}) }
	var st nemesisStats
	s := runNemesis(3, opts, func(n *nemesis) nemesisPlan { return randomPlan(n.r, 2, e7Chaos) }, &st)(3)
	expectCanaryFailure(t, s, "grace periods")
}

func TestCanaryE8ADeafSideNeverReachesOneVersionPerKey(t *testing.T) {
	opts := defaultSimOptions()
	healed := false
	deaf := func() bool { return healed }
	opts.wrap = func(id env.NodeID, e env.Env) env.Env {
		if id != "n003" {
			return e
		}
		return e.WithNet(deafNet{Transport: e.Net, clock: e.Clock, deaf: deaf})
	}
	c := newSimCluster(1, opts)
	c.onHeal = func() { healed = true }
	c.procWrap = func(id env.NodeID, p env.Process) env.Process {
		if id != "n003" {
			return p
		}
		return deafProc{Process: p, deaf: deaf}
	}
	if err := c.boot(); err != nil {
		t.Fatal(err)
	}
	_, err := scenarioE8(c, simBounds, e8Params{objects: 4, detect: 500 * time.Millisecond})
	if err != nil {
		c.fail(err)
	}
	expectCanaryFailure(t, c.s, "one version of every key on every replica after the heal: not within")
}

func TestCanaryE7WipingTheOnlyCopyOfAnAcknowledgedWriteIsCaught(t *testing.T) {
	const seed = 166963932986536
	var st nemesisStats
	s := runNemesis(seed, e7Options(), func(n *nemesis) nemesisPlan {
		n.loneWipe = true
		return randomPlan(n.r, e7Cycles, e7Chaos)
	}, &st)(seed)
	expectCanaryFailure(t, s, "on no discard record")
}

type dropBucketOnCopyStep struct {
	saga.Step
	cluster func() *simCluster
	bucket  string
}

func (s dropBucketOnCopyStep) Do(call saga.Call, done func([]byte, error)) {
	if c := s.cluster(); c != nil {
		for _, nd := range c.upNodes() {
			rd, err := nd.OCC().Reader()
			if err != nil {
				continue
			}
			for _, k := range rd.Keys() {
				if len(k) == 0 || occ.Kind(k[0]) != occ.KindManifest || !rd.Record(k).Live() {
					continue
				}
				v, ok := rd.Record(k).Version()
				if !ok {
					continue
				}
				man := manifestOf(k, v)
				if man != nil && man.Bucket == s.bucket {
					_, _ = nd.DeleteObject(man.Bucket, man.Key, storage.Condition{})
				}
			}
		}
	}
	s.Step.Do(call, done)
}

func TestCanaryE7CompletedMigrationMissingPreExistingShardsFails(t *testing.T) {
	const seed = 2
	var cl *simCluster
	opts := e7Options()
	opts.tweak = func(cfg *node.Config) {
		cfg.WrapMigration = func(d saga.Definition) saga.Definition {
			steps := make([]saga.Step, len(d.Steps))
			copy(steps, d.Steps)
			for i, st := range steps {
				if st.Name() == saga.StepCopy {
					steps[i] = dropBucketOnCopyStep{Step: st, cluster: func() *simCluster { return cl }, bucket: bucketM}
				}
			}
			d.Steps = steps
			return d
		}
	}
	c := newSimCluster(seed, opts)
	cl = c
	if err := c.boot(); err != nil {
		t.Fatal(err)
	}
	n := newNemesis(c)
	if err := n.run(randomPlan(n.r, e7Cycles, e7Chaos)); err != nil {
		c.fail(err)
	}
	expectCanaryFailure(t, c.s, "left pre-existing object")
}

func TestCanaryFuzzRoutedBucketCannotPlaceShards(t *testing.T) {
	old := fuzzOptions()
	old.nodes = 9
	old.cfg.Layout = storage.Layout{K: 4, M: 2, StripeSize: simStripe}
	t.Run("warm-up fails immediately on unplaceable routed bucket", func(t *testing.T) {
		_, err := checkFuzzCorpus(old, fuzzWarmM)
		if err == nil {
			t.Fatal("checkFuzzCorpus passed with 9-node 4+2 layout")
		}
		want := "fault-free warm-up put to routed bucket m"
		if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "unavailable") {
			t.Fatalf("failed for another reason than unplaceable routed bucket: %v", err)
		}
		t.Logf("caught: %v", err)
	})
	t.Run("corpus check catches zero acknowledged puts to routed bucket without warm-up", func(t *testing.T) {
		_, err := checkFuzzCorpus(old, 0)
		if err == nil {
			t.Fatal("checkFuzzCorpus(warmM=0) passed with 9-node 4+2 layout")
		}
		want := "acknowledged 0 traffic puts to routed bucket m"
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("failed for another reason: %v", err)
		}
		t.Logf("caught: %v", err)
	})
}

func TestCanaryNemesisForgottenAmbiguousMigrationFails(t *testing.T) {
	const seed = 702
	t.Run("2-cycle run fails with no migration when timed-out start is forgotten", func(t *testing.T) {
		s, _, err := verifyAmbiguousMigrationTracked(seed, 2, true)
		if err == nil {
			t.Fatal("ambiguous migration run passed with forgetAmbiguousMig enabled")
		}
		if s.Err() == nil || !strings.Contains(err.Error(), "bucket cannot migrate") {
			t.Fatalf("failed for another reason than forgotten ambiguous migration: %v", err)
		}
		t.Logf("caught: %v", err)
	})
	t.Run("1-cycle tracking assertions catch forgotten ambiguous start", func(t *testing.T) {
		_, _, err := verifyAmbiguousMigrationTracked(seed, 1, true)
		if err == nil {
			t.Fatal("1-cycle ambiguous migration check passed with forgetAmbiguousMig enabled")
		}
		want := "ambiguous migration start was not recovered"
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("failed for another reason than untracked ambiguous start: %v", err)
		}
		t.Logf("caught: %v", err)
	})
}

func TestCanaryE2ObjectOpsUnavailableDuringSplitFails(t *testing.T) {
	cases := []struct {
		name      string
		blockPut  bool
		blockGet  bool
		wantError string
	}{
		{name: "put and get unavailable during split", blockPut: true, blockGet: true, wantError: "must succeed"},
		{name: "only get unavailable during split", blockPut: false, blockGet: true, wantError: "get e2/"},
		{name: "only put unavailable during split", blockPut: true, blockGet: false, wantError: "put e2/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newSimCluster(1, defaultSimOptions())
			c.apiIntercept = func(n *node.Node, op byte) error {
				if len(n.Gossip().Members()) >= len(c.ids) {
					return nil
				}
				if tc.blockPut && op == opPut {
					return storage.ErrUnavailable
				}
				if tc.blockGet && op == opGet {
					return storage.ErrUnavailable
				}
				return nil
			}
			if err := c.boot(); err != nil {
				t.Fatal(err)
			}
			if _, err := scenarioE2(c, simBounds, e2Params{objects: 6, size: 3000, detect: 500 * time.Millisecond, keepCut: 2 * time.Second}); err != nil {
				c.fail(err)
			}
			expectCanaryFailure(t, c.s, tc.wantError)
		})
	}
}

type faultyDiscardHealthCluster struct {
	*simCluster
	mutate func(h *storage.Health)
}

func (f faultyDiscardHealthCluster) Health(n Node) (storage.Health, error) {
	h, err := f.simCluster.Health(n)
	if err == nil && f.mutate != nil {
		f.mutate(&h)
	}
	return h, err
}

func TestCanaryE2AndE8UnrecordedDiscardCounterFails(t *testing.T) {
	cases := []struct {
		name   string
		run    func(c Cluster) error
		mutate func(h *storage.Health)
		want   string
	}{
		{
			name: "E2 discards logged but Health.Discards is zero",
			run: func(c Cluster) error {
				_, err := scenarioE2(c, simBounds, e2Params{objects: 4, size: 3000, detect: 500 * time.Millisecond, keepCut: time.Second})
				return err
			},
			mutate: func(h *storage.Health) { h.Discards = 0 },
			want:   "Health.Discards=0",
		},
		{
			name: "E8 discards logged but Health.Discards is zero",
			run: func(c Cluster) error {
				_, err := scenarioE8(c, simBounds, e8Params{objects: 4, detect: 500 * time.Millisecond})
				return err
			},
			mutate: func(h *storage.Health) { h.Discards = 0 },
			want:   "Health.Discards=0",
		},
		{
			name: "E2 discards logged but Health.Discards undercounts RecentDiscards",
			run: func(c Cluster) error {
				_, err := scenarioE2(c, simBounds, e2Params{objects: 4, size: 3000, detect: 500 * time.Millisecond, keepCut: time.Second})
				return err
			},
			mutate: func(h *storage.Health) {
				if len(h.RecentDiscards) > 1 {
					h.Discards = 1
				}
			},
			want: "Health.Discards=1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := runCanary(1, defaultSimOptions(), func(c *simCluster) error {
				return tc.run(faultyDiscardHealthCluster{simCluster: c, mutate: tc.mutate})
			})
			expectCanaryFailure(t, s, tc.want)
		})
	}
}
