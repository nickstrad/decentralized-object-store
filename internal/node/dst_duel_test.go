package node_test

import (
	"bytes"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/sim"
	"github.com/nickstrad/decentralized-object-store/internal/sim/simtest"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
)

type duelSaga struct {
	coord env.NodeID
	id    saga.ID
	in    saga.MigrationInput
}

type duelResult struct {
	outcomes map[saga.ID]string
	elapsed  time.Duration
}

func duelRun(res *duelResult) func(seed int64) *sim.Sim {
	return func(seed int64) *sim.Sim {
		*res = duelResult{outcomes: map[saga.ID]string{}}
		cfg := partitionConfig()
		cfg.Layout = smallLayout()
		cfg.ViewSettle = 2 * time.Second
		h, err := newHarness(seed, mtNodes, cfg, sim.DefaultNetConfig())
		if err != nil {
			h.s.Fail(err)
			return h.s
		}
		s := h.s
		if err := s.RunFor(2 * time.Second); err != nil {
			return s
		}
		data := map[string]map[string][]byte{}
		w := s.WorkloadRand()
		for _, b := range []string{"da", "db"} {
			if err := h.node(h.ids[0]).CreateBucket(storage.BucketSpec{Name: b, Region: migFrom}); err != nil {
				s.Fail(err)
				return s
			}
			data[b] = map[string][]byte{}
		}
		if err := s.RunUntilConverged(s.Now().Add(20*time.Second), h.converged); err != nil {
			s.Fail(err)
			return s
		}
		pending := 0
		for _, b := range []string{"da", "db"} {
			for i := range 4 {
				key := fmt.Sprintf("o%d", i)
				body := payload(w, 1+w.IntN(2*dstStripe))
				pending++
				h.node(h.pick()).PutObject(storage.PutRequest{Bucket: b, Key: key, Data: body}, func(_ storage.ObjectInfo, err error) {
					pending--
					if err != nil {
						s.Fail(fmt.Errorf("seed put %s/%s: %w", b, key, err))
						return
					}
					data[b][key] = body
				})
			}
		}
		if err := s.RunUntilConverged(s.Now().Add(30*time.Second), func() bool { return pending == 0 && h.converged() }); err != nil {
			s.Fail(err)
			return s
		}
		sameTarget := seed%2 == 0
		bTarget := "r2"
		if sameTarget {
			bTarget = migTo
		}
		sagas := []duelSaga{
			{coord: h.ids[1], id: "duel-a", in: saga.MigrationInput{Bucket: "da", From: migFrom, To: migTo}},
			{coord: h.ids[2], id: "duel-b", in: saga.MigrationInput{Bucket: "da", From: migFrom, To: bTarget}},
			{coord: h.ids[4], id: "duel-c", in: saga.MigrationInput{Bucket: "db", From: migFrom, To: migTo}},
		}
		begin := s.Now()
		for _, d := range sagas {
			if err := h.node(d.coord).BeginMigration(d.id, d.in); err != nil {
				s.Fail(err)
				return s
			}
		}
		status := func(d duelSaga) saga.Status {
			st, _ := h.node(d.coord).Engine().Status(d.id)
			return st
		}
		if err := s.RunUntilConverged(begin.Add(90*time.Second), func() bool {
			for _, d := range sagas {
				if st := status(d); st.State != saga.StateCompleted && st.State != saga.StateCompensated {
					return false
				}
			}
			return true
		}); err != nil {
			var states []string
			for _, d := range sagas {
				st := status(d)
				states = append(states, fmt.Sprintf("%s=%v/step%d/%v", d.id, st.State, st.Step, st.Err))
			}
			s.Fail(fmt.Errorf("duelling migrations never ended (%s): %w", strings.Join(states, " "), err))
			return s
		}
		res.elapsed = s.Now().Sub(begin)
		for _, d := range sagas {
			st := status(d)
			out := st.State.String()
			if st.State == saga.StateCompleted && len(st.Outputs) == saga.MigrationSteps {
				out = string(st.Outputs[saga.MigrationSteps-1])
			}
			res.outcomes[d.id] = out
		}
		if res.outcomes["duel-c"] != saga.OutcomeMoved {
			s.Fail(fmt.Errorf("the migration of the other bucket ended %s, want moved", res.outcomes["duel-c"]))
			return s
		}
		moved := 0
		for _, id := range []saga.ID{"duel-a", "duel-b"} {
			if res.outcomes[id] == saga.OutcomeMoved {
				moved++
			}
		}
		if moved != 1 {
			s.Fail(fmt.Errorf("duel on one bucket ended %v, want exactly one moved", res.outcomes))
			return s
		}
		if err := s.RunUntilConverged(s.Now().Add(40*time.Second), h.converged); err != nil {
			s.Fail(err)
			return s
		}
		rd, err := h.up()[0].OCC().Reader()
		if err != nil {
			s.Fail(err)
			return s
		}
		for _, b := range []string{"da", "db"} {
			route, _ := rd.Route(b)
			live, _, _ := rd.Get(occ.TagKey(b, "", "~live"))
			if string(live) != string(route) {
				s.Fail(fmt.Errorf("bucket %s routed to %q but live at %q after the duel", b, route, live))
				return s
			}
			for _, k := range rd.Keys() {
				_, parts, err := occ.ParseKey(k)
				if err == nil && len(parts) == 3 && parts[0] == b && strings.HasPrefix(parts[2], "~") && rd.Record(k).Live() &&
					(strings.HasPrefix(parts[2], "~stage/") || strings.HasPrefix(parts[2], "~reg") || strings.HasPrefix(parts[2], "~claim/")) {
					s.Fail(fmt.Errorf("bucket %s keeps migration state %q after the duel", b, parts[2]))
					return s
				}
			}
			for _, key := range slices.Sorted(maps.Keys(data[b])) {
				want := data[b][key]
				val, _, err := rd.Get(occ.ManifestKey(b, key))
				if err != nil {
					s.Fail(fmt.Errorf("%s/%s lost in the duel: %w", b, key, err))
					return s
				}
				man, _ := manifestOf(val)
				if rs := h.shardRegions(man); len(rs) != 1 || !rs[route] {
					s.Fail(fmt.Errorf("%s/%s lives in %v, bucket routed to %s", b, key, rs, route))
					return s
				}
				got := false
				h.node(h.pick()).GetObject(b, key, func(_ storage.ObjectInfo, d []byte, err error) {
					got = true
					if err != nil || !bytes.Equal(d, want) {
						s.Fail(fmt.Errorf("%s/%s after the duel: %v", b, key, err))
					}
				})
				if err := s.RunUntilConverged(s.Now().Add(10*time.Second), func() bool { return got }); err != nil {
					s.Fail(err)
					return s
				}
			}
		}
		return s
	}
}

// TestClusterDuellingMigrationsDST starts two simultaneous migrations on one
// bucket (same or different targets) alongside a third on another bucket,
// proving exactly one duelling saga moves the bucket, the third moves cleanly,
// no migration metadata leaks, and every object reads from its final region.
func TestClusterDuellingMigrationsDST(t *testing.T) {
	t.Parallel()
	var m measure
	sweepSharded(t, func(t *testing.T, seed int64) {
		var res duelResult
		simtest.RequireDeterministic(t, seed, duelRun(&res))
		m.add("duel/elapsed_ms", res.elapsed.Milliseconds())
	})
	t.Cleanup(func() { m.report(t) })
}
