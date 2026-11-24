package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func openProm(t *testing.T, settings string) *prometheusAdapter {
	t.Helper()
	a, e := Open(context.Background(), Config{Enabled: true, Adapter: "prometheus", Interval: "100ms", Timeout: "50ms", Settings: json.RawMessage(settings)}, Identity{"n", "r", "b", 1})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	return a.(*prometheusAdapter)
}
func scrapeProm(t *testing.T, a *prometheusAdapter, path, method string) (int, string) {
	t.Helper()
	c := &http.Client{Timeout: time.Second}
	defer c.CloseIdleConnections()
	req, _ := http.NewRequest(method, "http://"+a.listener.Addr().String()+path, nil)
	r, e := c.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer r.Body.Close()
	b, e := io.ReadAll(r.Body)
	if e != nil {
		t.Fatal(e)
	}
	return r.StatusCode, string(b)
}
func TestPrometheusStalenessAndRecovery(t *testing.T) {
	a := openProm(t, `{"listen":"127.0.0.1:0","path":"/stats","stale_after":"200ms"}`)
	if s, _ := scrapeProm(t, a, "/stats", "GET"); s != 503 {
		t.Fatal(s)
	}
	b := testBatch(a.id)
	if e := a.Publish(context.Background(), b); e != nil {
		t.Fatal(e)
	}
	if s, body := scrapeProm(t, a, "/stats", "GET"); s != 200 || strings.Contains(body, "boot_id") || strings.Count(body, "# TYPE objstore_node_ready gauge") != 1 {
		t.Fatal(s, body)
	}
	a.mu.Lock()
	a.published = time.Now().Add(-time.Second)
	a.mu.Unlock()
	if s, body := scrapeProm(t, a, "/stats", "GET"); s != 503 || strings.Contains(body, "objstore_") {
		t.Fatal(s, body)
	}
	if e := a.Publish(context.Background(), b); e != nil {
		t.Fatal(e)
	}
	if s, body := scrapeProm(t, a, "/stats", "HEAD"); s != 200 || body != "" {
		t.Fatal(s, body)
	}
	if s, _ := scrapeProm(t, a, "/metrics", "GET"); s != 404 {
		t.Fatal(s)
	}
	if s, _ := scrapeProm(t, a, "/stats", "POST"); s != 405 {
		t.Fatal(s)
	}
	for range cap(a.slots) {
		a.slots <- struct{}{}
	}
	if s, _ := scrapeProm(t, a, "/stats", "GET"); s != 503 {
		t.Fatal(s)
	}
	for range cap(a.slots) {
		<-a.slots
	}
	b.NodeID = "other"
	if a.Publish(context.Background(), b) == nil {
		t.Fatal("identity")
	}
}
func TestPrometheusConfigAndListener(t *testing.T) {
	for _, s := range []string{`{"unknown":1}`, `{"stale_after":"0s"}`, `{"stale_after":"bad"}`, `{"path":"relative"}`, `{"path":"/x?q=1"}`, `{"path":"/%78"}`, `{"listen":"bad"}`, `{} {}`} {
		_, e := newPrometheus(context.Background(), Identity{}, json.RawMessage(s))
		if !errors.Is(e, ErrConfig) {
			t.Fatal(s, e)
		}
	}
	a := openProm(t, `{"listen":"127.0.0.1:0"}`)
	raw, _ := json.Marshal(map[string]string{"listen": a.listener.Addr().String()})
	if _, e := newPrometheus(context.Background(), a.id, raw); e == nil || errors.Is(e, ErrConfig) {
		t.Fatal(e)
	}
	if a.server.ReadHeaderTimeout <= 0 || a.server.WriteTimeout <= 0 || a.server.MaxHeaderBytes > 8192 {
		t.Fatal("bounds")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 4 {
				_ = a.Publish(context.Background(), testBatch(a.id))
			}
		})
	}
	wg.Wait()
	_ = a.Shutdown(context.Background())
	if _, e := net.DialTimeout("tcp", a.listener.Addr().String(), time.Second); e == nil {
		t.Fatal("listener remains")
	}
}
func TestPrometheusShutdownDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// HTTP sockets use real I/O; graceful shutdown must also handle a canceled budget.
		a := openProm(t, `{"listen":"127.0.0.1:0"}`)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		e := a.Shutdown(ctx)
		if e != nil && !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	})
}

func TestPrometheusStalenessIntervals(t *testing.T) {
	for _, interval := range []string{"16s", "1h"} {
		_, err := Open(context.Background(), Config{Enabled: true, Adapter: "prometheus", Interval: interval, Settings: json.RawMessage(`{"listen":"127.0.0.1:0"}`)}, Identity{"n", "r", "b", 1})
		if !errors.Is(err, ErrConfig) {
			t.Fatal(interval, err)
		}
	}
	// Disabled settings validation never opens a listener.
	if a, err := Open(context.Background(), Config{Adapter: "prometheus", Settings: json.RawMessage(`{"unknown":true}`)}, Identity{}); a != nil || err != nil {
		t.Fatal(a, err)
	}
}
