package telemetry_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nickstrad/object-store/internal/metrics"
	"github.com/nickstrad/object-store/internal/telemetry"
	"github.com/nickstrad/object-store/internal/telemetry/adaptertest"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestPrometheusContract(t *testing.T) {
	adaptertest.Run(t, func(t *testing.T) adaptertest.Fixture {
		ln, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		raw, _ := json.Marshal(map[string]string{"listen": addr})
		a, e := telemetry.Open(context.Background(), telemetry.Config{Enabled: true, Adapter: "prometheus", Settings: raw}, adaptertest.Identity())
		if e != nil {
			t.Fatal(e)
		}
		client := &http.Client{}
		t.Cleanup(client.CloseIdleConnections)
		return adaptertest.Fixture{Adapter: a, Readback: func(ctx context.Context) (metrics.Batch, error) {
			req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+addr+"/metrics", nil)
			resp, e := client.Do(req)
			if e != nil {
				return metrics.Batch{}, e
			}
			defer resp.Body.Close()
			body, e := io.ReadAll(resp.Body)
			if e != nil {
				return metrics.Batch{}, e
			}
			if resp.StatusCode != 200 {
				return metrics.Batch{}, fmt.Errorf("status %d", resp.StatusCode)
			}
			b := adaptertest.Batch()
			b.Snapshots[0].Points = nil
			labels := regexp.MustCompile(`([a-z_]+)=("(?:[^"\\]|\\.)*")`)
			for _, line := range strings.Split(string(body), "\n") {
				if line == "" || line[0] == '#' {
					continue
				}
				end := strings.LastIndex(line, "}")
				start := strings.Index(line, "{")
				if start < 0 || end < 0 {
					return b, fmt.Errorf("invalid sample")
				}
				p := metrics.Point{Name: line[:start]}
				v, e := strconv.ParseUint(strings.TrimSpace(line[end+1:]), 10, 64)
				if e != nil {
					return b, e
				}
				for _, d := range metrics.Catalogue() {
					if d.Name == p.Name {
						if d.Kind == metrics.Counter {
							p.Counter = v
						} else {
							p.Gauge = v
						}
						if !strings.Contains(string(body), "# HELP "+d.Name+" "+d.Help+"\n") {
							t.Error("help changed")
						}
					}
				}
				for _, match := range labels.FindAllStringSubmatch(line[start:end], -1) {
					v, e := strconv.Unquote(match[2])
					if e != nil {
						return b, e
					}
					switch match[1] {
					case "node_id":
						if v != b.NodeID {
							t.Error("escaping")
						}
					case "region":
						if v != b.Region {
							t.Error("region")
						}
					default:
						p.Labels = append(p.Labels, metrics.Label{Name: match[1], Value: v})
					}
				}
				b.Snapshots[0].Points = append(b.Snapshots[0].Points, p)
			}
			return b, nil
		}}
	})
}
