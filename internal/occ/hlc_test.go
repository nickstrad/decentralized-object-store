package occ_test

import (
	"errors"
	"math"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"pgregory.net/rapid"
)

func st(wall int64, logical uint32, node string) occ.Stamp {
	return occ.Stamp{Wall: wall, Logical: logical, Node: env.NodeID(node)}
}

func TestHLCRules(t *testing.T) {
	type step struct {
		clock   int64
		observe *occ.Stamp
		want    occ.Stamp
		wantErr error
	}
	obs := func(s occ.Stamp) *occ.Stamp { return &s }
	lead := int64(occ.MaxClockLead)
	rows := []struct {
		name  string
		steps []step
	}{
		{"physical time ahead of the clock gives (physical, 0)", []step{
			{clock: 100, want: st(100, 0, "n")},
			{clock: 200, want: st(200, 0, "n")},
		}},
		{"physical time not moving bumps the logical counter", []step{
			{clock: 100, want: st(100, 0, "n")},
			{clock: 100, want: st(100, 1, "n")},
			{clock: 100, want: st(100, 2, "n")},
		}},
		{"physical time going backwards keeps the wall and bumps the counter", []step{
			{clock: 100, want: st(100, 0, "n")},
			{clock: 50, want: st(100, 1, "n")},
		}},
		{"an observed stamp ahead of physical time pushes the next stamp past it", []step{
			{clock: 100, observe: obs(st(500, 7, "m"))},
			{clock: 100, want: st(500, 8, "n")},
			{clock: 600, want: st(600, 0, "n")},
		}},
		{"an observed stamp behind the clock changes nothing", []step{
			{clock: 100, want: st(100, 0, "n")},
			{clock: 100, observe: obs(st(10, 99, "m"))},
			{clock: 100, want: st(100, 1, "n")},
		}},
		{"an observed stamp more than MaxClockLead ahead is refused", []step{
			{clock: 100, observe: obs(st(100+lead+1, 0, "m")), wantErr: occ.ErrClockAhead},
			{clock: 100, want: st(100, 0, "n")},
		}},
		{"an observed stamp exactly MaxClockLead ahead is accepted", []step{
			{clock: 100, observe: obs(st(100+lead, 0, "m"))},
			{clock: 100, want: st(100+lead, 1, "n")},
		}},
		{"logical overflow carries into the wall", []step{
			{clock: 100, observe: obs(st(100, math.MaxUint32-1, "m"))},
			{clock: 100, want: st(100, math.MaxUint32, "n")},
			{clock: 100, want: st(101, 0, "n")},
		}},
		{"a clock at the end of time is exhausted", []step{
			{clock: math.MaxInt64, observe: obs(st(math.MaxInt64, math.MaxUint32, "m"))},
			{clock: math.MaxInt64, wantErr: occ.ErrClockExhausted},
		}},
		{"the lead limit saturates near the end of time", []step{
			{clock: math.MaxInt64 - 5, observe: obs(st(math.MaxInt64, 0, "m"))},
			{clock: math.MaxInt64 - 5, want: st(math.MaxInt64, 1, "n")},
		}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			c := &fakeClock{}
			h := occ.NewHLC(c, "n")
			if !h.Last().IsZero() {
				t.Fatal("a new clock must report the zero stamp")
			}
			for i, s := range r.steps {
				c.now = env.Time(s.clock)
				if s.observe != nil {
					err := h.Observe(*s.observe)
					if !errors.Is(err, s.wantErr) || (s.wantErr == nil && err != nil) {
						t.Fatalf("step %d observe err = %v, want %v", i, err, s.wantErr)
					}
					continue
				}
				got, err := h.Now()
				if s.wantErr != nil {
					if !errors.Is(err, s.wantErr) {
						t.Fatalf("step %d err = %v, want %v", i, err, s.wantErr)
					}
					continue
				}
				if err != nil || got != s.want || h.Last() != got {
					t.Fatalf("step %d: Now = %s (%v), want %s", i, got.String(), err, s.want.String())
				}
			}
		})
	}
}

func TestLWWSkewPicksTheHigherClock(t *testing.T) {
	fast, fc := newTestStore(t, "fast", 1, 1)
	slow, scl := newTestStore(t, "slow", 1, 2)
	fc.now = env.Time(10 * time.Second)
	scl.now = env.Time(9 * time.Second)
	if _, err := fast.SetRoute("b1", "region-a"); err != nil {
		t.Fatal(err)
	}
	scl.now += env.Time(500 * time.Millisecond)
	if _, err := slow.SetRoute("b1", "region-b"); err != nil {
		t.Fatal(err)
	}
	mustMerge(t, fast, slow.Doc())
	mustMerge(t, slow, fast.Doc())
	r1, _ := fast.Route("b1")
	r2, _ := slow.Route("b1")
	if r1 != "region-a" || r2 != "region-a" || slow.LWWDiscards() != 1 {
		t.Fatalf("routes %s %s: the skewed-ahead write should win and the later one be recorded as discarded", r1, r2)
	}
	if _, err := slow.SetRoute("b1", "region-c"); err != nil {
		t.Fatal(err)
	}
	if r3, _ := slow.Route("b1"); r3 != "region-c" {
		t.Fatal("after observing the fast stamp, the slow node's next write must win")
	}
	if _, _, err := slow.Scalar("missing"); !errors.Is(err, occ.ErrNotFound) {
		t.Fatal("a missing scalar")
	}
}

func TestPropHLCMonotonic(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := &fakeClock{now: env.Time(rapid.Int64Range(0, 1<<40).Draw(t, "start"))}
		h := occ.NewHLC(c, "n")
		var last occ.Stamp
		var maxSeen occ.Stamp
		for i := range rapid.IntRange(1, 200).Draw(t, "steps") {
			_ = i
			switch rapid.IntRange(0, 3).Draw(t, "op") {
			case 0:
				c.now += env.Time(rapid.Int64Range(-1000, 1000).Draw(t, "jump"))
			case 1:
				r := st(int64(c.now)+rapid.Int64Range(-2000, 2000).Draw(t, "rwall"), rapid.Uint32Range(math.MaxUint32-3, math.MaxUint32).Draw(t, "rlog"), "m")
				if rapid.Bool().Draw(t, "small") {
					r.Logical = rapid.Uint32Range(0, 5).Draw(t, "rlog2")
				}
				if err := h.Observe(r); err != nil {
					t.Fatalf("observe within lead: %v", err)
				}
				if r.Compare(maxSeen) > 0 {
					maxSeen = r
				}
			default:
				s, err := h.Now()
				if err != nil {
					t.Fatal(err)
				}
				if !last.IsZero() && (s.Wall < last.Wall || (s.Wall == last.Wall && s.Logical <= last.Logical)) {
					t.Fatalf("stamp %s not after %s", s.String(), last.String())
				}
				if !maxSeen.IsZero() && (s.Wall < maxSeen.Wall || (s.Wall == maxSeen.Wall && s.Logical <= maxSeen.Logical)) {
					t.Fatalf("stamp %s not after observed %s", s.String(), maxSeen.String())
				}
				if s.Wall < int64(c.now) {
					t.Fatalf("stamp %s behind physical time %d", s.String(), c.now)
				}
				last = s
			}
		}
	})
}
