package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nickstrad/object-store/internal/metrics"
	"github.com/nickstrad/object-store/internal/storage"
	"math"
	"sync/atomic"
)

func httpValue(s metrics.Snapshot, name string, labels ...string) uint64 {
	for _, p := range s.Points {
		if p.Name != "objstore_http_"+name || len(p.Labels) != len(labels) {
			continue
		}
		match := true
		for i := range labels {
			match = match && p.Labels[i].Value == labels[i]
		}
		if match {
			return p.Counter + p.Gauge
		}
	}
	return 0
}
func TestHTTPMetricsResponsesAndBytes(t *testing.T) {
	m := &meteredHandler{next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Error(err)
		}
		switch r.Method {
		case "PUT":
			w.WriteHeader(201)
		case "DELETE":
			w.WriteHeader(404)
		default:
			w.WriteHeader(503)
		}
		_, _ = w.Write([]byte("result"))
	})}
	for _, method := range []string{"PUT", "DELETE", "GET"} {
		m.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, "/bucket/key", strings.NewReader("abc")))
	}
	s := m.Snapshot()
	for _, status := range []string{"2xx", "4xx", "5xx"} {
		if httpValue(s, "requests_total", "object", status) != 1 {
			t.Fatalf("missing %s", status)
		}
	}
	if httpValue(s, "request_bytes_total") != 9 || httpValue(s, "response_bytes_total") != 18 || httpValue(s, "request_duration_count_total", "object") != 3 || httpValue(s, "request_duration_nanoseconds_total", "object") == 0 || httpValue(s, "requests_in_flight") != 0 {
		t.Fatalf("wrong observations: %+v", s)
	}
	s.Points[0].Labels[0].Value = "mutated"
	if m.Snapshot().Points[0].Labels[0].Value != "health" {
		t.Fatal("aliased snapshot")
	}
	batch := metrics.Batch{SchemaVersion: 1, NodeID: "node", Region: "local", BootID: "boot", Sequence: 1, StartUnixNano: 1, TimeUnixNano: 1, Snapshots: []metrics.Snapshot{m.Snapshot()}}
	if err := metrics.Validate(batch); err != nil {
		t.Fatal(err)
	}
}
func TestHTTPOperationDomain(t *testing.T) {
	for path, want := range map[string]string{"/_health": "health", "/_cluster/members": "members", "/_migrations/id": "migration", "/bucket": "bucket", "/bucket/key": "object", "/bucket/key?tag=x": "tag", "/bucket?acl=x": "acl", "/bucket?quota": "quota", "/": "unknown"} {
		if got := metricOperations[metricOperation(httptest.NewRequest("PUT", path, nil))]; got != want {
			t.Errorf("%s: %s", path, got)
		}
	}
}
func TestHTTPMetricsInFlightCancellationAndDisabled(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	m := &meteredHandler{next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		if !errors.Is(r.Context().Err(), context.Canceled) {
			t.Error("missing cancellation")
		}
		w.WriteHeader(503)
	})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		m.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/_health", nil).WithContext(ctx))
	}()
	<-entered
	if httpValue(m.Snapshot(), "requests_in_flight") != 1 {
		t.Fatal("not in flight")
	}
	cancel()
	close(release)
	<-done
	if httpValue(m.Snapshot(), "requests_total", "health", "5xx") != 1 || httpValue(m.Snapshot(), "requests_in_flight") != 0 {
		t.Fatal("missing completed cancellation")
	}
	srv := NewServer(":0", nil, Limits{})
	if len(srv.Snapshot().Points) != 0 {
		t.Fatal("disabled points")
	}
	if _, ok := NewHandler(nil, Limits{}).(*meteredHandler); ok {
		t.Fatal("disabled middleware")
	}
}
func TestHTTPMetricWriterCountsAcceptedBytesAndFinalStatus(t *testing.T) {
	var m meteredHandler
	base := &shortWriter{header: make(http.Header)}
	w := &metricWriter{ResponseWriter: base, bytes: &m.responseBytes}
	w.WriteHeader(103)
	w.WriteHeader(201)
	w.WriteHeader(500)
	n, err := w.Write([]byte("abc"))
	if n != 1 || err == nil || m.responseBytes.Load() != 1 || w.status != 201 {
		t.Fatal("wrong partial write/status accounting")
	}
}

type shortWriter struct{ header http.Header }

func (w *shortWriter) Header() http.Header       { return w.header }
func (w *shortWriter) WriteHeader(int)           {}
func (w *shortWriter) Write([]byte) (int, error) { return 1, io.ErrShortWrite }

func TestHTTPMetricsPanicPreservesPanicAndAvoidsSuccess(t *testing.T) {
	m := &meteredHandler{next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("broken") })}
	func() {
		defer func() {
			if recover() != "broken" {
				t.Error("panic was swallowed")
			}
		}()
		m.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/_health", nil))
	}()
	s := m.Snapshot()
	if httpValue(s, "requests_total", "health", "other") != 1 || httpValue(s, "requests_total", "health", "2xx") != 0 || httpValue(s, "requests_in_flight") != 0 {
		t.Fatal("panic recorded as success or leaked gauge")
	}
}
func TestHTTPTagOperationMatchesRouting(t *testing.T) {
	for _, method := range []string{"GET", "HEAD", "DELETE", "PUT"} {
		want := "object"
		if method == "PUT" {
			want = "tag"
		}
		if got := metricOperations[metricOperation(httptest.NewRequest(method, "/bucket/key?tag=x", nil))]; got != want {
			t.Errorf("%s: got %s want %s", method, got, want)
		}
	}
}

type metricHealthService struct {
	Service
	health func(context.Context) (storage.Health, error)
}

func (s metricHealthService) Health(ctx context.Context) (storage.Health, error) {
	return s.health(ctx)
}
func TestHTTPMetricsRealHandlerFailuresAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		status string
	}{{"missing", storage.ErrNotFound, "4xx"}, {"unavailable", storage.ErrUnavailable, "5xx"}, {"cancel", context.Canceled, "5xx"}} {
		t.Run(tc.name, func(t *testing.T) {
			svc := metricHealthService{health: func(ctx context.Context) (storage.Health, error) {
				if tc.name == "cancel" {
					<-ctx.Done()
					return storage.Health{}, ctx.Err()
				}
				return storage.Health{}, tc.err
			}}
			h := NewHandler(svc, Limits{MetricsEnabled: true}).(*meteredHandler)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/_health", nil).WithContext(ctx))
			if httpValue(h.Snapshot(), "requests_total", "health", tc.status) != 1 {
				t.Fatal("wrong error class")
			}
		})
	}
	h := NewHandler(nil, Limits{MetricsEnabled: true, MaxObjectSize: 1}).(*meteredHandler)
	r := httptest.NewRequest("PUT", "/bucket/key", strings.NewReader("abc"))
	r.ContentLength = -1
	h.ServeHTTP(httptest.NewRecorder(), r)
	if httpValue(h.Snapshot(), "requests_total", "object", "4xx") != 1 || httpValue(h.Snapshot(), "request_bytes_total") != 2 {
		t.Fatal("overflow was not measured from actual bounded read")
	}
}
func TestHTTPMetricImplicitSuccessAndSaturation(t *testing.T) {
	m := &meteredHandler{next: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}
	m.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/_health", nil))
	if httpValue(m.Snapshot(), "requests_total", "health", "2xx") != 1 {
		t.Fatal("empty successful response")
	}
	var counter atomic.Uint64
	counter.Store(math.MaxUint64 - 1)
	addMetric(&counter, 3)
	if counter.Load() != math.MaxUint64 {
		t.Fatal("counter wrapped")
	}
}
