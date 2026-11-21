package sim

import (
	"math"
	"math/bits"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

// local converts global virtual time g into node n's local clock using pure
// integer arithmetic: g + skew + g*drift/1e6. Because |skew| <= MaxSkew,
// |drift| <= MaxDriftPPM, and 0 <= g <= Horizon, the result cannot overflow
// int64 and is monotonic in g.
func (n *node) local(g env.Time) env.Time {
	gi := int64(g)
	return env.Time(gi + n.skew + (gi/1_000_000)*n.drift + (gi%1_000_000)*n.drift/1_000_000)
}

// toGlobal converts a local duration d into the global delay required for n's
// local clock to advance by at least d: ceil(d * 1e6 / (1e6 + drift)).
// Using 128-bit arithmetic avoids overflow; results past MaxInt64 saturate.
func (n *node) toGlobal(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if n.drift == 0 {
		return d
	}
	// den >= 900_000 while hi <= (MaxInt64 * 1e6) / 2^64 < 500_000, so
	// hi < den always holds and bits.Div64 cannot overflow.
	den := uint64(1_000_000 + n.drift)
	hi, lo := bits.Mul64(uint64(d), 1_000_000)
	q, r := bits.Div64(hi, lo, den)
	if r != 0 {
		q++
	}
	if q > math.MaxInt64 {
		return math.MaxInt64
	}
	return time.Duration(q)
}

// later returns t + d with saturation at int64 boundaries so time never wraps.
func later(t env.Time, d time.Duration) env.Time {
	if d > 0 && int64(t) > math.MaxInt64-int64(d) {
		return math.MaxInt64
	}
	if d < 0 && int64(t) < math.MinInt64-int64(d) {
		return math.MinInt64
	}
	return t.Add(d)
}

// scaled returns k * d (for non-negative k) with saturation at int64 boundaries.
func scaled(k int64, d time.Duration) time.Duration {
	if k == 0 || d == 0 {
		return 0
	}
	if d > 0 && k > math.MaxInt64/int64(d) {
		return math.MaxInt64
	}
	// Guard d < -1 (not d < 0): MinInt64 / -1 wraps in two's complement, while
	// k * -1 cannot overflow int64 for any non-negative k.
	if d < -1 && k > math.MinInt64/int64(d) {
		return math.MinInt64
	}
	return time.Duration(k) * d
}

// Now implements env.Clock using node n's skewed, drifting view of s.now.
func (h *handle) Now() env.Time { return h.n.local(h.s.now) }

type timer struct {
	h  *handle
	fn func()
	ev *event
}

// AfterFunc schedules fn to fire after d of local time; on a closed handle it
// returns an inert timer whose Stop and Reset return false.
func (h *handle) AfterFunc(d time.Duration, fn func()) env.Timer {
	t := &timer{h: h, fn: fn}
	if !h.closed() {
		t.arm(d)
	}
	return t
}

func (t *timer) arm(d time.Duration) {
	h := t.h
	// Events past Horizon are not queued (hi == -1), but t.ev remains non-nil
	// so Stop still reports that the timer was pending.
	t.ev = h.s.schedule(&event{at: later(h.s.now, h.n.toGlobal(d)), kind: kindTimer, node: h.n.idx, peer: -1, inc: h.inc, timer: t})
}

func (t *timer) Stop() bool {
	e := t.ev
	if e == nil {
		return false
	}
	t.ev = nil
	t.h.s.cancel(e)
	return !t.h.closed()
}

func (t *timer) Reset(d time.Duration) bool {
	was := t.Stop()
	if t.h.closed() {
		return false
	}
	t.arm(d)
	return was
}
