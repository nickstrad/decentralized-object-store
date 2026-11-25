package objload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
)

// Class groups outcomes: the HTTP status classes the server's metrics use,
// plus requests that got no response.
type Class int

// The outcome classes.
const (
	C2xx     Class = iota
	C3xx           // redirects (the API sends none)
	C4xx           // client errors, including the expected 404 and 412
	C5xx           // server errors
	CTimeout       // no response within the request timeout
	CNet           // connection refused, reset, or another transport error
	NumClasses
)

var classNames = [NumClasses]string{"2xx", "3xx", "4xx", "5xx", "timeout", "net"}

func (c Class) String() string {
	if c < 0 || c >= NumClasses {
		return fmt.Sprintf("class(%d)", int(c))
	}
	return classNames[c]
}

// Outcome is the result of one request.
type Outcome struct {
	Class Class
	// Status is the HTTP status code, 0 when there was no response.
	Status int
	// Failed is false for success and for the statuses a randomized load
	// expects (404 Not Found and 412 Precondition Failed).
	Failed bool
}

// Classify maps a Service error to an Outcome. Errors carrying an HTTP status
// (httpclient.HTTPError) use it; timeouts and transport errors get their own
// classes; other errors, from a Service called in process, take the status
// httpapi.StatusTable gives them.
func Classify(err error) Outcome {
	if err == nil {
		return Outcome{Class: C2xx, Status: http.StatusOK}
	}
	var sc interface{ StatusCode() int }
	status := 0
	switch {
	case errors.As(err, &sc):
		status = sc.StatusCode()
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(err, env.ErrTimeout):
		return Outcome{Class: CTimeout, Failed: true}
	case errors.Is(err, env.ErrUnreachable) || errors.Is(err, context.Canceled):
		return Outcome{Class: CNet, Failed: true}
	default:
		status = httpapi.StatusOf(httpapi.StatusTable(), err)
	}
	o := Outcome{Status: status, Failed: status != http.StatusNotFound && status != http.StatusPreconditionFailed}
	switch {
	case status >= 200 && status < 300:
		o.Class, o.Failed = C2xx, false
	case status >= 300 && status < 400:
		o.Class = C3xx
	case status >= 400 && status < 500:
		o.Class = C4xx
	default:
		o.Class = C5xx
	}
	return o
}

// Counts accumulates outcomes.
type Counts struct {
	Requests   int64
	ByOp       [NumOps][NumClasses]int64
	ByStatus   map[int]int64
	Failed     int64
	Missed     int64
	LatencySum time.Duration
	LatencyMax time.Duration
	OpLatSum   [NumOps]time.Duration
	OpLatMax   [NumOps]time.Duration
}

// Add records one request of op with outcome o that took d.
func (c *Counts) Add(op Op, o Outcome, d time.Duration) {
	c.Requests++
	c.ByOp[op][o.Class]++
	if o.Status != 0 && o.Class != C2xx {
		if c.ByStatus == nil {
			c.ByStatus = map[int]int64{}
		}
		c.ByStatus[o.Status]++
	}
	if o.Failed {
		c.Failed++
	}
	c.LatencySum += d
	c.LatencyMax = max(c.LatencyMax, d)
	c.OpLatSum[op] += d
	c.OpLatMax[op] = max(c.OpLatMax[op], d)
}

// Class returns the number of requests with outcome class cl.
func (c *Counts) Class(cl Class) int64 {
	var n int64
	for op := range c.ByOp {
		n += c.ByOp[op][cl]
	}
	return n
}

// Op returns the number of requests of op.
func (c *Counts) Op(op Op) int64 {
	var n int64
	for _, v := range c.ByOp[op] {
		n += v
	}
	return n
}

// MeanLatency returns the mean latency, 0 with no requests.
func (c *Counts) MeanLatency() time.Duration {
	if c.Requests == 0 {
		return 0
	}
	return c.LatencySum / time.Duration(c.Requests)
}

// Recorder is a Counts for the whole run and one for the current interval,
// safe for concurrent use.
type Recorder struct {
	mu              sync.Mutex
	total, interval Counts
}

// Record adds one request to both counts.
func (r *Recorder) Record(op Op, o Outcome, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total.Add(op, o, d)
	r.interval.Add(op, o, d)
}

// Miss counts n requests that were due but not sent.
func (r *Recorder) Miss(n int64) {
	if n == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total.Missed += n
	r.interval.Missed += n
}

// TakeInterval returns the interval counts and starts a new interval.
func (r *Recorder) TakeInterval() Counts {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.interval
	r.interval = Counts{}
	return c
}

// Total returns a copy of the counts of the whole run.
func (r *Recorder) Total() Counts {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := r.total
	if c.ByStatus != nil {
		m := make(map[int]int64, len(c.ByStatus))
		for k, v := range c.ByStatus {
			m[k] = v
		}
		c.ByStatus = m
	}
	return c
}

func rate(n int64, d time.Duration) float64 {
	if d <= 0 {
		return 0
	}
	return float64(n) / d.Seconds()
}

func ms(d time.Duration) string { return fmt.Sprintf("%.1fms", float64(d)/float64(time.Millisecond)) }

// StatusLine formats the counts of one interval of length span ending at
// elapsed into the run.
func StatusLine(elapsed, span time.Duration, c Counts, target float64) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%6.0fs  %7.1f req/s (target %g)", elapsed.Seconds(), rate(c.Requests, span), target)
	for cl := Class(0); cl < NumClasses; cl++ {
		if n := c.Class(cl); n > 0 || cl == C2xx || cl == C4xx || cl == C5xx {
			fmt.Fprintf(&b, "  %s %d", cl, n)
		}
	}
	if c.Missed > 0 {
		fmt.Fprintf(&b, "  MISSED %d", c.Missed)
	}
	fmt.Fprintf(&b, "  latency mean %s max %s", ms(c.MeanLatency()), ms(c.LatencyMax))
	return b.String()
}

// Summary is the result of a run.
type Summary struct {
	Counts
	Elapsed time.Duration
	Target  float64
	Seed    uint64
}

// Achieved returns the completed requests per second.
func (s Summary) Achieved() float64 { return rate(s.Requests, s.Elapsed) }

// Succeeded reports whether any request got a 2xx response.
func (s Summary) Succeeded() bool { return s.Class(C2xx) > 0 }

// Write prints the summary: totals by operation and outcome class, the
// non-2xx status codes, failures, and the achieved against the target rate.
func (s Summary) Write(w io.Writer) {
	pct := 0.0
	if s.Target > 0 {
		pct = 100 * s.Achieved() / s.Target
	}
	fmt.Fprintf(w, "\nsummary: %d requests in %.1fs, %.1f req/s achieved of %g req/s target (%.0f%%), seed %d\n",
		s.Requests, s.Elapsed.Seconds(), s.Achieved(), s.Target, pct, s.Seed)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintf(tw, "%-11s\t", "operation")
	for cl := Class(0); cl < NumClasses; cl++ {
		fmt.Fprintf(tw, "%s\t", cl)
	}
	fmt.Fprint(tw, "mean\tmax\t\n")
	for op := Op(0); op < NumOps; op++ {
		n := s.Op(op)
		if n == 0 {
			continue
		}
		fmt.Fprintf(tw, "%-11s\t", op)
		for cl := Class(0); cl < NumClasses; cl++ {
			fmt.Fprintf(tw, "%d\t", s.ByOp[op][cl])
		}
		fmt.Fprintf(tw, "%s\t%s\t\n", ms(s.OpLatSum[op]/time.Duration(n)), ms(s.OpLatMax[op]))
	}
	fmt.Fprintf(tw, "%-11s\t", "total")
	for cl := Class(0); cl < NumClasses; cl++ {
		fmt.Fprintf(tw, "%d\t", s.Class(cl))
	}
	fmt.Fprintf(tw, "%s\t%s\t\n", ms(s.MeanLatency()), ms(s.LatencyMax))
	_ = tw.Flush()
	codes := make([]int, 0, len(s.ByStatus))
	for code := range s.ByStatus {
		codes = append(codes, code)
	}
	slices.Sort(codes)
	if len(codes) > 0 {
		parts := make([]string, len(codes))
		for i, code := range codes {
			parts[i] = fmt.Sprintf("%d %s: %d", code, http.StatusText(code), s.ByStatus[code])
		}
		fmt.Fprintf(w, "non-2xx statuses: %s\n", strings.Join(parts, ", "))
	}
	fmt.Fprintf(w, "failures: %d (5xx, timeouts, network errors and 4xx other than 404 and 412)\n", s.Failed)
	if s.Missed > 0 {
		fmt.Fprintf(w, "missed: %d requests were due but not sent because every worker was busy; the target rate was not met\n", s.Missed)
	}
}
