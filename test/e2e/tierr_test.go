package e2e

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

var e1SizesR = []int{0, 1, 2, 3, 7, 13, 4093, realStripe - 1, realStripe, realStripe + 1, 3*realStripe + 7, 3<<20 + 29}

func routedRealOptions() realOptions {
	return realOptions{nodes: 9, layout: storage.Layout{K: 2, M: 1, StripeSize: realStripe}}
}

func logMeasure(t *testing.T, name string, v any) { t.Logf("MEASURE %s %v", name, v) }

func TestE1MultiRegionPutGetR(t *testing.T) {
	requireTierR(t)
	c := newRealCluster(t, 1, defaultRealOptions())
	logMeasure(t, "r/startup", c.startup)
	res, err := scenarioE1(c, realBounds, e1Params{sizes: e1SizesR})
	if err != nil {
		t.Fatal(err)
	}
	logMeasure(t, "e1r/objects", res.objects)
	logMeasure(t, "e1r/reads", res.reads)
	logMeasure(t, "e1r/stripes_spread_checked", res.stripes)
	logMeasure(t, "e1r/put_max", res.putTime)
	logMeasure(t, "e1r/get_max", res.getTime)
	var small, big []time.Duration
	for _, k := range c.ledger.Acked() {
		if k.Size >= 1<<20 {
			big = append(big, k.Ended-k.Began)
		} else {
			small = append(small, k.Ended-k.Began)
		}
	}
	logMeasure(t, "e1r/put_latencies_small", small)
	logMeasure(t, "e1r/put_latencies_3MiB", big)
}

func TestE2PartitionAndHealR(t *testing.T) {
	requireTierR(t)
	c := newRealCluster(t, 2, defaultRealOptions())
	res, err := scenarioE2(c, realBounds, e2Params{objects: 6, size: 3000, detect: 500 * time.Millisecond, keepCut: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	logMeasure(t, "e2r/detect", res.detect)
	logMeasure(t, "e2r/converge", res.converge)
	logMeasure(t, "e2r/puts_acked", res.putsAcked)
	logMeasure(t, "e2r/puts_unavailable", res.putsUnavailable)
	logMeasure(t, "e2r/contested", res.contested)
	logMeasure(t, "e2r/discards_max_node", res.discards)
	logMeasure(t, "e2r/declared_lost", res.lost)
}

func TestE3BitRotRepairR(t *testing.T) {
	requireTierR(t)
	c := newRealCluster(t, 3, defaultRealOptions())
	res, err := scenarioE3(c, realBounds, e3Params{objects: 16, damaged: 2, size: 3*realStripe + 100})
	if err != nil {
		t.Fatal(err)
	}
	logMeasure(t, "e3r/repair_under_reads", res.repairUnderReads)
	logMeasure(t, "e3r/repair", res.repair)
	logMeasure(t, "e3r/shards_rotted", res.shardsRotted)
	logMeasure(t, "e3r/damaged_bytes", res.damagedBytes)
	logMeasure(t, "e3r/rebuilt_bytes", res.rebuiltBytes)
	logMeasure(t, "e3r/bucket_bytes", res.bucketBytes)
}

func TestE4MigrationUnderTrafficR(t *testing.T) {
	requireTierR(t)
	c := newRealCluster(t, 4, routedRealOptions())
	res, err := scenarioE4(c, realBounds, e4Params{objects: 8, size: 2*realStripe + 5})
	if err != nil {
		t.Fatal(err)
	}
	logMeasure(t, "e4r/migrate_under_traffic", res.migrate)
	logMeasure(t, "e4r/traffic", res.traffic)
	logMeasure(t, "e4r/traffic_ok", res.trafficOK)
	logMeasure(t, "e4r/traffic_busy_or_unavailable", res.trafficBusy)
	logMeasure(t, "e4r/objects_readable", res.objects)
}

func TestE6CrashRestartWipeR(t *testing.T) {
	requireTierR(t)
	c := newRealCluster(t, 6, defaultRealOptions())
	res, err := scenarioE6(c, realBounds, e6Params{perWave: 8, size: 20000, down: 3 * time.Second, settle: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	logMeasure(t, "e6r/acked", res.acked)
	logMeasure(t, "e6r/unavailable", res.failed)
	logMeasure(t, "e6r/incarnation_before_after", fmt.Sprintf("%d->%d", res.incBefore, res.incAfter))
	logMeasure(t, "e6r/rejoin_and_metadata_catch_up", res.rejoin)
	logMeasure(t, "e6r/refill", res.refill)
	logMeasure(t, "e6r/wiped_shards", res.wipedShards)
}

func TestE8LocalFirstMetadataSyncR(t *testing.T) {
	requireTierR(t)
	c := newRealCluster(t, 8, defaultRealOptions())
	res, err := scenarioE8(c, realBounds, e8Params{objects: 6, detect: 500 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	logMeasure(t, "e8r/converge", res.converge)
	logMeasure(t, "e8r/contested", res.contested)
	logMeasure(t, "e8r/discards_max_node", res.discards)
	logMeasure(t, "e8r/stale_refused", res.staleRefused)
}

type e5RResult struct {
	terminal time.Duration
	state    string
	killedAt int
}

func scenarioE5R(c *realCluster, killCoordinator bool) (e5RResult, error) {
	var res e5RResult
	b := realBounds
	const bucket = "e5"
	from, to := c.Regions()[0], c.Regions()[1]
	if err := createBucket(c, b, storage.BucketSpec{Name: bucket, Region: from}); err != nil {
		return res, err
	}
	var pre []*Call
	for i := range 24 {
		pre = append(pre, c.Put(pick(c, c.Nodes()), bucket, fmt.Sprintf("s%02d", i), randomBytes(c.Rand(), 2*realStripe+i), storage.Condition{}))
	}
	if err := waitAll(c, pre...); err != nil {
		return res, err
	}
	if _, err := c.Await("objects replicated", b.converge, c.Converged); err != nil {
		return res, err
	}
	coord := inRegion(c, c.Regions()[2])[1]
	targets := inRegion(c, to)
	victim := targets[len(targets)-1]
	start := c.StartMigration(coord, bucket, to)
	if err := waitAll(c, start); err != nil {
		return res, err
	}
	id := start.Mig.ID
	if _, err := c.Await("the saga is copying", b.migrate, func() (bool, error) {
		st := c.Migration(coord, id)
		if err := c.Wait(st); err != nil || st.Err != nil {
			return false, errors.Join(err, st.Err)
		}
		if terminal(st.Mig.State) {
			return false, failf("the saga ended %s before the copy could be interrupted", st.Mig.State)
		}
		res.killedAt = st.Mig.Step
		return st.Mig.Step >= 1, nil
	}); err != nil {
		return res, err
	}
	if err := c.BlockOneWay([]Node{coord}, targets); err != nil {
		return res, err
	}
	if killCoordinator {
		if err := c.Crash(coord); err != nil {
			return res, err
		}
		if err := c.Restart(coord); err != nil {
			return res, err
		}
		if _, err := c.Await("the coordinator back", b.converge, func() (bool, error) {
			h, err := c.Health(coord)
			return err == nil && h.Ready, nil
		}); err != nil {
			return res, err
		}
	} else if err := c.Crash(victim); err != nil {
		return res, err
	}
	t0 := c.Now()
	if _, err := c.Await("compensation under way", b.migrate, func() (bool, error) {
		st := c.Migration(coord, id)
		if err := c.Wait(st); err != nil {
			return false, err
		}
		if st.Err != nil {
			return false, nil
		}
		if st.Mig.State == "completed" {
			return false, failf("the saga completed although its coordinator could not reach %s", to)
		}
		return st.Mig.State == "compensating" || st.Mig.State == "compensated", nil
	}); err != nil {
		return res, err
	}
	if err := c.Heal(); err != nil {
		return res, err
	}
	if !killCoordinator {
		if err := c.Restart(victim); err != nil {
			return res, err
		}
	}
	mi, _, _, err := awaitMigration(c, b, coord, id, nil)
	res.terminal, res.state = c.Now()-t0, mi.State
	if err != nil {
		return res, err
	}
	if mi.State != "compensated" {
		return res, failf("the saga ended %s, want compensated", mi.State)
	}
	if _, err := c.Await("converged after compensation", b.converge, c.Converged); err != nil {
		return res, err
	}
	var left []string
	if _, err := c.Await("no shard of the bucket left in "+to, b.migrate, func() (bool, error) {
		var err error
		left, err = shardsOutside(c, bucket, from)
		return len(left) == 0, err
	}); err != nil {
		if len(left) > 0 {
			return res, failf("%d orphan shards of %s outside %s after compensation; first %s", len(left), bucket, from, left[0])
		}
		return res, err
	}
	var gets []*Call
	for _, k := range pre {
		gets = append(gets, c.Get(pick(c, c.Nodes()), bucket, k.Key))
	}
	if err := waitAll(c, gets...); err != nil {
		return res, err
	}
	for i, g := range gets {
		if sha256.Sum256(g.Data) != pre[i].Sum {
			return res, failf("source object %s changed after compensation", g.Key)
		}
	}
	if err := routedTo(c, b, bucket, from); err != nil {
		return res, err
	}
	return res, c.Ledger().Wrong()
}

func TestE5RollbackKillTargetMidCopyR(t *testing.T) {
	requireTierR(t)
	c := newRealCluster(t, 51, routedRealOptions())
	res, err := scenarioE5R(c, false)
	if err != nil {
		t.Fatal(err)
	}
	logMeasure(t, "e5r/kill_target/killed_at_step", res.killedAt)
	logMeasure(t, "e5r/kill_target/to_compensated", res.terminal)
}

func TestE5RollbackKillCoordinatorMidSagaR(t *testing.T) {
	requireTierR(t)
	c := newRealCluster(t, 52, routedRealOptions())
	res, err := scenarioE5R(c, true)
	if err != nil {
		t.Fatal(err)
	}
	logMeasure(t, "e5r/kill_coordinator/killed_at_step", res.killedAt)
	logMeasure(t, "e5r/kill_coordinator/to_compensated", res.terminal)
}

func cleanClientError(err error) bool {
	if err == nil {
		return true
	}
	var he *httpError
	if errors.As(err, &he) {
		return he.status == 503 || he.status == 504
	}
	return errors.Is(err, env.ErrUnreachable)
}

func TestE9ShutdownUnderLoadR(t *testing.T) {
	requireTierR(t)
	c := newRealCluster(t, 9, defaultRealOptions())
	if err := createBucket(c, realBounds, storage.BucketSpec{Name: "e9"}); err != nil {
		t.Fatal(err)
	}
	target := c.Nodes()[5]
	var mu sync.Mutex
	var calls []*Call
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			last := ""
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				batch := []*Call{c.Put(target, "e9", fmt.Sprintf("w%d-%04d", w, i), bytes.Repeat([]byte{byte(w), byte(i)}, 20000+i), storage.Condition{})}
				if last != "" {
					batch = append(batch, c.Get(target, "e9", last))
				}
				if err := c.Wait(batch...); err != nil {
					return
				}
				if batch[0].Err == nil {
					last = batch[0].Key
				} else {
					time.Sleep(20 * time.Millisecond)
				}
				mu.Lock()
				calls = append(calls, batch...)
				mu.Unlock()
			}
		})
	}
	time.Sleep(2 * time.Second)
	p := c.proc(target)
	sent := time.Now()
	if err := p.signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.exited:
	case <-time.After(20 * time.Second):
		t.Fatalf("%s still running 20 s after SIGTERM", target.ID)
	}
	shutdown := time.Since(sent)
	close(stop)
	wg.Wait()
	if code := p.state.ExitCode(); code != 0 {
		t.Fatalf("%s exited %d after SIGTERM under load", target.ID, code)
	}
	log := c.logOf(target)
	if !strings.Contains(log, "goroutines left at exit: 0") {
		i := strings.LastIndex(log, "goroutines left at exit")
		t.Fatalf("%s did not report zero goroutines at exit: %q", target.ID, log[max(0, i):])
	}
	ok, refused, inflightOK, inflightFailed := 0, 0, 0, 0
	at := sent.Sub(c.t0)
	for _, k := range calls {
		if !cleanClientError(k.Err) {
			t.Fatalf("%s %s during shutdown failed uncleanly: %v", k.Op, k.Key, k.Err)
		}
		inflight := k.Began <= at && k.Ended >= at
		switch {
		case k.Err == nil && inflight:
			inflightOK++
		case inflight:
			inflightFailed++
		}
		if k.Err == nil {
			ok++
		} else {
			refused++
		}
	}
	if err := c.ledger.Wrong(); err != nil {
		t.Fatal(err)
	}
	if err := c.Restart(target); err != nil {
		t.Fatal(err)
	}
	restart, err := c.Await("restarted node rejoins", realBounds.converge, c.Converged)
	if err != nil {
		t.Fatal(err)
	}
	var gets []*Call
	acked := c.ledger.Acked()
	for _, k := range acked {
		gets = append(gets, c.Get(target, "e9", k.Key))
	}
	if err := waitAll(c, gets...); err != nil {
		t.Fatal(err)
	}
	for i, g := range gets {
		if sha256.Sum256(g.Data) != acked[i].Sum {
			t.Fatalf("acknowledged %s reads other bytes after the restart", g.Key)
		}
	}
	if err := c.ledger.Wrong(); err != nil {
		t.Fatal(err)
	}
	logMeasure(t, "e9r/shutdown_under_load", shutdown)
	logMeasure(t, "e9r/in_flight_at_sigterm_finished", inflightOK)
	logMeasure(t, "e9r/in_flight_at_sigterm_failed_cleanly", inflightFailed)
	logMeasure(t, "e9r/requests_ok", ok)
	logMeasure(t, "e9r/requests_refused_cleanly", refused)
	logMeasure(t, "e9r/acked_read_after_restart", len(gets))
	logMeasure(t, "e9r/restart_to_converged", restart)
}

type hygieneRecord struct {
	PIDs  []int
	Ports []int
	Dirs  []string
}

func TestTierRHarnessLeavesNothingBehindWhenATestFails(t *testing.T) {
	requireTierR(t)
	if out := os.Getenv("E2E_CHILD_OUT"); out != "" {
		c := newRealCluster(t, 99, realOptions{nodes: 3, layout: storage.Layout{K: 2, M: 1, StripeSize: realStripe}})
		var rec hygieneRecord
		for _, p := range c.procs {
			rec.PIDs = append(rec.PIDs, p.cmd.Process.Pid)
			rec.Ports = append(rec.Ports, p.ports...)
		}
		binMu.Lock()
		rec.Dirs = append(append(rec.Dirs, c.dir), binDirs...)
		binMu.Unlock()
		b, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Fatal("planted failure with a running cluster")
	}
	out := filepath.Join(t.TempDir(), "child.json")
	cmd := exec.Command(os.Args[0], "-test.run", "^TestTierRHarnessLeavesNothingBehindWhenATestFails$", "-test.count=1")
	cmd.Env = append(os.Environ(), "E2E_CHILD_OUT="+out, tierREnv+"=1")
	b, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the child passed; its planted failure did not fire:\n%s", b)
	}
	if !strings.Contains(string(b), "planted failure") {
		t.Fatalf("the child failed for another reason: %v\n%s", err, b)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var rec hygieneRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if len(rec.PIDs) != 3 || len(rec.Ports) != 6 || len(rec.Dirs) < 2 {
		t.Fatalf("child record incomplete: %+v", rec)
	}
	for _, pid := range rec.PIDs {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("process %d still exists after the failed test: %v", pid, err)
		}
	}
	for _, port := range rec.Ports {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err != nil {
			t.Errorf("tcp port %d still taken: %v", port, err)
			continue
		}
		if err := l.Close(); err != nil {
			t.Error(err)
		}
	}
	for _, d := range rec.Dirs {
		if _, err := os.Stat(d); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("directory %s left behind: %v", d, err)
		}
	}
}

func TestDefaultBuildCarriesNoFaultInjection(t *testing.T) {
	requireTierR(t)
	plain, err := os.ReadFile(buildObjnode(t, false, ""))
	if err != nil {
		t.Fatal(err)
	}
	tagged, err := os.ReadFile(buildObjnode(t, false, "e2efault"))
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{blocklistSuffix, "noderuntime.blockedPeer", "noderuntime.BlocklistPath"} {
		if !bytes.Contains(tagged, []byte(marker)) {
			t.Fatalf("the e2efault build lacks %q, so this check could not see it", marker)
		}
		if bytes.Contains(plain, []byte(marker)) {
			t.Fatalf("the default build contains %q", marker)
		}
	}
}
