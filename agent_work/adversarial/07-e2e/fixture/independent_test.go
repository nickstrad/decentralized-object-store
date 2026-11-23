package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/saga/sagasim"

	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

func mustCheck(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestIndependentScenarios(t *testing.T) {
	for _, seed := range []int64{731991, 731993, 732001} {
		for _, claim := range []string{"C1", "C2", "C3", "C4", "C6", "C8"} {
			t.Run(fmt.Sprintf("%s/seed=%d", claim, seed), func(t *testing.T) {
				opts := defaultSimOptions()
				opts.skew = 750 * time.Millisecond
				opts.driftP = 750
				runSim(t, seed, opts, func(c *simCluster) error {
					var err error
					switch claim {
					case "C1":
						r, e := scenarioE1(c, simBounds, e1Params{sizes: []int{0, 1, 17, 65521, 65537, 131071, 2<<20 + 43}})
						err = e
						t.Logf("objects=%d reads=%d spread_stripes=%d", r.objects, r.reads, r.stripes)
					case "C2":
						r, e := scenarioE2(c, simBounds, e2Params{objects: 4, size: 65537, detect: 750 * time.Millisecond, keepCut: 3 * time.Second})
						err = e
						t.Logf("acked=%d refused=%d discarded=%d convergence=%s", r.putsAcked, r.putsUnavailable, r.contested.discarded, r.converge)
					case "C3":
						r, e := scenarioE3(c, simBounds, e3Params{objects: 20, damaged: 2, size: 2*simStripe + 307})
						err = e
						t.Logf("rotted=%d damage=%d rebuilt=%d moved=%d", r.shardsRotted, r.damagedBytes, r.rebuiltBytes, r.movedBytes)
					case "C4":
						r, e := scenarioE4(c, simBounds, e4Params{objects: 5, size: simStripe + 17})
						err = e
						t.Logf("traffic=%d readable=%d migration=%s", r.traffic, r.objects, r.migrate)
					case "C6":
						r, e := scenarioE6(c, simBounds, e6Params{perWave: 5, size: 65539, down: 5 * time.Second, settle: 11 * time.Millisecond})
						err = e
						t.Logf("acked=%d incarnation=%d->%d wiped=%d", r.acked, r.incBefore, r.incAfter, r.wipedShards)
					case "C8":
						r, e := scenarioE8(c, simBounds, e8Params{objects: 4, detect: 850 * time.Millisecond})
						err = e
						t.Logf("conditional=%d discarded=%d stale_refused=%d", r.conditional, r.contested.discarded, r.staleRefused)
					}
					if err != nil {
						return err
					}
					_, _, err = c.finalChecks(true)
					return err
				})
			})
		}
	}
}

func TestIndependentMatrix(t *testing.T) {
	for i, cell := range sagasim.Cells() {
		seed := int64(741001 + i)
		t.Run(fmt.Sprintf("C5/%s/seed=%d", cell.Name(), seed), func(t *testing.T) {
			t.Parallel()
			rep := &sagasim.Report{}
			simtest.RequireDeterministic(t, seed, sagasim.Run(e5Harness{}, cell, sagasim.RunOptions{Report: rep, Settle: 9 * time.Second}))
			if rep.State != cell.Want() {
				t.Fatalf("terminal=%v want=%v", rep.State, cell.Want())
			}
			t.Logf("terminal=%v crashes=%d elapsed=%s", rep.State, rep.Crashes, rep.Elapsed)
		})
	}
}

func TestIndependentSchedules(t *testing.T) {
	for _, seed := range []int64{751001, 751003, 751007} {
		for variant := 0; variant < 3; variant++ {
			t.Run(fmt.Sprintf("C7/schedule=%d/seed=%d", variant, seed), func(t *testing.T) {
				opts := e7Options()
				opts.skew = 1500 * time.Millisecond
				opts.driftP = 1500
				var st nemesisStats
				simtest.RequireDeterministic(t, seed, runNemesis(seed, opts, func(n *nemesis) nemesisPlan {
					var p nemesisPlan
					for i := 0; i < 8; i++ {
						var ops []chaosOp
						switch variant {
						case 0:
							ops = []chaosOp{{opMigrate, 2, 0, 0}, {opCutOneWay, i, 10, 250 * time.Millisecond}, {opCrashNode, (i + 3) % 21, 0, 500 * time.Millisecond}, {opRestartNode, (i + 3) % 21, 0, 1500 * time.Millisecond}}
						case 1:
							ops = []chaosOp{{opRotShard, i, 1, 0}, {opPauseNode, (i + 7) % 21, 0, 250 * time.Millisecond}, {opReadErrors, (i + 14) % 21, 0, 500 * time.Millisecond}, {opRestartNode, (i + 7) % 21, 0, 1750 * time.Millisecond}}
						case 2:
							ops = []chaosOp{{opWipeNode, i, 0, 0}, {opClogLink, (i + 1) % 21, (i + 2) % 21, 250 * time.Millisecond}, {opCutRegion, i % 3, 0, time.Second}}
						}
						p.cycles = append(p.cycles, cycle{chaos: 3 * time.Second, ops: ops})
					}
					return p
				}, &st))
				if st.applied == 0 || st.cycles != 8 {
					t.Fatalf("scenario did not exercise faults: %+v", st)
				}
				t.Logf("cycles=%d applied=%d skipped=%d puts_ok=%d gets_ok=%d availability_gets=%d migrations=%d compensated=%d", st.cycles, st.applied, st.skipped, st.putsOK, st.getsOK, st.availabilityGets, st.migrated, st.compensated)
			})
		}
	}
}

func TestIndependentMalformed(t *testing.T) {
	for _, seed := range []int64{761001, 761003} {
		t.Run(fmt.Sprintf("C11/seed=%d", seed), func(t *testing.T) {
			runSim(t, seed, defaultSimOptions(), func(c *simCluster) error {
				if err := createBucket(c, simBounds, storage.BucketSpec{Name: "valid"}); err != nil {
					return err
				}
				for _, key := range []string{"", strings.Repeat("x", storage.MaxKeyLen+1)} {
					k := c.Put(c.nodes[0], "valid", key, []byte("invalid"), storage.Condition{})
					if err := c.Wait(k); err != nil {
						return err
					}
					if !errors.Is(k.Err, storage.ErrInvalid) {
						return fmt.Errorf("invalid key length %d returned %v", len(key), k.Err)
					}
				}
				p := c.Put(c.nodes[0], "valid", "key", []byte{0, 255, 0, 1}, storage.Condition{})
				if err := waitAll(c, p); err != nil {
					return err
				}
				if _, err := c.Await("valid metadata replicated", simBounds.converge, c.Converged); err != nil {
					return err
				}
				for _, tag := range []string{"~lost/x", "~stage/x", "~bucket", "~live"} {
					k := c.PutTag(c.nodes[0], "valid", "key", tag, "x", storage.Condition{})
					if err := c.Wait(k); err != nil {
						return err
					}
					if !errors.Is(k.Err, storage.ErrInvalid) {
						return fmt.Errorf("reserved tag %q returned %v", tag, k.Err)
					}
				}

				for _, nd := range c.nodes {
					g := c.Get(nd, "valid", "key")
					if err := waitAll(c, g); err != nil {
						return err
					}
					if !bytes.Equal(g.Data, []byte{0, 255, 0, 1}) {
						return fmt.Errorf("wrong bytes after rejection")
					}
				}
				return nil
			})
		})
	}
}

func TestIndependentSoak(t *testing.T) {
	const seed = 771001
	t.Run("C12/fixed-key-churn/seed=771001", func(t *testing.T) {
		opts := defaultSimOptions()
		opts.cfg.OrphanGrace = 15 * time.Second
		opts.cfg.GCGrace = 3 * time.Second
		c := newSimCluster(seed, opts)
		mustCheck(t, c.boot())
		mustCheck(t, createBucket(c, simBounds, storage.BucketSpec{Name: "soak"}))
		startG := runtime.NumGoroutine()
		var baseline uint64
		for wave := 0; wave < 30; wave++ {
			for k := 0; k < 4; k++ {
				key := fmt.Sprintf("k%d", k)
				via := c.nodes[(wave+k)%len(c.nodes)]
				mustCheck(t, waitAll(c, c.Put(via, "soak", key, bytes.Repeat([]byte{byte(wave), byte(k)}, 4099), storage.Condition{})))
				mustCheck(t, waitAll(c, c.Delete(via, "soak", key, storage.Condition{})))
			}
			mustCheck(t, c.Sleep(30*time.Second))
			_, err := c.Await("soak converged", simBounds.converge, c.Converged)
			mustCheck(t, err)
			mustCheck(t, c.o.checkOrphans())
			if wave == 4 || wave == 14 || wave == 29 {
				runtime.GC()
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				rd, err := c.upNodes()[0].OCC().Reader()
				mustCheck(t, err)
				tombs := 0
				for _, key := range rd.Keys() {
					r := rd.Record(key)
					if r.Present() && !r.Live() {
						tombs++
					}
				}
				shards := 0
				diskBytes := 0
				for _, id := range c.ids {
					for _, name := range c.s.Disk(id).Names() {
						b, _ := c.s.Disk(id).Peek(name)
						diskBytes += len(b)
						if strings.HasPrefix(name, "shards/") {
							shards++
						}
					}
				}
				t.Logf("wave=%d virtual=%s heap=%d goroutines=%d records=%d tombstones=%d shards=%d disk_bytes=%d", wave+1, c.Now(), m.HeapAlloc, runtime.NumGoroutine(), len(rd.Keys()), tombs, shards, diskBytes)
				if shards != 0 {
					t.Fatalf("%d orphan shards after grace", shards)
				}
				if wave == 4 {
					baseline = m.HeapAlloc
				}
				if wave == 29 && m.HeapAlloc > baseline+128<<20 {
					t.Fatalf("heap grew over 128 MiB tolerance: %d -> %d", baseline, m.HeapAlloc)
				}
			}
		}
		if runtime.NumGoroutine() > startG+2 {
			t.Fatalf("goroutines grew %d -> %d", startG, runtime.NumGoroutine())
		}
		t.Log("30 waves and at least 900 virtual seconds completed; retained metadata measured, not assumed reclaimable")
	})
}

func TestIndependentReal(t *testing.T) {
	t.Run("C2/partition/seed=781002", func(t *testing.T) {
		c := newCheckedRealCluster(t, 781002, defaultRealOptions())
		r, e := scenarioE2(c, realBounds, e2Params{objects: 4, size: 65537, detect: 750 * time.Millisecond, keepCut: 3 * time.Second})
		mustCheck(t, e)
		t.Logf("acked=%d discarded=%d converge=%s", r.putsAcked, r.contested.discarded, r.converge)
	})
	t.Run("C4/migration/seed=781004", func(t *testing.T) {
		c := newCheckedRealCluster(t, 781004, routedRealOptions())
		r, e := scenarioE4(c, realBounds, e4Params{objects: 5, size: realStripe + 17})
		mustCheck(t, e)
		t.Logf("readable=%d traffic=%d migrate=%s", r.objects, r.traffic, r.migrate)
	})
	t.Run("C8/conditional-sync/seed=781008", func(t *testing.T) {
		c := newCheckedRealCluster(t, 781008, defaultRealOptions())
		r, e := scenarioE8(c, realBounds, e8Params{objects: 4, detect: 850 * time.Millisecond})
		mustCheck(t, e)
		t.Logf("conditional=%d discarded=%d stale_refused=%d", r.conditional, r.contested.discarded, r.staleRefused)
	})
	t.Run("C1/sizes/seed=781001", func(t *testing.T) {
		c := newCheckedRealCluster(t, 781001, defaultRealOptions())
		r, e := scenarioE1(c, realBounds, e1Params{sizes: []int{0, 1, 65521, 65537, 2<<20 + 43}})
		mustCheck(t, e)
		t.Logf("objects=%d reads=%d stripes=%d", r.objects, r.reads, r.stripes)
	})
	t.Run("C3/rot/seed=781003", func(t *testing.T) {
		c := newCheckedRealCluster(t, 781003, defaultRealOptions())
		r, e := scenarioE3(c, realBounds, e3Params{objects: 20, damaged: 2, size: 2*realStripe + 307})
		mustCheck(t, e)
		t.Logf("rotted=%d rebuilt=%d damaged=%d", r.shardsRotted, r.rebuiltBytes, r.damagedBytes)
	})
	t.Run("C6/kill-restart-wipe/seed=781006", func(t *testing.T) {
		c := newCheckedRealCluster(t, 781006, defaultRealOptions())
		r, e := scenarioE6(c, realBounds, e6Params{perWave: 5, size: 65539, down: 4 * time.Second, settle: 7 * time.Millisecond})
		mustCheck(t, e)
		t.Logf("acked=%d incarnation=%d->%d", r.acked, r.incBefore, r.incAfter)
	})
	for _, coordinator := range []bool{false, true} {
		t.Run(fmt.Sprintf("C5/rollback-coordinator=%v/seed=781005", coordinator), func(t *testing.T) {
			c := newCheckedRealCluster(t, 781005, routedRealOptions())
			r, e := scenarioE5R(c, coordinator)
			mustCheck(t, e)
			t.Logf("state=%s killed_step=%d", r.state, r.killedAt)
		})
	}
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(fmt.Sprintf("C9/signal=%d/seed=781009", sig), func(t *testing.T) {
			c := newCheckedRealCluster(t, 781009, defaultRealOptions())
			r, e := scenarioE9(c, sig)
			mustCheck(t, e)
			t.Logf("shutdown=%s read_after_restart=%d", r.shutdown, r.readAfterRestart)
		})
	}
}

func TestIndependentFullDisk(t *testing.T) {
	t.Run("C13/enospc-recovery/seed=791001", func(t *testing.T) {
		buildObjnode(t, true, "e2efault")
		var fs syscall.Statfs_t
		mustCheck(t, syscall.Statfs("/dev/shm", &fs))
		if fs.Type != 0x01021994 || fs.Blocks*uint64(fs.Bsize) > 128<<20 {
			t.Fatal("SETUP: need private container tmpfs at /dev/shm <=128 MiB")
		}
		old := os.Getenv("TMPDIR")
		mustCheck(t, os.Setenv("TMPDIR", "/dev/shm"))
		c := newCheckedRealCluster(t, 791001, defaultRealOptions())
		mustCheck(t, os.Setenv("TMPDIR", old))
		mustCheck(t, createBucket(c, realBounds, storage.BucketSpec{Name: "full"}))
		payload := bytes.Repeat([]byte{1, 3, 7, 255}, 8193)
		p := c.Put(c.Nodes()[0], "full", "before", payload, storage.Condition{})
		mustCheck(t, waitAll(c, p))
		_, e := c.Await("replicated before full", realBounds.converge, c.Converged)
		mustCheck(t, e)
		filler := filepath.Join(c.dir, "filler")
		f, e := os.Create(filler)
		mustCheck(t, e)
		t.Cleanup(func() { os.Remove(filler) })
		block := make([]byte, 1<<20)
		total := 0
		for {
			n, err := f.Write(block)
			total += n
			if err != nil {
				if !errors.Is(err, syscall.ENOSPC) {
					t.Fatalf("SETUP: fill returned %v", err)
				}
				break
			}
			if total > 128<<20 {
				t.Fatal("SETUP: filling exceeded cap")
			}
		}
		mustCheck(t, f.Close())
		q := c.Put(c.Nodes()[1], "full", "during", payload, storage.Condition{})
		mustCheck(t, c.Wait(q))
		if q.Err == nil {
			t.Fatal("write acknowledged while filesystem has no free space")
		}
		t.Logf("filled=%d rejected=%v", total, q.Err)
		mustCheck(t, os.Remove(filler))
		for _, nd := range c.Nodes() {
			mustCheck(t, c.Crash(nd))
		}
		for _, nd := range c.Nodes() {
			mustCheck(t, c.Restart(nd))
		}
		_, e = c.Await("ready after space restored and restart", realBounds.converge, c.Converged)
		mustCheck(t, e)
		for _, nd := range c.Nodes() {
			g := c.Get(nd, "full", "before")
			mustCheck(t, waitAll(c, g))
			if !bytes.Equal(g.Data, payload) {
				t.Fatal("preexisting acknowledged data changed")
			}
		}
		mustCheck(t, waitAll(c, c.Put(c.Nodes()[2], "full", "after", payload, storage.Condition{})))
		for _, p := range c.procs {
			if !p.running() {
				t.Fatal("node exited on ENOSPC")
			}
		}
	})
}

func TestIndependentHTTP(t *testing.T) {
	t.Run("C11/http-rejections/seed=791011", func(t *testing.T) {
		c := newCheckedRealCluster(t, 791011, defaultRealOptions())
		mustCheck(t, createBucket(c, realBounds, storage.BucketSpec{Name: "httpcheck"}))
		for _, body := range []string{"{", `{"name":`, strings.Repeat("[", 4096)} {
			req, e := http.NewRequest("PUT", "http://"+c.procs[0].httpAddr+"/httpcheck?quota", strings.NewReader(body))
			mustCheck(t, e)
			req.Header.Set("Content-Type", "application/json")
			res, e := c.hc.Do(req)
			mustCheck(t, e)
			_, e = io.Copy(io.Discard, res.Body)
			mustCheck(t, e)
			mustCheck(t, res.Body.Close())
			if res.StatusCode < 400 || res.StatusCode >= 500 {
				t.Fatalf("malformed JSON returned %d", res.StatusCode)
			}
			t.Logf("malformed length=%d status=%d", len(body), res.StatusCode)
		}
		mustCheck(t, waitAll(c, c.Put(c.Nodes()[0], "httpcheck", "still-ready", []byte("ok"), storage.Condition{})))
	})
}

func TestIndependentHygiene(t *testing.T) {
	t.Setenv(tierREnv, "1")
	t.Run("C10/default-build/seed=0", TestDefaultBuildCarriesNoFaultInjection)
	t.Run("C10/failed-test-cleanup/seed=99", TestTierRHarnessLeavesNothingBehindWhenATestFails)
}

func newCheckedRealCluster(t testing.TB, seed int64, opts realOptions) *realCluster {
	c := newRealCluster(t, seed, opts)
	for _, p := range c.procs {
		if !p.running() {
			t.Fatalf("SETUP: intended node %s failed startup: %s", p.node.ID, c.logOf(p.node))
		}
	}
	c.slots = make(chan struct{}, 1)
	return c
}
