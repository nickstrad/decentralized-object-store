package saga

import (
	"fmt"
	"math"
	"math/rand/v2"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
)

const ppm = 1_000_000

// Policy controls step retry bounds, exponential backoff, jitter, and per-attempt
// timeouts for an Engine.
type Policy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	StepTimeout time.Duration
	JitterPPM   int64
}

// DefaultPolicy returns a valid retry and timeout policy for general use.
func DefaultPolicy() Policy {
	return Policy{MaxAttempts: 5, BaseDelay: 50 * time.Millisecond, MaxDelay: 5 * time.Second, StepTimeout: 10 * time.Second, JitterPPM: 100_000}
}

// Validate checks that every Policy field is within its legal range, returning
// env.ErrInvalidArgument otherwise.
func (p Policy) Validate() error {
	switch {
	case p.MaxAttempts < 1:
		return fmt.Errorf("saga: policy: max attempts %d: %w", p.MaxAttempts, env.ErrInvalidArgument)
	case p.BaseDelay <= 0 || p.MaxDelay < p.BaseDelay:
		return fmt.Errorf("saga: policy: delays %v..%v: %w", p.BaseDelay, p.MaxDelay, env.ErrInvalidArgument)
	case p.StepTimeout <= 0:
		return fmt.Errorf("saga: policy: step timeout %v: %w", p.StepTimeout, env.ErrInvalidArgument)
	case p.JitterPPM < 0 || p.JitterPPM > ppm:
		return fmt.Errorf("saga: policy: jitter %d ppm: %w", p.JitterPPM, env.ErrInvalidArgument)
	}
	return nil
}

// Backoff returns BaseDelay*2^(attempt-1) capped at MaxDelay, plus uniform
// jitter in [0, delay*JitterPPM/1e6] drawn from r when JitterPPM > 0 and r != nil.
// Arithmetic stays in integers and saturates at math.MaxInt64.
func (p Policy) Backoff(attempt int, r *rand.Rand) time.Duration {
	d := p.BaseDelay
	for i := 1; i < attempt && d < p.MaxDelay; i++ {
		if d > p.MaxDelay/2 {
			d = p.MaxDelay
			break
		}
		d *= 2
	}
	if d > p.MaxDelay {
		d = p.MaxDelay
	}
	if p.JitterPPM <= 0 || r == nil {
		return d
	}
	// Split d/ppm and d%ppm to avoid int64 overflow before dividing by ppm.
	span := int64(d)/ppm*p.JitterPPM + int64(d)%ppm*p.JitterPPM/ppm
	if span <= 0 {
		return d
	}
	if span < math.MaxInt64 {
		span++
	}
	j := r.Int64N(span)
	if j > math.MaxInt64-int64(d) {
		return time.Duration(math.MaxInt64)
	}
	return d + time.Duration(j)
}
