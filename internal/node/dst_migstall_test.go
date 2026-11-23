package node_test

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/nodetest"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

// migStallPhase selects where a migration stall or destination-holder pause is
// injected past the orphan grace period.
type migStallPhase int

const (
	stallMidCopy migStallPhase = iota
	stallAfterStageBeforeVerify
	stallAfterVerifyBeforeFlip
	stallAfterFlipBeforeRetire
	stallDestHolder
)

func (p migStallPhase) String() string {
	switch p {
	case stallMidCopy:
		return "mid-copy"
	case stallAfterStageBeforeVerify:
		return "after-stage-before-verify"
	case stallAfterVerifyBeforeFlip:
		return "after-verify-before-flip"
	case stallAfterFlipBeforeRetire:
		return "after-flip-before-retire"
	case stallDestHolder:
		return "dest-holder"
	default:
		return "unknown"
	}
}

type hookStep struct {
	saga.Step
	before func()
	after  func(err error)
}

func (s hookStep) Do(c saga.Call, done func([]byte, error)) {
	if s.before != nil {
		s.before()
	}
	s.Step.Do(c, func(out []byte, err error) {
		if s.after != nil {
			s.after(err)
		}
		done(out, err)
	})
}

// migStallRun stalls the coordinator or a destination holder for longer than
// OrphanGrace at phase and proves the migration still reaches a clean terminal
// state with zero declared losses, no stray shards outside the final region,
// intact byte-for-byte readback, and passing lineage and GC oracles.
func migStallRun(phase migStallPhase, nodes int, layout storage.Layout) func(int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		const (
			bucket  = "m"
			from    = "r0"
			to      = "r1"
			numObjs = 2
		)
		orphanGrace := 20 * time.Second
		pauseDur := orphanGrace + 10*time.Second
		cfg := dstConfig()
		cfg.Layout = layout
		cfg.OrphanGrace = orphanGrace
		cfg.GCGrace = 5 * time.Second
		cfg.ViewSettle = 2 * time.Second
		cfg.MaintainPeriod = time.Second

		var h *harness
		var coord env.NodeID
		paused := false
		pauseNode := func(target env.NodeID) {
			if paused || target == "" {
				return
			}
			paused = true
			if err := h.s.Pause(target); err != nil {
				h.s.Fail(fmt.Errorf("pause %s: %w", target, err))
				return
			}
			h.s.InjectAt(h.s.Now().Add(pauseDur), sim.Fault{Kind: sim.FaultResume, Node: target})
		}

		cfg.WrapMigration = func(d saga.Definition) saga.Definition {
			steps := append([]saga.Step(nil), d.Steps...)
			for i, st := range steps {
				switch {
				case phase == stallAfterStageBeforeVerify && st.Name() == saga.StepVerify:
					steps[i] = hookStep{Step: st, before: func() { pauseNode(coord) }}
				case phase == stallAfterVerifyBeforeFlip && st.Name() == saga.StepFlip:
					steps[i] = hookStep{Step: st, before: func() { pauseNode(coord) }}
				case phase == stallAfterFlipBeforeRetire && st.Name() == saga.StepDeleteSource:
					steps[i] = hookStep{Step: st, before: func() { pauseNode(coord) }}
				}
			}
			d.Steps = steps
			return d
		}

		var err error
		h, err = newHarness(seed, nodes, cfg, sim.DefaultNetConfig())
		if err != nil {
			h.s.Fail(err)
			return h.s
		}
		s := h.s
		if err := s.RunFor(5 * time.Second); err != nil {
			return s
		}
		if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: bucket, Region: from}); err != nil {
			s.Fail(err)
			return s
		}
		if err := s.RunUntilConverged(s.Now().Add(20*time.Second), h.converged); err != nil {
			s.Fail(err)
			return s
		}

		w := s.WorkloadRand()
		data := map[string][]byte{}
		acked := 0
		for i := range numObjs {
			key := fmt.Sprintf("warm%02d", i)
			body := payload(w, layout.StripeSize+137+i)
			data[key] = body
			h.node(h.ids[i%len(h.ids)]).PutObject(storage.PutRequest{Bucket: bucket, Key: key, Data: body}, func(_ storage.ObjectInfo, err error) {
				if err != nil {
					s.Fail(fmt.Errorf("warm put %s: %w", key, err))
					return
				}
				acked++
			})
		}
		if err := s.RunUntilConverged(s.Now().Add(20*time.Second), func() bool { return acked == numObjs && h.converged() }); err != nil {
			s.Fail(err)
			return s
		}

		coord = h.ids[int(seed%int64(len(h.ids)))]
		if s.Region(coord) == to {
			for _, id := range h.ids {
				if s.Region(id) != to {
					coord = id
					break
				}
			}
		}

		destWrites := 0
		needWrites := layout.K + layout.M
		h.gc.onWrite = func(id env.NodeID, name string) {
			if paused || s.Region(id) != to || !strings.HasPrefix(name, "shards/"+bucket+"/") {
				return
			}
			destWrites++
			switch phase {
			case stallMidCopy:
				if destWrites >= needWrites {
					pauseNode(coord)
				}
			case stallDestHolder:
				if id != coord {
					pauseNode(id)
				}
			}
		}

		mi, err := h.node(coord).StartMigration(bucket, to)
		if err != nil {
			s.Fail(fmt.Errorf("start migration on %s: %w", coord, err))
			return s
		}

		var st saga.Status
		if err := s.RunUntilConverged(s.Now().Add(pauseDur+90*time.Second), func() bool {
			nd := h.node(coord)
			if nd == nil {
				return false
			}
			got, err := nd.Engine().Status(saga.ID(mi.ID))
			st = got
			return err == nil && got.State.Terminal()
		}); err != nil {
			s.Fail(fmt.Errorf("phase %s: migration %s never reached terminal state (state=%v step=%d err=%v): %w", phase, mi.ID, st.State, st.Step, st.Err, err))
			return s
		}
		if !paused {
			s.Fail(fmt.Errorf("phase %s: pause trigger never fired", phase))
			return s
		}
		if st.State != saga.StateCompleted && st.State != saga.StateCompensated {
			s.Fail(fmt.Errorf("phase %s: unexpected terminal state %v", phase, st.State))
			return s
		}
		if err := s.RunUntilConverged(s.Now().Add(40*time.Second), h.converged); err != nil {
			s.Fail(fmt.Errorf("phase %s: not converged after terminal: %w", phase, err))
			return s
		}

		finalRegion := from
		if st.State == saga.StateCompleted {
			finalRegion = to
		}
		for _, nd := range h.up() {
			rd, err := nd.OCC().Reader()
			if err != nil {
				s.Fail(err)
				return s
			}
			r, err := rd.Route(bucket)
			if err != nil || string(r) != finalRegion {
				s.Fail(fmt.Errorf("phase %s: node %s routes %s to %q (%v), want %q", phase, nd.ID(), bucket, r, err, finalRegion))
				return s
			}
			for _, k := range rd.Keys() {
				if strings.Contains(k, "~lost/") {
					s.Fail(fmt.Errorf("phase %s: node %s has loss record %q", phase, nd.ID(), k))
					return s
				}
			}
		}
		if len(h.declared) > 0 {
			s.Fail(fmt.Errorf("phase %s: loss declared: %v", phase, h.declared))
			return s
		}

		noOutside := func() bool {
			for _, id := range h.ids {
				if string(s.Region(id)) == finalRegion {
					continue
				}
				for _, name := range s.Disk(id).Names() {
					if strings.HasPrefix(name, "shards/"+bucket+"/") {
						return false
					}
				}
			}
			return true
		}
		if err := s.RunUntilConverged(s.Now().Add(orphanGrace+cfg.GCGrace+20*time.Second), noOutside); err != nil {
			s.Fail(fmt.Errorf("phase %s: shard of %s left outside %s: %w", phase, bucket, finalRegion, err))
			return s
		}

		for i := range numObjs {
			key := fmt.Sprintf("warm%02d", i)
			got := false
			h.node(h.ids[0]).GetObject(bucket, key, func(_ storage.ObjectInfo, body []byte, err error) {
				got = true
				if err != nil {
					s.Fail(fmt.Errorf("phase %s: get %s/%s failed: %w", phase, bucket, key, err))
					return
				}
				if !bytes.Equal(body, data[key]) {
					s.Fail(fmt.Errorf("phase %s: get %s/%s returned wrong bytes", phase, bucket, key))
				}
			})
			if err := s.RunUntilConverged(s.Now().Add(10*time.Second), func() bool { return got }); err != nil {
				s.Fail(fmt.Errorf("phase %s: get %s/%s timed out: %w", phase, bucket, key, err))
				return s
			}
		}

		if err := s.RunFor(10 * time.Second); err != nil {
			return s
		}
		if len(h.declared) > 0 {
			s.Fail(fmt.Errorf("phase %s: loss declared after readback: %v", phase, h.declared))
			return s
		}
		rd, err := h.up()[0].OCC().Reader()
		if err != nil {
			s.Fail(err)
			return s
		}
		if _, err := h.acks.Check(rd, nil); err != nil {
			s.Fail(fmt.Errorf("phase %s: ack lineage: %w", phase, err))
			return s
		}
		if err := h.gc.check(); err != nil {
			s.Fail(fmt.Errorf("phase %s: gc oracle: %w", phase, err))
		}
		return s
	}
}

// TestMigrationCoordinatorStalledPastOrphanGraceMidCopy proves a coordinator
// paused past OrphanGrace right after copying shards into the destination
// cannot complete onto orphan-collected destination shards or lose objects.
func TestMigrationCoordinatorStalledPastOrphanGraceMidCopy(t *testing.T) {
	t.Parallel()
	seeds := []int64{31337, 3, 4, 1, 2, 5, 6, 7, 8, 9, 10, 11}
	layout := storage.Layout{K: 2, M: 1, StripeSize: dstStripe}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			simtest.RequireDeterministic(t, seed, migStallRun(stallMidCopy, 12, layout))
		})
	}
}

// TestMigrationStalledPastOrphanGraceAllPhases proves stalls past OrphanGrace
// before verify, before flip, before retire, or on a destination holder (at
// both 2+1 and 4+2 layouts) still end cleanly with all acknowledged objects
// readable and zero losses declared.
func TestMigrationStalledPastOrphanGraceAllPhases(t *testing.T) {
	t.Parallel()
	phases := []migStallPhase{
		stallAfterStageBeforeVerify,
		stallAfterVerifyBeforeFlip,
		stallAfterFlipBeforeRetire,
		stallDestHolder,
	}
	seeds := []int64{31337, 3, 4}
	layout21 := storage.Layout{K: 2, M: 1, StripeSize: dstStripe}
	for _, ph := range phases {
		t.Run(ph.String(), func(t *testing.T) {
			t.Parallel()
			for _, seed := range seeds {
				t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
					simtest.RequireDeterministic(t, seed, migStallRun(ph, 12, layout21))
				})
			}
		})
	}
	t.Run("dest-holder-4+2", func(t *testing.T) {
		t.Parallel()
		layout42 := storage.Layout{K: 4, M: 2, StripeSize: dstStripe}
		for _, seed := range seeds {
			t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
				simtest.RequireDeterministic(t, seed, migStallRun(stallDestHolder, dstNodes, layout42))
			})
		}
	})
}

// blockMetaNet drops OCC and metadata-Merkle channels (3 and 4) for matching
// peer pairs so staged records cannot replicate through background anti-entropy.
type blockMetaNet struct {
	env.Transport
	self  env.NodeID
	clock env.Clock
	block func(from, to env.NodeID) bool
}

func (b blockMetaNet) Send(to env.NodeID, msg []byte) error {
	if b.block(b.self, to) && len(msg) > 0 && (msg[0] == 3 || msg[0] == 4) {
		return nil
	}
	return b.Transport.Send(to, msg)
}

func (b blockMetaNet) Request(to env.NodeID, req []byte, timeout time.Duration, cb func([]byte, error)) {
	if b.block(b.self, to) && len(req) > 0 && (req[0] == 3 || req[0] == 4) {
		b.clock.AfterFunc(0, func() { cb(nil, env.ErrUnreachable) })
		return
	}
	b.Transport.Request(to, req, timeout, cb)
}

func newWrappedHarness(seed int64, n int, cfg node.Config, wrapEnv func(env.Env) env.Env) (*harness, error) {
	table, err := node.CodeTable()
	if err != nil {
		return &harness{s: sim.New(sim.Config{Seed: seed})}, err
	}
	h := &harness{s: sim.New(sim.Config{Seed: seed, Codes: table, Net: sim.DefaultNetConfig()}), ids: sim.NodeIDs(n), acks: nodetest.NewLineage(), mans: map[string]*storage.Manifest{}}
	h.gc = newGCOracle(h)
	cfg.Seeds = []env.NodeID{h.ids[0], h.ids[1], h.ids[2]}
	sink := cfg.DiscardSink
	cfg.DiscardSink = func(key string, w, l occ.Version) {
		h.discards = append(h.discards, discardRec{key: key, winner: w, loser: l})
		h.acks.Discard(l)
		if sink != nil {
			sink(key, w, l)
		}
	}
	commits := cfg.CommitSink
	h.cfg = cfg
	inner := func(e env.Env) env.Process {
		c := cfg
		id := e.ID
		c.CommitSink = func(key string, held occ.Record, v occ.Version) {
			h.acks.Commit(id, key, held, v)
			h.gc.onCommit(key, v)
			h.onCommitLoss(id, key, v)
			if commits != nil {
				commits(key, held, v)
			}
		}
		e = e.WithDisk(watchDisk{Disk: e.Disk, id: e.ID, o: h.gc})
		if wrapEnv != nil {
			e = wrapEnv(e)
		}
		return node.NewFactory(c)(e)
	}
	if err := h.s.AddCluster(h.ids, dstRegions, inner); err != nil {
		return h, err
	}
	h.s.AddInvariant("node/no-shard-of-a-possible-winner-deleted", 500, h.gc.check)
	return h, nil
}

// TestMigrationCheckThenActGapClosed proves opVerifyShards pins staged records
// on destination holders during StepVerify so even when background metadata
// sync is blocked and the coordinator stalls past OrphanGrace before StepFlip,
// destination holders record zero orphans and the migration completes cleanly.
func TestMigrationCheckThenActGapClosed(t *testing.T) {
	t.Parallel()
	seeds := []int64{31337, 3, 4}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			simtest.RequireDeterministic(t, seed, func(seed int64) *sim.Sim {
				const (
					bucket = "m"
					from   = "r0"
					to     = "r1"
				)
				orphanGrace := 15 * time.Second
				cfg := dstConfig()
				cfg.Layout = storage.Layout{K: 2, M: 1, StripeSize: dstStripe}
				cfg.OrphanGrace = orphanGrace
				cfg.GCGrace = 5 * time.Second
				cfg.ViewSettle = 2 * time.Second
				cfg.MaintainPeriod = 500 * time.Millisecond

				var h *harness
				var coord env.NodeID
				blockMeta := false
				delayedVerify := false
				pausedAfterVerify := false

				cfg.WrapMigration = func(d saga.Definition) saga.Definition {
					steps := append([]saga.Step(nil), d.Steps...)
					for i, st := range steps {
						switch st.Name() {
						case saga.StepVerify:
							inner := st
							steps[i] = hookStep{Step: delayOnceStep{Step: inner, delay: orphanGrace - 3*time.Second, triggered: &delayedVerify, sim: func() *sim.Sim { return h.s }}}
						case saga.StepFlip:
							steps[i] = hookStep{Step: st, before: func() {
								if pausedAfterVerify {
									return
								}
								pausedAfterVerify = true
								if err := h.s.Pause(coord); err != nil {
									h.s.Fail(err)
									return
								}
								h.s.At(h.s.Now().Add(orphanGrace+5*time.Second), "resume-after-grace", func() {
									blockMeta = false
									if err := h.s.Resume(coord); err != nil {
										h.s.Fail(err)
									}
								})
							}}
						}
					}
					d.Steps = steps
					return d
				}
				cfg.SagaPolicy = saga.DefaultPolicy()
				cfg.SagaPolicy.StepTimeout = orphanGrace + 10*time.Second

				var err error
				h, err = newWrappedHarness(seed, 12, cfg,
					func(e env.Env) env.Env {
						return e.WithNet(blockMetaNet{Transport: e.Net, self: e.ID, clock: e.Clock, block: func(from, to env.NodeID) bool {
							return blockMeta && (from == coord || to == coord)
						}})
					},
				)
				if err != nil {
					h.s.Fail(err)
					return h.s
				}
				s := h.s
				if err := s.RunFor(5 * time.Second); err != nil {
					return s
				}
				if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: bucket, Region: from}); err != nil {
					s.Fail(err)
					return s
				}
				if err := s.RunUntilConverged(s.Now().Add(20*time.Second), h.converged); err != nil {
					s.Fail(err)
					return s
				}
				body := payload(s.WorkloadRand(), cfg.Layout.StripeSize+77)
				acked := false
				h.node(h.ids[0]).PutObject(storage.PutRequest{Bucket: bucket, Key: "gap0", Data: body}, func(_ storage.ObjectInfo, err error) {
					if err != nil {
						s.Fail(err)
						return
					}
					acked = true
				})
				if err := s.RunUntilConverged(s.Now().Add(20*time.Second), func() bool { return acked && h.converged() }); err != nil {
					s.Fail(err)
					return s
				}

				coord = h.ids[0]
				blockMeta = true
				mi, err := h.node(coord).StartMigration(bucket, to)
				if err != nil {
					s.Fail(err)
					return s
				}
				var st saga.Status
				if err := s.RunUntilConverged(s.Now().Add(3*orphanGrace+60*time.Second), func() bool {
					nd := h.node(coord)
					if nd == nil {
						return false
					}
					got, err := nd.Engine().Status(saga.ID(mi.ID))
					st = got
					return err == nil && got.State.Terminal()
				}); err != nil {
					s.Fail(fmt.Errorf("gap test never reached terminal (state=%v step=%d err=%v): %w", st.State, st.Step, st.Err, err))
					return s
				}
				if !delayedVerify || !pausedAfterVerify {
					s.Fail(fmt.Errorf("gap test hooks did not fire: delayedVerify=%v pausedAfterVerify=%v", delayedVerify, pausedAfterVerify))
					return s
				}
				if st.State != saga.StateCompleted {
					s.Fail(fmt.Errorf("gap test ended %v (err=%v), want completed after verify pinned destination shards", st.State, st.Err))
					return s
				}
				if err := s.RunUntilConverged(s.Now().Add(30*time.Second), h.converged); err != nil {
					s.Fail(err)
					return s
				}
				for _, id := range h.region(to) {
					if n := h.node(id); n != nil && n.Stats().OrphanRecords != 0 {
						s.Fail(fmt.Errorf("destination node %s recorded %d orphans after verify pinned staged shards", id, n.Stats().OrphanRecords))
						return s
					}
				}
				got := false
				h.node(h.ids[0]).GetObject(bucket, "gap0", func(_ storage.ObjectInfo, b []byte, err error) {
					got = true
					if err != nil || !bytes.Equal(b, body) {
						s.Fail(fmt.Errorf("get after gap test: len=%d err=%v", len(b), err))
					}
				})
				if err := s.RunUntilConverged(s.Now().Add(10*time.Second), func() bool { return got }); err != nil {
					s.Fail(err)
					return s
				}
				return s
			})
		})
	}
}

type delayOnceStep struct {
	saga.Step
	delay     time.Duration
	triggered *bool
	sim       func() *sim.Sim
}

func (d delayOnceStep) Do(c saga.Call, done func([]byte, error)) {
	if *d.triggered {
		d.Step.Do(c, done)
		return
	}
	*d.triggered = true
	s := d.sim()
	s.At(s.Now().Add(d.delay), "delay-before-"+d.Step.Name(), func() {
		d.Step.Do(c, done)
	})
}

// TestMigrationStalledAfterStageWithoutSyncCompensates proves that when staged
// metadata cannot sync and the coordinator stalls past OrphanGrace before
// StepVerify, destination shards are orphan-collected and StepVerify forces
// compensation rather than flipping onto missing shards.
func TestMigrationStalledAfterStageWithoutSyncCompensates(t *testing.T) {
	t.Parallel()
	seeds := []int64{31337, 3, 4}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			simtest.RequireDeterministic(t, seed, func(seed int64) *sim.Sim {
				const (
					bucket = "m"
					from   = "r0"
					to     = "r1"
				)
				orphanGrace := 15 * time.Second
				pauseDur := orphanGrace + 10*time.Second
				cfg := dstConfig()
				cfg.Layout = storage.Layout{K: 2, M: 1, StripeSize: dstStripe}
				cfg.OrphanGrace = orphanGrace
				cfg.GCGrace = 5 * time.Second
				cfg.ViewSettle = 2 * time.Second
				cfg.MaintainPeriod = 500 * time.Millisecond

				var h *harness
				var coord env.NodeID
				blockMeta := false
				paused := false

				cfg.WrapMigration = func(d saga.Definition) saga.Definition {
					steps := append([]saga.Step(nil), d.Steps...)
					for i, st := range steps {
						if st.Name() == saga.StepVerify {
							steps[i] = hookStep{Step: st, before: func() {
								if paused {
									return
								}
								paused = true
								if err := h.s.Pause(coord); err != nil {
									h.s.Fail(err)
									return
								}
								h.s.At(h.s.Now().Add(pauseDur), "resume-after-orphan", func() {
									blockMeta = false
									if err := h.s.Resume(coord); err != nil {
										h.s.Fail(err)
									}
								})
							}}
						}
					}
					d.Steps = steps
					return d
				}

				var err error
				h, err = newWrappedHarness(seed, 12, cfg,
					func(e env.Env) env.Env {
						return e.WithNet(blockMetaNet{Transport: e.Net, self: e.ID, clock: e.Clock, block: func(from, to env.NodeID) bool {
							return blockMeta && (from == coord || to == coord)
						}})
					},
				)
				if err != nil {
					h.s.Fail(err)
					return h.s
				}
				h.gc.off = true
				s := h.s
				if err := s.RunFor(5 * time.Second); err != nil {
					return s
				}
				if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: bucket, Region: from}); err != nil {
					s.Fail(err)
					return s
				}
				if err := s.RunUntilConverged(s.Now().Add(20*time.Second), h.converged); err != nil {
					s.Fail(err)
					return s
				}
				body := payload(s.WorkloadRand(), cfg.Layout.StripeSize+55)
				acked := false
				h.node(h.ids[0]).PutObject(storage.PutRequest{Bucket: bucket, Key: "st0", Data: body}, func(_ storage.ObjectInfo, err error) {
					if err != nil {
						s.Fail(err)
						return
					}
					acked = true
				})
				if err := s.RunUntilConverged(s.Now().Add(20*time.Second), func() bool { return acked && h.converged() }); err != nil {
					s.Fail(err)
					return s
				}

				coord = h.ids[0]
				blockMeta = true
				mi, err := h.node(coord).StartMigration(bucket, to)
				if err != nil {
					s.Fail(err)
					return s
				}
				var st saga.Status
				if err := s.RunUntilConverged(s.Now().Add(pauseDur+60*time.Second), func() bool {
					nd := h.node(coord)
					if nd == nil {
						return false
					}
					got, err := nd.Engine().Status(saga.ID(mi.ID))
					st = got
					return err == nil && got.State.Terminal()
				}); err != nil {
					s.Fail(fmt.Errorf("after-stage-no-sync never reached terminal (state=%v step=%d err=%v): %w", st.State, st.Step, st.Err, err))
					return s
				}
				if !paused {
					s.Fail(fmt.Errorf("pause never fired"))
					return s
				}
				if st.State != saga.StateCompensated {
					s.Fail(fmt.Errorf("ended %v, want compensated when staged shards were orphan-collected before verify", st.State))
					return s
				}
				if err := s.RunUntilConverged(s.Now().Add(30*time.Second), h.converged); err != nil {
					s.Fail(err)
					return s
				}
				if len(h.declared) > 0 {
					s.Fail(fmt.Errorf("loss declared: %v", h.declared))
					return s
				}
				got := false
				h.node(h.ids[0]).GetObject(bucket, "st0", func(_ storage.ObjectInfo, b []byte, err error) {
					got = true
					if err != nil || !bytes.Equal(b, body) {
						s.Fail(fmt.Errorf("get after compensation: len=%d err=%v", len(b), err))
					}
				})
				if err := s.RunUntilConverged(s.Now().Add(10*time.Second), func() bool { return got }); err != nil {
					s.Fail(err)
					return s
				}
				for _, id := range h.region(to) {
					for _, name := range s.Disk(id).Names() {
						if strings.HasPrefix(name, "shards/"+bucket+"/") {
							s.Fail(fmt.Errorf("shard %s left on %s in %s after compensation", name, id, to))
							return s
						}
					}
				}
				return s
			})
		})
	}
}

// TestMigrationPostPivotCatchupStallRecopies proves a coordinator stalled past
// OrphanGrace while copying a late pre-flip write during post-pivot catch-up
// detects the missing staged shards, re-copies from the live source, and
// completes with both base and late objects intact.
func TestMigrationPostPivotCatchupStallRecopies(t *testing.T) {
	t.Parallel()
	seeds := []int64{31337, 3, 4}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			simtest.RequireDeterministic(t, seed, func(seed int64) *sim.Sim {
				const (
					bucket = "m"
					from   = "r0"
					to     = "r1"
				)
				orphanGrace := 15 * time.Second
				pauseDur := orphanGrace + 10*time.Second
				cfg := dstConfig()
				cfg.Layout = storage.Layout{K: 2, M: 1, StripeSize: dstStripe}
				cfg.OrphanGrace = orphanGrace
				cfg.GCGrace = 5 * time.Second
				cfg.ViewSettle = 2 * time.Second
				cfg.MaintainPeriod = 500 * time.Millisecond

				var h *harness
				var coord env.NodeID
				var writer env.NodeID
				inCatchup := false
				pausedCatchup := false
				lateBody := []byte(nil)

				cfg.WrapMigration = func(d saga.Definition) saga.Definition {
					steps := append([]saga.Step(nil), d.Steps...)
					steps[saga.MigrationPivot] = hookStep{Step: steps[saga.MigrationPivot], before: func() {
						if writer != "" {
							return
						}
						for _, id := range h.region(from) {
							if id != coord && h.node(id) != nil {
								writer = id
								break
							}
						}
						lateBody = payload(h.s.WorkloadRand(), cfg.Layout.StripeSize+99)
						h.node(writer).PutObject(storage.PutRequest{Bucket: bucket, Key: "late0", Data: lateBody}, func(_ storage.ObjectInfo, err error) {
							if err != nil {
								h.s.Fail(fmt.Errorf("late put: %w", err))
							}
						})
					}}
					steps[saga.MigrationSteps-1] = hookStep{Step: steps[saga.MigrationSteps-1], before: func() {
						inCatchup = true
					}}
					d.Steps = steps
					return d
				}

				var err error
				h, err = newHarness(seed, 12, cfg, sim.DefaultNetConfig())
				if err != nil {
					h.s.Fail(err)
					return h.s
				}
				s := h.s
				if err := s.RunFor(5 * time.Second); err != nil {
					return s
				}
				if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: bucket, Region: from}); err != nil {
					s.Fail(err)
					return s
				}
				if err := s.RunUntilConverged(s.Now().Add(20*time.Second), h.converged); err != nil {
					s.Fail(err)
					return s
				}
				body0 := payload(s.WorkloadRand(), cfg.Layout.StripeSize+33)
				acked := false
				h.node(h.ids[0]).PutObject(storage.PutRequest{Bucket: bucket, Key: "base0", Data: body0}, func(_ storage.ObjectInfo, err error) {
					if err != nil {
						s.Fail(err)
						return
					}
					acked = true
				})
				if err := s.RunUntilConverged(s.Now().Add(20*time.Second), func() bool { return acked && h.converged() }); err != nil {
					s.Fail(err)
					return s
				}

				coord = h.ids[2]
				catchupWrites := 0
				h.gc.onWrite = func(id env.NodeID, name string) {
					if !inCatchup || pausedCatchup || s.Region(id) != to || !strings.HasPrefix(name, "shards/"+bucket+"/") {
						return
					}
					catchupWrites++
					if catchupWrites >= cfg.Layout.K+cfg.Layout.M {
						pausedCatchup = true
						if err := s.Pause(coord); err != nil {
							s.Fail(err)
							return
						}
						s.InjectAt(s.Now().Add(pauseDur), sim.Fault{Kind: sim.FaultResume, Node: coord})
					}
				}

				mi, err := h.node(coord).StartMigration(bucket, to)
				if err != nil {
					s.Fail(err)
					return s
				}
				var st saga.Status
				if err := s.RunUntilConverged(s.Now().Add(pauseDur+90*time.Second), func() bool {
					nd := h.node(coord)
					if nd == nil {
						return false
					}
					got, err := nd.Engine().Status(saga.ID(mi.ID))
					st = got
					return err == nil && got.State == saga.StateCompleted
				}); err != nil {
					s.Fail(fmt.Errorf("post-pivot catchup stall never completed (state=%v step=%d err=%v): %w", st.State, st.Step, st.Err, err))
					return s
				}
				if !pausedCatchup {
					s.Fail(fmt.Errorf("catchup pause never fired"))
					return s
				}
				if err := s.RunUntilConverged(s.Now().Add(30*time.Second), h.converged); err != nil {
					s.Fail(err)
					return s
				}
				if len(h.declared) > 0 {
					s.Fail(fmt.Errorf("loss declared: %v", h.declared))
					return s
				}
				for _, tc := range []struct {
					key  string
					want []byte
				}{{"base0", body0}, {"late0", lateBody}} {
					got := false
					h.node(h.ids[0]).GetObject(bucket, tc.key, func(_ storage.ObjectInfo, b []byte, err error) {
						got = true
						if err != nil || !bytes.Equal(b, tc.want) {
							s.Fail(fmt.Errorf("get %s/%s: len=%d err=%v", bucket, tc.key, len(b), err))
						}
					})
					if err := s.RunUntilConverged(s.Now().Add(10*time.Second), func() bool { return got }); err != nil {
						s.Fail(err)
						return s
					}
				}
				rd, err := h.up()[0].OCC().Reader()
				if err != nil {
					s.Fail(err)
					return s
				}
				if _, err := h.acks.Check(rd, nil); err != nil {
					s.Fail(err)
					return s
				}
				if err := h.gc.check(); err != nil {
					s.Fail(err)
				}
				return s
			})
		})
	}
}

func stagedManifests(nd *node.Node, bucket, sagaID string) ([]*storage.Manifest, error) {
	rd, err := nd.OCC().Reader()
	if err != nil {
		return nil, err
	}
	wantTag := "~stage/" + sagaID
	var out []*storage.Manifest
	for _, k := range rd.Keys() {
		if len(k) == 0 || occ.Kind(k[0]) != occ.KindTag {
			continue
		}
		_, parts, err := occ.ParseKey(k)
		if err != nil || len(parts) < 3 || parts[0] != bucket || parts[2] != wantTag {
			continue
		}
		val, _, err := rd.Get(k)
		if err != nil {
			continue
		}
		man, err := node.ManifestOfStaged(val)
		if err != nil {
			return nil, err
		}
		out = append(out, man)
	}
	return out, nil
}

// migThresholdRun corrupts destination shards before StepVerify so every stripe
// holds exactly keep verified shards, then checks that StepVerify rejects the
// under-quorum stripe, StepFlip never runs below quorum, and the saga ends in
// wantState with intact readback and passing oracles.
func migThresholdRun(keep int, wantState saga.State) func(int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		const (
			bucket = "m"
			from   = "r0"
			to     = "r1"
			objKey = "th0"
		)
		layout := storage.Layout{K: 4, M: 2, StripeSize: dstStripe}
		quorum := min(layout.K+1, layout.K+layout.M)
		orphanGrace := 20 * time.Second
		cfg := dstConfig()
		cfg.Layout = layout
		cfg.OrphanGrace = orphanGrace
		cfg.GCGrace = 5 * time.Second
		cfg.ViewSettle = 2 * time.Second
		cfg.MaintainPeriod = 500 * time.Millisecond
		cfg.SagaPolicy = saga.DefaultPolicy()
		cfg.SagaPolicy.MaxAttempts = 12
		cfg.SagaPolicy.BaseDelay = 200 * time.Millisecond

		var h *harness
		var coord env.NodeID
		var sagaID string
		damaged := false
		verifyFails := 0
		verifyOKs := 0
		flipRuns := 0

		cfg.WrapMigration = func(d saga.Definition) saga.Definition {
			steps := append([]saga.Step(nil), d.Steps...)
			for i, st := range steps {
				switch st.Name() {
				case saga.StepVerify:
					steps[i] = hookStep{
						Step: st,
						before: func() {
							if damaged {
								return
							}
							damaged = true
							nd := h.node(coord)
							if nd == nil {
								h.s.Fail(fmt.Errorf("coordinator %s down before verify", coord))
								return
							}
							mans, err := stagedManifests(nd, bucket, sagaID)
							if err != nil || len(mans) == 0 {
								h.s.Fail(fmt.Errorf("no staged manifests before verify (n=%d err=%v)", len(mans), err))
								return
							}
							for _, man := range mans {
								for si, stripe := range man.Stripes {
									rot := len(stripe.Shards) - keep
									if rot <= 0 {
										h.s.Fail(fmt.Errorf("keep=%d leaves nothing to corrupt for %d shards", keep, len(stripe.Shards)))
										return
									}
									off := int(seed % int64(len(stripe.Shards)))
									for j := range rot {
										idx := (off + j) % len(stripe.Shards)
										ref := stripe.Shards[idx]
										if err := h.s.BitRot(ref.Node, man.ShardID(si, idx).BlobName()); err != nil {
											h.s.Fail(fmt.Errorf("bitrot stripe %d shard %d on %s: %w", si, idx, ref.Node, err))
											return
										}
									}
									if got := h.verifiableShards(man, si); got != keep {
										h.s.Fail(fmt.Errorf("after bitrot stripe %d has %d verified shards, want %d", si, got, keep))
										return
									}
								}
							}
						},
						after: func(err error) {
							if err != nil {
								verifyFails++
							} else {
								verifyOKs++
							}
						},
					}
				case saga.StepFlip:
					steps[i] = hookStep{
						Step: st,
						before: func() {
							flipRuns++
							if flipRuns != 1 {
								return
							}
							nd := h.node(coord)
							if nd == nil {
								h.s.Fail(fmt.Errorf("coordinator %s down at flip", coord))
								return
							}
							mans, err := stagedManifests(nd, bucket, sagaID)
							if err != nil || len(mans) == 0 {
								h.s.Fail(fmt.Errorf("no staged manifests at flip (n=%d err=%v)", len(mans), err))
								return
							}
							for _, man := range mans {
								for si := range man.Stripes {
									if got := h.verifiableShards(man, si); got < quorum {
										h.s.Fail(fmt.Errorf("flip ran while %s/%s version %s stripe %d had %d verified shards, want >= quorum=%d", bucket, man.Key, man.Version, si, got, quorum))
										return
									}
								}
							}
						},
					}
				}
			}
			d.Steps = steps
			return d
		}

		var err error
		h, err = newHarness(seed, dstNodes, cfg, sim.DefaultNetConfig())
		if err != nil {
			h.s.Fail(err)
			return h.s
		}
		s := h.s
		if err := s.RunFor(5 * time.Second); err != nil {
			return s
		}
		if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: bucket, Region: from}); err != nil {
			s.Fail(err)
			return s
		}
		if err := s.RunUntilConverged(s.Now().Add(20*time.Second), h.converged); err != nil {
			s.Fail(err)
			return s
		}

		body := payload(s.WorkloadRand(), layout.StripeSize+113)
		acked := false
		h.node(h.ids[0]).PutObject(storage.PutRequest{Bucket: bucket, Key: objKey, Data: body}, func(_ storage.ObjectInfo, err error) {
			if err != nil {
				s.Fail(fmt.Errorf("put %s: %w", objKey, err))
				return
			}
			acked = true
		})
		if err := s.RunUntilConverged(s.Now().Add(20*time.Second), func() bool { return acked && h.converged() }); err != nil {
			s.Fail(err)
			return s
		}

		coord = h.ids[int(seed%int64(len(h.ids)))]
		if string(s.Region(coord)) == to {
			for _, id := range h.ids {
				if string(s.Region(id)) != to {
					coord = id
					break
				}
			}
		}

		mi, err := h.node(coord).StartMigration(bucket, to)
		if err != nil {
			s.Fail(fmt.Errorf("start migration on %s: %w", coord, err))
			return s
		}
		sagaID = mi.ID

		var st saga.Status
		if err := s.RunUntilConverged(s.Now().Add(90*time.Second), func() bool {
			nd := h.node(coord)
			if nd == nil {
				return false
			}
			got, err := nd.Engine().Status(saga.ID(mi.ID))
			st = got
			return err == nil && got.State.Terminal()
		}); err != nil {
			s.Fail(fmt.Errorf("keep=%d: migration never reached terminal state (state=%v step=%d err=%v): %w", keep, st.State, st.Step, st.Err, err))
			return s
		}
		if !damaged {
			s.Fail(fmt.Errorf("keep=%d: verify damage hook never fired", keep))
			return s
		}
		if verifyFails < 1 {
			s.Fail(fmt.Errorf("keep=%d: verify never failed before passing (fails=%d ok=%d)", keep, verifyFails, verifyOKs))
			return s
		}
		if st.State != wantState {
			s.Fail(fmt.Errorf("keep=%d: migration ended %v (err=%v), want %v", keep, st.State, st.Err, wantState))
			return s
		}
		if wantState == saga.StateCompensated && flipRuns != 0 {
			s.Fail(fmt.Errorf("keep=%d: flip ran %d times despite fewer than k verified shards", keep, flipRuns))
			return s
		}
		if wantState == saga.StateCompleted && (verifyOKs < 1 || flipRuns < 1) {
			s.Fail(fmt.Errorf("keep=%d: expected verify ok and flip (verifyOKs=%d flipRuns=%d)", keep, verifyOKs, flipRuns))
			return s
		}
		if err := s.RunUntilConverged(s.Now().Add(30*time.Second), h.converged); err != nil {
			s.Fail(err)
			return s
		}

		finalRegion := from
		if wantState == saga.StateCompleted {
			finalRegion = to
		}
		for _, nd := range h.up() {
			rd, err := nd.OCC().Reader()
			if err != nil {
				s.Fail(err)
				return s
			}
			r, err := rd.Route(bucket)
			if err != nil || string(r) != finalRegion {
				s.Fail(fmt.Errorf("keep=%d: node %s routes %s to %q (%v), want %q", keep, nd.ID(), bucket, r, err, finalRegion))
				return s
			}
			for _, k := range rd.Keys() {
				if strings.Contains(k, "~lost/") {
					s.Fail(fmt.Errorf("keep=%d: node %s has loss record %q", keep, nd.ID(), k))
					return s
				}
			}
		}
		if len(h.declared) > 0 {
			s.Fail(fmt.Errorf("keep=%d: loss declared: %v", keep, h.declared))
			return s
		}

		noOutside := func() bool {
			for _, id := range h.ids {
				if string(s.Region(id)) == finalRegion {
					continue
				}
				for _, name := range s.Disk(id).Names() {
					if strings.HasPrefix(name, "shards/"+bucket+"/") {
						return false
					}
				}
			}
			return true
		}
		if err := s.RunUntilConverged(s.Now().Add(orphanGrace+cfg.GCGrace+20*time.Second), noOutside); err != nil {
			s.Fail(fmt.Errorf("keep=%d: shards of %s left outside %s: %w", keep, bucket, finalRegion, err))
			return s
		}

		rd, err := h.up()[0].OCC().Reader()
		if err != nil {
			s.Fail(err)
			return s
		}
		val, _, err := rd.Get(occManifestKey(bucket, objKey))
		if err != nil {
			s.Fail(fmt.Errorf("keep=%d: manifest %s/%s missing: %w", keep, bucket, objKey, err))
			return s
		}
		man, err := manifestOf(val)
		if err != nil {
			s.Fail(err)
			return s
		}
		for si, stripe := range man.Stripes {
			inFinal := 0
			for idx, ref := range stripe.Shards {
				if string(s.Region(ref.Node)) != finalRegion {
					s.Fail(fmt.Errorf("keep=%d: live stripe %d shard %d on %s in %s, want %s", keep, si, idx, ref.Node, s.Region(ref.Node), finalRegion))
					return s
				}
				blob, ok := s.Disk(ref.Node).Peek(man.ShardID(si, idx).BlobName())
				if !ok {
					continue
				}
				if _, _, derr := storage.DecodeShard(blob); derr == nil && storage.BlobChecksum(blob) == ref.Checksum {
					inFinal++
				}
			}
			if inFinal < quorum {
				s.Fail(fmt.Errorf("keep=%d: live stripe %d holds %d verified shards in %s, want >= quorum=%d", keep, si, inFinal, finalRegion, quorum))
				return s
			}
		}

		got := false
		h.node(h.ids[0]).GetObject(bucket, objKey, func(_ storage.ObjectInfo, b []byte, err error) {
			got = true
			if err != nil || !bytes.Equal(b, body) {
				s.Fail(fmt.Errorf("keep=%d: get %s/%s: len=%d err=%v", keep, bucket, objKey, len(b), err))
			}
		})
		if err := s.RunUntilConverged(s.Now().Add(10*time.Second), func() bool { return got }); err != nil {
			s.Fail(err)
			return s
		}
		if _, err := h.acks.Check(rd, nil); err != nil {
			s.Fail(fmt.Errorf("keep=%d: ack lineage: %w", keep, err))
			return s
		}
		if err := h.gc.check(); err != nil {
			s.Fail(fmt.Errorf("keep=%d: gc oracle: %w", keep, err))
		}
		return s
	}
}

// TestMigrationVerifyBelowKCompensates proves that leaving 3 verified shards
// (< k=4) on a staged 4+2 object before StepVerify prevents StepFlip from
// running and compensates the saga with the source object intact.
func TestMigrationVerifyBelowKCompensates(t *testing.T) {
	t.Parallel()
	seeds := []int64{31337, 3, 4}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			simtest.RequireDeterministic(t, seed, migThresholdRun(3, saga.StateCompensated))
		})
	}
}

// TestMigrationVerifyBelowQuorumWaitsForRebuild proves that leaving 4 verified
// shards (k=4 <= 4 < quorum=5) before StepVerify blocks StepFlip until
// destination holders rebuild to quorum and then completes cleanly.
func TestMigrationVerifyBelowQuorumWaitsForRebuild(t *testing.T) {
	t.Parallel()
	seeds := []int64{31337, 3, 4}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			simtest.RequireDeterministic(t, seed, migThresholdRun(4, saga.StateCompleted))
		})
	}
}

// verifyWaveNet fires onWave right after the coordinator's opVerifyShards
// (channel 5, op 4) wave finishes so tests can wipe destination holders in the
// window between catch-up Root verification and Retire's manifest swap.
type verifyWaveNet struct {
	env.Transport
	self     env.NodeID
	coord    *env.NodeID
	active   *bool
	inflight *int
	onWave   func()
}

func (v verifyWaveNet) Request(to env.NodeID, req []byte, timeout time.Duration, cb func([]byte, error)) {
	if v.self == *v.coord && *v.active && len(req) >= 2 && req[0] == 5 && req[1] == 4 {
		*v.inflight++
		v.Transport.Request(to, req, timeout, func(resp []byte, err error) {
			cb(resp, err)
			*v.inflight--
			if *v.inflight == 0 && *v.active {
				v.onWave()
			}
		})
		return
	}
	v.Transport.Request(to, req, timeout, cb)
}

// migRetireRecheckRun wipes wipeCount destination holders of stripe 0 right
// after catch-up Root verification completes and before Retire swaps manifests,
// checking via CommitSink that Retire never swaps onto an under-quorum or
// damaged version.
func migRetireRecheckRun(wipeCount int, expectRecopy bool) func(int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		const (
			bucket = "m"
			from   = "r0"
			to     = "r1"
			objKey = "ret0"
		)
		layout := storage.Layout{K: 4, M: 2, StripeSize: dstStripe}
		quorum := min(layout.K+1, layout.K+layout.M)
		orphanGrace := 20 * time.Second
		cfg := dstConfig()
		cfg.Layout = layout
		cfg.OrphanGrace = orphanGrace
		cfg.GCGrace = 5 * time.Second
		cfg.ViewSettle = 2 * time.Second
		cfg.MaintainPeriod = 500 * time.Millisecond
		cfg.SagaPolicy = saga.DefaultPolicy()
		cfg.SagaPolicy.BaseDelay = 200 * time.Millisecond

		var h *harness
		var coord env.NodeID
		var sagaID string
		var sourceVer string
		var damagedVer storage.VersionID
		waveActive := false
		inflightVerify := 0
		damaged := false
		deleteSourceFails := 0
		swaps := 0
		mk := occManifestKey(bucket, objKey)

		cfg.CommitSink = func(key string, _ occ.Record, v occ.Version) {
			if key != mk || v.Tombstone() || !damaged {
				return
			}
			man, err := manifestOf(v.Value())
			if err != nil {
				h.s.Fail(err)
				return
			}
			if man.Version.String() == sourceVer {
				return
			}
			swaps++
			if expectRecopy && man.Version == damagedVer {
				h.s.Fail(fmt.Errorf("retire swapped manifest to damaged version %s with fewer than k=%d shards", damagedVer, layout.K))
				return
			}
			for si := range man.Stripes {
				if got := h.verifiableShards(man, si); got < quorum {
					h.s.Fail(fmt.Errorf("retire swapped manifest to version %s while stripe %d had %d verified shards, want >= quorum=%d", man.Version, si, got, quorum))
					return
				}
			}
		}

		cfg.WrapMigration = func(d saga.Definition) saga.Definition {
			steps := append([]saga.Step(nil), d.Steps...)
			for i, st := range steps {
				if st.Name() == saga.StepDeleteSource {
					steps[i] = hookStep{
						Step: st,
						before: func() {
							if !damaged {
								waveActive = true
							}
						},
						after: func(err error) {
							if err != nil {
								deleteSourceFails++
							}
						},
					}
				}
			}
			d.Steps = steps
			return d
		}

		onWave := func() {
			waveActive = false
			if damaged {
				return
			}
			damaged = true
			nd := h.node(coord)
			if nd == nil {
				h.s.Fail(fmt.Errorf("coordinator %s down before retire", coord))
				return
			}
			mans, err := stagedManifests(nd, bucket, sagaID)
			if err != nil || len(mans) != 1 {
				h.s.Fail(fmt.Errorf("expected 1 staged manifest before retire, got %d (err=%v)", len(mans), err))
				return
			}
			man := mans[0]
			damagedVer = man.Version
			st0 := man.Stripes[0]
			off := int(seed % int64(len(st0.Shards)))
			for j := range wipeCount {
				idx := (off + j) % len(st0.Shards)
				holder := st0.Shards[idx].Node
				if err := h.s.WipeRejoin(holder); err != nil {
					h.s.Fail(fmt.Errorf("wipe destination holder %s: %w", holder, err))
					return
				}
			}
			wantLeft := len(st0.Shards) - wipeCount
			if got := h.verifiableShards(man, 0); got != wantLeft {
				h.s.Fail(fmt.Errorf("after wiping %d holders stripe 0 has %d verified shards, want %d", wipeCount, got, wantLeft))
				return
			}
		}

		var err error
		h, err = newWrappedHarness(seed, dstNodes, cfg, func(e env.Env) env.Env {
			return e.WithNet(verifyWaveNet{
				Transport: e.Net,
				self:      e.ID,
				coord:     &coord,
				active:    &waveActive,
				inflight:  &inflightVerify,
				onWave:    onWave,
			})
		})
		if err != nil {
			h.s.Fail(err)
			return h.s
		}
		s := h.s
		if err := s.RunFor(5 * time.Second); err != nil {
			return s
		}
		if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: bucket, Region: from}); err != nil {
			s.Fail(err)
			return s
		}
		if err := s.RunUntilConverged(s.Now().Add(20*time.Second), h.converged); err != nil {
			s.Fail(err)
			return s
		}

		body := payload(s.WorkloadRand(), layout.StripeSize+73)
		acked := false
		h.node(h.ids[0]).PutObject(storage.PutRequest{Bucket: bucket, Key: objKey, Data: body}, func(oi storage.ObjectInfo, err error) {
			if err != nil {
				s.Fail(fmt.Errorf("put %s: %w", objKey, err))
				return
			}
			sourceVer = oi.Version
			acked = true
		})
		if err := s.RunUntilConverged(s.Now().Add(20*time.Second), func() bool { return acked && h.converged() }); err != nil {
			s.Fail(err)
			return s
		}

		coord = h.ids[int(seed%int64(len(h.ids)))]
		if string(s.Region(coord)) == to {
			for _, id := range h.ids {
				if string(s.Region(id)) != to {
					coord = id
					break
				}
			}
		}

		mi, err := h.node(coord).StartMigration(bucket, to)
		if err != nil {
			s.Fail(fmt.Errorf("start migration on %s: %w", coord, err))
			return s
		}
		sagaID = mi.ID

		var st saga.Status
		if err := s.RunUntilConverged(s.Now().Add(120*time.Second), func() bool {
			nd := h.node(coord)
			if nd == nil {
				return false
			}
			got, err := nd.Engine().Status(saga.ID(mi.ID))
			st = got
			return err == nil && got.State.Terminal()
		}); err != nil {
			s.Fail(fmt.Errorf("wipeCount=%d: migration never reached terminal state (state=%v step=%d err=%v): %w", wipeCount, st.State, st.Step, st.Err, err))
			return s
		}
		if !damaged {
			s.Fail(fmt.Errorf("wipeCount=%d: pre-retire wipe trigger never fired", wipeCount))
			return s
		}
		if deleteSourceFails < 1 {
			s.Fail(fmt.Errorf("wipeCount=%d: delete-source never failed after pre-retire wipe (swaps=%d)", wipeCount, swaps))
			return s
		}
		if st.State != saga.StateCompleted {
			s.Fail(fmt.Errorf("wipeCount=%d: migration ended %v (err=%v), want completed", wipeCount, st.State, st.Err))
			return s
		}
		if swaps < 1 {
			s.Fail(fmt.Errorf("wipeCount=%d: expected manifest swap commit after recovery, got 0", wipeCount))
			return s
		}
		if err := s.RunUntilConverged(s.Now().Add(40*time.Second), h.converged); err != nil {
			s.Fail(err)
			return s
		}

		for _, nd := range h.up() {
			rd, err := nd.OCC().Reader()
			if err != nil {
				s.Fail(err)
				return s
			}
			r, err := rd.Route(bucket)
			if err != nil || string(r) != to {
				s.Fail(fmt.Errorf("wipeCount=%d: node %s routes %s to %q (%v), want %q", wipeCount, nd.ID(), bucket, r, err, to))
				return s
			}
			for _, k := range rd.Keys() {
				if strings.Contains(k, "~lost/") {
					s.Fail(fmt.Errorf("wipeCount=%d: node %s has loss record %q", wipeCount, nd.ID(), k))
					return s
				}
			}
		}
		if len(h.declared) > 0 {
			s.Fail(fmt.Errorf("wipeCount=%d: loss declared: %v", wipeCount, h.declared))
			return s
		}

		noOutside := func() bool {
			for _, id := range h.ids {
				if string(s.Region(id)) == to {
					continue
				}
				for _, name := range s.Disk(id).Names() {
					if strings.HasPrefix(name, "shards/"+bucket+"/") {
						return false
					}
				}
			}
			return true
		}
		if err := s.RunUntilConverged(s.Now().Add(orphanGrace+cfg.GCGrace+20*time.Second), noOutside); err != nil {
			s.Fail(fmt.Errorf("wipeCount=%d: shards left outside %s: %w", wipeCount, to, err))
			return s
		}

		rd, err := h.up()[0].OCC().Reader()
		if err != nil {
			s.Fail(err)
			return s
		}
		val, _, err := rd.Get(mk)
		if err != nil {
			s.Fail(fmt.Errorf("wipeCount=%d: manifest %s/%s missing: %w", wipeCount, bucket, objKey, err))
			return s
		}
		man, err := manifestOf(val)
		if err != nil {
			s.Fail(err)
			return s
		}
		if man.Version.String() == sourceVer {
			s.Fail(fmt.Errorf("wipeCount=%d: live version is still source version %s", wipeCount, sourceVer))
			return s
		}
		if expectRecopy && man.Version == damagedVer {
			s.Fail(fmt.Errorf("wipeCount=%d: live version is damaged version %s, want recopied version", wipeCount, damagedVer))
			return s
		}
		for si, stripe := range man.Stripes {
			inDest := 0
			for idx, ref := range stripe.Shards {
				if string(s.Region(ref.Node)) != to {
					s.Fail(fmt.Errorf("wipeCount=%d: live stripe %d shard %d on %s in %s, want %s", wipeCount, si, idx, ref.Node, s.Region(ref.Node), to))
					return s
				}
				blob, ok := s.Disk(ref.Node).Peek(man.ShardID(si, idx).BlobName())
				if !ok {
					continue
				}
				if _, _, derr := storage.DecodeShard(blob); derr == nil && storage.BlobChecksum(blob) == ref.Checksum {
					inDest++
				}
			}
			if inDest < quorum {
				s.Fail(fmt.Errorf("wipeCount=%d: live stripe %d holds %d verified shards in %s, want >= quorum=%d", wipeCount, si, inDest, to, quorum))
				return s
			}
		}

		got := false
		h.node(h.ids[0]).GetObject(bucket, objKey, func(_ storage.ObjectInfo, b []byte, err error) {
			got = true
			if err != nil || !bytes.Equal(b, body) {
				s.Fail(fmt.Errorf("wipeCount=%d: get %s/%s: len=%d err=%v", wipeCount, bucket, objKey, len(b), err))
			}
		})
		if err := s.RunUntilConverged(s.Now().Add(10*time.Second), func() bool { return got }); err != nil {
			s.Fail(err)
			return s
		}
		if _, err := h.acks.Check(rd, nil); err != nil {
			s.Fail(fmt.Errorf("wipeCount=%d: ack lineage: %w", wipeCount, err))
			return s
		}
		if err := h.gc.check(); err != nil {
			s.Fail(fmt.Errorf("wipeCount=%d: gc oracle: %w", wipeCount, err))
		}
		return s
	}
}

// TestMigrationRetireBelowQuorumWaitsForRebuild proves Retire refuses to swap
// manifests when a pre-retire wipe leaves 4 verified shards (< quorum=5) and
// swaps only after destination holders rebuild to quorum.
func TestMigrationRetireBelowQuorumWaitsForRebuild(t *testing.T) {
	t.Parallel()
	seeds := []int64{31337, 3, 4}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			simtest.RequireDeterministic(t, seed, migRetireRecheckRun(2, false))
		})
	}
}

// TestMigrationRetireBelowKRecopiesAndNeverGoesLive proves Retire rejects a
// staged version reduced to 3 verified shards (< k=4) after catch-up Root,
// deletes the damaged staged record, and lets post-pivot retry re-copy from
// the live source before completing.
func TestMigrationRetireBelowKRecopiesAndNeverGoesLive(t *testing.T) {
	t.Parallel()
	seeds := []int64{31337, 3, 4}
	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed=%d", seed), func(t *testing.T) {
			t.Parallel()
			simtest.RequireDeterministic(t, seed, migRetireRecheckRun(3, true))
		})
	}
}
