package telemetry

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nickstrad/object-store/internal/metrics"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type otelSettings struct {
	Endpoint    string `json:"endpoint"`
	Insecure    bool   `json:"insecure"`
	CAFile      string `json:"ca_file"`
	HeadersFile string `json:"headers_file"`
}
type otelAdapter struct {
	client    *http.Client
	transport *http.Transport
	endpoint  string
	headers   map[string]string
	id        Identity
	mu        sync.Mutex
	closed    bool
	cancel    context.CancelFunc
	life      context.Context
	active    sync.WaitGroup
	warningAt time.Time
}

func newOTel(ctx context.Context, id Identity, raw json.RawMessage) (Adapter, error) {
	var s otelSettings
	if err := decodeStrict(raw, &s); err != nil {
		return nil, fmt.Errorf("%w: otel settings", ErrConfig)
	}
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Path != "/v1/metrics" || (u.Scheme != "https" && u.Scheme != "http") || (u.Scheme == "http" && !s.Insecure) {
		return nil, fmt.Errorf("%w: otel requires full /v1/metrics URL and explicit plaintext opt-in", ErrConfig)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if s.CAFile != "" {
		pem, err := os.ReadFile(s.CAFile)
		if err != nil {
			return nil, fmt.Errorf("%w: cannot read CA file", ErrConfig)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%w: invalid CA PEM", ErrConfig)
		}
		tlsConfig.RootCAs = pool
	}
	headers := map[string]string{}
	if s.HeadersFile != "" {
		b, err := os.ReadFile(s.HeadersFile)
		if err != nil {
			return nil, fmt.Errorf("%w: cannot read headers file", ErrConfig)
		}
		if len(b) > 65536 || decodeStrict(b, &headers) != nil || headers == nil {
			return nil, fmt.Errorf("%w: invalid headers file", ErrConfig)
		}
		for k, v := range headers {
			if !validHeader(k, v) || strings.EqualFold(k, "Content-Type") || strings.EqualFold(k, "Host") || strings.EqualFold(k, "Content-Length") {
				return nil, fmt.Errorf("%w: invalid export header", ErrConfig)
			}
		}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsConfig
	tr.MaxConnsPerHost = 1
	tr.MaxIdleConnsPerHost = 1
	tr.ResponseHeaderTimeout = 2 * time.Second
	life, cancel := context.WithCancel(context.Background())
	a := &otelAdapter{transport: tr, endpoint: s.Endpoint, headers: headers, id: id, life: life, cancel: cancel}
	a.client = &http.Client{Transport: tr, Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return a, nil
}
func validHeader(k, v string) bool {
	if k == "" {
		return false
	}
	for _, c := range k {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)) {
			return false
		}
	}
	for _, c := range v {
		if c == '\r' || c == '\n' || c == 0 || c == 127 || c < 32 && c != '\t' {
			return false
		}
	}
	return true
}

// OTLP JSON subset: https://opentelemetry.io/docs/specs/otlp/ (sums/gauges only).
type otelValue struct {
	StringValue string `json:"stringValue"`
}
type otelAttribute struct {
	Key   string    `json:"key"`
	Value otelValue `json:"value"`
}
type otelPoint struct {
	Attributes []otelAttribute `json:"attributes"`
	Start      string          `json:"startTimeUnixNano"`
	Time       string          `json:"timeUnixNano"`
	Int        *string         `json:"asInt,omitempty"`
	Double     *float64        `json:"asDouble,omitempty"`
}
type otelSum struct {
	Points      []otelPoint `json:"dataPoints"`
	Temporality int         `json:"aggregationTemporality"`
	Monotonic   bool        `json:"isMonotonic"`
}
type otelGauge struct {
	Points []otelPoint `json:"dataPoints"`
}
type otelMetric struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Unit        string     `json:"unit"`
	Sum         *otelSum   `json:"sum,omitempty"`
	Gauge       *otelGauge `json:"gauge,omitempty"`
}
type otelResource struct {
	Attributes []otelAttribute `json:"attributes"`
}
type otelScope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}
type otelScopeMetrics struct {
	Scope   otelScope    `json:"scope"`
	Metrics []otelMetric `json:"metrics"`
}
type otelResourceMetrics struct {
	Resource     otelResource       `json:"resource"`
	ScopeMetrics []otelScopeMetrics `json:"scopeMetrics"`
}
type otelRequest struct {
	ResourceMetrics []otelResourceMetrics `json:"resourceMetrics"`
}

func attr(k, v string) otelAttribute { return otelAttribute{k, otelValue{v}} }
func encodeOTel(b metrics.Batch) ([]byte, error) {
	desc := map[string]metrics.Descriptor{}
	for _, d := range metrics.Catalogue() {
		desc[d.Name] = d
	}
	families := map[string]*otelMetric{}
	for _, s := range b.Snapshots {
		for _, p := range s.Points {
			d := desc[p.Name]
			m := families[p.Name]
			if m == nil {
				m = &otelMetric{Name: d.Name, Description: d.Help, Unit: d.Unit}
				if d.Kind == metrics.Counter {
					m.Sum = &otelSum{Temporality: 2, Monotonic: true}
				} else {
					m.Gauge = &otelGauge{}
				}
				families[p.Name] = m
			}
			op := otelPoint{Attributes: []otelAttribute{attr("node_id", b.NodeID), attr("region", b.Region)}, Start: strconv.FormatInt(b.StartUnixNano, 10), Time: strconv.FormatInt(b.TimeUnixNano, 10)}
			labels := slices.Clone(p.Labels)
			slices.SortFunc(labels, func(a, b metrics.Label) int { return strings.Compare(a.Name, b.Name) })
			for _, l := range labels {
				op.Attributes = append(op.Attributes, attr(l.Name, l.Value))
			}
			v := p.Counter
			if d.Kind == metrics.Gauge {
				v = p.Gauge
			}
			if v <= math.MaxInt64 {
				s := strconv.FormatUint(v, 10)
				op.Int = &s
			} else {
				f := float64(v)
				op.Double = &f
			}
			if m.Sum != nil {
				m.Sum.Points = append(m.Sum.Points, op)
			} else {
				m.Gauge.Points = append(m.Gauge.Points, op)
			}
		}
	}
	names := make([]string, 0, len(families))
	for n := range families {
		names = append(names, n)
	}
	slices.Sort(names)
	ms := make([]otelMetric, 0, len(names))
	for _, n := range names {
		ms = append(ms, *families[n])
	}
	req := otelRequest{[]otelResourceMetrics{{Resource: otelResource{[]otelAttribute{attr("service.name", "objstore"), attr("service.instance.id", b.NodeID), attr("region", b.Region), attr("boot_id", b.BootID)}}, ScopeMetrics: []otelScopeMetrics{{Scope: otelScope{"objstore.metrics", "1"}, Metrics: ms}}}}}
	out, err := json.Marshal(req)
	if len(out) > metrics.MaxBatchBytes {
		return nil, errors.New("OTLP batch exceeds byte bound")
	}
	return out, err
}
func (a *otelAdapter) Publish(ctx context.Context, b metrics.Batch) error {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return ErrClosed
	}
	a.active.Add(1)
	a.mu.Unlock()
	defer a.active.Done()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	stop := context.AfterFunc(a.life, cancel)
	defer stop()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := metrics.Validate(b); err != nil {
		return err
	}
	if b.NodeID != a.id.NodeID || b.Region != a.id.Region || b.BootID != a.id.BootID || b.StartUnixNano != a.id.StartUnixNano {
		return errors.New("OTLP batch identity mismatch")
	}
	body, err := encodeOTel(b)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		retry, delay, err := a.send(ctx, body)
		if err == nil || !retry || attempt == 1 {
			return err
		}
		deadline, _ := ctx.Deadline()
		if time.Until(deadline) <= delay {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}
func (a *otelAdapter) send(ctx context.Context, body []byte) (bool, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return false, 0, errors.New("invalid export request")
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range a.headers {
		req.Header.Set(k, v)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, 0, ctx.Err()
		}
		return true, 100 * time.Millisecond, errors.New("OTLP transport failed")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil {
		return true, 100 * time.Millisecond, errors.New("OTLP response read failed")
	}
	if len(data) > 65536 {
		return false, 0, errors.New("OTLP response exceeds byte bound")
	}
	if resp.StatusCode != http.StatusOK {
		retry := resp.StatusCode == 429 || resp.StatusCode == 502 || resp.StatusCode == 503 || resp.StatusCode == 504
		delay := 100 * time.Millisecond
		if v := resp.Header.Get("Retry-After"); v != "" {
			if secs, e := strconv.ParseUint(v, 10, 32); e == nil {
				delay = time.Duration(secs) * time.Second
			} else if at, e := http.ParseTime(v); e == nil {
				delay = time.Until(at)
			}
			if delay < 100*time.Millisecond {
				delay = 100 * time.Millisecond
			}
		}
		return retry, delay, fmt.Errorf("OTLP HTTP status %d", resp.StatusCode)
	}
	var result struct {
		Partial *struct {
			Rejected json.RawMessage `json:"rejectedDataPoints"`
			Message  string          `json:"errorMessage"`
		} `json:"partialSuccess"`
	}
	// Unknown response fields are permitted by the OTLP forward-compatibility rule.
	dec := json.NewDecoder(bytes.NewReader(data))
	if len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' || dec.Decode(&result) != nil {
		return false, 0, errors.New("malformed OTLP success response")
	}
	var trailing any
	if dec.Decode(&trailing) != io.EOF {
		return false, 0, errors.New("malformed OTLP success response")
	}
	if result.Partial != nil {
		rejected := uint64(0)
		if raw := result.Partial.Rejected; len(raw) > 0 {
			s := string(raw)
			if raw[0] == '"' {
				if json.Unmarshal(raw, &s) != nil {
					return false, 0, errors.New("invalid rejected count")
				}
			}
			var e error
			rejected, e = strconv.ParseUint(s, 10, 64)
			if e != nil {
				return false, 0, errors.New("invalid rejected count")
			}
		}
		if rejected > 0 {
			return false, 0, ErrPartial
		}
		if result.Partial.Message != "" {
			a.mu.Lock()
			if time.Since(a.warningAt) >= time.Minute {
				log.Print("observability OTLP receiver warning (zero rejected points; remote text omitted)")
				a.warningAt = time.Now()
			}
			a.mu.Unlock()
		}
	}
	return false, 0, nil
}
func (a *otelAdapter) Shutdown(ctx context.Context) error {
	a.mu.Lock()
	a.closed = true
	a.cancel()
	a.mu.Unlock()
	done := make(chan struct{})
	go func() { a.active.Wait(); close(done) }()
	select {
	case <-done:
		a.transport.CloseIdleConnections()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
