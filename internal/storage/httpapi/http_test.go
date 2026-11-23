package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nickstrad/decentralized-object-store/internal/env"
	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
	"github.com/nickstrad/decentralized-object-store/internal/storage/httpapi"
)

type modelObject struct {
	data []byte
	etag string
	ct   string
}

type model struct {
	mu      sync.Mutex
	buckets map[string]map[string]*modelObject
	tags    map[string]string
	meta    map[string]*modelObject
	seq     int
}

func newModel() *model {
	return &model{buckets: map[string]map[string]*modelObject{}, tags: map[string]string{}, meta: map[string]*modelObject{}}
}

func (m *model) etag() string {
	m.seq++
	return fmt.Sprintf("%064x", m.seq)
}

func (m *model) objs(bucket string) (map[string]*modelObject, error) {
	b, ok := m.buckets[bucket]
	if !ok {
		return nil, storage.ErrNoSuchBucket
	}
	return b, nil
}

func holds(c storage.Condition, o *modelObject) bool {
	if c.IfNoneMatch && o != nil {
		return false
	}
	if c.IfMatch != "" && (o == nil || (c.IfMatch != "*" && c.IfMatch != o.etag)) {
		return false
	}
	if c.IfMatchList && (o == nil || !slices.Contains(c.IfMatchAny, o.etag)) {
		return false
	}
	return true
}

func info(bucket, key string, o *modelObject) storage.ObjectInfo {
	sum := sha256.Sum256(o.data)
	return storage.ObjectInfo{Bucket: bucket, Key: key, Size: int64(len(o.data)), ETag: o.etag, Version: o.etag[48:], ContentSHA: hex.EncodeToString(sum[:]), ContentType: o.ct}
}

func (m *model) CreateBucket(_ context.Context, spec storage.BucketSpec) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.buckets[spec.Name]; ok {
		return storage.ErrBucketExists
	}
	m.buckets[spec.Name] = map[string]*modelObject{}
	return nil
}

func (m *model) ListObjects(_ context.Context, bucket, prefix string, limit int) ([]storage.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := m.objs(bucket)
	if err != nil {
		return nil, err
	}
	var keys []string
	for k := range b {
		if strings.HasPrefix(k, prefix) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var out []storage.ObjectInfo
	for _, k := range keys {
		if len(out) < limit {
			out = append(out, info(bucket, k, b[k]))
		}
	}
	return out, nil
}

func (m *model) PutObject(_ context.Context, req storage.PutRequest) (storage.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, err := m.objs(req.Bucket)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	if !holds(req.Cond, b[req.Key]) {
		return storage.ObjectInfo{}, occ.ErrPreconditionFailed
	}
	o := &modelObject{data: slices.Clone(req.Data), etag: m.etag(), ct: req.ContentType}
	b[req.Key] = o
	return info(req.Bucket, req.Key, o), nil
}

func (m *model) get(bucket, key string) (*modelObject, error) {
	b, err := m.objs(bucket)
	if err != nil {
		return nil, err
	}
	o, ok := b[key]
	if !ok {
		return nil, occ.ErrNotFound
	}
	return o, nil
}

func (m *model) GetObject(_ context.Context, bucket, key string) (storage.ObjectInfo, []byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, err := m.get(bucket, key)
	if err != nil {
		return storage.ObjectInfo{}, nil, err
	}
	return info(bucket, key, o), slices.Clone(o.data), nil
}

func (m *model) HeadObject(_ context.Context, bucket, key string) (storage.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, err := m.get(bucket, key)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	return info(bucket, key, o), nil
}

func (m *model) DeleteObject(_ context.Context, bucket, key string, c storage.Condition) (storage.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, err := m.get(bucket, key)
	if err != nil {
		return storage.ObjectInfo{}, err
	}
	if !holds(c, o) {
		return storage.ObjectInfo{}, occ.ErrPreconditionFailed
	}
	delete(m.buckets[bucket], key)
	return storage.ObjectInfo{Bucket: bucket, Key: key, ETag: m.etag(), Deleted: true}, nil
}

func (m *model) PutTag(_ context.Context, bucket, key, name, value string, c storage.Condition) (storage.TagInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.get(bucket, key); err != nil {
		return storage.TagInfo{}, err
	}
	if name == "" {
		return storage.TagInfo{}, storage.ErrInvalid
	}
	k := bucket + "/" + key + "/" + name
	if !holds(c, m.meta[k]) {
		return storage.TagInfo{}, occ.ErrPreconditionFailed
	}
	m.tags[k] = value
	e := m.etag()
	m.meta[k] = &modelObject{etag: e}
	return storage.TagInfo{Name: name, Value: value, ETag: e}, nil
}

func (m *model) Tags(_ context.Context, bucket, key string) ([]storage.TagInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.get(bucket, key); err != nil {
		return nil, err
	}
	var out []storage.TagInfo
	for _, k := range slices.Sorted(func(yield func(string) bool) {
		for k := range m.tags {
			if !yield(k) {
				return
			}
		}
	}) {
		if strings.HasPrefix(k, bucket+"/"+key+"/") {
			out = append(out, storage.TagInfo{Name: strings.TrimPrefix(k, bucket+"/"+key+"/"), Value: m.tags[k]})
		}
	}
	return out, nil
}

func (m *model) PutACL(_ context.Context, bucket, principal, perm string, c storage.Condition) (storage.ACLInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.objs(bucket); err != nil {
		return storage.ACLInfo{}, err
	}
	if principal == "" || strings.Trim(perm, "rwa") != "" {
		return storage.ACLInfo{}, storage.ErrInvalid
	}
	k := bucket + "?acl=" + principal
	if !holds(c, m.meta[k]) {
		return storage.ACLInfo{}, occ.ErrPreconditionFailed
	}
	e := m.etag()
	m.meta[k] = &modelObject{etag: e}
	return storage.ACLInfo{Principal: principal, Perm: perm, ETag: e}, nil
}

func (m *model) ACL(_ context.Context, bucket, principal string) (storage.ACLInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.objs(bucket); err != nil {
		return storage.ACLInfo{}, err
	}
	return storage.ACLInfo{}, occ.ErrNotFound
}

func (m *model) Quota(_ context.Context, bucket string) (storage.QuotaInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.objs(bucket); err != nil {
		return storage.QuotaInfo{}, err
	}
	return storage.QuotaInfo{Bucket: bucket}, nil
}

func (m *model) SetQuota(ctx context.Context, bucket string, b, o uint64) (storage.QuotaInfo, error) {
	q, err := m.Quota(ctx, bucket)
	q.LimitBytes, q.LimitObjects = b, o
	return q, err
}

func (m *model) StartMigration(_ context.Context, bucket, to string) (storage.MigrationInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.objs(bucket); err != nil {
		return storage.MigrationInfo{}, err
	}
	if to == "" {
		return storage.MigrationInfo{}, storage.ErrInvalid
	}
	return storage.MigrationInfo{ID: "m1", Bucket: bucket, To: to, State: "running"}, nil
}

func (m *model) Migration(_ context.Context, id string) (storage.MigrationInfo, error) {
	if id != "m1" {
		return storage.MigrationInfo{}, saga.ErrSagaNotFound
	}
	return storage.MigrationInfo{ID: id, State: "completed"}, nil
}

func (m *model) Members(context.Context) ([]storage.MemberInfo, error) {
	return []storage.MemberInfo{{ID: "a", Region: "r0", State: "alive"}}, nil
}

func (m *model) Health(context.Context) (storage.Health, error) {
	return storage.Health{Node: "a", Ready: true, LiveMembers: 1}, nil
}

func do(t testing.TB, srv *httptest.Server, method, path string, body []byte, hdr map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Error(err)
		}
	}()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, b
}

// TestHTTPObjectAPIAgainstTheModel proves every HTTP route, response header,
// error status, and conditional If-Match/If-None-Match case (including quoted
// commas, weak tags, empty elements, and malformed values) against an in-memory
// reference model.
func TestHTTPObjectAPIAgainstTheModel(t *testing.T) {
	srv := httptest.NewServer(httpapi.NewHandler(newModel(), httpapi.Limits{MaxObjectSize: 1 << 20}))
	defer srv.Close()
	steps := []struct {
		name   string
		method string
		path   string
		body   string
		hdr    map[string]string
		want   int
	}{
		{"creating a bucket", "PUT", "/b?region=r0&quota-bytes=10", "", nil, 200},
		{"creating it twice", "PUT", "/b", "", nil, 409},
		{"a bad quota parameter", "PUT", "/c?quota-bytes=x", "", nil, 400},
		{"an invalid bucket name", "GET", "/B_/k", "", nil, 400},
		{"no bucket in the path", "GET", "/", "", nil, 400},
		{"reading a missing object", "GET", "/b/k", "", nil, 404},
		{"reading from a missing bucket", "GET", "/nob/k", "", nil, 404},
		{"putting an object", "PUT", "/b/k", "hello", map[string]string{"Content-Type": "text/plain"}, 200},
		{"a create-only put over it", "PUT", "/b/k", "x", map[string]string{"If-None-Match": "*"}, 412},
		{"an If-None-Match other than *", "PUT", "/b/k", "x", map[string]string{"If-None-Match": `"abc"`}, 400},
		{"a put with a stale etag", "PUT", "/b/k", "x", map[string]string{"If-Match": `"0000"`}, 412},
		{"reading it", "GET", "/b/k", "", nil, 200},
		{"heading it", "HEAD", "/b/k", "", nil, 200},
		{"its metadata", "GET", "/b/k?meta", "", nil, 200},
		{"tagging it", "PUT", "/b/k?tag=color", "red", nil, 200},
		{"an empty tag name", "PUT", "/b/k?tag=", "red", nil, 400},
		{"listing its tags", "GET", "/b/k?tags", "", nil, 200},
		{"listing the bucket", "GET", "/b?prefix=k&limit=5", "", nil, 200},
		{"a list limit over the maximum", "GET", "/b?limit=99999999", "", nil, 400},
		{"setting an acl", "PUT", "/b?acl=alice", "rw", nil, 200},
		{"an invalid acl", "PUT", "/b?acl=alice", "rx", nil, 400},
		{"reading a missing acl", "GET", "/b?acl=bob", "", nil, 404},
		{"reading the quota", "GET", "/b?quota", "", nil, 200},
		{"setting the quota", "PUT", "/b?quota", `{"bytes":5,"objects":2}`, nil, 200},
		{"a malformed quota body", "PUT", "/b?quota", `{`, nil, 400},
		{"starting a migration", "POST", "/_migrations", `{"bucket":"b","to":"r1"}`, nil, 202},
		{"a malformed migration body", "POST", "/_migrations", `nope`, nil, 400},
		{"a migration status", "GET", "/_migrations/m1", "", nil, 200},
		{"an unknown migration", "GET", "/_migrations/zz", "", nil, 404},
		{"deleting migrations", "DELETE", "/_migrations", "", nil, 405},
		{"the members", "GET", "/_cluster/members", "", nil, 200},
		{"posting to members", "POST", "/_cluster/members", "", nil, 405},
		{"the health", "GET", "/_health", "", nil, 200},
		{"posting to health", "POST", "/_health", "", nil, 405},
		{"patching an object", "PATCH", "/b/k", "", nil, 405},
		{"patching a bucket", "PATCH", "/b", "", nil, 405},
		{"a key over the length limit", "GET", "/b/" + strings.Repeat("k", storage.MaxKeyLen+1), "", nil, 400},
		{"an object over the size limit", "PUT", "/b/big", strings.Repeat("x", 1<<20+1), nil, 413},
		{"deleting with a stale etag", "DELETE", "/b/k", "", map[string]string{"If-Match": `"0"`}, 412},
		{"If-Match empty quoted etag on PUT", "PUT", "/b/k", "x", map[string]string{"If-Match": `""`}, 412},
		{"If-Match empty field on PUT", "PUT", "/b/k", "x", map[string]string{"If-Match": ""}, 412},
		{"If-Match whitespace only on PUT", "PUT", "/b/k", "x", map[string]string{"If-Match": "   "}, 412},
		{"If-Match empty list elements on PUT", "PUT", "/b/k", "x", map[string]string{"If-Match": " , ,"}, 412},
		{"If-Match unquoted etag on PUT", "PUT", "/b/k", "x", map[string]string{"If-Match": "abc"}, 400},
		{"If-Match unterminated quote on PUT", "PUT", "/b/k", "x", map[string]string{"If-Match": `"abc`}, 400},
		{"If-Match a doubled quote on PUT", "PUT", "/b/k", "x", map[string]string{"If-Match": `""""`}, 400},
		{"If-Match star inside a list on PUT", "PUT", "/b/k", "x", map[string]string{"If-Match": `*, "a"`}, 400},
		{"If-Match a weak validator on PUT", "PUT", "/b/k", "x", map[string]string{"If-Match": `W/"@"`}, 412},
		{"If-Match a list without the current etag on PUT", "PUT", "/b/k", "x", map[string]string{"If-Match": `"a", "b"`}, 412},
		{"If-None-Match empty field on PUT", "PUT", "/b/k", "x", map[string]string{"If-None-Match": ""}, 400},
		{"If-None-Match whitespace on PUT", "PUT", "/b/k", "x", map[string]string{"If-None-Match": "  "}, 400},
		{"If-None-Match a list on PUT", "PUT", "/b/k", "x", map[string]string{"If-None-Match": `"a", *`}, 400},
		{"both preconditions on PUT", "PUT", "/b/k", "x", map[string]string{"If-Match": "*", "If-None-Match": "*"}, 400},
		{"If-Match empty quoted etag on DELETE", "DELETE", "/b/k", "", map[string]string{"If-Match": `""`}, 412},
		{"If-Match whitespace only on DELETE", "DELETE", "/b/k", "", map[string]string{"If-Match": "  "}, 412},
		{"If-Match malformed on DELETE", "DELETE", "/b/k", "", map[string]string{"If-Match": "abc"}, 400},
		{"If-Match a weak validator on DELETE", "DELETE", "/b/k", "", map[string]string{"If-Match": `W/"@"`}, 412},
		{"If-Match empty quoted etag on a tag", "PUT", "/b/k?tag=c", "x", map[string]string{"If-Match": `""`}, 412},
		{"If-Match a list holding the current etag on PUT", "PUT", "/b/k", "hello", map[string]string{"If-Match": `"a", "@"`, "Content-Type": "text/plain"}, 200},
		{"If-Match star on PUT", "PUT", "/b/k", "hello", map[string]string{"If-Match": "*", "Content-Type": "text/plain"}, 200},
		{"If-Match a quoted comma in a list on PUT", "PUT", "/b/k", "hello", map[string]string{"If-Match": `"a,b", "@"`, "Content-Type": "text/plain"}, 200},
		{"If-Match a quoted comma in a list on a tag", "PUT", "/b/k?tag=color", "blue", map[string]string{"If-Match": `"a,b", "@"`}, 200},
		{"If-Match a quoted comma in a list on an acl", "PUT", "/b?acl=alice", "r", map[string]string{"If-Match": `"a,b", "@"`}, 200},
		{"If-Match a quoted comma in a list without the current etag on PUT", "PUT", "/b/k", "x", map[string]string{"If-Match": `"a,@"`}, 412},
		{"If-Match a quoted comma in a list on DELETE", "DELETE", "/b/k", "", map[string]string{"If-Match": `"a,b", "@"`}, 204},
		{"putting it back", "PUT", "/b/k", "hello", map[string]string{"Content-Type": "text/plain"}, 200},
		{"deleting it", "DELETE", "/b/k", "", nil, 204},
		{"If-Match star on a missing object", "PUT", "/b/k", "x", map[string]string{"If-Match": "*"}, 412},
		{"deleting it again", "DELETE", "/b/k", "", nil, 404},
	}
	current := map[string]string{}
	for _, s := range steps {
		hdr := map[string]string{}
		for k, v := range s.hdr {
			hdr[k] = strings.ReplaceAll(v, "@", current[s.path])
		}
		resp, body := do(t, srv, s.method, s.path, []byte(s.body), hdr)
		if e := resp.Header.Get("ETag"); e != "" && s.method == "PUT" && resp.StatusCode == 200 {
			current[s.path] = strings.Trim(e, `"`)
		}
		if resp.StatusCode != s.want {
			t.Fatalf("%s: status %d, want %d (%s)", s.name, resp.StatusCode, s.want, body)
		}
		switch s.name {
		case "putting an object":
			if resp.Header.Get("ETag") == "" || resp.Header.Get(httpapi.HeaderVersion) == "" {
				t.Fatalf("%s: no ETag or version header", s.name)
			}
		case "reading it":
			if string(body) != "hello" || resp.Header.Get("Content-Type") != "text/plain" || resp.Header.Get("ETag") == "" {
				t.Fatalf("%s: body %q headers %v", s.name, body, resp.Header)
			}
		case "listing the bucket":
			var objs []storage.ObjectInfo
			if err := json.Unmarshal(body, &objs); err != nil || len(objs) != 1 || objs[0].Key != "k" {
				t.Fatalf("%s: %s %v", s.name, body, err)
			}
		}
	}
}

// TestStatusTableRowByRowThroughHTTP proves every row in StatusTable maps a
// wrapped error to its declared HTTP status and JSON error body, unknown errors
// map to 500, and caller-supplied extra rows override defaults.
func TestStatusTableRowByRowThroughHTTP(t *testing.T) {
	for _, row := range httpapi.StatusTable() {
		t.Run(row.Err.Error(), func(t *testing.T) {
			srv := httptest.NewServer(httpapi.NewHandler(errService{err: fmt.Errorf("wrapped: %w", row.Err)}, httpapi.Limits{}))
			defer srv.Close()
			for _, req := range []struct{ method, path string }{{"GET", "/b/k"}, {"PUT", "/b/k"}, {"DELETE", "/b/k"}, {"GET", "/b"}, {"GET", "/_health"}} {
				resp, body := do(t, srv, req.method, req.path, nil, nil)
				if resp.StatusCode != row.Status {
					t.Fatalf("%s %s: status %d, want %d", req.method, req.path, resp.StatusCode, row.Status)
				}
				var eb struct {
					Error  string `json:"error"`
					Status int    `json:"status"`
				}
				if err := json.Unmarshal(body, &eb); err != nil || eb.Status != row.Status || eb.Error == "" {
					t.Fatalf("%s %s: error body %q %v", req.method, req.path, body, err)
				}
			}
		})
	}
	if got := httpapi.StatusOf(httpapi.StatusTable(), errors.New("unknown")); got != 500 {
		t.Fatalf("an unknown error maps to %d", got)
	}
	extra := storage.StatusRow{Err: errors.New("extra"), Status: 418}
	if got := httpapi.StatusOf(httpapi.StatusTable(extra), fmt.Errorf("x: %w", extra.Err)); got != 418 {
		t.Fatalf("an extra row maps to %d", got)
	}
}

func TestStatusTableJoinedUnavailablePrecedesShardCauses(t *testing.T) {
	table := httpapi.StatusTable()
	causes := []struct {
		name           string
		err            error
		wantStandalone int
	}{
		{"storage.ErrNotFound", storage.ErrNotFound, http.StatusNotFound},
		{"storage.ErrCorrupt", storage.ErrCorrupt, http.StatusInternalServerError},
		{"env.ErrNotFound", env.ErrNotFound, http.StatusNotFound},
		{"env.ErrTimeout", env.ErrTimeout, http.StatusGatewayTimeout},
		{"env.ErrUnreachable", env.ErrUnreachable, http.StatusServiceUnavailable},
		{"storage.ErrBusy", storage.ErrBusy, http.StatusServiceUnavailable},
		{"storage.ErrExists", storage.ErrExists, http.StatusConflict},
		{"storage.ErrInvalid", storage.ErrInvalid, http.StatusBadRequest},
	}
	all := []error{storage.ErrUnavailable}
	for _, c := range causes {
		all = append(all, c.err)
		t.Run("unavailable_with_"+c.name, func(t *testing.T) {
			if got := httpapi.StatusOf(table, errors.Join(storage.ErrUnavailable, c.err)); got != http.StatusServiceUnavailable {
				t.Fatalf("StatusOf(Join(ErrUnavailable, %s)) = %d, want 503", c.name, got)
			}
			if got := httpapi.StatusOf(table, errors.Join(c.err, storage.ErrUnavailable)); got != http.StatusServiceUnavailable {
				t.Fatalf("StatusOf(Join(%s, ErrUnavailable)) = %d, want 503", c.name, got)
			}
		})
		t.Run("standalone_"+c.name, func(t *testing.T) {
			if got := httpapi.StatusOf(table, c.err); got != c.wantStandalone {
				t.Fatalf("StatusOf(%s) = %d, want %d", c.name, got, c.wantStandalone)
			}
		})
	}
	t.Run("unavailable_with_all_shard_causes", func(t *testing.T) {
		if got := httpapi.StatusOf(table, errors.Join(all...)); got != http.StatusServiceUnavailable {
			t.Fatalf("StatusOf(all) = %d, want 503", got)
		}
	})
	t.Run("standalone_missing_object_and_bucket", func(t *testing.T) {
		if got := httpapi.StatusOf(table, occ.ErrNotFound); got != http.StatusNotFound {
			t.Fatalf("StatusOf(occ.ErrNotFound) = %d, want 404", got)
		}
		if got := httpapi.StatusOf(table, storage.ErrNoSuchBucket); got != http.StatusNotFound {
			t.Fatalf("StatusOf(storage.ErrNoSuchBucket) = %d, want 404", got)
		}
	})
}

type errService struct{ err error }

func (e errService) CreateBucket(context.Context, storage.BucketSpec) error { return e.err }
func (e errService) ListObjects(context.Context, string, string, int) ([]storage.ObjectInfo, error) {
	return nil, e.err
}
func (e errService) PutObject(context.Context, storage.PutRequest) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, e.err
}
func (e errService) GetObject(context.Context, string, string) (storage.ObjectInfo, []byte, error) {
	return storage.ObjectInfo{}, nil, e.err
}
func (e errService) HeadObject(context.Context, string, string) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, e.err
}
func (e errService) DeleteObject(context.Context, string, string, storage.Condition) (storage.ObjectInfo, error) {
	return storage.ObjectInfo{}, e.err
}
func (e errService) PutTag(context.Context, string, string, string, string, storage.Condition) (storage.TagInfo, error) {
	return storage.TagInfo{}, e.err
}
func (e errService) Tags(context.Context, string, string) ([]storage.TagInfo, error) {
	return nil, e.err
}
func (e errService) PutACL(context.Context, string, string, string, storage.Condition) (storage.ACLInfo, error) {
	return storage.ACLInfo{}, e.err
}
func (e errService) ACL(context.Context, string, string) (storage.ACLInfo, error) {
	return storage.ACLInfo{}, e.err
}
func (e errService) Quota(context.Context, string) (storage.QuotaInfo, error) {
	return storage.QuotaInfo{}, e.err
}
func (e errService) SetQuota(context.Context, string, uint64, uint64) (storage.QuotaInfo, error) {
	return storage.QuotaInfo{}, e.err
}
func (e errService) StartMigration(context.Context, string, string) (storage.MigrationInfo, error) {
	return storage.MigrationInfo{}, e.err
}
func (e errService) Migration(context.Context, string) (storage.MigrationInfo, error) {
	return storage.MigrationInfo{}, e.err
}
func (e errService) Members(context.Context) ([]storage.MemberInfo, error) { return nil, e.err }
func (e errService) Health(context.Context) (storage.Health, error) {
	return storage.Health{}, e.err
}

func startServer(t *testing.T, l httpapi.Limits) *httpapi.Server {
	t.Helper()
	s := httpapi.NewServer("127.0.0.1:0", newModel(), l)
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}

// TestServerBoundsHeadersBodiesAndSlowClients proves the HTTP server rejects
// oversized bodies (413) and headers (431) and closes slow header or body
// clients within its configured timeouts.
func TestServerBoundsHeadersBodiesAndSlowClients(t *testing.T) {
	s := startServer(t, httpapi.Limits{MaxObjectSize: 1024, MaxHeaderBytes: 2048, ReadHeaderTimeout: 200 * time.Millisecond, ReadTimeout: 500 * time.Millisecond})
	base := "http://" + s.Addr()
	req, err := http.NewRequest("PUT", base+"/b", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest("PUT", base+"/b/k", strings.NewReader(strings.Repeat("x", 4096)))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body: %d", resp.StatusCode)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	req, _ = http.NewRequest("GET", base+"/b/k", nil)
	req.Header.Set("X-Big", strings.Repeat("h", 8192))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Fatalf("oversized header: %d", resp.StatusCode)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	for _, partial := range []string{"GET /b/k HTTP/1.1\r\nHost: x\r\n", "PUT /b/k HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\nabc"} {
		c, err := net.Dial("tcp", s.Addr())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Write([]byte(partial)); err != nil {
			t.Fatal(err)
		}
		if err := c.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, err = io.Copy(io.Discard, bufio.NewReader(c))
		if err != nil {
			t.Fatalf("slow client: %v", err)
		}
		if time.Since(start) > 5*time.Second {
			t.Fatalf("a slow client held its connection for %v", time.Since(start))
		}
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// TestServerShutdownTwiceAndAfterUse proves Server.Shutdown is idempotent,
// Addr is empty before Start, and Start rejects an unbindable address.
func TestServerShutdownTwiceAndAfterUse(t *testing.T) {
	s := httpapi.NewServer("127.0.0.1:0", newModel(), httpapi.Limits{})
	if err := s.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Addr() == "" {
		t.Fatal("no address")
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := httpapi.NewServer("256.0.0.1:1", newModel(), httpapi.Limits{}).Start(context.Background()); err == nil {
		t.Fatal("listening on an invalid address succeeded")
	}
	if httpapi.NewServer(":0", newModel(), httpapi.Limits{}).Addr() != "" {
		t.Fatal("an unstarted server has an address")
	}
}

var fuzzMethods = []string{"GET", "PUT", "HEAD", "DELETE", "POST", "PATCH"}

var fuzzStatus = map[int]bool{200: true, 202: true, 204: true, 400: true, 404: true, 405: true, 409: true, 412: true, 413: true, 500: true, 503: true, 504: true, 507: true}

type tagList struct {
	strong []string
	star   bool
	ok     bool
}

func referenceEntityTags(v string) tagList {
	const (
		between = iota
		afterW
		wantQuote
		inTag
		afterElement
	)
	var out tagList
	state, weak, nonEmpty := between, false, 0
	var cur []byte
	for _, c := range []byte(v) {
		switch state {
		case between:
			switch {
			case c == ' ' || c == '\t' || c == ',':
			case c == '*':
				out.star = true
				nonEmpty++
				state = afterElement
			case c == 'W':
				nonEmpty++
				weak, state = true, afterW
			case c == '"':
				nonEmpty++
				weak, state, cur = false, inTag, cur[:0]
			default:
				return tagList{}
			}
		case afterW:
			if c != '/' {
				return tagList{}
			}
			state = wantQuote
		case wantQuote:
			if c != '"' {
				return tagList{}
			}
			state, cur = inTag, cur[:0]
		case inTag:
			switch {
			case c == '"':
				if !weak {
					out.strong = append(out.strong, string(cur))
				}
				state = afterElement
			case c == 0x21 || c >= 0x23 && c <= 0x7e || c >= 0x80:
				cur = append(cur, c)
			default:
				return tagList{}
			}
		case afterElement:
			switch c {
			case ' ', '\t':
			case ',':
				state = between
			default:
				return tagList{}
			}
		}
	}
	if state != between && state != afterElement {
		return tagList{}
	}
	if out.star && nonEmpty > 1 {
		return tagList{}
	}
	out.ok = true
	return out
}

func parsed(v string) tagList {
	strong, star, err := httpapi.ParseEntityTags(v)
	if err != nil {
		if !errors.Is(err, storage.ErrInvalid) {
			return tagList{strong: []string{"wrong error: " + err.Error()}}
		}
		return tagList{}
	}
	return tagList{strong: strong, star: star, ok: true}
}

func sameTagList(a, b tagList) bool {
	return a.ok == b.ok && a.star == b.star && slices.Equal(a.strong, b.strong)
}

// TestParseEntityTagsTable proves ParseEntityTags matches an independent
// byte-level RFC 9110 state machine across valid lists, quoted commas, weak
// tags, stars, empty elements, obs-text, and malformed inputs.
func TestParseEntityTagsTable(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want tagList
	}{
		{"one strong tag", `"abc"`, tagList{strong: []string{"abc"}, ok: true}},
		{"a comma inside the quotes", `"a,b"`, tagList{strong: []string{"a,b"}, ok: true}},
		{"a quoted comma then the current tag", `"a,b", "e1"`, tagList{strong: []string{"a,b", "e1"}, ok: true}},
		{"only commas inside the quotes", `",,,"`, tagList{strong: []string{",,,"}, ok: true}},
		{"several tags with optional whitespace", " \t\"a\"\t, \"b\" ,\"c\" ", tagList{strong: []string{"a", "b", "c"}, ok: true}},
		{"tags with no whitespace", `"a","b"`, tagList{strong: []string{"a", "b"}, ok: true}},
		{"an empty quoted tag", `""`, tagList{strong: []string{""}, ok: true}},
		{"a weak validator is dropped", `W/"a"`, tagList{ok: true}},
		{"a weak validator with a quoted comma", `W/"a,b", "c"`, tagList{strong: []string{"c"}, ok: true}},
		{"weak and strong with the same opaque tag", `W/"a", "a"`, tagList{strong: []string{"a"}, ok: true}},
		{"star", "*", tagList{star: true, ok: true}},
		{"star with whitespace", " * ", tagList{star: true, ok: true}},
		{"star with empty elements", ", * ,", tagList{star: true, ok: true}},
		{"star inside a list", `*, "a"`, tagList{}},
		{"star after a tag", `"a", *`, tagList{}},
		{"a quoted star is a tag", `"*"`, tagList{strong: []string{"*"}, ok: true}},
		{"an empty field", "", tagList{ok: true}},
		{"whitespace only", " \t ", tagList{ok: true}},
		{"only empty elements", " , ,", tagList{ok: true}},
		{"leading and doubled empty elements", `,,"a",, ,"b"`, tagList{strong: []string{"a", "b"}, ok: true}},
		{"a trailing comma", `"a",`, tagList{strong: []string{"a"}, ok: true}},
		{"a trailing comma and whitespace", `"a" , `, tagList{strong: []string{"a"}, ok: true}},
		{"an unterminated quote", `"abc`, tagList{}},
		{"an unterminated quote holding a comma", `"a, "b"`, tagList{}},
		{"an unterminated quote after a tag", `"a", "b`, tagList{}},
		{"a lone quote", `"`, tagList{}},
		{"an unquoted tag", "abc", tagList{}},
		{"an unquoted tag after a quoted comma", `"a,b", c`, tagList{}},
		{"a doubled quote", `""""`, tagList{}},
		{"two tags without a comma", `"a" "b"`, tagList{}},
		{"bytes after a tag", `"a"x`, tagList{}},
		{"a space inside the quotes", `"a b"`, tagList{}},
		{"a tab inside the quotes", "\"a\tb\"", tagList{}},
		{"a control byte inside the quotes", "\"a\x01b\"", tagList{}},
		{"DEL inside the quotes", "\"a\x7fb\"", tagList{}},
		{"obs-text inside the quotes", "\"a\x80\xffb\"", tagList{strong: []string{"a\x80\xffb"}, ok: true}},
		{"a lowercase weak prefix", `w/"a"`, tagList{}},
		{"a weak prefix without a tag", `W/`, tagList{}},
		{"space between the weak prefix and the tag", `W/ "a"`, tagList{}},
		{"a weak prefix on star", `W/*`, tagList{}},
		{"a star followed by bytes", `*x`, tagList{}},
		{"a vertical tab is not whitespace", "\v\"a\"", tagList{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := parsed(tc.in); !sameTagList(got, tc.want) {
				t.Fatalf("ParseEntityTags(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
			if got := referenceEntityTags(tc.in); !sameTagList(got, tc.want) {
				t.Fatalf("referenceEntityTags(%q) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func mayApply(name, value string, present bool, current string) bool {
	switch name {
	case "If-None-Match":
		return strings.TrimSpace(value) == "*" && !present
	case "If-Match":
		l := referenceEntityTags(value)
		if !l.ok || !present {
			return false
		}
		return l.star || slices.Contains(l.strong, current)
	}
	return true
}

var fuzzHeaders = []string{"", "If-Match", "If-None-Match", "If-Match"}

// FuzzObjectAPI proves arbitrary HTTP request sequences never panic, always
// return a status in the table, never apply a write whose precondition cannot
// hold under the reference tokeniser, and never return GET bytes disagreeing
// with the last acknowledged PUT.
func FuzzObjectAPI(f *testing.F) {
	f.Add([]byte("\x01/b\x00\x00\x00\x01/b/k\x00\x00hello\x00\x00/b/k\x00\x00\x00"))
	f.Add([]byte("\x01/b/k\x00*\x00x\x00"))
	f.Add([]byte{})
	f.Add([]byte("\x05/_migrations\x00\x00{\"bucket\":\"b\",\"to\":\"r\"}\x00"))
	f.Add([]byte("\x00/b?tags\x00\"etag\"\x00\x00\x03/b/k\x00\x00\x00"))
	f.Add([]byte("\x01/b\x00\x00\x00\x01/b/k\x00\x00v1\x00\x01/b/k\x00\x01\"\"\x00v2\x00"))
	f.Add([]byte("\x01/b\x00\x00\x00\x01/b/k\x00\x00v1\x00\x01/b/k\x00\x01   \x00v2\x00\x03/b/k\x00\x01W/\"@\"\x00\x00"))
	f.Add([]byte("\x01/b\x00\x00\x00\x01/b/k\x00\x00v1\x00\x01/b/k\x00\x01\"x\", \"@\"\x00v2\x00\x01/b/k\x00\x02\x00v3\x00"))
	f.Add([]byte("\x01/b\x00\x00\x00\x00\x01/b/k\x00\x00v1\x00\x00\x01/b/k\x00\x01\"a,b\", \"@\"\x00v2\x00\x00\x03/b/k\x00\x01\"a,@\"\x00\x00\x00\x03/b/k\x00\x01\"a,b\",\"@\"\x00\x00"))
	f.Add([]byte("\x01/b\x00\x00\x00\x00\x01/b/k\x00\x00v1\x00\x00\x01/b/k\x00\x01, W/\"a,\" ,,\"@\",\x00v2\x00\x00\x03/b/k\x00\x01\"@\" \"@\"\x00\x00\x00\x01/b/k\x00\x01\"a, \"@\"\x00v3\x00"))
	f.Fuzz(func(t *testing.T, in []byte) {
		m := newModel()
		h := httpapi.NewHandler(m, httpapi.Limits{MaxObjectSize: 4096, MaxSmallBody: 1024})
		puts := map[string][]byte{}
		etags := map[string]string{}
		parts := bytes.Split(in, []byte{0})
		for i := 0; i+3 < len(parts) && i < 4*12; i += 4 {
			if len(parts[i]) == 0 {
				continue
			}
			method := fuzzMethods[int(parts[i][0])%len(fuzzMethods)]
			path := string(parts[i][1:])
			if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, " \r\n\t#") {
				path = "/" + strings.Map(func(r rune) rune {
					if r < 0x21 || r == 0x7f || r == '#' || r == '%' {
						return -1
					}
					return r
				}, path)
			}
			req, err := http.NewRequest(method, "http://h"+path, bytes.NewReader(parts[i+2]))
			if err != nil {
				continue
			}
			key := req.URL.Path
			name, value := "", string(parts[i+1])
			if len(value) > 0 && value[0] < 4 {
				name, value = fuzzHeaders[value[0]], value[1:]
			} else if value != "" {
				name = "If-Match"
				if value == "*" {
					name = "If-None-Match"
				}
			}
			value = strings.ReplaceAll(value, "@", etags[key])
			if got, want := parsed(value), referenceEntityTags(value); !sameTagList(got, want) {
				t.Fatalf("ParseEntityTags(%q) = %+v, the reference tokeniser gives %+v", value, got, want)
			}
			if name != "" && strings.ContainsAny(value, "\r\n\x00") {
				name = ""
			}
			if name != "" {
				req.Header[name] = []string{value}
			}
			cur, present := etags[key]
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if !fuzzStatus[w.Code] {
				t.Fatalf("%s %s: status %d outside the table", method, path, w.Code)
			}
			applied := req.URL.RawQuery == "" && strings.Count(key, "/") == 2 &&
				(method == "PUT" && w.Code == 200 || method == "DELETE" && w.Code == 204)
			if applied && name != "" && !mayApply(name, value, present, cur) {
				t.Fatalf("%s %s with %s: %q applied (status %d) though the precondition cannot hold (exists=%v, etag %s)", method, key, name, value, w.Code, present, cur)
			}
			if method == "PUT" && w.Code == 200 && strings.Count(key, "/") == 2 && req.URL.RawQuery == "" {
				puts[key] = parts[i+2]
				if w.Header().Get("ETag") == "" {
					t.Fatalf("PUT %s: no ETag", key)
				}
				etags[key] = strings.Trim(w.Header().Get("ETag"), `"`)
			}
			if method == "DELETE" && w.Code == 204 {
				delete(puts, key)
				delete(etags, key)
			}
			if method == "GET" && w.Code == 200 && req.URL.RawQuery == "" {
				if want, ok := puts[key]; ok && !bytes.Equal(w.Body.Bytes(), want) {
					t.Fatalf("GET %s returned %q, last acknowledged PUT wrote %q", key, w.Body.Bytes(), want)
				}
			}
		}
	})
}
