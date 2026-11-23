package main

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/saga"
	"github.com/nickstrad/object-store/internal/sim"
	"reflect"
	"time"
)

var flaky = errors.New("lost response")

type step struct {
	name string
	do   func(saga.Call, func([]byte, error))
	comp func(saga.Call, func(error))
}

func (s *step) Name() string                          { return s.name }
func (s *step) Do(c saga.Call, d func([]byte, error)) { s.do(c, d) }
func (s *step) Compensate(c saga.Call, d func(error)) { s.comp(c, d) }

type process struct {
	start func()
	g     *saga.Engine
}

func (p *process) Start()                                        { p.start() }
func (p *process) OnDatagram(env.NodeID, []byte)                 {}
func (p *process) OnRequest(_ env.NodeID, _ []byte, r env.Reply) { r(nil, env.ErrInvalidArgument) }
func (p *process) EncodeState() []byte {
	if p.g == nil {
		return nil
	}
	return p.g.EncodeState()
}

type fixture struct {
	s                                     *sim.Sim
	g                                     *saga.Engine
	e                                     env.Env
	p                                     saga.Policy
	n, pivot                              int
	failAt, failCount                     int
	permanent, forever, silent, duplicate bool
	attempts, comps                       []int
	keys                                  map[int]string
	order                                 []int
	calls                                 int
	afterTerminal                         int
	preload                               []byte
	hasLog                                bool
	repeat                                bool
	readFaults                            int
	hook                                  func(saga.Point) bool
	setupErr                              error
	recoverErr                            error
	initial                               bool
}

func fresh(seed int64, n, pivot int) *fixture {
	f := &fixture{n: n, pivot: pivot, failAt: -1, attempts: make([]int, n), comps: make([]int, n), keys: map[int]string{}, p: saga.Policy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: 8 * time.Millisecond, StepTimeout: 10 * time.Millisecond}}
	f.s = sim.New(sim.Config{Seed: seed})
	return f
}
func (f *fixture) launch() {
	must(f.s.AddNode(sim.NodeSpec{ID: "n", Region: "r"}, func(e env.Env) env.Process {
		p := &process{}
		p.start = func() {
			f.e = e
			steps := make([]saga.Step, f.n)
			for i := range steps {
				i := i
				steps[i] = &step{name: fmt.Sprint("s", i), do: func(c saga.Call, d func([]byte, error)) {
					f.calls++
					f.attempts[i]++
					if st, er := p.g.Status("x"); er == nil && st.State.Terminal() {
						f.afterTerminal++
					}
					raw, ok := f.s.Disk("n").PeekDurable("saga/x")
					check(ok, "step before log exists")
					rs, _, er := saga.DecodeLog(raw)
					must(er)
					check(len(rs) > 0 && rs[len(rs)-1].Kind == saga.RecIntent, "step before durable intent")
					key := string(c.Key.Bytes())
					if prev, ok := f.keys[i]; ok {
						check(prev == key, "key changed across retry/crash")
					} else {
						f.keys[i] = key
					}
					if f.silent && i == f.failAt {
						return
					}
					er = nil
					if i == f.failAt && (f.permanent || f.attempts[i] <= f.failCount) {
						er = flaky
						if f.permanent {
							er = saga.ErrPermanent
						}
					}
					e.Clock.AfterFunc(time.Microsecond, func() {
						d([]byte("output"), er)
						if f.duplicate {
							d([]byte("second"), nil)
							e.Clock.AfterFunc(30*time.Millisecond, func() { d(nil, saga.ErrPermanent) })
						}
					})
				}, comp: func(c saga.Call, d func(error)) {
					f.comps[i]++
					f.order = append(f.order, i)
					if f.forever {
						d(flaky)
						return
					}
					if f.comps[i] <= 2 {
						d(flaky)
					} else {
						d(nil)
					}
				}}
			}
			var er error
			p.g, er = saga.New(e, saga.Config{Policy: f.p, Hook: f.hook}, saga.Definition{Name: "d", Steps: steps, Pivot: f.pivot})
			if er != nil {
				f.setupErr = er
				return
			}
			f.g = p.g
			if !f.initial {
				f.initial = true
				if f.hasLog {
					must(e.Disk.Write("saga/x", f.preload))
					must(e.Disk.Sync("saga/x"))
				}
				if f.readFaults > 0 {
					must(f.s.FailReads("n", "saga/x", f.readFaults))
				}
			}
			f.recoverErr = p.g.Recover()
			if f.repeat {
				_ = p.g.Recover()
			}
			if !f.hasLog {
				if _, er = p.g.Status("x"); errors.Is(er, saga.ErrSagaNotFound) {
					er = p.g.Begin("x", "d", []byte("input"))
					if !errors.Is(er, env.ErrClosed) {
						must(er)
					}
				}
			}
		}
		return p
	}))
}
func (f *fixture) run(d time.Duration) { must(f.s.RunUntil(env.Time(d))); must(f.setupErr) }
func (f *fixture) status() saga.Status { st, e := f.g.Status("x"); must(e); return st }
func (f *fixture) assertTerminal(want saga.State) {
	st := f.status()
	check(st.State == want && !st.Suspended, "seed=%d state=%s suspended=%v error=%v", f.s.Seed(), st.State, st.Suspended, st.Err)
	check(f.afterTerminal == 0, "steps after terminal")
}
func engine() {
	add("C7", "reverse-compensation", func() string {
		for fail := 0; fail < 6; fail++ {
			f := fresh(92001, 6, 6)
			f.failAt = fail
			f.permanent = true
			f.launch()
			f.run(time.Second)
			f.assertTerminal(saga.StateCompensated)
			want := []int{}
			for j := fail; j >= 0; j-- {
				want = append(want, j, j, j)
			}
			check(reflect.DeepEqual(f.order, want), "seed=92001 fail=%d compensation order %v", fail, f.order)
			for i := fail + 1; i < 6; i++ {
				check(f.attempts[i] == 0, "forward step after abort")
			}
		}
		return "six permanent-failure positions compensated in reverse order with two failures before each success"
	})
	add("C7", "forever-compensation", func() string {
		f := fresh(92002, 3, 3)
		f.failAt = 1
		f.permanent = true
		f.forever = true
		f.launch()
		f.run(time.Second)
		st := f.status()
		var se *saga.StepError
		check(st.State == saga.StateCompensating && !st.Suspended && f.comps[1] > 100 && f.comps[0] == 0 && errors.As(st.Err, &se) && se.Compensation && se.Attempt == f.comps[1], "seed=92002 status=%+v comps=%v", st, f.comps)
		return "after 1 virtual second compensation still retried with observable StepError and no false terminal state"
	})
	add("C9", "pivot-and-postpivot", func() string {
		for i := 1; i <= 2; i++ {
			f := fresh(92003, 3, 1)
			f.failAt = i
			f.failCount = 30
			f.launch()
			f.run(time.Second)
			f.assertTerminal(saga.StateCompleted)
			check(f.attempts[i] == 31 && len(f.order) == 0, "seed=92003 step=%d attempts=%v comps=%v", i, f.attempts, f.order)
		}
		return "pivot and post-pivot each survived 30 lost responses without compensating"
	})
	add("C9", "permanent-pivot-vs-after", func() string {
		f := fresh(92004, 3, 1)
		f.failAt = 1
		f.permanent = true
		f.launch()
		f.run(time.Second)
		f.assertTerminal(saga.StateCompensated)
		check(f.attempts[1] == 1 && f.comps[1] == 0 && f.comps[0] == 3, "pivot compensated or retried")
		g := fresh(92004, 3, 1)
		g.failAt = 2
		g.permanent = true
		g.launch()
		g.run(time.Second)
		check(g.status().State == saga.StateRunning && g.attempts[2] > 100 && len(g.order) == 0, "postpivot permanent failure stopped forward retries")
		return "permanent pivot rejection aborted once; permanent post-pivot failure kept retrying"
	})
	add("C10", "permanent-and-status-errors", func() string {
		f := fresh(92005, 1, 1)
		f.failAt = 0
		f.permanent = true
		f.forever = true
		f.launch()
		f.run(20 * time.Millisecond)
		st := f.status()
		var se *saga.StepError
		check(f.attempts[0] == 1 && errors.As(st.Err, &se) && se.Saga == "x" && se.Step == "s0" && se.Compensation && errors.Is(st.Err, flaky), "bad StepError %+v", st)
		_, e := f.g.Status("absent")
		check(errors.Is(e, saga.ErrSagaNotFound), "missing ID error %v", e)
		return "permanent failure stopped Do and compensation StepError plus missing-ID sentinel were accurate"
	})
	add("C6", "silent-timeout-bound", func() string {
		f := fresh(92006, 1, 1)
		f.failAt = 0
		f.silent = true
		f.launch()
		f.run(29 * time.Millisecond)
		check(f.status().State == saga.StateRunning && f.attempts[0] == 3, "unexpected timeout schedule")
		f.run(time.Second)
		f.assertTerminal(saga.StateCompensated)
		check(f.attempts[0] == 3, "silent step escaped bound")
		return "silent pre-pivot step timed out three times and compensated after the exact retry schedule"
	})
	add("C13", "duplicate-late-end", func() string {
		f := fresh(92007, 4, 2)
		f.duplicate = true
		f.launch()
		f.run(time.Second)
		f.assertTerminal(saga.StateCompleted)
		for _, n := range f.attempts {
			check(n == 1, "duplicate callback ran a step twice")
		}
		must(f.s.Crash("n"))
		must(f.s.Restart("n"))
		f.run(2 * time.Second)
		f.assertTerminal(saga.StateCompleted)
		check(f.calls == 4, "terminal replay reran steps")
		return "immediate duplicates, delayed failures and terminal restart did not run extra steps"
	})
	add("C8", "lost-response-key", func() string {
		f := fresh(92008, 3, 0)
		f.failAt = 0
		f.failCount = 20
		f.launch()
		f.run(10 * time.Millisecond)
		must(f.s.Crash("n"))
		must(f.s.Restart("n"))
		f.run(time.Second)
		f.assertTerminal(saga.StateCompleted)
		check(f.attempts[0] >= 21, "lost responses not exercised")
		return "21+ attempts spanning restart used the identical saga/step key and durable intents"
	})
	add("C2", "all-prefix-recovery", func() string {
		raw := enc(completeLog()...)
		for cut := 0; cut <= len(raw); cut++ {
			f := fresh(92009, 1, 1)
			f.preload = raw[:cut]
			f.hasLog = true
			f.launch()
			f.run(time.Second)
			if cut < len(enc(begin())) {
				_, e := f.g.Status("x")
				check(errors.Is(e, saga.ErrSagaNotFound), "seed=92009 cut=%d partial begin became saga", cut)
			} else {
				f.assertTerminal(saga.StateCompleted)
				b, ok := f.s.Disk("n").PeekDurable("saga/x")
				check(ok, "log removed")
				_, v, e := saga.DecodeLog(b)
				must(e)
				check(v == len(b), "torn residue after recovery cut=%d", cut)
			}
		}
		return "every byte prefix of a complete trace recovered cleanly or deleted a torn first record"
	})
	add("C12", "double-recover-prefixes", func() string {
		raw := enc(completeLog()...)
		for cut := 0; cut <= len(raw); cut++ {
			a := fresh(92010, 1, 1)
			a.hasLog = true
			a.preload = raw[:cut]
			a.launch()
			a.run(time.Second)
			b := fresh(92010, 1, 1)
			b.hasLog = true
			b.preload = raw[:cut]
			b.repeat = true
			b.launch()
			b.run(time.Second)
			x, _ := a.s.Disk("n").PeekDurable("saga/x")
			y, _ := b.s.Disk("n").PeekDurable("saga/x")
			check(bytes.Equal(x, y) && reflect.DeepEqual(a.attempts, b.attempts), "seed=92010 cut=%d repeat recovery differs", cut)
		}
		return "recover-once and recover-twice produced identical logs and calls for every byte prefix"
	})
	add("C3", "corrupt-middle-recovery", func() string {
		raw := enc(completeLog()...)
		start := len(enc(begin()))
		for j := start; j < start+len(enc(intent(0, 1))); j++ {
			b := bytes.Clone(raw)
			b[j] ^= 1
			f := fresh(92011, 1, 1)
			f.hasLog = true
			f.preload = b
			f.launch()
			f.run(time.Second)
			check(errors.Is(f.recoverErr, saga.ErrLogCorrupt) && f.calls == 0, "seed=92011 byte=%d err=%v calls=%d", j, f.recoverErr, f.calls)
			x, _ := f.s.Disk("n").PeekDurable("saga/x")
			check(bytes.Equal(b, x), "corrupt log modified")
		}
		return "each byte of the middle intent was corrupted independently; recovery preserved bytes and ran nothing"
	})
	add("C3", "corrupt-last-and-unknown-definition", func() string {
		for k := 0; k < 2; k++ {
			b := enc(completeLog()...)
			if k == 0 {
				b[len(b)-1] ^= 1
			} else {
				r := begin()
				r.Def = "unknown"
				b = enc(r)
			}
			f := fresh(92012, 1, 1)
			f.hasLog = true
			f.preload = b
			f.launch()
			f.run(time.Second)
			check(f.recoverErr != nil && f.calls == 0, "case=%d no recovery error", k)
			x, _ := f.s.Disk("n").PeekDurable("saga/x")
			check(bytes.Equal(b, x), "broken log modified")
		}
		return "last-record corruption and unknown definition were left untouched without execution"
	})
	add("C4", "read-fault-recovery", func() string {
		for _, n := range []int{1, 2, 17} {
			f := fresh(92013, 1, 1)
			f.hasLog = true
			f.preload = enc(begin())
			f.readFaults = n
			f.launch()
			f.run(time.Second)
			f.assertTerminal(saga.StateCompleted)
			check(f.attempts[0] == 1, "seed=92013 reads=%d replayed action", n)
		}
		return "1, 2 and 17 transient read errors recovered on timers and executed the step once"
	})
	add("C4", "permanent-read-fault", func() string {
		f := fresh(92014, 1, 1)
		f.hasLog = true
		f.preload = enc(begin())
		f.readFaults = 10000
		f.launch()
		f.run(time.Second)
		_, e := f.g.Status("x")
		check(errors.Is(e, saga.ErrSagaNotFound) && f.calls == 0, "unreadable log unexpectedly ran")
		b, _ := f.s.Disk("n").PeekDurable("saga/x")
		check(bytes.Equal(b, f.preload), "unreadable log changed")
		return "10000 injected read failures left the log unchanged and recovery pending"
	})
	add("C11", "terminal-reference-grid", func() string {
		for p := 0; p <= 5; p++ {
			for fail := -1; fail < 5; fail++ {
				for retry := 0; retry <= 4; retry++ {
					f := fresh(93000+int64(p*100+fail*10+retry), 5, p)
					f.failAt = fail
					f.failCount = retry
					f.launch()
					f.run(time.Second)
					want := saga.StateCompleted
					if fail >= 0 && fail < p && retry >= 3 {
						want = saga.StateCompensated
					}
					f.assertTerminal(want)
				}
			}
		}
		return "180 pivot/failure/retry combinations matched an independent terminal-state rule"
	})
	add("C11", "crash-and-double-crash-grid", func() string {
		for point := 2; point <= 9; point++ {
			for _, torn := range []bool{false, true} {
				f := fresh(94000+int64(point), 3, 2)
				f.failAt = 0
				f.failCount = 1
				count, crashes := 0, 0
				f.hook = func(p saga.Point) bool {
					count++
					if crashes < 2 && (count == point || crashes == 1) {
						crashes++
						f.s.At(f.s.Now(), "crash", func() {
							if torn {
								must(f.s.CrashTorn("n"))
							} else {
								must(f.s.Crash("n"))
							}
							f.s.At(f.s.Now().Add(time.Millisecond), "restart", func() { must(f.s.Restart("n")) })
						})
						return true
					}
					return false
				}
				f.launch()
				f.run(time.Second)
				st := f.status()
				check(crashes >= 1 && (point == 9 || crashes == 2), "seed=%d point=%d torn=%v crashes=%d", 94000+point, point, torn, crashes)
				check(st.State.Terminal(), "seed=%d point=%d torn=%v state=%v", 94000+point, point, torn, st.State)
			}
		}
		return "16 reachable append-boundary schedules verified injection and reached a legal terminal state; pre-end points verified both crashes"
	})
}
