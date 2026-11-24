// Package httpapi serves the unauthenticated HTTP object, bucket, metadata,
// migration, membership, and health API over a Service implementation.
//
// Objects are buffered whole in memory up to Limits.MaxObjectSize (no
// per-stripe streaming). Every present If-Match or If-None-Match header is
// strictly parsed according to RFC 9110 and never degrades to an unconditional
// write. Errors are mapped to HTTP status codes through one table, StatusTable.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/occ"
	"github.com/nickstrad/object-store/internal/saga"
	"github.com/nickstrad/object-store/internal/storage"
)

// Default HTTP server bounds, list cap, and custom response header names.
const (
	DefaultMaxObjectSize      = 32 << 20
	DefaultMaxSmallBody       = 64 << 10
	DefaultMaxHeaderBytes     = 16 << 10
	DefaultReadHeaderTimeout  = 5 * time.Second
	DefaultReadTimeout        = 60 * time.Second
	DefaultWriteTimeout       = 60 * time.Second
	DefaultIdleTimeout        = 60 * time.Second
	DefaultHTTPRequestTimeout = 30 * time.Second
	MaxListLimit              = 10000
	HeaderVersion             = "X-Object-Version"
	HeaderContentSHA          = "X-Content-Sha256"
)

// Limits bounds request sizes, headers, and timeouts on the HTTP handler and
// server. Zero fields are replaced by Default* values via WithDefaults.
type Limits struct {
	MetricsEnabled    bool
	MaxObjectSize     int64
	MaxSmallBody      int64
	MaxHeaderBytes    int
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	RequestTimeout    time.Duration
}

// WithDefaults returns a copy of l with any zero field filled from the
// Default* constants.
func (l Limits) WithDefaults() Limits {
	if l.MaxObjectSize == 0 {
		l.MaxObjectSize = DefaultMaxObjectSize
	}
	if l.MaxSmallBody == 0 {
		l.MaxSmallBody = DefaultMaxSmallBody
	}
	if l.MaxHeaderBytes == 0 {
		l.MaxHeaderBytes = DefaultMaxHeaderBytes
	}
	if l.ReadHeaderTimeout == 0 {
		l.ReadHeaderTimeout = DefaultReadHeaderTimeout
	}
	if l.ReadTimeout == 0 {
		l.ReadTimeout = DefaultReadTimeout
	}
	if l.WriteTimeout == 0 {
		l.WriteTimeout = DefaultWriteTimeout
	}
	if l.IdleTimeout == 0 {
		l.IdleTimeout = DefaultIdleTimeout
	}
	if l.RequestTimeout == 0 {
		l.RequestTimeout = DefaultHTTPRequestTimeout
	}
	return l
}

// StatusTable returns the ordered sentinel-to-HTTP-status table, prepending
// any caller-supplied extra rows so they take precedence over the defaults.
func StatusTable(extra ...storage.StatusRow) []storage.StatusRow {
	rows := []storage.StatusRow{
		{Err: storage.ErrMethod, Status: http.StatusMethodNotAllowed},
		{Err: env.ErrTooLarge, Status: http.StatusRequestEntityTooLarge},
		{Err: storage.ErrNoSuchBucket, Status: http.StatusNotFound},
		{Err: storage.ErrBusy, Status: http.StatusServiceUnavailable},
		{Err: storage.ErrUnavailable, Status: http.StatusServiceUnavailable},
		{Err: storage.ErrNotFound, Status: http.StatusNotFound},
		{Err: occ.ErrNotFound, Status: http.StatusNotFound},
		{Err: env.ErrNotFound, Status: http.StatusNotFound},
		{Err: saga.ErrSagaNotFound, Status: http.StatusNotFound},
		{Err: storage.ErrBucketExists, Status: http.StatusConflict},
		{Err: storage.ErrExists, Status: http.StatusConflict},
		{Err: occ.ErrPreconditionFailed, Status: http.StatusPreconditionFailed},
		{Err: occ.ErrQuotaExceeded, Status: http.StatusInsufficientStorage},
		{Err: storage.ErrInvalid, Status: http.StatusBadRequest},
		{Err: env.ErrInvalidArgument, Status: http.StatusBadRequest},
		{Err: occ.ErrMalformed, Status: http.StatusBadRequest},
		{Err: saga.ErrPermanent, Status: http.StatusBadRequest},
		{Err: occ.ErrNotReady, Status: http.StatusServiceUnavailable},
		{Err: env.ErrClosed, Status: http.StatusServiceUnavailable},
		{Err: env.ErrUnreachable, Status: http.StatusServiceUnavailable},
		{Err: env.ErrTimeout, Status: http.StatusGatewayTimeout},
		{Err: context.DeadlineExceeded, Status: http.StatusGatewayTimeout},
		{Err: context.Canceled, Status: http.StatusServiceUnavailable},
		{Err: storage.ErrCorrupt, Status: http.StatusInternalServerError},
	}
	return append(extra, rows...)
}

// StatusOf returns the HTTP status code for the first row in rows matching err
// via errors.Is, 200 for a nil error, or 500 when no row matches.
func StatusOf(rows []storage.StatusRow, err error) int {
	if err == nil {
		return http.StatusOK
	}
	for _, r := range rows {
		if errors.Is(err, r.Err) {
			return r.Status
		}
	}
	return http.StatusInternalServerError
}

type handler struct {
	svc   Service
	l     Limits
	table []storage.StatusRow
}

// NewHandler returns an http.Handler routing the object-store HTTP API to svc
// with the given size/timeout limits and optional extra error-status rows.
func NewHandler(svc Service, l Limits, extra ...storage.StatusRow) http.Handler {
	h := &handler{svc: svc, l: l.WithDefaults(), table: StatusTable(extra...)}
	if l.MetricsEnabled {
		return &meteredHandler{next: h}
	}
	return h
}

type errorBody struct {
	Error  string `json:"error"`
	Status int    `json:"status"`
}

func (h *handler) fail(w http.ResponseWriter, err error) {
	status := StatusOf(h.table, err)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: err.Error(), Status: status})
}

func (h *handler) json(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func objectHeaders(w http.ResponseWriter, oi storage.ObjectInfo) {
	if oi.ETag != "" {
		w.Header().Set("ETag", `"`+oi.ETag+`"`)
	}
	if oi.Version != "" {
		w.Header().Set(HeaderVersion, oi.Version)
	}
	if oi.ContentSHA != "" {
		w.Header().Set(HeaderContentSHA, oi.ContentSHA)
	}
}

func skipOWS(v string, i int) int {
	for i < len(v) && (v[i] == ' ' || v[i] == '\t') {
		i++
	}
	return i
}

// parseEntityTags scans an RFC 9110 entity-tag list left to right in one pass
// so commas inside quoted opaque-tags are preserved. Empty list elements are
// ignored, weak tags (W/"...") are validated and omitted from the strong list,
// and "*" is rejected if combined with any other non-empty element.
func parseEntityTags(v string) ([]string, bool, error) {
	var strong []string
	star := false
	elements := 0
	for i := skipOWS(v, 0); i < len(v); i = skipOWS(v, i) {
		if v[i] == ',' {
			i++
			continue
		}
		start := i
		elements++
		if v[i] == '*' {
			star = true
			i++
		} else {
			weak := strings.HasPrefix(v[i:], "W/")
			if weak {
				i += 2
			}
			if i == len(v) || v[i] != '"' {
				return nil, false, fmt.Errorf("storage: entity-tag at %q is not quoted: %w", v[start:], storage.ErrInvalid)
			}
			i++
			open := i
			for i < len(v) && v[i] != '"' {
				if c := v[i]; c < 0x21 || c == 0x7f {
					return nil, false, fmt.Errorf("storage: entity-tag at %q: byte %#x outside etagc: %w", v[start:], c, storage.ErrInvalid)
				}
				i++
			}
			if i == len(v) {
				return nil, false, fmt.Errorf("storage: entity-tag at %q is not terminated: %w", v[start:], storage.ErrInvalid)
			}
			if !weak {
				strong = append(strong, v[open:i])
			}
			i++
		}
		i = skipOWS(v, i)
		if i < len(v) && v[i] != ',' {
			return nil, false, fmt.Errorf("storage: entity-tag list %q: unexpected %q after an element: %w", v, v[i], storage.ErrInvalid)
		}
	}
	if star && elements > 1 {
		return nil, false, fmt.Errorf("storage: * inside an entity-tag list %q: %w", v, storage.ErrInvalid)
	}
	return strong, star, nil
}

// condition parses If-Match and If-None-Match headers into a storage.Condition.
// Only If-None-Match: * is supported; sending both headers or malformed tags
// returns storage.ErrInvalid. Any present If-Match sets IfMatchList or "*" so
// it never degrades to an unconditional write.
func condition(r *http.Request) (storage.Condition, error) {
	var c storage.Condition
	if vs, ok := r.Header["If-None-Match"]; ok {
		v := strings.Join(vs, ",")
		if strings.TrimSpace(v) != "*" {
			return c, fmt.Errorf("storage: If-None-Match %q: only * is supported: %w", v, storage.ErrInvalid)
		}
		c.IfNoneMatch = true
	}
	if vs, ok := r.Header["If-Match"]; ok {
		strong, star, err := parseEntityTags(strings.Join(vs, ","))
		if err != nil {
			return c, err
		}
		if star {
			c.IfMatch = "*"
		} else {
			c.IfMatchList, c.IfMatchAny = true, strong
		}
	}
	if c.IfNoneMatch && (c.IfMatch != "" || c.IfMatchList) {
		return c, fmt.Errorf("storage: both If-Match and If-None-Match: %w", storage.ErrInvalid)
	}
	return c, nil
}

func (h *handler) body(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, error) {
	if r.ContentLength > limit {
		return nil, fmt.Errorf("storage: body of %d bytes over %d: %w", r.ContentLength, limit, env.ErrTooLarge)
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, fmt.Errorf("storage: body over %d bytes: %w", limit, env.ErrTooLarge)
		}
		return nil, fmt.Errorf("storage: read body: %v: %w", err, storage.ErrInvalid)
	}
	return b, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), h.l.RequestTimeout)
	defer cancel()
	path := strings.TrimPrefix(r.URL.Path, "/")
	switch {
	case path == "_health":
		h.health(ctx, w, r)
	case path == "_cluster/members":
		h.members(ctx, w, r)
	case path == "_migrations" || strings.HasPrefix(path, "_migrations/"):
		h.migrations(ctx, w, r, strings.TrimPrefix(strings.TrimPrefix(path, "_migrations"), "/"))
	case path == "":
		h.fail(w, fmt.Errorf("storage: no bucket in path: %w", storage.ErrInvalid))
	default:
		bucket, key, hasKey := strings.Cut(path, "/")
		if err := storage.ValidBucket(bucket); err != nil {
			h.fail(w, err)
			return
		}
		if hasKey {
			h.object(ctx, w, r, bucket, key)
		} else {
			h.bucket(ctx, w, r, bucket)
		}
	}
}

func (h *handler) health(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.fail(w, storage.ErrMethod)
		return
	}
	hl, err := h.svc.Health(ctx)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.json(w, http.StatusOK, hl)
}

func (h *handler) members(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.fail(w, storage.ErrMethod)
		return
	}
	ms, err := h.svc.Members(ctx)
	if err != nil {
		h.fail(w, err)
		return
	}
	h.json(w, http.StatusOK, ms)
}

type migrationRequest struct {
	Bucket string `json:"bucket"`
	To     string `json:"to"`
}

func (h *handler) migrations(ctx context.Context, w http.ResponseWriter, r *http.Request, id string) {
	switch {
	case id == "" && r.Method == http.MethodPost:
		b, err := h.body(w, r, h.l.MaxSmallBody)
		if err != nil {
			h.fail(w, err)
			return
		}
		var req migrationRequest
		if err := json.Unmarshal(b, &req); err != nil {
			h.fail(w, fmt.Errorf("storage: migration request: %v: %w", err, storage.ErrInvalid))
			return
		}
		mi, err := h.svc.StartMigration(ctx, req.Bucket, req.To)
		if err != nil {
			h.fail(w, err)
			return
		}
		h.json(w, http.StatusAccepted, mi)
	case id != "" && r.Method == http.MethodGet:
		mi, err := h.svc.Migration(ctx, id)
		if err != nil {
			h.fail(w, err)
			return
		}
		h.json(w, http.StatusOK, mi)
	default:
		h.fail(w, storage.ErrMethod)
	}
}

func uintParam(q map[string][]string, name string) (uint64, error) {
	v := ""
	if vs := q[name]; len(vs) > 0 {
		v = vs[0]
	}
	if v == "" {
		return 0, nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("storage: parameter %s=%q: %w", name, v, storage.ErrInvalid)
	}
	return n, nil
}

type quotaRequest struct {
	Bytes   uint64 `json:"bytes"`
	Objects uint64 `json:"objects"`
}

func (h *handler) bucket(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket string) {
	q := r.URL.Query()
	_, quota := q["quota"]
	principal, acl := q.Get("acl"), q.Has("acl")
	switch {
	case r.Method == http.MethodPut && quota:
		b, err := h.body(w, r, h.l.MaxSmallBody)
		if err != nil {
			h.fail(w, err)
			return
		}
		var req quotaRequest
		if err := json.Unmarshal(b, &req); err != nil {
			h.fail(w, fmt.Errorf("storage: quota request: %v: %w", err, storage.ErrInvalid))
			return
		}
		qi, err := h.svc.SetQuota(ctx, bucket, req.Bytes, req.Objects)
		if err != nil {
			h.fail(w, err)
			return
		}
		h.json(w, http.StatusOK, qi)
	case r.Method == http.MethodGet && quota:
		qi, err := h.svc.Quota(ctx, bucket)
		if err != nil {
			h.fail(w, err)
			return
		}
		h.json(w, http.StatusOK, qi)
	case r.Method == http.MethodPut && acl:
		b, err := h.body(w, r, h.l.MaxSmallBody)
		if err != nil {
			h.fail(w, err)
			return
		}
		c, err := condition(r)
		if err != nil {
			h.fail(w, err)
			return
		}
		ai, err := h.svc.PutACL(ctx, bucket, principal, strings.TrimSpace(string(b)), c)
		if err != nil {
			h.fail(w, err)
			return
		}
		w.Header().Set("ETag", `"`+ai.ETag+`"`)
		h.json(w, http.StatusOK, ai)
	case r.Method == http.MethodGet && acl:
		ai, err := h.svc.ACL(ctx, bucket, principal)
		if err != nil {
			h.fail(w, err)
			return
		}
		w.Header().Set("ETag", `"`+ai.ETag+`"`)
		h.json(w, http.StatusOK, ai)
	case r.Method == http.MethodPut:
		if _, err := h.body(w, r, h.l.MaxSmallBody); err != nil {
			h.fail(w, err)
			return
		}
		qb, err := uintParam(q, "quota-bytes")
		if err != nil {
			h.fail(w, err)
			return
		}
		qo, err := uintParam(q, "quota-objects")
		if err != nil {
			h.fail(w, err)
			return
		}
		if err := h.svc.CreateBucket(ctx, storage.BucketSpec{Name: bucket, Region: q.Get("region"), QuotaBytes: qb, QuotaObjects: qo}); err != nil {
			h.fail(w, err)
			return
		}
		h.json(w, http.StatusOK, storage.BucketSpec{Name: bucket, Region: q.Get("region"), QuotaBytes: qb, QuotaObjects: qo})
	case r.Method == http.MethodGet:
		limit, err := uintParam(q, "limit")
		if err != nil || limit > MaxListLimit {
			h.fail(w, fmt.Errorf("storage: list limit: %w", storage.ErrInvalid))
			return
		}
		if limit == 0 {
			limit = MaxListLimit
		}
		objs, err := h.svc.ListObjects(ctx, bucket, q.Get("prefix"), int(limit))
		if err != nil {
			h.fail(w, err)
			return
		}
		if objs == nil {
			objs = []storage.ObjectInfo{}
		}
		h.json(w, http.StatusOK, objs)
	default:
		h.fail(w, storage.ErrMethod)
	}
}

func (h *handler) object(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket, key string) {
	if key == "" || len(key) > storage.MaxKeyLen {
		h.fail(w, fmt.Errorf("storage: object key length %d: %w", len(key), storage.ErrInvalid))
		return
	}
	q := r.URL.Query()
	switch {
	case r.Method == http.MethodPut && q.Has("tag"):
		b, err := h.body(w, r, h.l.MaxSmallBody)
		if err != nil {
			h.fail(w, err)
			return
		}
		c, err := condition(r)
		if err != nil {
			h.fail(w, err)
			return
		}
		ti, err := h.svc.PutTag(ctx, bucket, key, q.Get("tag"), string(b), c)
		if err != nil {
			h.fail(w, err)
			return
		}
		w.Header().Set("ETag", `"`+ti.ETag+`"`)
		h.json(w, http.StatusOK, ti)
	case r.Method == http.MethodGet && q.Has("tags"):
		ts, err := h.svc.Tags(ctx, bucket, key)
		if err != nil {
			h.fail(w, err)
			return
		}
		if ts == nil {
			ts = []storage.TagInfo{}
		}
		h.json(w, http.StatusOK, ts)
	case r.Method == http.MethodGet && q.Has("meta"):
		oi, err := h.svc.HeadObject(ctx, bucket, key)
		if err != nil {
			h.fail(w, err)
			return
		}
		objectHeaders(w, oi)
		h.json(w, http.StatusOK, oi)
	case r.Method == http.MethodPut:
		c, err := condition(r)
		if err != nil {
			h.fail(w, err)
			return
		}
		b, err := h.body(w, r, h.l.MaxObjectSize)
		if err != nil {
			h.fail(w, err)
			return
		}
		ct := r.Header.Get("Content-Type")
		if len(ct) > 255 {
			h.fail(w, fmt.Errorf("storage: content type of %d bytes: %w", len(ct), storage.ErrInvalid))
			return
		}
		oi, err := h.svc.PutObject(ctx, storage.PutRequest{Bucket: bucket, Key: key, Data: b, ContentType: ct, Cond: c})
		if err != nil {
			h.fail(w, err)
			return
		}
		objectHeaders(w, oi)
		h.json(w, http.StatusOK, oi)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		var (
			oi   storage.ObjectInfo
			data []byte
			err  error
		)
		if r.Method == http.MethodHead {
			oi, err = h.svc.HeadObject(ctx, bucket, key)
		} else {
			oi, data, err = h.svc.GetObject(ctx, bucket, key)
		}
		if err != nil {
			h.fail(w, err)
			return
		}
		objectHeaders(w, oi)
		ct := oi.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Content-Length", strconv.FormatInt(oi.Size, 10))
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
	case r.Method == http.MethodDelete:
		c, err := condition(r)
		if err != nil {
			h.fail(w, err)
			return
		}
		oi, err := h.svc.DeleteObject(ctx, bucket, key, c)
		if err != nil {
			h.fail(w, err)
			return
		}
		objectHeaders(w, oi)
		w.WriteHeader(http.StatusNoContent)
	default:
		h.fail(w, storage.ErrMethod)
	}
}

// Server wraps an http.Server with bounded listen, serve, and graceful
// shutdown lifecycle management.
type Server struct {
	meter *meteredHandler
	srv   *http.Server
	mu    sync.Mutex
	ln    net.Listener
	wg    sync.WaitGroup
	err   error
}

// NewServer constructs an unstarted HTTP Server bound to addr with Limits l
// (filled via WithDefaults) and optional extra status rows.
func NewServer(addr string, svc Service, l Limits, extra ...storage.StatusRow) *Server {
	l = l.WithDefaults()
	handler := NewHandler(svc, l, extra...)
	meter, _ := handler.(*meteredHandler)
	return &Server{meter: meter, srv: &http.Server{
		Addr: addr, Handler: handler, MaxHeaderBytes: l.MaxHeaderBytes,
		ReadHeaderTimeout: l.ReadHeaderTimeout, ReadTimeout: l.ReadTimeout, WriteTimeout: l.WriteTimeout, IdleTimeout: l.IdleTimeout,
	}}
}

// Start binds the TCP listener on s.srv.Addr and begins serving requests in a
// tracked goroutine. It returns an error if listening fails.
func (s *Server) Start(ctx context.Context) error {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", s.srv.Addr)
	if err != nil {
		return fmt.Errorf("storage: http listen %s: %w", s.srv.Addr, err)
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()
	s.wg.Go(func() {
		if err := s.srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.mu.Lock()
			s.err = err
			s.mu.Unlock()
		}
	})
	return nil
}

// Addr returns the bound listener address, or "" before Start succeeds.
func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Shutdown gracefully drains active HTTP requests within ctx, forcefully
// closes the server if ctx expires, waits for the serving goroutine to exit,
// and returns any shutdown or serve error.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.srv.Shutdown(ctx)
	if err != nil {
		err = errors.Join(err, s.srv.Close())
	}
	s.wg.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	return errors.Join(err, s.err)
}
