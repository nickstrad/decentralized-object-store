package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nickstrad/object-store/internal/noderuntime"
)

func TestObservabilityConfigAndFlagPrecedence(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(file, []byte(`{"observability":{"enabled":true,"adapter":"otel","interval":"1s","timeout":"500ms","settings":{"endpoint":"http://localhost:4318/v1/metrics","insecure":true}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	o, err := parse([]string{"-config", file, "-observability=false", "-observability-interval", "3s", "-observability-settings", `{"endpoint":"other"}`}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if o.Observability.Enabled || o.Observability.Adapter != "otel" || o.Observability.Interval != "3s" || o.Observability.Timeout != "500ms" || string(o.Observability.Settings) != `{"endpoint":"other"}` {
		t.Fatalf("merge %+v", o.Observability)
	}
	o, err = parse(nil, io.Discard)
	if err != nil || o.Observability.Enabled {
		t.Fatal("enabled by default")
	}
	for _, raw := range []string{`{"unknown":true}`, `{"enabled":null}`, `null`, `{"interval":1}`} {
		if err := os.WriteFile(file, []byte(`{"observability":`+raw+`}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := parse([]string{"-config", file}, io.Discard); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
func obsArgs(t *testing.T) []string {
	t.Helper()
	return []string{"-id", "127.0.0.1:0", "-region", "local", "-listen", "127.0.0.1:0", "-http", "127.0.0.1:0", "-data", t.TempDir(), "-k", "1", "-m", "0", "-shutdown-timeout", "2s"}
}
func TestObservabilityInvalidConfigExitsTwoBeforeRuntimeStartup(t *testing.T) {
	for _, flags := range [][]string{
		{"-observability-adapter", "missing"},
		{"-observability-interval", "oops"},
		{"-observability-interval", "1ms"},
		{"-observability-timeout", "20s"},
		{"-observability-settings", "[]"},
		{"-observability", "-observability-settings", `{"endpoint":"invalid"}`},
		{"-observability", "-observability-settings", `{"endpoint":"http://localhost:4318/v1/metrics"}`},
		{"-observability", "-observability-settings", `{"endpoint":"https://localhost:4318/v1/metrics","unknown":true}`},
	} {
		args := append(obsArgs(t), flags...)
		var out bytes.Buffer
		ready := false
		if code := run(context.Background(), args, &out, func(string, *noderuntime.Runtime) { ready = true }); code != 2 || ready {
			t.Fatalf("%v: code=%d ready=%v %s", flags, code, ready, out.String())
		}
	}
}
func TestObservabilityDisabledDoesNotExportOrValidateSink(t *testing.T) {
	var exports atomic.Uint64
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { exports.Add(1); w.WriteHeader(200) }))
	defer sink.Close()
	for _, settings := range []string{`{"endpoint":"` + sink.URL + `","insecure":true}`, `{"ignored":true}`} {
		ctx, cancel := context.WithCancel(context.Background())
		args := append(obsArgs(t), "-observability=false", "-observability-settings", settings)
		var out bytes.Buffer
		code := run(ctx, args, &out, func(addr string, rt *noderuntime.Runtime) {
			if len(rt.Snapshot().Points) != 0 {
				t.Error("runtime metrics enabled")
			}
			cancel()
		})
		cancel()
		if code != 0 || exports.Load() != 0 {
			t.Fatalf("disabled: %d exports %d %s", code, exports.Load(), out.String())
		}
	}
}
func TestObjnodeExportsOwnHTTPMetricAndJoinsCollector(t *testing.T) {
	exported := make(chan []byte, 8)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		select {
		case exported <- body:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer sink.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	args := append(obsArgs(t), "-observability", "-observability-interval", "100ms", "-observability-timeout", "90ms", "-observability-settings", `{"endpoint":"`+sink.URL+`/v1/metrics","insecure":true}`)
	done := make(chan int, 1)
	var out bytes.Buffer
	go func() {
		done <- run(ctx, args, &out, func(addr string, _ *noderuntime.Runtime) {
			resp, err := http.Get("http://" + addr + "/_health")
			if err != nil {
				t.Error(err)
				cancel()
				return
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		})
	}()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	var boot string
	sawHTTP := false
	for !sawHTTP {
		select {
		case body := <-exported:
			if !json.Valid(body) {
				t.Errorf("bad JSON: %s", body)
			}
			if !strings.Contains(string(body), "objstore_runtime_running") {
				t.Errorf("no runtime metric: %s", body)
			}
			if !strings.Contains(string(body), "objstore_http_requests_total") {
				t.Errorf("no HTTP metric: %s", body)
			}
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			resource := payload["resourceMetrics"].([]any)[0].(map[string]any)["resource"].(map[string]any)
			for _, attr := range resource["attributes"].([]any) {
				a := attr.(map[string]any)
				if a["key"] == "boot_id" {
					value := a["value"].(map[string]any)["stringValue"].(string)
					if boot != "" && boot != value {
						t.Error("identity changed")
					}
					boot = value
				}
			}
			for _, scope := range payload["resourceMetrics"].([]any)[0].(map[string]any)["scopeMetrics"].([]any) {
				for _, rawMetric := range scope.(map[string]any)["metrics"].([]any) {
					metric := rawMetric.(map[string]any)
					if metric["name"] != "objstore_http_requests_total" {
						continue
					}
					for _, rawPoint := range metric["sum"].(map[string]any)["dataPoints"].([]any) {
						point := rawPoint.(map[string]any)
						health, ok := false, false
						for _, rawAttribute := range point["attributes"].([]any) {
							attr := rawAttribute.(map[string]any)
							value := attr["value"].(map[string]any)["stringValue"]
							health = health || attr["key"] == "operation" && value == "health"
							ok = ok || attr["key"] == "status_class" && value == "2xx"
						}
						sawHTTP = sawHTTP || health && ok && point["asInt"] == "1"
					}
				}
			}
		case <-deadline.C:
			cancel()
			<-done
			t.Fatalf("no real node export: %s", out.String())
		case code := <-done:
			t.Fatalf("early exit %d: %s", code, out.String())
		}
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("collector did not join")
	}
	if boot == "" {
		t.Fatal("missing boot identity")
	}
}
func TestObservabilityRollsBackAdapterAfterHTTPStartupFailure(t *testing.T) {
	// The mandatory direct Prometheus adapter is tested once registered by O3.
	// OTel has no persistent listener; proving object-runtime rollback still catches leaks.
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	args := append(obsArgs(t), "-http", occupied.Addr().String(), "-observability", "-observability-settings", `{"endpoint":"http://127.0.0.1:1/v1/metrics","insecure":true}`)
	var out bytes.Buffer
	if code := run(context.Background(), args, &out, nil); code != 1 {
		t.Fatalf("exit %d %s", code, out.String())
	}
}

func TestObjnodeShutdownCancelsExportOutage(t *testing.T) {
	entered := make(chan struct{}, 1)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer sink.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	args := append(obsArgs(t), "-observability", "-observability-interval", "1s", "-observability-timeout", "900ms", "-observability-settings", `{"endpoint":"`+sink.URL+`/v1/metrics","insecure":true}`)
	var out bytes.Buffer
	done := make(chan int, 1)
	go func() { done <- run(ctx, args, &out, nil) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		cancel()
		<-done
		t.Fatalf("export never started: %s", out.String())
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, out.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("export cancellation did not join")
	}
}

func TestObjnodePrometheusStartupRollbackAndSinkExitOne(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	base := obsArgs(t)
	args := append(base, "-observability", "-observability-adapter", "prometheus", "-observability-settings", `{"listen":"`+occupied.Addr().String()+`"}`)
	var out bytes.Buffer
	if code := run(context.Background(), args, &out, nil); code != 1 {
		t.Fatalf("occupied metrics address exit=%d: %s", code, out.String())
	}
	spare, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	metricsAddr := spare.Addr().String()
	_ = spare.Close()
	args = append(obsArgs(t), "-http", occupied.Addr().String(), "-observability", "-observability-adapter", "prometheus", "-observability-settings", `{"listen":"`+metricsAddr+`"}`)
	out.Reset()
	if code := run(context.Background(), args, &out, nil); code != 1 {
		t.Fatalf("HTTP startup exit=%d: %s", code, out.String())
	}
	check, err := net.Listen("tcp", metricsAddr)
	if err != nil {
		t.Fatalf("adapter listener leaked after HTTP failure: %v", err)
	}
	_ = check.Close()
	ctx, cancel := context.WithCancel(context.Background())
	args = append(obsArgs(t), "-observability=false", "-observability-adapter", "prometheus", "-observability-settings", `{"listen":"`+occupied.Addr().String()+`"}`)
	out.Reset()
	if code := run(ctx, args, &out, func(string, *noderuntime.Runtime) { cancel() }); code != 0 {
		t.Fatalf("disabled sink opened: %d: %s", code, out.String())
	}
	cancel()
}
