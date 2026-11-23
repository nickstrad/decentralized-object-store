package e2e

import (
	"crypto/sha256"
	"encoding/binary"
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

// deafNet drops outbound OCC and metadata-Merkle traffic (channels 3 and 4)
// while deaf returns true for anti-entropy canary tests.
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

// deafProc drops inbound OCC and metadata-Merkle traffic while deaf returns true.
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

// ignoreShardDeletes silently swallows shard Delete calls to test orphan checks.
type ignoreShardDeletes struct{ env.Disk }

func (d ignoreShardDeletes) Delete(name string) error {
	if strings.HasPrefix(name, "shards/") {
		return nil
	}
	return d.Disk.Delete(name)
}

// skipStep replaces a saga step's Do or Compensate with a no-op success.
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

// checkCanaryFailure requires a non-empty match string and fails unless err is
// non-nil and contains want, ensuring canaries fail only for their planted bug.
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

// TestExpectCanaryFailureRejectsEmptyMatch proves checkCanaryFailure rejects
// empty/blank match strings, nil errors, and unrelated errors.
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

// TestCanaryE1NodesThatMisreportTheirRegionFailTheSpreadCheck plants every node
// reporting region r0 and proves E1's spread check catches > m shards in r0.
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

// TestCanaryE2ANodeDeafToAntiEntropyNeverConverges plants n004 dropping OCC and
// metadata-Merkle traffic after the heal and proves E2 misses convergence.
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

// TestCanaryE3RepairThatNeverRunsMissesTheBound plants MaintainPeriod=1h (no
// scrub or rebuild) and proves E3 catches un-repaired rotted shards.
func TestCanaryE3RepairThatNeverRunsMissesTheBound(t *testing.T) {
	opts := defaultSimOptions()
	opts.tweak = func(cfg *node.Config) { cfg.MaintainPeriod = time.Hour }
	s := runCanary(1, opts, func(c *simCluster) error {
		_, err := scenarioE3(c, simBounds, e3Params{objects: 16, damaged: 2, size: 3*simStripe + 100})
		return err
	})
	expectCanaryFailure(t, s, "rebuilt: not within")
}

// TestCanaryE4AMigrationThatSkipsDeletingTheSourceFails plants a no-op
// StepDeleteSource and proves E4 catches source shards remaining in r0.
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

// TestCanaryE5ACopyThatIsNeverCompensatedLeavesOrphans plants a no-op StepCopy
// compensation and proves E5's CheckCompensated catches the leftover staged entry.
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

// TestCanaryE6ARestartedNodeDeafToAntiEntropyNeverCatchesUp plants n004
// dropping metadata sync on restart and proves E6 catches failed catch-up.
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

// TestCanaryE7ShardsThatAreNeverCollectedFailTheOrphanCheck plants disks that
// ignore shard deletes and proves E7's post-grace orphan check catches them.
func TestCanaryE7ShardsThatAreNeverCollectedFailTheOrphanCheck(t *testing.T) {
	opts := e7Options()
	opts.wrap = func(_ env.NodeID, e env.Env) env.Env { return e.WithDisk(ignoreShardDeletes{Disk: e.Disk}) }
	var st nemesisStats
	s := runNemesis(3, opts, func(n *nemesis) nemesisPlan { return randomPlan(n.r, 2, e7Chaos) }, &st)(3)
	expectCanaryFailure(t, s, "grace periods")
}

// TestCanaryE8ADeafSideNeverReachesOneVersionPerKey plants n003 dropping
// metadata sync after heal and proves E8 misses single-version convergence.
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

// TestCanaryE7WipingTheOnlyCopyOfAnAcknowledgedWriteIsCaught replays pinned
// seed 166963932986536 with the lone-holder wipe guard disabled and proves the
// lineage oracle flags the unrecorded loss of a write wiped with its sole disk.
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

// TestCanaryE7CompletedMigrationMissingPreExistingShardsFails tombstones
// bucket m's objects inside StepCopy so metadata and orphan checks stay clean,
// proving E7's per-key destination shard check catches missing pre-existing
// objects.
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

// TestCanaryFuzzRoutedBucketCannotPlaceShards restores the unplaceable 9-node
// 4+2 fuzz layout (3 nodes per region for 6 shards) and proves both the warm-up
// put and the corpus check reject an empty routed migrating bucket.
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

// TestCanaryNemesisForgottenAmbiguousMigrationFails enables forgetAmbiguousMig
// and proves a timed-out StartMigration that actually started a saga is caught
// by tracking assertions, a stale next-cycle region, and non-terminal saga
// checks.
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
	t.Run("forgetting slow ambiguous start leaves non-terminal saga into next cycle", func(t *testing.T) {
		var st nemesisStats
		s := slowAmbiguousMigrationRun(seed, true, &st)(seed)
		expectCanaryFailure(t, s, "still holds non-terminal saga")
	})
}

// TestCanaryE2ObjectOpsUnavailableDuringSplitFails intercepts PUT, GET, or both
// with ErrUnavailable whenever a node's view shrinks, proving E2 requires
// surviving-side object operations to succeed during a partition.
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

// TestCanaryE2AndE8UnrecordedDiscardCounterFails mutates Health.Discards to 0
// or undercounts RecentDiscards, proving E2 and E8 catch broken discard
// counters even when discards still appear in RecentDiscards.
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

type pauseBeforeStep struct {
	saga.Step
	onBefore func()
}

func (p pauseBeforeStep) Do(c saga.Call, done func([]byte, error)) {
	if p.onBefore != nil {
		p.onBefore()
	}
	p.Step.Do(c, done)
}

type unverifiedVerifyStep struct{ saga.Step }

func (unverifiedVerifyStep) Do(c saga.Call, done func([]byte, error)) {
	done(c.Outputs[1], nil)
}

type unverifiedRetireStep struct {
	saga.Step
	cluster func() *simCluster
	coord   env.NodeID
}

func stagedObjectBytes(raw []byte) ([]byte, error) {
	if len(raw) < 2 || raw[0] != 1 {
		return nil, fmt.Errorf("bad staged record version")
	}
	b := raw[1:]
	n, k := binary.Uvarint(b)
	if k <= 0 || uint64(len(b)-k) < n+uint64(len(occ.ETag{})) {
		return nil, fmt.Errorf("bad staged saga field")
	}
	b = b[k+int(n)+len(occ.ETag{}):]
	m, k := binary.Uvarint(b)
	if k <= 0 || uint64(len(b)-k) < m {
		return nil, fmt.Errorf("bad staged object field")
	}
	return b[k : k+int(m)], nil
}

func (u unverifiedRetireStep) Do(c saga.Call, done func([]byte, error)) {
	cl := u.cluster()
	nd := cl.node(u.coord)
	if nd == nil {
		done(nil, env.ErrUnreachable)
		return
	}
	m, err := saga.DecodeMigrationInput(c.Input)
	if err != nil {
		done(nil, err)
		return
	}
	rd, err := nd.OCC().Reader()
	if err != nil {
		done(nil, err)
		return
	}
	id := string(c.Key.Saga)
	for _, k := range rd.Keys() {
		if len(k) == 0 || occ.Kind(k[0]) != occ.KindTag || !rd.Record(k).Live() {
			continue
		}
		_, parts, err := occ.ParseKey(k)
		if err != nil || parts[0] != m.Bucket || parts[2] != "~stage/"+id {
			continue
		}
		val, _, err := rd.Get(k)
		if err != nil {
			continue
		}
		objBytes, err := stagedObjectBytes(val)
		if err != nil {
			done(nil, err)
			return
		}
		mk := occ.ManifestKey(m.Bucket, parts[1])
		cur := rd.Record(mk)
		v, err := nd.OCC().Put(mk, objBytes, occ.Unconditional())
		if err != nil {
			done(nil, err)
			return
		}
		cl.o.commit(u.coord, mk, cur, v)
		nd.RecordMeta(mk)
		if _, err := nd.OCC().Delete(k, occ.Unconditional()); err != nil {
			done(nil, err)
			return
		}
		nd.RecordMeta(k)
	}
	lk := occ.TagKey(m.Bucket, "", "~live")
	if _, err := nd.OCC().SetScalar(lk, []byte(m.To)); err != nil {
		done(nil, err)
		return
	}
	nd.RecordMeta(lk)
	done([]byte(saga.OutcomeMoved), nil)
}

func runStallCanarySchedule(seed int64, bypassVerify bool) *sim.Sim {
	const (
		bucket = "ms"
		from   = "r0"
		to     = "r1"
	)
	opts := fuzzOptions()
	opts.cfg.OrphanGrace = 15 * time.Second
	opts.cfg.GCGrace = 5 * time.Second
	opts.cfg.MaintainPeriod = 500 * time.Millisecond
	pauseDur := opts.cfg.OrphanGrace + 10*time.Second

	var cl *simCluster
	coordID := env.NodeID("n000")
	blockMeta := false
	paused := false

	opts.wrap = func(id env.NodeID, e env.Env) env.Env {
		if id != coordID {
			return e
		}
		return e.WithNet(deafNet{Transport: e.Net, clock: e.Clock, deaf: func() bool { return blockMeta }})
	}
	opts.tweak = func(cfg *node.Config) {
		cfg.WrapMigration = func(d saga.Definition) saga.Definition {
			steps := make([]saga.Step, len(d.Steps))
			copy(steps, d.Steps)
			for i, st := range steps {
				switch st.Name() {
				case saga.StepVerify:
					inner := st
					if bypassVerify {
						inner = unverifiedVerifyStep{Step: st}
					}
					steps[i] = pauseBeforeStep{Step: inner, onBefore: func() {
						if paused || cl == nil {
							return
						}
						paused = true
						cl.o.off = true
						if err := cl.s.Pause(coordID); err != nil {
							cl.fail(err)
							return
						}
						cl.s.At(cl.s.Now().Add(pauseDur), "resume-stall-canary", func() {
							blockMeta = false
							cl.o.off = false
							if err := cl.s.Resume(coordID); err != nil {
								cl.fail(err)
							}
						})
					}}
				case saga.StepDeleteSource:
					if bypassVerify {
						steps[i] = unverifiedRetireStep{Step: st, cluster: func() *simCluster { return cl }, coord: coordID}
					}
				}
			}
			d.Steps = steps
			return d
		}
	}

	c := newSimCluster(seed, opts)
	cl = c
	c.procWrap = func(id env.NodeID, p env.Process) env.Process {
		if id != coordID {
			return p
		}
		return deafProc{Process: p, deaf: func() bool { return blockMeta }}
	}
	if err := c.boot(); err != nil {
		c.fail(err)
		return c.s
	}
	if err := createBucket(c, simBounds, storage.BucketSpec{Name: bucket, Region: from}); err != nil {
		c.fail(err)
		return c.s
	}
	put := c.Put(c.nodes[0], bucket, "warm00", randomBytes(c.Rand(), 4096), storage.Condition{})
	if err := waitAll(c, put); err != nil {
		c.fail(err)
		return c.s
	}
	if _, err := c.Await("replicated", simBounds.converge, c.Converged); err != nil {
		c.fail(err)
		return c.s
	}
	blockMeta = true
	start := c.StartMigration(c.nodes[0], bucket, to)
	if err := waitAll(c, start); err != nil {
		c.fail(err)
		return c.s
	}
	b := simBounds
	b.migrate = pauseDur + 60*time.Second
	mi, _, _, err := awaitMigration(c, b, c.nodes[0], start.Mig.ID, nil)
	if err != nil {
		c.fail(err)
		return c.s
	}
	if !paused || !terminal(mi.State) {
		c.fail(failf("paused=%v state=%q", paused, mi.State))
		return c.s
	}
	if _, err := c.Await("converged", simBounds.converge, c.Converged); err != nil {
		c.fail(err)
		return c.s
	}
	g := c.Get(c.nodes[0], bucket, "warm00")
	if err := waitAll(c, g); err != nil {
		c.fail(err)
		return c.s
	}
	if sha256.Sum256(g.Data) != put.Sum {
		c.fail(failf("wrong bytes after stalled migration"))
		return c.s
	}
	if len(c.o.declared) > 0 {
		c.fail(failf("loss declared: %v", c.o.declared))
	}
	return c.s
}

// TestCanaryMigrationUnverifiedStallLosesObject proves the real product
// survives a coordinator stall past OrphanGrace without losing objects, while
// bypassing destination shard verification in StepVerify and StepDeleteSource
// is caught by the commit-after-delete oracle.
func TestCanaryMigrationUnverifiedStallLosesObject(t *testing.T) {
	t.Run("real product survives stall without losing object", func(t *testing.T) {
		s := runStallCanarySchedule(31337, false)
		if err := s.Verify(); err != nil {
			t.Fatalf("real product failed stall schedule: %v", err)
		}
	})
	t.Run("disabling destination shard verification via WrapMigration is caught", func(t *testing.T) {
		s := runStallCanarySchedule(31337, true)
		expectCanaryFailure(t, s, "after its shard")
	})
}
