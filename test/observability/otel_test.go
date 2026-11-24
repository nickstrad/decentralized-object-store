package observability_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestObjnodeOTelPrometheus(t *testing.T) {
	if os.Getenv("OBS_INTEGRATION") != "1" {
		t.Skip("requires Docker observability services and O2 objnode wiring")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	request := func(endpoint string) []byte {
		t.Helper()
		req, e := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if e != nil {
			t.Fatal(e)
		}
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		b, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if e != nil || resp.StatusCode != 200 {
			t.Fatalf("HTTP %d: %v", resp.StatusCode, e)
		}
		return b
	}
	health := func() {
		for range 5 {
			request("http://obs-node:8080/_health")
		}
	}
	poll := func(q string, above float64) float64 {
		t.Helper()
		for {
			b := request("http://prometheus:9090/api/v1/query?query=" + url.QueryEscape(q))
			var v struct {
				Status string
				Data   struct {
					ResultType string
					Result     []struct{ Value []json.RawMessage }
				}
			}
			if e := json.Unmarshal(b, &v); e != nil {
				t.Fatal(e)
			}
			if v.Status == "success" && v.Data.ResultType == "vector" && len(v.Data.Result) > 0 && len(v.Data.Result[0].Value) == 2 {
				var s string
				_ = json.Unmarshal(v.Data.Result[0].Value[1], &s)
				n, e := strconv.ParseFloat(s, 64)
				if e == nil && n > above {
					t.Logf("query=%s result=%s", q, b)
					return n
				}
			}
			select {
			case <-ctx.Done():
				t.Fatalf("metric did not advance: %s %s", q, b)
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	health()
	q := `objstore_http_requests_total{node_id="obs-node:7946",operation="health",status_class="2xx"}`
	first := poll(q, 0)
	health()
	poll(q, first)
	poll(`objstore_http_requests_in_flight{node_id="obs-node:7946"}`, -1)
}
