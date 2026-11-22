package storage_test

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

	"github.com/nickstrad/decentralized-object-store/internal/occ"
	"github.com/nickstrad/decentralized-object-store/internal/saga"
	"github.com/nickstrad/decentralized-object-store/internal/storage"
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
	seq     int
}

func newModel() *model {
	return &model{buckets: map[string]map[string]*modelObject{}, tags: map[string]string{}}
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
	m.tags[bucket+"/"+key+"/"+name] = value
	return storage.TagInfo{Name: name, Value: value, ETag: m.etag()}, nil
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

func (m *model) PutACL(_ context.Context, bucket, principal, perm string, _ storage.Condition) (storage.ACLInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.objs(bucket); err != nil {
		return storage.ACLInfo{}, err
	}
	if principal == "" || strings.Trim(perm, "rwa") != "" {
		return storage.ACLInfo{}, storage.ErrInvalid
	}
	return storage.ACLInfo{Principal: principal, Perm: perm, ETag: m.etag()}, nil
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

func TestHTTPObjectAPIAgainstTheModel(t *testing.T) {
	srv := httptest.NewServer(storage.NewHandler(newModel(), storage.Limits{MaxObjectSize: 1 << 20}))
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
		{"deleting with a stale etag", "DELETE", "/b/k", "", map[string]string{"If-Match": "0"}, 412},
		{"deleting it", "DELETE", "/b/k", "", nil, 204},
		{"deleting it again", "DELETE", "/b/k", "", nil, 404},
	}
	for _, s := range steps {
		resp, body := do(t, srv, s.method, s.path, []byte(s.body), s.hdr)
		if resp.StatusCode != s.want {
			t.Fatalf("%s: status %d, want %d (%s)", s.name, resp.StatusCode, s.want, body)
		}
		switch s.name {
		case "putting an object":
			if resp.Header.Get("ETag") == "" || resp.Header.Get(storage.HeaderVersion) == "" {
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

func TestStatusTableRowByRowThroughHTTP(t *testing.T) {
	for _, row := range storage.StatusTable() {
		t.Run(row.Err.Error(), func(t *testing.T) {
			srv := httptest.NewServer(storage.NewHandler(errService{err: fmt.Errorf("wrapped: %w", row.Err)}, storage.Limits{}))
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
	if got := storage.StatusOf(storage.StatusTable(), errors.New("unknown")); got != 500 {
		t.Fatalf("an unknown error maps to %d", got)
	}
	extra := storage.StatusRow{Err: errors.New("extra"), Status: 418}
	if got := storage.StatusOf(storage.StatusTable(extra), fmt.Errorf("x: %w", extra.Err)); got != 418 {
		t.Fatalf("an extra row maps to %d", got)
	}
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

func startServer(t *testing.T, l storage.Limits) *storage.Server {
	t.Helper()
	s := storage.NewServer("127.0.0.1:0", newModel(), l)
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

func TestServerBoundsHeadersBodiesAndSlowClients(t *testing.T) {
	s := startServer(t, storage.Limits{MaxObjectSize: 1024, MaxHeaderBytes: 2048, ReadHeaderTimeout: 200 * time.Millisecond, ReadTimeout: 500 * time.Millisecond})
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

func TestServerShutdownTwiceAndAfterUse(t *testing.T) {
	s := storage.NewServer("127.0.0.1:0", newModel(), storage.Limits{})
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
	if err := storage.NewServer("256.0.0.1:1", newModel(), storage.Limits{}).Start(context.Background()); err == nil {
		t.Fatal("listening on an invalid address succeeded")
	}
	if storage.NewServer(":0", newModel(), storage.Limits{}).Addr() != "" {
		t.Fatal("an unstarted server has an address")
	}
}

var fuzzMethods = []string{"GET", "PUT", "HEAD", "DELETE", "POST", "PATCH"}

var fuzzStatus = map[int]bool{200: true, 202: true, 204: true, 400: true, 404: true, 405: true, 409: true, 412: true, 413: true, 500: true, 503: true, 504: true, 507: true}

func FuzzObjectAPI(f *testing.F) {
	f.Add([]byte("\x01/b\x00\x00\x00\x01/b/k\x00\x00hello\x00\x00/b/k\x00\x00\x00"))
	f.Add([]byte("\x01/b/k\x00*\x00x\x00"))
	f.Add([]byte{})
	f.Add([]byte("\x05/_migrations\x00\x00{\"bucket\":\"b\",\"to\":\"r\"}\x00"))
	f.Add([]byte("\x00/b?tags\x00\"etag\"\x00\x00\x03/b/k\x00\x00\x00"))
	f.Fuzz(func(t *testing.T, in []byte) {
		m := newModel()
		h := storage.NewHandler(m, storage.Limits{MaxObjectSize: 4096, MaxSmallBody: 1024})
		puts := map[string][]byte{}
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
			if v := string(parts[i+1]); v != "" && !strings.ContainsAny(v, "\r\n") {
				if v == "*" {
					req.Header.Set("If-None-Match", v)
				} else {
					req.Header.Set("If-Match", v)
				}
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if !fuzzStatus[w.Code] {
				t.Fatalf("%s %s: status %d outside the table", method, path, w.Code)
			}
			key := req.URL.Path
			if method == "PUT" && w.Code == 200 && strings.Count(key, "/") == 2 && req.URL.RawQuery == "" {
				puts[key] = parts[i+2]
				if w.Header().Get("ETag") == "" {
					t.Fatalf("PUT %s: no ETag", key)
				}
			}
			if method == "DELETE" && w.Code == 204 {
				delete(puts, key)
			}
			if method == "GET" && w.Code == 200 && req.URL.RawQuery == "" {
				if want, ok := puts[key]; ok && !bytes.Equal(w.Body.Bytes(), want) {
					t.Fatalf("GET %s returned %q, last acknowledged PUT wrote %q", key, w.Body.Bytes(), want)
				}
			}
		}
	})
}

func TestAPICodesAreInTheirBlockAndDistinct(t *testing.T) {
	seen := map[int]bool{}
	for _, e := range storage.APICodeEntries() {
		if e.Code < 160 || e.Code > 175 || seen[int(e.Code)] || e.Err == nil {
			t.Fatalf("api code %d for %v", e.Code, e.Err)
		}
		seen[int(e.Code)] = true
	}
}
