package node_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/node"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

const (
	mtBucket  = "mt"
	mtKeys    = 8
	mtBegin   = env.Time(6 * time.Second)
	mtTraffic = env.Time(13 * time.Second)
	mtGap     = 150 * time.Millisecond
	mtSaga    = saga.ID("mt-1")
	mtNodes   = 9
	mtBurst   = 8
)

type atFlip struct {
	saga.Step
	fn func()
}

func (a atFlip) Do(c saga.Call, done func([]byte, error)) {
	a.fn()
	a.Step.Do(c, done)
}

func smallLayout() storage.Layout { return storage.Layout{K: 2, M: 1, StripeSize: dstStripe} }

type refuseAfter struct{ saga.Step }

func (r refuseAfter) Do(c saga.Call, done func([]byte, error)) {
	r.Step.Do(c, func(_ []byte, _ error) {
		done(nil, fmt.Errorf("test: %s refused after acting: %w", r.Name(), saga.ErrPermanent))
	})
}

func refuseStep(i int) func(saga.Definition) saga.Definition {
	return func(d saga.Definition) saga.Definition {
		steps := append([]saga.Step(nil), d.Steps...)
		steps[i] = refuseAfter{steps[i]}
		d.Steps = steps
		return d
	}
}

func lossyNet() sim.NetConfig {
	c := sim.DefaultNetConfig()
	c.DropPPM = 20_000
	c.Latency = sim.Range{Min: time.Millisecond, Max: 20 * time.Millisecond}
	c.InterRegionLatency = sim.Range{Min: 20 * time.Millisecond, Max: 150 * time.Millisecond}
	return c
}

type trafficResult struct {
	state                         saga.State
	acked, puts, deletes, refused int
	duringMigration               int
	elapsed                       time.Duration
	tally                         map[string]int
}

func shaHex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func (h *harness) shardRegions(man *storage.Manifest) map[env.Region]bool {
	out := map[env.Region]bool{}
	for _, st := range man.Stripes {
		for _, sh := range st.Shards {
			out[h.s.Region(sh.Node)] = true
		}
	}
	return out
}

type trafficGen struct {
	h        *harness
	bucket   string
	avoid    []env.NodeID
	contents map[string][]byte
	res      *trafficResult
	next     int
}

func (h *harness) traffic(bucket string, from, to env.Time, gap time.Duration, avoid []env.NodeID, contents map[string][]byte, res *trafficResult) *trafficGen {
	g := &trafficGen{h: h, bucket: bucket, avoid: avoid, contents: contents, res: res, next: mtKeys}
	w := h.s.WorkloadRand()
	at := from
	for at < to {
		at = at.Add(gap/2 + time.Duration(w.IntN(int(gap))))
		h.s.At(at, "traffic", g.op)
	}
	return g
}

func (g *trafficGen) op() { g.run(false) }

func (g *trafficGen) fresh() { g.run(true) }

func (g *trafficGen) run(fresh bool) {
	h, s, bucket, res, contents := g.h, g.h.s, g.bucket, g.res, g.contents
	w := s.WorkloadRand()
	{
		{
			id := h.pick(g.avoid...)
			n := h.node(id)
			if n == nil {
				return
			}
			key := fmt.Sprintf("k%d", w.IntN(mtKeys))
			r := w.IntN(8)
			if fresh {
				r = 0
			}
			switch {
			case r < 2:
				key = fmt.Sprintf("n%d", g.next)
				g.next++
				fallthrough
			case r < 6:
				body := payload(w, 1+w.IntN(2*dstStripe))
				res.puts++
				n.PutObject(storage.PutRequest{Bucket: bucket, Key: key, Data: body}, func(_ storage.ObjectInfo, err error) {
					switch {
					case err == nil:
						res.acked++
						contents[shaHex(body)] = body
					case errors.Is(err, storage.ErrBusy), errors.Is(err, storage.ErrNoSuchBucket):
						res.refused++
					case errors.Is(err, storage.ErrUnavailable), errors.Is(err, occ.ErrQuotaExceeded):
					default:
						s.Fail(fmt.Errorf("traffic put %s on %s: %w", key, id, err))
					}
				})
			default:
				res.deletes++
				_, err := n.DeleteObject(bucket, key, storage.Condition{})
				switch {
				case err == nil:
					res.acked++
				case errors.Is(err, storage.ErrBusy), errors.Is(err, storage.ErrNoSuchBucket):
					res.refused++
				case isNotFoundErr(err):
				default:
					s.Fail(fmt.Errorf("traffic delete %s on %s: %w", key, id, err))
				}
			}
		}
	}
}

func isNotFoundErr(err error) bool {
	return errors.Is(err, occ.ErrNotFound) || errors.Is(err, env.ErrNotFound) || errors.Is(err, storage.ErrNotFound)
}

func (h *harness) checkObjects(bucket string, contents map[string][]byte, region string, strays bool) {
	s := h.s
	ref := h.up()[0]
	rd, err := ref.OCC().Reader()
	if err != nil {
		s.Fail(err)
		return
	}
	end := s.Now()
	i := 0
	for _, k := range rd.Keys() {
		kind, parts, err := occ.ParseKey(k)
		if err != nil || kind != occ.KindManifest || parts[0] != bucket {
			continue
		}
		key := parts[1]
		rec := rd.Record(k)
		if rec.Live() {
			v, _ := rec.Version()
			man, err := manifestOf(v.Value())
			if err != nil {
				s.Fail(err)
				return
			}
			if region != "" && !strays {
				if rs := h.shardRegions(man); len(rs) != 1 || !rs[env.Region(region)] {
					s.Fail(fmt.Errorf("object %s lives in %v, want only %s", key, rs, region))
					return
				}
			}
		}
		live := rec.Live()
		i++
		s.At(end.Add(time.Duration(i)*20*time.Millisecond), "final get", func() {
			from := h.pick()
			h.node(from).GetObject(bucket, key, func(_ storage.ObjectInfo, got []byte, err error) {
				switch {
				case !live && isNotFoundErr(err):
				case !live:
					s.Fail(fmt.Errorf("deleted %s/%s from %s: %v", bucket, key, from, err))
				case err != nil:
					s.Fail(fmt.Errorf("final get %s/%s from %s: %w", bucket, key, from, err))
				default:
					if want, ok := contents[shaHex(got)]; !ok || !bytes.Equal(want, got) {
						s.Fail(fmt.Errorf("final get %s/%s from %s returned bytes of no acknowledged write", bucket, key, from))
					}
				}
			})
		})
	}
	_ = s.RunFor(time.Duration(i+5) * 20 * time.Millisecond)
}

func migrationTrafficRun(res *trafficResult) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		*res = trafficResult{}
		cfg := partitionConfig()
		cfg.Layout = smallLayout()
		cfg.ViewSettle = 2 * time.Second
		compensate := seed%2 == 0
		var gen *trafficGen
		var h *harness
		cfg.WrapMigration = func(d saga.Definition) saga.Definition {
			steps := append([]saga.Step(nil), d.Steps...)
			steps[saga.MigrationPivot] = atFlip{Step: steps[saga.MigrationPivot], fn: func() {
				for i := range mtBurst {
					h.s.At(h.s.Now().Add(time.Duration(i)*250*time.Millisecond), "burst", gen.fresh)
				}
			}}
			d.Steps = steps
			if compensate {
				d = refuseStep(2)(d)
			}
			return d
		}
		h, err := newHarness(seed, mtNodes, cfg, lossyNet())
		if err != nil {
			h.s.Fail(err)
			return h.s
		}
		s := h.s
		coord := h.ids[1]
		contents := map[string][]byte{}
		h.createBucket(env.Time(2*time.Second), storage.BucketSpec{Name: mtBucket, Region: migFrom})
		gen = h.traffic(mtBucket, env.Time(3*time.Second), mtTraffic, mtGap, []env.NodeID{coord}, contents, res)
		if err := s.RunUntil(mtBegin); err != nil {
			return s
		}
		if err := s.RunUntilConverged(mtBegin.Add(10*time.Second), func() bool {
			rd, err := h.node(coord).OCC().Reader()
			if err != nil {
				return false
			}
			r, _ := rd.Route(mtBucket)
			_, lerr := h.node(coord).ListObjects(mtBucket, "", 1)
			return string(r) == migFrom && lerr == nil
		}); err != nil {
			s.Fail(fmt.Errorf("the coordinator never saw the bucket and its route: %w", err))
			return s
		}
		if err := h.node(coord).BeginMigration(mtSaga, saga.MigrationInput{Bucket: mtBucket, From: migFrom, To: migTo}); err != nil {
			s.Fail(err)
			return s
		}
		before, begun := res.acked, s.Now()
		var st saga.Status
		if err := s.RunUntilConverged(begun.Add(60*time.Second), func() bool {
			n := h.node(coord)
			if n == nil {
				return false
			}
			got, err := n.Engine().Status(mtSaga)
			st = got
			return err == nil && (got.State == saga.StateCompleted || got.State == saga.StateCompensated)
		}); err != nil {
			s.Fail(fmt.Errorf("migration never ended (state %v, step %d, err %v): %w", st.State, st.Step, st.Err, err))
			return s
		}
		res.state, res.elapsed = st.State, s.Now().Sub(begun)
		res.duringMigration = res.acked - before
		want := saga.StateCompleted
		if compensate {
			want = saga.StateCompensated
		}
		if st.State != want {
			s.Fail(fmt.Errorf("migration ended %v (step %d: %v), want %v", st.State, st.Step, st.Err, want))
			return s
		}
		if err := s.RunUntil(max(s.Now(), mtTraffic).Add(time.Second)); err != nil {
			return s
		}
		if err := s.RunUntilConverged(s.Now().Add(40*time.Second), h.converged); err != nil {
			s.Fail(fmt.Errorf("no convergence after the migration: %w", err))
			return s
		}
		if err := s.RunFor(3 * time.Second); err != nil {
			return s
		}
		rd, err := h.up()[0].OCC().Reader()
		if err != nil {
			s.Fail(err)
			return s
		}
		tally, err := h.acks.check(rd, func(k string) bool {
			kind, parts, err := occ.ParseKey(k)
			return err == nil && kind == occ.KindManifest && parts[0] == mtBucket
		})
		res.tally = tally
		if err != nil {
			s.Fail(err)
			return s
		}
		region := migTo
		if compensate {
			region = migFrom
		}
		h.checkObjects(mtBucket, contents, region, false)
		if err := h.gc.check(); err != nil {
			s.Fail(err)
		}
		return s
	}
}

func TestClusterMigrationUnderTrafficDST(t *testing.T) {
	t.Parallel()
	var m measure
	sweepSharded(t, func(t *testing.T, seed int64) {
		var res trafficResult
		simtest.RequireDeterministic(t, seed, migrationTrafficRun(&res))
		m.add("migtraffic/elapsed_ms", res.elapsed.Milliseconds())
		m.add("migtraffic/acked", int64(res.acked))
		m.add("migtraffic/acked_during_migration", int64(res.duringMigration))
		m.add("migtraffic/refused_busy", int64(res.refused))
		for k, v := range res.tally {
			m.add("migtraffic/oracle_"+k, int64(v))
		}
	})
	t.Cleanup(func() { m.report(t) })
}

var _ = strings.HasPrefix

var _ = node.Config{}
