package node_test

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

const lateKey = "late"

type lateResult struct {
	committedBeforeRoute bool
	state                saga.State
	outcome              string
}

func lateFlipRun(res *lateResult, isolate bool) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		*res = lateResult{}
		cfg := partitionConfig()
		cfg.Layout = smallLayout()
		cfg.ViewSettle = 2 * time.Second
		cfg.OCCFanout = mtNodes - 1
		if !isolate {
			cfg.MetaPeriod = 10 * time.Second
			cfg.OCCPeriod = 10 * time.Second
		}
		var h *harness
		var writer env.NodeID
		coord := env.NodeID("")
		body := []byte(nil)
		seenLate := false
		cfg.CommitSink = func(key string, _ occ.Record, _ occ.Version) {
			if key != occ.ManifestKey(mtBucket, lateKey) || writer == "" || seenLate {
				return
			}
			seenLate = true
			rd, err := h.node(writer).OCC().Reader()
			if err == nil {
				r, _ := rd.Route(mtBucket)
				res.committedBeforeRoute = string(r) == migFrom
			}
			if isolate {
				if err := h.s.Isolate(writer); err != nil {
					h.s.Fail(err)
				}
				return
			}
			if err := h.s.SetLinkLoss(writer, coord, 0); err != nil {
				h.s.Fail(err)
			}
			if err := h.s.Unclog(coord, writer); err != nil {
				h.s.Fail(err)
			}
		}
		cfg.WrapMigration = func(d saga.Definition) saga.Definition {
			steps := append([]saga.Step(nil), d.Steps...)
			steps[saga.MigrationPivot] = atFlip{Step: steps[saga.MigrationPivot], fn: func() {
				if writer != "" {
					return
				}
				for _, id := range h.region(migFrom) {
					if id != coord && h.node(id) != nil {
						writer = id
						break
					}
				}
				if !isolate {
					if err := h.s.SetLinkLoss(writer, coord, sim.PPM); err != nil {
						h.s.Fail(err)
					}
					if err := h.s.Clog(coord, writer); err != nil {
						h.s.Fail(err)
					}
				}
				body = payload(h.s.WorkloadRand(), 1+dstStripe)
				h.node(writer).PutObject(storage.PutRequest{Bucket: mtBucket, Key: lateKey, Data: body}, func(_ storage.ObjectInfo, err error) {
					if err != nil {
						h.s.Fail(fmt.Errorf("late put on %s: %w", writer, err))
					}
				})
				if isolate {
					for _, id := range h.ids {
						if id != writer {
							if err := h.s.SetLinkLoss(writer, id, sim.PPM); err != nil {
								h.s.Fail(err)
							}
						}
					}
				}
			}}
			d.Steps = steps
			return d
		}
		h, err := newHarness(seed, mtNodes, cfg, sim.DefaultNetConfig())
		if err != nil {
			h.s.Fail(err)
			return h.s
		}
		s := h.s
		coord = h.ids[1]
		h.createBucket(env.Time(2*time.Second), storage.BucketSpec{Name: mtBucket, Region: migFrom})
		s.At(env.Time(3*time.Second), "seed object", func() {
			h.node(h.ids[0]).PutObject(storage.PutRequest{Bucket: mtBucket, Key: "o", Data: []byte("seed")}, func(_ storage.ObjectInfo, err error) {
				if err != nil {
					s.Fail(err)
				}
			})
		})
		if err := s.RunUntilConverged(env.Time(60*time.Second), func() bool { return s.Now() > env.Time(5*time.Second) && h.converged() }); err != nil {
			s.Fail(err)
			return s
		}
		if err := h.node(coord).BeginMigration(mtSaga, saga.MigrationInput{Bucket: mtBucket, From: migFrom, To: migTo}); err != nil {
			s.Fail(err)
			return s
		}
		var st saga.Status
		if err := s.RunUntilConverged(s.Now().Add(90*time.Second), func() bool {
			got, err := h.node(coord).Engine().Status(mtSaga)
			st = got
			return err == nil && got.State == saga.StateCompleted
		}); err != nil {
			s.Fail(fmt.Errorf("migration never completed (state %v, step %d, err %v): %w", st.State, st.Step, st.Err, err))
			return s
		}
		res.state = st.State
		if len(st.Outputs) == saga.MigrationSteps {
			res.outcome = string(st.Outputs[saga.MigrationSteps-1])
		}
		if isolate {
			if err := s.RunFor(5 * time.Second); err != nil {
				return s
			}
			s.HealAll()
		}
		if err := s.RunUntilConverged(s.Now().Add(60*time.Second), h.converged); err != nil {
			s.Fail(fmt.Errorf("no convergence after the migration: %w", err))
			return s
		}
		if err := s.RunFor(h.cfg.GCGrace + 3*time.Second); err != nil {
			return s
		}
		rd, err := h.up()[0].OCC().Reader()
		if err != nil {
			s.Fail(err)
			return s
		}
		if _, err := h.acks.Check(rd, func(k string) bool { return k == occ.ManifestKey(mtBucket, lateKey) }); err != nil {
			s.Fail(err)
			return s
		}
		if !rd.Record(occ.ManifestKey(mtBucket, lateKey)).Live() {
			return s
		}
		got := false
		h.node(h.ids[0]).GetObject(mtBucket, lateKey, func(_ storage.ObjectInfo, data []byte, err error) {
			got = true
			if err != nil || !bytes.Equal(data, body) {
				s.Fail(fmt.Errorf("late write live in metadata but unreadable: %v", err))
			}
		})
		if err := s.RunUntilConverged(s.Now().Add(10*time.Second), func() bool { return got }); err != nil {
			s.Fail(err)
		}
		if err := h.gc.check(); err != nil {
			s.Fail(err)
		}
		return s
	}
}

// TestMigrationHoldsAWriteAcknowledgedJustBeforeTheFlipDST proves a write
// committed on a non-coordinator peer right before the routing flip reaches it
// (with its delta to the coordinator dropped) is held across pointer agreement,
// copied, and never purged.
func TestMigrationHoldsAWriteAcknowledgedJustBeforeTheFlipDST(t *testing.T) {
	t.Parallel()
	sweepSharded(t, func(t *testing.T, seed int64) {
		var res lateResult
		simtest.RequireDeterministic(t, seed, lateFlipRun(&res, false))
		if !res.committedBeforeRoute {
			t.Fatalf("the late write committed after its node saw the new route; the scenario did not set up the race")
		}
	})
}

// TestMigrationLateWriteOfAnIsolatedPeerStaysReadableDST proves a pre-flip
// write on a peer that is isolated before the migration finishes is omitted
// from Retire's explicit dead-version purge list and stays readable (or on a
// discard record) once the peer rejoins within OrphanGrace.
func TestMigrationLateWriteOfAnIsolatedPeerStaysReadableDST(t *testing.T) {
	t.Parallel()
	sweepSharded(t, func(t *testing.T, seed int64) {
		var res lateResult
		simtest.RequireDeterministic(t, seed, lateFlipRun(&res, true))
		if !res.committedBeforeRoute {
			t.Fatalf("the late write committed after its node saw the new route; the scenario did not set up the race")
		}
	})
}
