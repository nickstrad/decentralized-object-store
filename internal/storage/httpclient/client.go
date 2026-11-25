// Package httpclient is the HTTP transport adapter for the object-store API:
// Client implements httpapi.Service by calling a node's HTTP endpoint, taking
// and returning the storage entity types unchanged.
//
// Requests are built so the server receives exactly the caller's arguments.
// Object keys are percent-encoded byte for byte, so keys containing "/", "?",
// "#", "%", spaces, non-ASCII bytes, "." or ".." segments and trailing slashes
// arrive unchanged. Inputs the wire cannot carry are rejected locally with
// storage.ErrInvalid (or env.ErrTooLarge for oversized bodies) before anything
// is sent; the method comments state each rule. Response bodies are read up
// to fixed bounds, and every call honours its context's deadline.
package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nickstrad/object-store/internal/env"
	"github.com/nickstrad/object-store/internal/storage"
	"github.com/nickstrad/object-store/internal/storage/httpapi"
)

// DefaultMaxResponse bounds a JSON response body (a full listing of
// httpapi.MaxListLimit objects with long keys fits well inside it).
const DefaultMaxResponse = 64 << 20

// Options configures a Client. Zero fields take defaults.
type Options struct {
	// Transport sends requests; nil uses http.DefaultTransport.
	Transport http.RoundTripper
	// MaxObjectSize bounds PUT and GET object bodies (httpapi.DefaultMaxObjectSize).
	MaxObjectSize int64
	// MaxSmallBody bounds tag, ACL, quota and migration request bodies and
	// error response bodies (httpapi.DefaultMaxSmallBody).
	MaxSmallBody int64
	// MaxResponse bounds JSON response bodies (DefaultMaxResponse).
	MaxResponse int64
	// Trace, when set, receives one line per request and one per response.
	Trace func(line string)
}

// Client calls one object-store node's HTTP API.
type Client struct {
	base string
	hc   *http.Client
	o    Options
	rows []storage.StatusRow
}

var _ httpapi.Service = (*Client)(nil)

// New returns a Client for addr, given as host:port or an http(s) URL with no
// path. It returns storage.ErrInvalid for an unusable address.
func New(addr string, o Options) (*Client, error) {
	raw := addr
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return nil, fmt.Errorf("httpclient: address %q: want host:port or http://host:port: %w", addr, storage.ErrInvalid)
	}
	if o.MaxObjectSize <= 0 {
		o.MaxObjectSize = httpapi.DefaultMaxObjectSize
	}
	if o.MaxSmallBody <= 0 {
		o.MaxSmallBody = httpapi.DefaultMaxSmallBody
	}
	if o.MaxResponse <= 0 {
		o.MaxResponse = DefaultMaxResponse
	}
	tr := o.Transport
	if tr == nil {
		tr = http.DefaultTransport
	}
	hc := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return &Client{base: u.Scheme + "://" + u.Host, hc: hc, o: o, rows: httpapi.StatusTable()}, nil
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("httpclient: "+format+": %w", append(a, storage.ErrInvalid)...)
}

// escape percent-encodes every byte of s except unreserved characters and "/",
// so the server's decoded path holds exactly s.
func escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' || c == '/' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

func objectPath(bucket, key string) (string, error) {
	if err := storage.ValidBucket(bucket); err != nil {
		return "", err
	}
	if err := storage.ValidKey(key); err != nil {
		return "", err
	}
	return "/" + bucket + "/" + escape(key), nil
}

func bucketPath(bucket string) (string, error) {
	if err := storage.ValidBucket(bucket); err != nil {
		return "", err
	}
	return "/" + bucket, nil
}

// validHeaderValue reports whether v survives an HTTP/1.1 header unchanged: no
// control bytes other than tab, and no leading or trailing space or tab (the
// receiver trims those).
func validHeaderValue(v string) bool {
	for i := 0; i < len(v); i++ {
		if c := v[i]; (c < 0x20 && c != '\t') || c == 0x7f {
			return false
		}
	}
	return strings.Trim(v, " \t") == v
}

// wireCondition returns the condition the server decodes from the headers
// Client sends for c: c.Canonical(), which selects the same precondition on a
// node. It returns storage.ErrInvalid when c combines If-Match with
// If-None-Match or when a tag holds a byte an RFC 9110 entity-tag cannot
// carry (a double quote, a control byte, space or DEL), since such a tag
// would reach the server altered.
func wireCondition(c storage.Condition) (storage.Condition, error) {
	w, err := c.Canonical()
	if err != nil {
		return storage.Condition{}, err
	}
	for _, t := range w.IfMatchAny {
		for i := 0; i < len(t); i++ {
			if b := t[i]; b < 0x21 || b == 0x7f || b == '"' {
				return storage.Condition{}, invalid("entity-tag %q: byte %#x cannot be sent", t, b)
			}
		}
	}
	return w, nil
}

func conditionHeader(h http.Header, c storage.Condition) error {
	w, err := wireCondition(c)
	if err != nil {
		return err
	}
	switch {
	case w.IfNoneMatch:
		h.Set("If-None-Match", "*")
	case w.IfMatch == "*":
		h.Set("If-Match", "*")
	case w.IfMatchList:
		quoted := make([]string, len(w.IfMatchAny))
		for i, t := range w.IfMatchAny {
			quoted[i] = `"` + t + `"`
		}
		h["If-Match"] = []string{strings.Join(quoted, ", ")}
	}
	return nil
}

type call struct {
	method string
	path   string
	query  url.Values
	header http.Header
	body   []byte
	limit  int64
}

func (c *Client) trace(format string, a ...any) {
	if c.o.Trace != nil {
		c.o.Trace(fmt.Sprintf(format, a...))
	}
}

func traceHeaders(h http.Header) string {
	var parts []string
	for _, k := range []string{"If-Match", "If-None-Match", "Content-Type"} {
		if vs, ok := h[k]; ok {
			parts = append(parts, fmt.Sprintf("%s: %q", k, strings.Join(vs, ",")))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " [" + strings.Join(parts, " ") + "]"
}

// transportErr wraps a failure to exchange k: the context's error when it is
// done, env.ErrUnreachable otherwise.
func transportErr(ctx context.Context, k call, what string, err error) error {
	if cerr := ctx.Err(); cerr != nil {
		return fmt.Errorf("httpclient: %s %s: %s%w", k.method, k.path, what, cerr)
	}
	return fmt.Errorf("httpclient: %s %s: %s%v: %w", k.method, k.path, what, err, env.ErrUnreachable)
}

// readBody reads at most limit bytes of body and reports whether more were
// sent. A declared length within limit is read into an exact buffer, then
// probed for one byte of overrun; a declared length over limit is not read
// unless truncate is set; an unknown length (-1) is read up to limit+1.
func readBody(body io.Reader, declared, limit int64, truncate bool) ([]byte, bool, error) {
	switch {
	case declared > limit && !truncate:
		return nil, true, nil
	case declared >= 0 && declared <= limit:
		b := make([]byte, declared)
		if _, err := io.ReadFull(body, b); err != nil {
			return nil, false, err
		}
		var probe [1]byte
		n, _ := io.ReadFull(body, probe[:])
		return b, n > 0, nil
	}
	b, err := io.ReadAll(io.LimitReader(body, limit+1))
	if int64(len(b)) > limit {
		return b[:limit], true, err
	}
	return b, false, err
}

// do sends k, reads at most k.limit bytes of a 2xx body (MaxSmallBody of an
// error body, truncated), and returns an *HTTPError for any non-2xx status.
func (c *Client) do(ctx context.Context, k call) (*http.Response, []byte, error) {
	target := c.base + k.path
	if len(k.query) > 0 {
		target += "?" + k.query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, k.method, target, bytes.NewReader(k.body))
	if err != nil {
		return nil, nil, invalid("request %s %s: %v", k.method, k.path, err)
	}
	for name, vs := range k.header {
		req.Header[name] = vs
	}
	start := time.Now()
	c.trace("> %s %s%s body=%dB", k.method, req.URL.RequestURI(), traceHeaders(req.Header), len(k.body))
	resp, err := c.hc.Do(req)
	if err != nil {
		c.trace("< error %v", err)
		return nil, nil, transportErr(ctx, k, "", err)
	}
	ok := resp.StatusCode >= 200 && resp.StatusCode < 300
	limit := k.limit
	if !ok {
		limit = c.o.MaxSmallBody
	}
	b, over, rerr := readBody(resp.Body, resp.ContentLength, limit, !ok)
	_ = resp.Body.Close()
	var meta string
	if v := resp.Header.Get("ETag"); v != "" {
		meta += " etag=" + v
	}
	if v := resp.Header.Get(httpapi.HeaderVersion); v != "" {
		meta += " version=" + v
	}
	c.trace("< %d %s%s body=%dB (%v)", resp.StatusCode, http.StatusText(resp.StatusCode), meta, len(b), time.Since(start).Round(time.Millisecond))
	if !ok {
		return resp, b, c.statusError(resp.StatusCode, b)
	}
	if over {
		return resp, nil, fmt.Errorf("httpclient: %s %s: response body over %d bytes: %w", k.method, k.path, limit, env.ErrTooLarge)
	}
	if rerr != nil {
		return resp, nil, transportErr(ctx, k, "read body: ", rerr)
	}
	return resp, b, nil
}

func (c *Client) statusError(status int, body []byte) error {
	var eb httpapi.ErrorBody
	msg := ""
	if json.Unmarshal(body, &eb) == nil {
		msg = eb.Error
	}
	if msg == "" {
		msg = strings.TrimSpace(strings.ToValidUTF8(string(body), "�"))
	}
	return &HTTPError{Status: status, Message: msg, rows: c.rows}
}

// jsonCall sends k and decodes its JSON response body into a T, returning the
// zero T on any error.
func jsonCall[T any](ctx context.Context, c *Client, k call) (T, error) {
	var out T
	if k.limit == 0 {
		k.limit = c.o.MaxResponse
	}
	_, b, err := c.do(ctx, k)
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(b, &out); err != nil {
		var zero T
		return zero, fmt.Errorf("httpclient: %s %s: decode response: %v: %w", k.method, k.path, err, storage.ErrCorrupt)
	}
	return out, nil
}

func (c *Client) smallBody(what string, b []byte) error {
	if int64(len(b)) > c.o.MaxSmallBody {
		return fmt.Errorf("httpclient: %s of %d bytes over %d: %w", what, len(b), c.o.MaxSmallBody, env.ErrTooLarge)
	}
	return nil
}

func objectInfo(resp *http.Response, bucket, key string) storage.ObjectInfo {
	etag := resp.Header.Get("ETag")
	if len(etag) >= 2 && etag[0] == '"' && etag[len(etag)-1] == '"' {
		etag = etag[1 : len(etag)-1]
	}
	return storage.ObjectInfo{Bucket: bucket, Key: key, ETag: etag, Version: resp.Header.Get(httpapi.HeaderVersion),
		ContentSHA: resp.Header.Get(httpapi.HeaderContentSHA), ContentType: resp.Header.Get("Content-Type")}
}

// CreateBucket creates spec.Name with its optional region and quota limits.
func (c *Client) CreateBucket(ctx context.Context, spec storage.BucketSpec) error {
	p, err := bucketPath(spec.Name)
	if err != nil {
		return err
	}
	q := url.Values{}
	if spec.Region != "" {
		q.Set("region", spec.Region)
	}
	if spec.QuotaBytes > 0 {
		q.Set("quota-bytes", strconv.FormatUint(spec.QuotaBytes, 10))
	}
	if spec.QuotaObjects > 0 {
		q.Set("quota-objects", strconv.FormatUint(spec.QuotaObjects, 10))
	}
	_, _, err = c.do(ctx, call{method: http.MethodPut, path: p, query: q, limit: c.o.MaxSmallBody})
	return err
}

// ListObjects lists up to limit live objects whose keys start with prefix. A
// limit <= 0 asks for the server maximum, httpapi.MaxListLimit; a larger limit
// returns storage.ErrInvalid.
func (c *Client) ListObjects(ctx context.Context, bucket, prefix string, limit int) ([]storage.ObjectInfo, error) {
	p, err := bucketPath(bucket)
	if err != nil {
		return nil, err
	}
	if limit > httpapi.MaxListLimit {
		return nil, invalid("list limit %d over %d", limit, httpapi.MaxListLimit)
	}
	q := url.Values{}
	if prefix != "" {
		q.Set("prefix", prefix)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	return jsonCall[[]storage.ObjectInfo](ctx, c, call{method: http.MethodGet, path: p, query: q})
}

// PutObject writes req.Data under req.Cond and returns the server's ObjectInfo.
// The content type must be at most httpapi.MaxContentType bytes with no
// control bytes other than tab and no surrounding space or tab, which a header
// would alter.
func (c *Client) PutObject(ctx context.Context, req storage.PutRequest) (storage.ObjectInfo, error) {
	p, err := objectPath(req.Bucket, req.Key)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	if len(req.ContentType) > httpapi.MaxContentType || !validHeaderValue(req.ContentType) {
		return storage.ObjectInfo{}, invalid("content type %q cannot be sent", req.ContentType)
	}
	if int64(len(req.Data)) > c.o.MaxObjectSize {
		return storage.ObjectInfo{}, fmt.Errorf("httpclient: object of %d bytes over %d: %w", len(req.Data), c.o.MaxObjectSize, env.ErrTooLarge)
	}
	h := http.Header{}
	if err := conditionHeader(h, req.Cond); err != nil {
		return storage.ObjectInfo{}, err
	}
	if req.ContentType != "" {
		h.Set("Content-Type", req.ContentType)
	}
	return jsonCall[storage.ObjectInfo](ctx, c, call{method: http.MethodPut, path: p, header: h, body: req.Data})
}

// GetObject reads the object's bytes and rebuilds its ObjectInfo from the
// response headers. The HTTP API reports an object stored without a content
// type as application/octet-stream, and carries no Deleted flag.
func (c *Client) GetObject(ctx context.Context, bucket, key string) (storage.ObjectInfo, []byte, error) {
	p, err := objectPath(bucket, key)
	if err != nil {
		return storage.ObjectInfo{}, nil, err
	}
	resp, b, err := c.do(ctx, call{method: http.MethodGet, path: p, limit: c.o.MaxObjectSize})
	if err != nil {
		return storage.ObjectInfo{}, nil, err
	}
	if cl := resp.Header.Get("Content-Length"); cl != "" && cl != strconv.Itoa(len(b)) {
		return storage.ObjectInfo{}, nil, fmt.Errorf("httpclient: get %s/%s: %d bytes for a Content-Length of %s: %w", bucket, key, len(b), cl, env.ErrUnreachable)
	}
	oi := objectInfo(resp, bucket, key)
	oi.Size = int64(len(b))
	return oi, b, nil
}

// HeadObject returns the object's metadata exactly as the server reports it,
// through the ?meta JSON form rather than HEAD headers.
func (c *Client) HeadObject(ctx context.Context, bucket, key string) (storage.ObjectInfo, error) {
	p, err := objectPath(bucket, key)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	return jsonCall[storage.ObjectInfo](ctx, c, call{method: http.MethodGet, path: p, query: url.Values{"meta": {""}}})
}

// DeleteObject deletes the object under cond. The 204 response carries only
// headers, so the result holds Bucket, Key, ETag, Version, ContentSHA and
// Deleted set to true.
func (c *Client) DeleteObject(ctx context.Context, bucket, key string, cond storage.Condition) (storage.ObjectInfo, error) {
	p, err := objectPath(bucket, key)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	h := http.Header{}
	if err := conditionHeader(h, cond); err != nil {
		return storage.ObjectInfo{}, err
	}
	resp, _, err := c.do(ctx, call{method: http.MethodDelete, path: p, header: h, limit: c.o.MaxSmallBody})
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	oi := objectInfo(resp, bucket, key)
	oi.ContentType, oi.Deleted = "", true
	return oi, nil
}

// PutTag sets tag name to value on the object under cond.
func (c *Client) PutTag(ctx context.Context, bucket, key, name, value string, cond storage.Condition) (storage.TagInfo, error) {
	p, err := objectPath(bucket, key)
	if err != nil {
		return storage.TagInfo{}, err
	}
	if err := c.smallBody("tag value", []byte(value)); err != nil {
		return storage.TagInfo{}, err
	}
	h := http.Header{}
	if err := conditionHeader(h, cond); err != nil {
		return storage.TagInfo{}, err
	}
	return jsonCall[storage.TagInfo](ctx, c, call{method: http.MethodPut, path: p, query: url.Values{"tag": {name}}, header: h, body: []byte(value)})
}

// Tags lists the object's user tags.
func (c *Client) Tags(ctx context.Context, bucket, key string) ([]storage.TagInfo, error) {
	p, err := objectPath(bucket, key)
	if err != nil {
		return nil, err
	}
	return jsonCall[[]storage.TagInfo](ctx, c, call{method: http.MethodGet, path: p, query: url.Values{"tags": {""}}})
}

// PutACL sets principal's permission letters on bucket under cond. The server
// trims surrounding white space from perm, so a perm that has any is rejected
// with storage.ErrInvalid.
func (c *Client) PutACL(ctx context.Context, bucket, principal, perm string, cond storage.Condition) (storage.ACLInfo, error) {
	p, err := bucketPath(bucket)
	if err != nil {
		return storage.ACLInfo{}, err
	}
	if strings.TrimSpace(perm) != perm {
		return storage.ACLInfo{}, invalid("permission %q has surrounding white space", perm)
	}
	if err := c.smallBody("permission", []byte(perm)); err != nil {
		return storage.ACLInfo{}, err
	}
	h := http.Header{}
	if err := conditionHeader(h, cond); err != nil {
		return storage.ACLInfo{}, err
	}
	return jsonCall[storage.ACLInfo](ctx, c, call{method: http.MethodPut, path: p, query: url.Values{"acl": {principal}}, header: h, body: []byte(perm)})
}

// ACL returns principal's permission entry on bucket.
func (c *Client) ACL(ctx context.Context, bucket, principal string) (storage.ACLInfo, error) {
	p, err := bucketPath(bucket)
	if err != nil {
		return storage.ACLInfo{}, err
	}
	return jsonCall[storage.ACLInfo](ctx, c, call{method: http.MethodGet, path: p, query: url.Values{"acl": {principal}}})
}

// Quota returns the bucket's limits and recorded usage.
func (c *Client) Quota(ctx context.Context, bucket string) (storage.QuotaInfo, error) {
	p, err := bucketPath(bucket)
	if err != nil {
		return storage.QuotaInfo{}, err
	}
	return jsonCall[storage.QuotaInfo](ctx, c, call{method: http.MethodGet, path: p, query: url.Values{"quota": {""}}})
}

// SetQuota sets the bucket's byte and object limits (0 means unlimited).
func (c *Client) SetQuota(ctx context.Context, bucket string, bytes, objects uint64) (storage.QuotaInfo, error) {
	p, err := bucketPath(bucket)
	if err != nil {
		return storage.QuotaInfo{}, err
	}
	body, err := json.Marshal(httpapi.QuotaRequest{Bytes: bytes, Objects: objects})
	if err != nil {
		return storage.QuotaInfo{}, invalid("quota request: %v", err)
	}
	return jsonCall[storage.QuotaInfo](ctx, c, call{method: http.MethodPut, path: p, query: url.Values{"quota": {""}}, body: body})
}

// StartMigration asks the node to migrate bucket to region to. JSON cannot
// carry invalid UTF-8, so such a bucket or region is rejected with
// storage.ErrInvalid.
func (c *Client) StartMigration(ctx context.Context, bucket, to string) (storage.MigrationInfo, error) {
	if !utf8.ValidString(bucket) || !utf8.ValidString(to) {
		return storage.MigrationInfo{}, invalid("migration of %q to %q: not valid UTF-8", bucket, to)
	}
	body, err := json.Marshal(httpapi.MigrationRequest{Bucket: bucket, To: to})
	if err != nil {
		return storage.MigrationInfo{}, invalid("migration request: %v", err)
	}
	if err := c.smallBody("migration request", body); err != nil {
		return storage.MigrationInfo{}, err
	}
	return jsonCall[storage.MigrationInfo](ctx, c, call{method: http.MethodPost, path: "/_migrations", body: body})
}

// Migration returns the state of migration id as seen by its coordinator.
func (c *Client) Migration(ctx context.Context, id string) (storage.MigrationInfo, error) {
	if id == "" {
		return storage.MigrationInfo{}, invalid("empty migration id")
	}
	return jsonCall[storage.MigrationInfo](ctx, c, call{method: http.MethodGet, path: "/_migrations/" + escape(id)})
}

// Members returns the node's gossip membership view.
func (c *Client) Members(ctx context.Context) ([]storage.MemberInfo, error) {
	return jsonCall[[]storage.MemberInfo](ctx, c, call{method: http.MethodGet, path: "/_cluster/members"})
}

// Health returns the node's readiness and diagnostic summary.
func (c *Client) Health(ctx context.Context) (storage.Health, error) {
	return jsonCall[storage.Health](ctx, c, call{method: http.MethodGet, path: "/_health"})
}
