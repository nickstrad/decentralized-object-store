package objload

import (
	"math"
	"time"
)

// Pacer schedules an open-loop request stream: request i is due at
// start + i/rate whatever the latency of earlier requests. A sender that falls
// more than maxLag behind skips the overdue requests instead of bursting to
// catch up, and the skipped requests are reported as missed.
type Pacer struct {
	start  time.Time
	rate   float64
	maxLag time.Duration
	next   int64
}

// NewPacer returns a Pacer whose first request is due at start.
func NewPacer(start time.Time, rate float64, maxLag time.Duration) *Pacer {
	return &Pacer{start: start, rate: rate, maxLag: maxLag}
}

// Due returns the time request i is due.
func (p *Pacer) Due(i int64) time.Time {
	return p.start.Add(time.Duration(float64(i) * float64(time.Second) / p.rate))
}

// Next claims the next request at time now. It returns the request's due
// time (wait until then before sending) and how many overdue requests it
// skipped to get within maxLag of now.
func (p *Pacer) Next(now time.Time) (due time.Time, skipped int64) {
	if late := now.Sub(p.Due(p.next)); late > p.maxLag {
		j := int64(math.Ceil(float64(now.Add(-p.maxLag).Sub(p.start)) * p.rate / float64(time.Second)))
		if j > p.next {
			skipped, p.next = j-p.next, j
		}
	}
	due = p.Due(p.next)
	p.next++
	return due, skipped
}
