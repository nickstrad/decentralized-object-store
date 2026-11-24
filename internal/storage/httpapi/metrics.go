package httpapi

import (
	"io"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nickstrad/object-store/internal/metrics"
	"github.com/nickstrad/object-store/internal/storage"
)

var metricOperations = [...]string{"health", "members", "bucket", "object", "tag", "acl", "quota", "migration", "unknown"}
var metricStatuses = [...]string{"2xx", "3xx", "4xx", "5xx", "other"}

type meteredHandler struct {
	next                                  http.Handler
	requests                              [9][5]atomic.Uint64
	durations                             [9]atomic.Uint64
	counts                                [9]atomic.Uint64
	requestBytes, responseBytes, inFlight atomic.Uint64
}

func addMetric(counter *atomic.Uint64, n uint64) {
	for {
		old := counter.Load()
		if counter.CompareAndSwap(old, old+min(n, math.MaxUint64-old)) {
			return
		}
	}
}

func metricOperation(r *http.Request) int {
	path := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case path == "_health":
		return 0
	case path == "_cluster/members":
		return 1
	case path == "_migrations" || strings.HasPrefix(path, "_migrations/"):
		return 7
	}
	bucket, key, hasKey := strings.Cut(path, "/")
	if storage.ValidBucket(bucket) != nil {
		return 8
	}
	q := r.URL.Query()
	if !hasKey {
		if q.Has("quota") {
			return 6
		}
		if q.Has("acl") {
			return 5
		}
		return 2
	}
	if key == "" || len(key) > storage.MaxKeyLen {
		return 8
	}
	if r.Method == http.MethodPut && q.Has("tag") {
		return 4
	}
	return 3
}

type metricBody struct {
	io.ReadCloser
	bytes *atomic.Uint64
}

func (b metricBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	addMetric(b.bytes, uint64(n))
	return n, err
}

type metricWriter struct {
	http.ResponseWriter
	status int
	bytes  *atomic.Uint64
}

func (w *metricWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *metricWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	if status >= 200 || status == http.StatusSwitchingProtocols {
		w.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *metricWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	addMetric(w.bytes, uint64(n))
	return n, err
}
func (m *meteredHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	completed := false
	op := metricOperation(r)
	addMetric(&m.inFlight, 1)
	writer := &metricWriter{ResponseWriter: w, bytes: &m.responseBytes}
	if r.Body != nil {
		r.Body = metricBody{r.Body, &m.requestBytes}
	}
	defer func() {
		m.inFlight.Add(^uint64(0))
		status := writer.status
		if status == 0 && completed {
			status = http.StatusOK
		}
		class := 4
		if status >= 200 && status < 600 {
			class = status/100 - 2
		}
		addMetric(&m.requests[op][class], 1)
		addMetric(&m.counts[op], 1)
		addMetric(&m.durations[op], uint64(max(0, time.Since(start))))
	}()
	m.next.ServeHTTP(writer, r)
	completed = true
}

func (m *meteredHandler) Snapshot() metrics.Snapshot {
	s := metrics.Snapshot{Component: "http"}
	if m == nil {
		return s
	}
	for op, operation := range metricOperations {
		label := []metrics.Label{{Name: "operation", Value: operation}}
		for status, statusClass := range metricStatuses {
			s.Points = append(s.Points, metrics.Point{Name: "objstore_http_requests_total", Labels: []metrics.Label{{Name: "operation", Value: operation}, {Name: "status_class", Value: statusClass}}, Counter: m.requests[op][status].Load()})
		}
		s.Points = append(s.Points, metrics.Point{Name: "objstore_http_request_duration_nanoseconds_total", Labels: label, Counter: m.durations[op].Load()}, metrics.Point{Name: "objstore_http_request_duration_count_total", Labels: []metrics.Label{{Name: "operation", Value: operation}}, Counter: m.counts[op].Load()})
	}
	s.Points = append(s.Points, metrics.Point{Name: "objstore_http_request_bytes_total", Counter: m.requestBytes.Load()}, metrics.Point{Name: "objstore_http_response_bytes_total", Counter: m.responseBytes.Load()}, metrics.Point{Name: "objstore_http_requests_in_flight", Gauge: m.inFlight.Load()})
	return s
}
func (s *Server) Snapshot() metrics.Snapshot { return s.meter.Snapshot() }
