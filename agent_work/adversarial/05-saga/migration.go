package main

import (
	"errors"
	"fmt"
	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/saga"
	"github.com/nickstrad/object-store/internal/saga/sagasim"
	"github.com/nickstrad/object-store/internal/sim"
	"time"
)

type cluster struct{ m *sagasim.Model }

func (c cluster) Coordinator() env.NodeID               { return "c1" }
func (c cluster) Begin(id saga.ID) error                { return c.m.Begin("c1", id, input()) }
func (c cluster) Status(id saga.ID) (saga.Status, bool) { return c.m.Status("c1", id) }
func (c cluster) LogName(id saga.ID) string             { return "saga/" + string(id) }
func (c cluster) CheckMoved() error                     { return c.m.CheckMoved(input()) }
func (c cluster) CheckCompensated() error               { return c.m.CheckCompensated(input()) }
func input() saga.MigrationInput {
	return saga.MigrationInput{Bucket: sagasim.Bucket, From: "a", To: "b"}
}
func opts(n int) sagasim.Options {
	return sagasim.Options{Coordinators: []sim.NodeSpec{{ID: "c1", Region: "ra"}}, Stores: []sim.NodeSpec{{ID: "a", Region: "ra"}, {ID: "b", Region: "rb"}, {ID: "c", Region: "rc"}}, Source: "a", Objects: n, Policy: sagasim.MatrixPolicy()}
}

type harness struct {
	n    int
	skip int
}

func (h harness) Build(seed int64, o sagasim.BuildOptions) (*sim.Sim, sagasim.Cluster, error) {
	p := opts(h.n)
	p.Policy = o.Policy
	p.Wrap = func(_ env.NodeID, d saga.Definition) saga.Definition {
		if h.skip >= 0 {
			orig := d.Steps[h.skip]
			d.Steps[h.skip] = &step{name: orig.Name(), do: orig.Do, comp: func(_ saga.Call, done func(error)) { done(nil) }}
		}
		if o.Wrap != nil {
			return o.Wrap(d)
		}
		return d
	}
	p.Configure = func(_ env.NodeID, c *saga.Config) {
		if o.Configure != nil {
			o.Configure(c)
		}
	}
	s, m, e := sagasim.Build(seed, p)
	if e == nil {
		m.AddInvariants(s)
		e = s.RunUntil(0)
		if e == nil {
			e = m.CheckCompensated(input())
		}
	}
	return s, cluster{m}, e
}
func migration() {
	for _, cell := range sagasim.Cells() {
		c := cell
		add("C16", c.Name(), func() string {
			for i := int64(0); i < 40; i++ {
				seed := int64(951001) + i*7919
				rep := &sagasim.Report{}
				s := sagasim.Run(harness{n: []int{1, 8, 32}[i%3], skip: -1}, c, sagasim.RunOptions{Report: rep})(seed)
				check(s.Err() == nil, "seed=%d objects=%d err=%v", seed, []int{1, 8, 32}[i%3], s.Err())
				check(rep.State == c.Want() && rep.Elapsed <= sagasim.MatrixBound, "seed=%d state=%v elapsed=%v", seed, rep.State, rep.Elapsed)
			}
			return "40 new seeds with 1/8/32 objects met terminal, cleanup and 20-second bounds"
		})
	}
	for _, skip := range []int{0, 1, 2} {
		k := skip
		add("C19", fmt.Sprintf("skipped-compensation-%d", k), func() string {
			for seed := int64(960001); seed < 960011; seed++ {
				c := sagasim.Cell{Step: 3, Kind: sagasim.FailBeforeAction}
				s := sagasim.Run(harness{n: 8, skip: k}, c, sagasim.RunOptions{})(seed)
				if k < 2 {
					check(errors.Is(s.Err(), sagasim.ErrCheck), "seed=%d skipped=%d canary not detected: %v", seed, k, s.Err())
				} else {
					check(s.Err() == nil, "seed=%d verify no-op caused failure %v", seed, s.Err())
				}
			}
			return "10 seeds detected each required cleanup omission and tolerated the verify no-op"
		})
	}
	for _, dense := range []bool{false, true} {
		v := dense
		add("C17", fmt.Sprintf("nemesis-dense-%v", v), func() string {
			for i := 0; i < 150; i++ {
				seed := int64(970001 + i*101)
				p := sagasim.NemesisParams{Start: env.Time(200 * time.Millisecond), Phases: 4, Chaos: 3 * time.Second, Heal: time.Second, MaxGap: 150 * time.Millisecond, Converge: 20 * time.Second, Writes: 24, WriteGap: 300 * time.Millisecond, Tick: 50 * time.Millisecond}
				if v {
					p.Writes = 24
					p.WriteGap = 50 * time.Millisecond
					p.MaxGap = 40 * time.Millisecond
				}
				rep := &sagasim.NemesisReport{}
				s := sagasim.NemesisRun(p, rep)(seed)
				check(s.Err() == nil && rep.Sagas > 0 && rep.Settled <= p.Converge, "seed=%d dense=%v err=%v report=%+v", seed, v, s.Err(), rep)
			}
			return "150 new seeds with four chaos phases preserved acknowledged writes and settled within 20 seconds"
		})
	}
	for _, stagger := range []time.Duration{0, 2 * time.Second} {
		st := stagger
		add("C18", fmt.Sprintf("duel-stagger-%d", st), func() string {
			for i := 0; i < 150; i++ {
				seed := int64(980001 + i*103)
				p := sagasim.DuelParams{Converge: 90 * time.Second, MaxStagger: st, MinPartition: 10 * time.Second, MaxPartition: 20 * time.Second}
				r := &sagasim.DuelReport{}
				s := sagasim.DuelRun(p, r)(seed)
				check(s.Err() == nil, "seed=%d stagger=%v err=%v", seed, st, s.Err())
				check(r.Finished.Sub(r.Healed) <= 10*time.Second, "seed=%d slow settlement %v", seed, r.Finished.Sub(r.Healed))
			}
			return "150 seeds with 10–20 second partitions converged to one location with loser cleanup"
		})
	}
	add("C18", "same-target-contention", func() string {
		for i := 0; i < 100; i++ {
			seed := int64(990001 + i)
			o := opts(8)
			o.Coordinators = append(o.Coordinators, sim.NodeSpec{ID: "c2", Region: "rc"})
			s, m, e := sagasim.Build(seed, o)
			must(e)
			m.AddInvariants(s)
			s.At(1, "both begin", func() { must(m.Begin("c1", "x", input())); must(m.Begin("c2", "y", input())) })
			must(s.RunUntil(env.Time(30 * time.Second)))
			a, ok := m.Status("c1", "x")
			b, ok2 := m.Status("c2", "y")
			check(ok && ok2 && a.State.Terminal() && b.State.Terminal(), "seed=%d states=%v/%v", seed, a.State, b.State)
			check((a.State == saga.StateCompleted) != (b.State == saga.StateCompleted), "seed=%d both won or lost", seed)
			check(m.CheckMoved(input()) == nil, "seed=%d cleanup: %v", seed, m.CheckMoved(input()))
		}
		return "100 same-target duels had one winner, one compensation and clean target ownership"
	})
	add("C17", "writes-during-copy", func() string {
		for i := 0; i < 100; i++ {
			seed := int64(991001 + i)
			o := opts(8)
			o.Client = &sim.NodeSpec{ID: "cl", Region: "ra"}
			var s *sim.Sim
			o.Wrap = func(_ env.NodeID, d saga.Definition) saga.Definition {
				orig := d.Steps[1]
				d.Steps[1] = &step{name: orig.Name(), comp: orig.Compensate, do: func(c saga.Call, cb func([]byte, error)) {
					orig.Do(c, func(b []byte, e error) {
						s.At(s.Now().Add(300*time.Millisecond), "late copy response", func() { cb(b, e) })
					})
				}}
				return d
			}
			var m *sagasim.Model
			var e error
			s, m, e = sagasim.Build(seed, o)
			must(e)
			m.AddInvariants(s)
			s.At(1, "begin", func() { must(m.Begin("c1", "x", input())) })
			s.Workload(env.Time(20*time.Millisecond), 16, 10*time.Millisecond, func(n int) { m.Write(n) })
			must(s.RunUntil(env.Time(20 * time.Second)))
			st, ok := m.Status("c1", "x")
			check(ok && st.State == saga.StateCompleted && m.Acked() > 0, "seed=%d state=%v acks=%d", seed, st.State, m.Acked())
			check(m.CheckWrites(sagasim.Bucket, "b") == nil, "seed=%d lost writes: %v", seed, m.CheckWrites(sagasim.Bucket, "b"))
			must(m.CheckMoved(input()))
		}
		return "100 seeds with delayed copy responses, 8 initial objects and 16 concurrent writes preserved every acknowledgment"
	})
	add("C20", "model-wire-sentinels", func() string {
		t, e := env.NewCodeTable(sagasim.Codes()...)
		must(e)
		for _, r := range sagasim.Codes() {
			c, m := t.Encode(fmt.Errorf("wrapped: %w", r.Err))
			check(c == r.Code && errors.Is(t.Decode(c, m), r.Err), "code=%d failed", r.Code)
		}
		return "all model and saga sentinels survived wrapped wire roundtrips"
	})
	add("C20", "unknown-source-target", func() string {
		for _, in := range []saga.MigrationInput{{Bucket: "missing", From: "a", To: "b"}, {Bucket: "bk", From: "a", To: "unknown"}, {Bucket: "bk", From: "a", To: "a"}} {
			s, m, e := sagasim.Build(992001, opts(8))
			must(e)
			s.At(1, "invalid migration", func() { must(m.Begin("c1", "bad", in)) })
			must(s.RunUntil(env.Time(20 * time.Second)))
			st, ok := m.Status("c1", "bad")
			check(ok && st.State == saga.StateCompensated, "seed=992001 input=%+v state=%v", in, st.State)
			must(m.CheckCompensated(input()))
		}
		return "unknown bucket, unknown target and identical source/target compensated without damaging the live bucket"
	})
	add("C21", "coordinator-lost", func() string {
		o := opts(8)
		var s *sim.Sim
		frozen := false
		o.Configure = func(_ env.NodeID, c *saga.Config) {
			c.Hook = func(p saga.Point) bool {
				if !frozen && p.Kind == saga.RecDone && p.Step == 1 {
					frozen = true
					s.At(s.Now(), "permanent coordinator crash", func() { must(s.Crash("c1")) })
					return true
				}
				return false
			}
		}
		var m *sagasim.Model
		var e error
		s, m, e = sagasim.Build(993001, o)
		must(e)
		s.At(1, "begin", func() { must(m.Begin("c1", "x", input())) })
		must(s.RunUntil(env.Time(time.Minute)))
		check(frozen && m.Engine("c1") == nil, "coordinator not lost")
		b, ok := s.Disk("c1").PeekDurable("saga/x")
		check(ok, "no durable log")
		rs, _, e := saga.DecodeLog(b)
		must(e)
		snap, e := saga.Resume(rs, 5, 3, 3)
		must(e)
		check(!snap.State.Terminal(), "lost coordinator magically completed")
		return "permanent loss after copy left an unfinished inspectable log, as explicitly limited"
	})
}
