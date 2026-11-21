package sim

import (
	"math/bits"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

func (n *node) local(g env.Time) env.Time {
	gi := int64(g)
	return env.Time(gi + n.skew + (gi/1_000_000)*n.drift + (gi%1_000_000)*n.drift/1_000_000)
}

func (n *node) toGlobal(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d > 1<<62 {
		d = 1 << 62
	}
	if n.drift == 0 {
		return d
	}
	den := uint64(1_000_000 + n.drift)
	hi, lo := bits.Mul64(uint64(d), 1_000_000)
	q, r := bits.Div64(hi, lo, den)
	if r != 0 {
		q++
	}
	return time.Duration(q)
}

func (h *handle) Now() env.Time { return h.n.local(h.s.now) }

type timer struct {
	h  *handle
	fn func()
	ev *event
}

func (h *handle) AfterFunc(d time.Duration, fn func()) env.Timer {
	t := &timer{h: h, fn: fn}
	if !h.closed() {
		t.arm(d)
	}
	return t
}

func (t *timer) arm(d time.Duration) {
	h := t.h
	t.ev = h.s.schedule(&event{at: h.s.now.Add(h.n.toGlobal(d)), kind: kindTimer, node: h.n.idx, peer: -1, inc: h.inc, timer: t})
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
