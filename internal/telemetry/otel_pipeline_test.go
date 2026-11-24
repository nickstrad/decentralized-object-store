package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nickstrad/object-store/internal/metrics"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// This fixture proves only the port and transport. Objnode proof is a separate A2 gate.
func TestOTelCollectorFixture(t *testing.T) {
	if os.Getenv("OBS_FIXTURE") != "1" {
		t.Skip("manual Collector fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	id := NewIdentity("fixture-node", "local")
	cfg := Config{Enabled: true, Interval: "100ms", Timeout: "90ms", Settings: json.RawMessage(`{"endpoint":"http://otel-collector:4318/v1/metrics","insecure":true}`)}
	adapter, e := Open(ctx, cfg, id)
	if e != nil {
		t.Fatal(e)
	}
	var counter atomic.Uint64
	counter.Store(7)
	collector := collectFunc(func(context.Context) (metrics.Batch, error) {
		return metrics.Batch{Snapshots: []metrics.Snapshot{{Component: "http", Points: []metrics.Point{{Name: "objstore_http_requests_total", Labels: []metrics.Label{{Name: "operation", Value: "health"}, {Name: "status_class", Value: "2xx"}}, Counter: counter.Load()}, {Name: "objstore_http_requests_in_flight", Gauge: 1}}}}}, nil
	})
	runner, e := NewRunner(cfg, id, collector, adapter, t.Logf)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	defer func() {
		stopCtx, c := context.WithTimeout(context.Background(), time.Second)
		defer c()
		if e := runner.Shutdown(stopCtx); e != nil {
			t.Error(e)
		}
		<-done
	}()
	query := `objstore_http_requests_total{node_id="fixture-node",region="local",operation="health",status_class="2xx"}`
	poll := func(q string, want float64) {
		t.Helper()
		for {
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://prometheus:9090/api/v1/query?query="+url.QueryEscape(q), nil)
			resp, e := http.DefaultClient.Do(req)
			if e == nil {
				var result struct {
					Status string
					Data   struct {
						Result []struct {
							Metric map[string]string
							Value  []json.RawMessage
						}
					}
				}
				e = json.NewDecoder(resp.Body).Decode(&result)
				resp.Body.Close()
				if e == nil && result.Status == "success" && len(result.Data.Result) > 0 {
					var v string
					_ = json.Unmarshal(result.Data.Result[0].Value[1], &v)
					f, _ := strconv.ParseFloat(v, 64)
					if f == want {
						b, _ := json.Marshal(result)
						t.Logf("query=%s result=%s", q, b)
						return
					}
				}
			}
			select {
			case <-ctx.Done():
				t.Fatal(fmt.Errorf("query never reached %v: %s: %w", want, q, ctx.Err()))
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	poll(query, 7)
	counter.Store(15)
	poll(query, 15)
	poll(`objstore_http_requests_in_flight{node_id="fixture-node",region="local"}`, 1)
	poll(`count({boot_id!=""}) or vector(0)`, 0)
}
