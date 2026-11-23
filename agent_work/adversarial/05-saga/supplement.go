package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"reflect"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/saga"
	"github.com/nickstrad/object-store/internal/saga/sagasim"
	"github.com/nickstrad/object-store/internal/sim"
)

type workflowOps struct {
	calls []string
	mode  string
	roots int
}

func (o *workflowOps) note(s string) { o.calls = append(o.calls, s) }
func (o *workflowOps) Claim(_ saga.Call, _ saga.MigrationInput, d func(error)) {
	o.note("claim")
	d(nil)
}
func (o *workflowOps) Unclaim(_ saga.Call, _ saga.MigrationInput, d func(error)) {
	o.note("unclaim")
	d(nil)
}
func (o *workflowOps) Copy(_ saga.Call, _ saga.MigrationInput, d func([]byte, error)) {
	o.note("copy")
	d([]byte("root"), nil)
}
func (o *workflowOps) Discard(_ saga.Call, _ saga.MigrationInput, d func(error)) {
	o.note("discard")
	d(nil)
}
func (o *workflowOps) Root(_ saga.Call, _, loc string, d func([]byte, error)) {
	o.note("root-" + loc)
	o.roots++
	if o.mode == "mismatch" || (o.mode == "transient-root" && o.roots == 1) {
		d([]byte("different"), nil)
		return
	}
	d([]byte("root"), nil)
}
func (o *workflowOps) Flip(_ saga.Call, _ saga.MigrationInput, d func(error)) { o.note("flip"); d(nil) }
func (o *workflowOps) Pointer(_ saga.Call, _ string, d func(string, error)) {
	o.note("pointer")
	if o.mode == "superseded" {
		d("c", nil)
	} else {
		d("b", nil)
	}
}
func (o *workflowOps) Seal(_ saga.Call, _ saga.MigrationInput, d func(bool, error)) {
	o.note("seal")
	d(o.mode == "retired", nil)
}
func (o *workflowOps) Promote(_ saga.Call, _ saga.MigrationInput, d func(error)) {
	o.note("promote")
	d(nil)
}
func (o *workflowOps) Retire(_ saga.Call, _ saga.MigrationInput, d func(error)) {
	o.note("retire")
	d(nil)
}

func supplement() {
	add("C15", "workflow-branch-oracle", func() string {
		for _, mode := range []string{"moved", "superseded", "retired", "mismatch", "transient-root"} {
			s := sim.New(sim.Config{Seed: 1001001})
			o := &workflowOps{mode: mode}
			var g *saga.Engine
			must(s.AddNode(sim.NodeSpec{ID: "n"}, func(e env.Env) env.Process {
				return &process{start: func() {
					var er error
					g, er = saga.New(e, saga.Config{Policy: sagasim.MatrixPolicy()}, saga.Migration(o))
					must(er)
					must(g.Begin("x", saga.MigrationName, input().Encode()))
				}}
			}))
			must(s.RunUntil(env.Time(20 * time.Second)))
			st, er := g.Status("x")
			must(er)
			want := []string{"claim", "copy", "root-b"}
			outcome := saga.OutcomeMoved
			switch mode {
			case "mismatch":
				want = append(want, "root-b", "root-b", "discard", "unclaim")
				check(st.State == saga.StateCompensated, "seed=1001001 mode=%s state=%v", mode, st.State)
			case "superseded":
				want = append(want, "flip", "pointer", "discard", "unclaim")
				outcome = saga.OutcomeSuperseded
			case "retired":
				want = append(want, "flip", "pointer", "seal", "promote")
			default:
				if mode == "transient-root" {
					want = append(want, "root-b")
				}
				want = append(want, "flip", "pointer", "seal", "copy", "root-a", "root-b", "retire", "promote")
			}
			check(reflect.DeepEqual(want, o.calls), "seed=1001001 mode=%s calls=%v want=%v", mode, o.calls, want)
			if mode != "mismatch" {
				check(st.State == saga.StateCompleted && string(st.Outputs[4]) == outcome, "seed=1001001 mode=%s state=%v outputs=%q", mode, st.State, st.Outputs)
			}
		}
		return "five workflow paths matched independently specified operation order, cleanup and outcome"
	})
	add("C6", "exact-backoff-log-times", func() string {
		f := fresh(1001002, 1, 0)
		f.failAt = 0
		f.failCount = 8
		f.launch()
		f.run(time.Second)
		raw, _ := f.s.Disk("n").PeekDurable("saga/x")
		rs, _, er := saga.DecodeLog(raw)
		must(er)
		// Record timestamps are zero for intents; capture simulator call times in a separate one-step definition below.
		intents := 0
		for _, r := range rs {
			if r.Kind == saga.RecIntent {
				intents++
			}
		}
		check(intents == 9, "seed=1001002 intents=%d", intents)
		s := sim.New(sim.Config{Seed: 1001002})
		var times []env.Time
		var g *saga.Engine
		must(s.AddNode(sim.NodeSpec{ID: "n"}, func(e env.Env) env.Process {
			return &process{start: func() {
				st := &step{name: "wait", do: func(_ saga.Call, d func([]byte, error)) {
					times = append(times, e.Clock.Now())
					if len(times) < 9 {
						d(nil, flaky)
					} else {
						d(nil, nil)
					}
				}, comp: func(_ saga.Call, d func(error)) { d(nil) }}
				var er error
				g, er = saga.New(e, saga.Config{Policy: f.p}, saga.Definition{Name: "d", Steps: []saga.Step{st}, Pivot: 0})
				must(er)
				must(g.Begin("x", "d", nil))
			}}
		}))
		must(s.RunUntil(env.Time(time.Second)))
		expected := []int64{0, 1, 3, 7, 15, 23, 31, 39, 47}
		check(len(times) == len(expected), "seed=1001002 times=%v", times)
		for i, t := range times {
			check(t == env.Time(expected[i]*int64(time.Millisecond)), "seed=1001002 attempt=%d time=%v", i+1, t)
		}
		return "nine attempts occurred at exactly 0/1/3/7/15/23/31/39/47 milliseconds with zero timing tolerance"
	})
	add("C8", "idempotent-effect-after-lost-response", func() string {
		for _, crash := range []bool{false, true} {
			s := sim.New(sim.Config{Seed: 1001003})
			effects, calls := 0, 0
			seen := map[string]bool{}
			var g *saga.Engine
			must(s.AddNode(sim.NodeSpec{ID: "n"}, func(e env.Env) env.Process {
				return &process{start: func() {
					st := &step{name: "effect", do: func(c saga.Call, d func([]byte, error)) {
						calls++
						key := string(c.Key.Bytes())
						if !seen[key] {
							seen[key] = true
							effects++
						}
						if calls == 1 {
							if crash {
								s.At(s.Now(), "crash after effect", func() {
									must(s.Crash("n"))
									s.At(s.Now().Add(time.Millisecond), "restart", func() { must(s.Restart("n")) })
								})
							}
							return
						}
						d(nil, nil)
					}, comp: func(_ saga.Call, d func(error)) { d(nil) }}
					var er error
					g, er = saga.New(e, saga.Config{Policy: sagasim.MatrixPolicy()}, saga.Definition{Name: "d", Steps: []saga.Step{st}, Pivot: 0})
					must(er)
					must(g.Recover())
					if _, er = g.Status("x"); errors.Is(er, saga.ErrSagaNotFound) {
						must(g.Begin("x", "d", nil))
					}
				}}
			}))
			must(s.RunUntil(env.Time(20 * time.Second)))
			st, er := g.Status("x")
			must(er)
			check(st.State == saga.StateCompleted && effects == 1 && calls == 2, "seed=1001003 crash=%v effects=%d calls=%d state=%v", crash, effects, calls, st.State)
		}
		return "timeout and crash each caused two calls but one effect when the external step deduplicated the stable key"
	})
	add("C20", "late-copy-fence", func() string {
		for seed := int64(1001100); seed < 1001120; seed++ {
			s, m, er := sagasim.Build(seed, opts(8))
			must(er)
			var op *sagasim.Ops
			must(s.AddNode(sim.NodeSpec{ID: "probe", Region: "rb"}, func(e env.Env) env.Process { return &process{start: func() { op = sagasim.NewOps(e, m.Stores()) }} }))
			must(s.RunUntil(env.Time(time.Millisecond)))
			c := saga.Call{Key: saga.Key{Saga: "probe", Step: 0}, Begun: 1}
			claimed := false
			op.Claim(c, input(), func(er error) { must(er); claimed = true })
			must(s.RunUntil(env.Time(time.Second)))
			check(claimed, "seed=%d claim not finished", seed)
			must(s.Pause("a"))
			copyDone, discarded := false, false
			var copyErr error
			op.Copy(c, input(), func(_ []byte, er error) { copyDone = true; copyErr = er })
			s.At(s.Now().Add(100*time.Millisecond), "discard before dump returns", func() { op.Discard(c, input(), func(er error) { must(er); discarded = true }) })
			s.At(s.Now().Add(300*time.Millisecond), "release source reply", func() { must(s.Resume("a")) })
			must(s.RunUntil(env.Time(2 * time.Second)))
			check(copyDone && discarded && errors.Is(copyErr, sagasim.ErrBusy), "seed=%d copyDone=%v discarded=%v err=%v", seed, copyDone, discarded, copyErr)
			released := false
			op.Unclaim(c, input(), func(er error) { must(er); released = true })
			must(s.RunUntil(env.Time(3 * time.Second)))
			check(released && m.CheckCompensated(input()) == nil, "seed=%d late copy left objects: %v", seed, m.CheckCompensated(input()))
		}
		return "20 paused-source schedules rejected late two-hop copies after discard and left no target objects"
	})
	add("C20", "malformed-store-requests", func() string {
		s, m, er := sagasim.Build(1001200, opts(8))
		must(er)
		var e env.Env
		must(s.AddNode(sim.NodeSpec{ID: "probe"}, func(x env.Env) env.Process { return &process{start: func() { e = x }} }))
		must(s.RunUntil(1))
		requests := [][]byte{nil, {255, 0}, {1, 0}, {1, 65}, {1, 1, 128}, {1, 1, 1, 'x', 0}}
		count := 0
		for _, b := range requests {
			e.Net.Request("a", b, time.Second, func(_ []byte, er error) {
				check(errors.Is(er, sagasim.ErrMalformed), "seed=1001200 err=%v", er)
				count++
			})
		}
		must(s.RunUntil(env.Time(2 * time.Second)))
		check(count == len(requests), "seed=1001200 callbacks=%d", count)
		must(m.CheckCompensated(input()))
		return "six malformed field-count, length, opcode and trailing-byte requests were rejected without state changes"
	})
	add("C1", "checksummed-length-limits", func() string {
		// Valid checksums distinguish range validation from accidental checksum rejection.
		for _, n := range []uint32{0, saga.MaxRecord + 1, 1 << 31} {
			b := enc(begin())
			binary.BigEndian.PutUint32(b[1:5], n)
			rechecksumHeader(b)
			_, _, er := saga.DecodeLog(b)
			check(errors.Is(er, saga.ErrLogCorrupt), "seed=0 length=%d err=%v", n, er)
		}
		return "zero and two oversized declared lengths were rejected even with valid header checksums"
	})
	add("C12", "compensating-prefix-recovery", func() string {
		rs := []saga.Record{begin(), intent(0, 1), done(0, 1), intent(1, 1), {Kind: saga.RecAbort, Step: 1}, {Kind: saga.RecCompIntent, Step: 1, Attempt: 1}, {Kind: saga.RecCompDone, Step: 1, Attempt: 1}, {Kind: saga.RecCompIntent, Step: 0, Attempt: 1}, {Kind: saga.RecCompDone, Step: 0, Attempt: 1}, {Kind: saga.RecEnd, State: saga.StateCompensated}}
		raw := enc(rs...)
		start := len(enc(rs[:5]...))
		for cut := start; cut <= len(raw); cut++ {
			a := fresh(1001300, 2, 2)
			a.hasLog = true
			a.preload = raw[:cut]
			a.launch()
			a.run(time.Second)
			a.assertTerminal(saga.StateCompensated)
			b := fresh(1001300, 2, 2)
			b.hasLog = true
			b.preload = raw[:cut]
			b.repeat = true
			b.launch()
			b.run(time.Second)
			b.assertTerminal(saga.StateCompensated)
			x, _ := a.s.Disk("n").PeekDurable("saga/x")
			y, _ := b.s.Disk("n").PeekDurable("saga/x")
			check(bytes.Equal(x, y) && reflect.DeepEqual(a.order, b.order), "seed=1001300 cut=%d recovery differed", cut)
		}
		return fmt.Sprintf("%d compensating byte prefixes produced identical terminal logs and reverse-order calls on repeated recovery", len(raw)-start+1)
	})
}

func rechecksumHeader(b []byte) {
	binary.BigEndian.PutUint32(b[5:9], crc32.Checksum(b[:5], crc32.MakeTable(crc32.Castagnoli)))
}
