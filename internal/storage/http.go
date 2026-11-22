package storage

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

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
)

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

type Limits struct {
	MaxObjectSize     int64
	MaxSmallBody      int64
	MaxHeaderBytes    int
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	RequestTimeout    time.Duration
}

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

type StatusRow struct {
	Err    error
	Status int
}

func StatusTable(extra ...StatusRow) []StatusRow {
	rows := []StatusRow{
		{ErrMethod, http.StatusMethodNotAllowed},
		{env.ErrTooLarge, http.StatusRequestEntityTooLarge},
		{ErrNoSuchBucket, http.StatusNotFound},
		{ErrNotFound, http.StatusNotFound},
		{occ.ErrNotFound, http.StatusNotFound},
		{env.ErrNotFound, http.StatusNotFound},
		{saga.ErrSagaNotFound, http.StatusNotFound},
		{ErrBucketExists, http.StatusConflict},
		{ErrExists, http.StatusConflict},
		{occ.ErrPreconditionFailed, http.StatusPreconditionFailed},
		{occ.ErrQuotaExceeded, http.StatusInsufficientStorage},
		{ErrInvalid, http.StatusBadRequest},
		{env.ErrInvalidArgument, http.StatusBadRequest},
		{occ.ErrMalformed, http.StatusBadRequest},
		{saga.ErrPermanent, http.StatusBadRequest},
		{ErrBusy, http.StatusServiceUnavailable},
		{ErrUnavailable, http.StatusServiceUnavailable},
		{occ.ErrNotReady, http.StatusServiceUnavailable},
		{env.ErrClosed, http.StatusServiceUnavailable},
		{env.ErrUnreachable, http.StatusServiceUnavailable},
		{env.ErrTimeout, http.StatusGatewayTimeout},
		{context.DeadlineExceeded, http.StatusGatewayTimeout},
		{context.Canceled, http.StatusServiceUnavailable},
		{ErrCorrupt, http.StatusInternalServerError},
	}
	return append(extra, rows...)
}

func StatusOf(rows []StatusRow, err error) int {
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
	table []StatusRow
}

func NewHandler(svc Service, l Limits, extra ...StatusRow) http.Handler {
	return &handler{svc: svc, l: l.WithDefaults(), table: StatusTable(extra...)}
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

func objectHeaders(w http.ResponseWriter, oi ObjectInfo) {
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

func parseEntityTags(v string) ([]string, bool, error) {
	var strong []string
	star := false
	elements := 0
	for _, el := range strings.Split(v, ",") {
		el = strings.Trim(el, " \t")
		if el == "" {
			continue
		}
		elements++
		if el == "*" {
			star = true
			continue
		}
		weak := strings.HasPrefix(el, "W/")
		tag := strings.TrimPrefix(el, "W/")
		if len(tag) < 2 || tag[0] != '"' || tag[len(tag)-1] != '"' {
			return nil, false, fmt.Errorf("storage: entity-tag %q is not quoted: %w", el, ErrInvalid)
		}
		opaque := tag[1 : len(tag)-1]
		for i := 0; i < len(opaque); i++ {
			if c := opaque[i]; c == '"' || c < 0x21 || c == 0x7f {
				return nil, false, fmt.Errorf("storage: entity-tag %q: %w", el, ErrInvalid)
			}
		}
		if !weak {
			strong = append(strong, opaque)
		}
	}
	if star && elements > 1 {
		return nil, false, fmt.Errorf("storage: * inside an entity-tag list %q: %w", v, ErrInvalid)
	}
	return strong, star, nil
}

func condition(r *http.Request) (Condition, error) {
	var c Condition
	if vs, ok := r.Header["If-None-Match"]; ok {
		v := strings.Join(vs, ",")
		if strings.TrimSpace(v) != "*" {
			return c, fmt.Errorf("storage: If-None-Match %q: only * is supported: %w", v, ErrInvalid)
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
		return c, fmt.Errorf("storage: both If-Match and If-None-Match: %w", ErrInvalid)
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
		return nil, fmt.Errorf("storage: read body: %v: %w", err, ErrInvalid)
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
		h.fail(w, fmt.Errorf("storage: no bucket in path: %w", ErrInvalid))
	default:
		bucket, key, hasKey := strings.Cut(path, "/")
		if err := ValidBucket(bucket); err != nil {
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
		h.fail(w, ErrMethod)
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
		h.fail(w, ErrMethod)
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
			h.fail(w, fmt.Errorf("storage: migration request: %v: %w", err, ErrInvalid))
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
		h.fail(w, ErrMethod)
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
		return 0, fmt.Errorf("storage: parameter %s=%q: %w", name, v, ErrInvalid)
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
			h.fail(w, fmt.Errorf("storage: quota request: %v: %w", err, ErrInvalid))
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
		if err := h.svc.CreateBucket(ctx, BucketSpec{Name: bucket, Region: q.Get("region"), QuotaBytes: qb, QuotaObjects: qo}); err != nil {
			h.fail(w, err)
			return
		}
		h.json(w, http.StatusOK, BucketSpec{Name: bucket, Region: q.Get("region"), QuotaBytes: qb, QuotaObjects: qo})
	case r.Method == http.MethodGet:
		limit, err := uintParam(q, "limit")
		if err != nil || limit > MaxListLimit {
			h.fail(w, fmt.Errorf("storage: list limit: %w", ErrInvalid))
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
			objs = []ObjectInfo{}
		}
		h.json(w, http.StatusOK, objs)
	default:
		h.fail(w, ErrMethod)
	}
}

func (h *handler) object(ctx context.Context, w http.ResponseWriter, r *http.Request, bucket, key string) {
	if key == "" || len(key) > MaxKeyLen {
		h.fail(w, fmt.Errorf("storage: object key length %d: %w", len(key), ErrInvalid))
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
			ts = []TagInfo{}
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
			h.fail(w, fmt.Errorf("storage: content type of %d bytes: %w", len(ct), ErrInvalid))
			return
		}
		oi, err := h.svc.PutObject(ctx, PutRequest{Bucket: bucket, Key: key, Data: b, ContentType: ct, Cond: c})
		if err != nil {
			h.fail(w, err)
			return
		}
		objectHeaders(w, oi)
		h.json(w, http.StatusOK, oi)
	case r.Method == http.MethodGet || r.Method == http.MethodHead:
		var (
			oi   ObjectInfo
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
		h.fail(w, ErrMethod)
	}
}

type Server struct {
	srv *http.Server
	mu  sync.Mutex
	ln  net.Listener
	wg  sync.WaitGroup
	err error
}

func NewServer(addr string, svc Service, l Limits, extra ...StatusRow) *Server {
	l = l.WithDefaults()
	return &Server{srv: &http.Server{
		Addr: addr, Handler: NewHandler(svc, l, extra...), MaxHeaderBytes: l.MaxHeaderBytes,
		ReadHeaderTimeout: l.ReadHeaderTimeout, ReadTimeout: l.ReadTimeout, WriteTimeout: l.WriteTimeout, IdleTimeout: l.IdleTimeout,
	}}
}

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

func (s *Server) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

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
