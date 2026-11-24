package telemetry_test

import (
	"context"
	"encoding/json"
	"github.com/nickstrad/object-store/internal/metrics"
	"github.com/nickstrad/object-store/internal/telemetry"
	"github.com/nickstrad/object-store/internal/telemetry/adaptertest"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

func TestOTelContract(t *testing.T) {
	adaptertest.Run(t, func(t *testing.T) adaptertest.Fixture {
		var mu sync.Mutex
		var latest metrics.Batch
		fail := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			defer mu.Unlock()
			if fail {
				fail = false
				w.WriteHeader(400)
				return
			}
			if r.Method != "POST" || r.URL.Path != "/v1/metrics" || r.Header.Get("Content-Type") != "application/json" {
				t.Error("request protocol")
			}
			var payload struct {
				ResourceMetrics []struct {
					ScopeMetrics []struct {
						Metrics []struct {
							Name        string
							Description string
							Unit        string
							Sum         *struct {
								DataPoints             []readPoint
								AggregationTemporality int
								IsMonotonic            bool
							}
							Gauge *struct{ DataPoints []readPoint }
						}
					}
				}
			}
			if e := json.NewDecoder(r.Body).Decode(&payload); e != nil {
				t.Error(e)
				w.WriteHeader(400)
				return
			}
			b := adaptertest.Batch()
			b.Snapshots[0].Points = nil
			for _, rm := range payload.ResourceMetrics {
				for _, sm := range rm.ScopeMetrics {
					for _, m := range sm.Metrics {
						for _, descriptor := range metrics.Catalogue() {
							if descriptor.Name == m.Name && (descriptor.Help != m.Description || descriptor.Unit != m.Unit) {
								t.Error("descriptor changed")
							}
						}
						points := []readPoint{}
						if m.Sum != nil {
							points = m.Sum.DataPoints
							if m.Sum.AggregationTemporality != 2 || !m.Sum.IsMonotonic {
								t.Error("sum semantics")
							}
						} else {
							points = m.Gauge.DataPoints
						}
						for _, p := range points {
							v, e := strconv.ParseUint(p.AsInt, 10, 64)
							if e != nil {
								t.Error(e)
							}
							out := metrics.Point{Name: m.Name}
							if m.Sum != nil {
								out.Counter = v
							} else {
								out.Gauge = v
							}
							for _, a := range p.Attributes {
								switch a.Key {
								case "node_id":
									if a.Value.StringValue != b.NodeID {
										t.Error("identity escaping")
									}
								case "region":
								default:
									out.Labels = append(out.Labels, metrics.Label{Name: a.Key, Value: a.Value.StringValue})
								}
							}
							b.Snapshots[0].Points = append(b.Snapshots[0].Points, out)
						}
					}
				}
			}
			latest = b
			_, _ = w.Write([]byte(`{}`))
		}))
		t.Cleanup(server.Close)
		raw, _ := json.Marshal(map[string]any{"endpoint": server.URL + "/v1/metrics", "insecure": true})
		a, e := telemetry.Open(context.Background(), telemetry.Config{Enabled: true, Settings: raw}, adaptertest.Identity())
		if e != nil {
			t.Fatal(e)
		}
		return adaptertest.Fixture{Adapter: a, Readback: func(context.Context) (metrics.Batch, error) {
			mu.Lock()
			defer mu.Unlock()
			return metrics.Clone(latest), nil
		}, FailNext: func() { mu.Lock(); fail = true; mu.Unlock() }}
	})
}

type readPoint struct {
	AsInt      string
	Attributes []struct {
		Key   string
		Value struct{ StringValue string }
	}
}
