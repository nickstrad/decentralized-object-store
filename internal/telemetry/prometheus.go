package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickstrad/object-store/internal/metrics"
)

type prometheusSettings struct {
	Listen     string `json:"listen"`
	Path       string `json:"path"`
	StaleAfter string `json:"stale_after"`
}
type prometheusAdapter struct {
	mu        sync.RWMutex
	id        Identity
	body      string
	published time.Time
	stale     time.Duration
	closed    bool
	server    *http.Server
	listener  net.Listener
	done      chan struct{}
	slots     chan struct{}
}

func validatePrometheusSettings(interval time.Duration, raw json.RawMessage) error {
	s := prometheusSettings{StaleAfter: "30s"}
	if err := decodeStrict(raw, &s); err != nil {
		return fmt.Errorf("%w: prometheus settings: %v", ErrConfig, err)
	}
	stale, err := time.ParseDuration(s.StaleAfter)
	if err != nil || stale < 2*interval {
		return fmt.Errorf("%w: stale_after must be at least two collection intervals", ErrConfig)
	}
	return nil
}
func newPrometheus(ctx context.Context, id Identity, raw json.RawMessage) (Adapter, error) {
	s := prometheusSettings{Listen: "127.0.0.1:9464", Path: "/metrics", StaleAfter: "30s"}
	if err := decodeStrict(raw, &s); err != nil {
		return nil, fmt.Errorf("%w: prometheus settings: %v", ErrConfig, err)
	}
	stale, err := time.ParseDuration(s.StaleAfter)
	u, e := url.Parse(s.Path)
	if err != nil || stale < 200*time.Millisecond || stale > 2*time.Hour || e != nil || u.Path != s.Path || !strings.HasPrefix(s.Path, "/") || strings.ContainsAny(s.Path, "\r\n?#") {
		return nil, fmt.Errorf("%w: prometheus path or staleness", ErrConfig)
	}
	if _, _, e = net.SplitHostPort(s.Listen); e != nil {
		return nil, fmt.Errorf("%w: prometheus listen", ErrConfig)
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.Listen)
	if err != nil {
		return nil, err
	}
	a := &prometheusAdapter{id: id, stale: stale, listener: ln, done: make(chan struct{}), slots: make(chan struct{}, 8)}
	a.server = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != s.Path {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method", 405)
			return
		}
		select {
		case a.slots <- struct{}{}:
			defer func() { <-a.slots }()
		default:
			http.Error(w, "busy", 503)
			return
		}
		a.mu.RLock()
		body, at, closed := a.body, a.published, a.closed
		a.mu.RUnlock()
		if closed || at.IsZero() || time.Since(at) >= a.stale {
			http.Error(w, "metrics unavailable", 503)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(body))
		}
	}), ReadTimeout: 2 * time.Second, ReadHeaderTimeout: time.Second, WriteTimeout: 2 * time.Second, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 8192}
	go func() { defer close(a.done); _ = a.server.Serve(ln) }()
	return a, nil
}
func prometheusEscape(s string) string {
	return strings.NewReplacer("\\", "\\\\", "\n", "\\n", "\"", "\\\"").Replace(s)
}
func encodePrometheus(b metrics.Batch) (string, error) {
	families := map[string][]metrics.Point{}
	for _, s := range b.Snapshots {
		for _, p := range s.Points {
			families[p.Name] = append(families[p.Name], p)
		}
	}
	var out strings.Builder
	for _, d := range metrics.Catalogue() {
		ps := families[d.Name]
		if len(ps) == 0 {
			continue
		}
		kind := "counter"
		if d.Kind == metrics.Gauge {
			kind = "gauge"
		}
		fmt.Fprintf(&out, "# HELP %s %s\n# TYPE %s %s\n", d.Name, strings.NewReplacer("\\", "\\\\", "\n", "\\n").Replace(d.Help), d.Name, kind)
		rows := make([]string, 0, len(ps))
		for _, p := range ps {
			labels := append([]metrics.Label{{Name: "node_id", Value: b.NodeID}, {Name: "region", Value: b.Region}}, p.Labels...)
			slices.SortFunc(labels, func(a, b metrics.Label) int { return strings.Compare(a.Name, b.Name) })
			var row strings.Builder
			row.WriteString(p.Name + "{")
			for i, l := range labels {
				if i > 0 {
					row.WriteByte(',')
				}
				fmt.Fprintf(&row, "%s=\"%s\"", l.Name, prometheusEscape(l.Value))
			}
			v := p.Counter
			if d.Kind == metrics.Gauge {
				v = p.Gauge
			}
			fmt.Fprintf(&row, "} %d\n", v)
			rows = append(rows, row.String())
		}
		slices.Sort(rows)
		for _, row := range rows {
			out.WriteString(row)
		}
	}
	if out.Len() > metrics.MaxBatchBytes {
		return "", errors.New("Prometheus exposition exceeds byte bound")
	}
	return out.String(), nil
}
func (a *prometheusAdapter) Publish(ctx context.Context, b metrics.Batch) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := metrics.Validate(b); err != nil {
		return err
	}
	if b.NodeID != a.id.NodeID || b.Region != a.id.Region || b.BootID != a.id.BootID || b.StartUnixNano != a.id.StartUnixNano {
		return errors.New("Prometheus batch identity mismatch")
	}
	body, err := encodePrometheus(b)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	a.body = body
	a.published = time.Now()
	return nil
}
func (a *prometheusAdapter) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	a.closed = true
	a.body = ""
	a.mu.Unlock()
	err := a.server.Shutdown(ctx)
	if err != nil {
		_ = a.server.Close()
	}
	select {
	case <-a.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
