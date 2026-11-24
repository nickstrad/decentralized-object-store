package telemetry

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/nickstrad/object-store/internal/metrics"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testBatch(id Identity) metrics.Batch {
	return metrics.Batch{SchemaVersion: 1, NodeID: id.NodeID, Region: id.Region, BootID: id.BootID, StartUnixNano: id.StartUnixNano, TimeUnixNano: id.StartUnixNano + 1, Sequence: 1, Snapshots: []metrics.Snapshot{{Component: "node", Points: []metrics.Point{{Name: "objstore_node_rebuilt_bytes_total", Counter: math.MaxUint64}, {Name: "objstore_node_ready", Gauge: 1}}}}}
}
func openTest(t *testing.T, url string) (*otelAdapter, metrics.Batch) {
	t.Helper()
	id := NewIdentity("node", "local")
	raw, _ := json.Marshal(otelSettings{Endpoint: url + "/v1/metrics", Insecure: true})
	a, e := Open(context.Background(), Config{Enabled: true, Settings: raw}, id)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	return a.(*otelAdapter), testBatch(id)
}
func TestOTelResponses(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body, retry string
		calls       int
		partial     bool
	}{
		{"ok", 200, `{}`, "", 1, false}, {"unknown-fields", 200, `{"future":1}`, "", 1, false}, {"warning", 200, `{"partialSuccess":{"rejectedDataPoints":"0","errorMessage":"notice"}}`, "", 1, false},
		{"partial", 200, `{"partialSuccess":{"rejectedDataPoints":"2"}}`, "", 1, true}, {"numeric-partial", 200, `{"partialSuccess":{"rejectedDataPoints":2}}`, "", 1, true},
		{"bad", 200, `garbage`, "", 1, false}, {"empty", 200, ``, "", 1, false}, {"null", 200, `null`, "", 1, false}, {"trailing", 200, `{} {}`, "", 1, false}, {"negative-reject", 200, `{"partialSuccess":{"rejectedDataPoints":"-1"}}`, "", 1, false},
		{"large", 200, strings.Repeat(" ", 65537), "", 1, false}, {"bad-request", 400, `{}`, "", 1, false}, {"redirect", 302, `{}`, "", 1, false}, {"retry", 503, `{}`, "0", 2, false}, {"rate", 429, `{}`, "0", 2, false}, {"gateway", 502, `{}`, "", 2, false}, {"timeout", 504, `{}`, "", 2, false}, {"long-retry", 503, `{}`, "9999", 1, false}, {"date-retry", 503, `{}`, time.Now().Add(time.Hour).UTC().Format(http.TimeFormat), 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if tc.retry != "" {
					w.Header().Set("Retry-After", tc.retry)
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			a, b := openTest(t, server.URL)
			e := a.Publish(context.Background(), b)
			if calls != tc.calls {
				t.Fatal(calls)
			}
			if tc.partial && !errors.Is(e, ErrPartial) {
				t.Fatal(e)
			}
			ok := tc.name == "ok" || tc.name == "unknown-fields" || tc.name == "warning"
			if (e == nil) != ok {
				t.Fatal(e)
			}
		})
	}
}
func TestOTelWire(t *testing.T) {
	id := Identity{NodeID: "n", Region: "r", BootID: "b", StartUnixNano: 1}
	b := testBatch(id)
	out, e := encodeOTel(b)
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{`"asDouble":18446744073709552000`, `"asInt":"1"`, `"aggregationTemporality":2`, `"isMonotonic":true`, `"startTimeUnixNano":"1"`, `"timeUnixNano":"2"`, `"service.instance.id"`, `"boot_id"`} {
		if !strings.Contains(string(out), s) {
			t.Fatalf("missing %s in %s", s, out)
		}
	}
	b.Snapshots[0].Points[0].Counter = 1<<53 + 1
	out, _ = encodeOTel(b)
	if !strings.Contains(string(out), `"asInt":"9007199254740993"`) {
		t.Fatal(string(out))
	}
	// Frozen golden checks all field spellings, grouping, descriptors and resource attributes.
	if os.Getenv("UPDATE_OTEL_GOLDEN") == "1" {
		if e = os.MkdirAll("testdata", 0755); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile("testdata/otel.json", out, 0644); e != nil {
			t.Fatal(e)
		}
	}
	want, e := os.ReadFile("testdata/otel.json")
	if e != nil {
		t.Fatal(e)
	}
	if string(out) != string(want) {
		t.Fatal("OTLP golden differs")
	}
}
func TestOTelConfig(t *testing.T) {
	id := NewIdentity("n", "r")
	for _, raw := range []string{`{"endpoint":"http://localhost/v1/metrics"}`, `{"endpoint":"https://localhost/no"}`, `{"endpoint":"https://u:p@localhost/v1/metrics"}`, `{"endpoint":"ftp://host/v1/metrics"}`, `{"endpoint":"https://host/v1/metrics?token=secret"}`, `{"endpoint":"https://host/v1/metrics","extra":1}`, `{"endpoint":"https://host/v1/metrics","ca_file":"/no/such/file"}`, `{"endpoint":"https://host/v1/metrics","headers_file":"/no/such/file"}`} {
		if _, e := Open(context.Background(), Config{Enabled: true, Settings: json.RawMessage(raw)}, id); !errors.Is(e, ErrConfig) {
			t.Fatal(raw, e)
		}
	}
	for _, tc := range []struct{ key, body string }{{"ca_file", "bad pem"}, {"headers_file", "null"}, {"headers_file", `{"Content-Type":"x"}`}, {"headers_file", `{"bad header":"x"}`}, {"headers_file", `{"Authorization":"a\r\nb"}`}} {
		p := filepath.Join(t.TempDir(), "settings")
		_ = os.WriteFile(p, []byte(tc.body), 0600)
		raw, _ := json.Marshal(map[string]any{"endpoint": "https://localhost/v1/metrics", tc.key: p})
		if _, e := Open(context.Background(), Config{Enabled: true, Settings: raw}, id); !errors.Is(e, ErrConfig) {
			t.Fatal(e)
		}
	}
	for _, kv := range [][2]string{{"", "a"}, {"a", "\x00"}, {"a", "\x7f"}} {
		if validHeader(kv[0], kv[1]) {
			t.Fatal(kv)
		}
	}
}
func TestOTelCancellationAndTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(io.Discard, r.Body); <-r.Context().Done() }))
	a, b := openTest(t, server.URL)
	ctx, c := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer c()
	if e := a.Publish(ctx, b); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
	_ = a.Shutdown(context.Background())
	server.Close()
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	a, b = openTest(t, url)
	if a.Publish(context.Background(), b) == nil {
		t.Fatal("transport failure ignored")
	}
	b.NodeID = "other"
	if a.Publish(context.Background(), b) == nil {
		t.Fatal("identity mismatch")
	}
}
func TestOTelTLSAndHeaders(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "secret" {
			t.Error("header missing")
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	headers := filepath.Join(dir, "headers.json")
	if e := os.WriteFile(ca, pemCertificate(server.Certificate().Raw), 0600); e != nil {
		t.Fatal(e)
	}
	_ = os.WriteFile(headers, []byte(`{"Authorization":"secret"}`), 0600)
	id := NewIdentity("n", "r")
	raw, _ := json.Marshal(otelSettings{Endpoint: server.URL + "/v1/metrics", CAFile: ca, HeadersFile: headers})
	a, e := Open(context.Background(), Config{Enabled: true, Settings: raw}, id)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Shutdown(context.Background())
	if e = a.Publish(context.Background(), testBatch(id)); e != nil {
		t.Fatal(e)
	}
}

func pemCertificate(b []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: b})
}

func TestOTelRetryRecoveryAndRestart(t *testing.T) {
	calls := 0
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		requests = append(requests, string(b))
		if calls == 1 {
			w.WriteHeader(503)
			return
		}
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	a, b := openTest(t, server.URL)
	if err := a.Publish(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	if calls != 2 || requests[0] != requests[1] {
		t.Fatal("retry changed cumulative batch", calls)
	}
	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	next, reset := openTest(t, server.URL)
	reset.Snapshots[0].Points[0].Counter = 0
	if reset.BootID == b.BootID {
		t.Fatal("boot identity reused")
	}
	if err := next.Publish(context.Background(), reset); err != nil {
		t.Fatal(err)
	}
	if requests[1] == requests[2] || !strings.Contains(requests[2], reset.BootID) {
		t.Fatal("restart metadata lost")
	}
}

func TestOTelShutdownCancelsActivePublish(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	a, b := openTest(t, server.URL)
	finished := make(chan error, 1)
	go func() { finished <- a.Publish(context.Background(), b) }()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := a.Shutdown(ctx)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err = a.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestOTelHonorsCallerTimeoutBeyondDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	a, b := openTest(t, server.URL)
	if a.client.Timeout != 0 || a.transport.ResponseHeaderTimeout != 0 {
		t.Fatalf("unexpected fixed client timeouts: client=%v header=%v", a.client.Timeout, a.transport.ResponseHeaderTimeout)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.Publish(ctx, b); err != nil {
		t.Fatal(err)
	}
}
