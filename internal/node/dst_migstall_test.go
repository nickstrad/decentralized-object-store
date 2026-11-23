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
